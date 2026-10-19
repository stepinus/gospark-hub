package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// Video constants mirror daemon/services/video_service.py.
const (
	videoMaxSeconds      = 15
	videoMaxInputBytes   = 200 * 1024 * 1024
	videoAudioInputBytes = 50 * 1024 * 1024
	videoPollSeconds     = 5
	videoMaxWait         = 600
)

// VideoAspectRatios is upstream ASPECT_RATIOS.
var VideoAspectRatios = []string{"16:9", "9:16", "21:9", "4:3", "1:1", "3:4"}

var videoTagKinds = map[string]string{
	"text-to-video": "text", "image-to-video": "image", "video-edit": "video",
	"last-frame": "last_image", "multi-reference": "images", "audio-reference": "audio",
}

var videoAccepts = map[string]string{
	"text": "text-to-video", "image": "image-to-video", "video": "video-to-video",
	"last_image": "last-frame", "images": "multi-reference", "audio": "audio-reference",
}

var videoKindLabels = map[string]string{
	"text": "make video from text", "image": "turn an image into video",
	"video": "edit a video", "last_image": "end on a given last frame",
	"images": "take several reference pictures", "audio": "take a reference sound",
}

type videoBackend struct {
	slug     string
	kinds    map[string]bool
	summary  string
	defaults *registry.RecipeVideoDefaults
}

func (b videoBackend) aspectRatios() []string {
	if b.defaults != nil && len(b.defaults.AspectRatios) > 0 {
		return b.defaults.AspectRatios
	}
	return []string{"16:9", "9:16"}
}

func (b videoBackend) maxSeconds() int {
	if b.defaults != nil && b.defaults.MaxSeconds != nil {
		return *b.defaults.MaxSeconds
	}
	return 10
}

type videoReference struct {
	kind       string
	raw        []byte
	frameIndex *int
}

type videoInfo struct {
	JobID          string
	Model          string
	Status         string
	VideoID        string
	Poster         string
	Seconds        any
	Size           string
	Seed           *int
	Steps          *int
	Progress       any
	InferenceTimeS *float64
	Err            string
}

type videoJob struct {
	id       string
	slug     string
	remoteID string
	created  time.Time
	seed     int
	size     string
	seconds  any
	steps    *int
	kind     string
	userID   int64

	videoID        string
	poster         string
	inferenceTimeS *float64
	failed         bool
	err            string
}

type videoJobs struct {
	mu   sync.Mutex
	jobs map[string]*videoJob
}

func newVideoJobs() *videoJobs { return &videoJobs{jobs: map[string]*videoJob{}} }

func (v *videoJobs) prune() {
	for len(v.jobs) >= maxJobs {
		oldest := ""
		var at time.Time
		for id, j := range v.jobs {
			if oldest == "" || j.created.Before(at) {
				oldest, at = id, j.created
			}
		}
		if oldest == "" {
			return
		}
		delete(v.jobs, oldest)
	}
}

// videoBackends lists every catalog recipe tagged openai-videos.
func (s *Server) videoBackends() []videoBackend {
	recipes, err := s.allRecipes()
	if err != nil {
		return nil
	}
	var out []videoBackend
	for _, r := range recipes {
		if !hasTag(r.Tags, "openai-videos") {
			continue
		}
		kinds := map[string]bool{}
		for _, t := range r.Tags {
			if k, ok := videoTagKinds[t]; ok {
				kinds[k] = true
			}
		}
		if len(kinds) == 0 {
			continue
		}
		out = append(out, videoBackend{slug: r.Slug, kinds: kinds, summary: r.Name, defaults: r.VideoDefaults})
	}
	return out
}

func (b videoBackend) acceptsList() []string {
	var out []string
	for _, k := range []string{"text", "image", "video", "last_image", "images", "audio"} {
		if b.kinds[k] {
			out = append(out, videoAccepts[k])
		}
	}
	return out
}

func (s *Server) pickVideoBackend(ctx context.Context, needs map[string]bool, model string) (videoBackend, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return videoBackend{}, err
	}
	var candidates []videoBackend
	for _, b := range s.videoBackends() {
		if !installed[b.slug] {
			continue
		}
		ok := true
		for k := range needs {
			if !b.kinds[k] {
				ok = false
				break
			}
		}
		if ok {
			candidates = append(candidates, b)
		}
	}
	label := videoNeedsLabel(needs)
	if model != "" {
		found := false
		for _, b := range candidates {
			if b.slug == model {
				candidates, found = []videoBackend{b}, true
				break
			}
		}
		if !found {
			return videoBackend{}, imageErrorf("'%s' is not an installed model that can %s. "+
				"Installed models that can: %s.", model, label, videoSlugList(candidates))
		}
	}
	idx, err := s.pickReady(ctx, videoSlugs(candidates), "video model that can "+label)
	if err != nil {
		return videoBackend{}, err
	}
	return candidates[idx], nil
}

func videoNeedsLabel(needs map[string]bool) string {
	var parts []string
	for _, k := range []string{"text", "image", "video", "last_image", "images", "audio"} {
		if needs[k] {
			parts = append(parts, videoKindLabels[k])
		}
	}
	return strings.Join(parts, " and ")
}

func videoSlugs(bs []videoBackend) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.slug
	}
	return out
}

func videoSlugList(bs []videoBackend) string {
	if len(bs) == 0 {
		return "none installed"
	}
	return strings.Join(videoSlugs(bs), ", ")
}

// generateVideo is upstream video_service.start.
func (s *Server) generateVideo(ctx context.Context, args map[string]any, user *store.User) (*videoInfo, error) {
	prompt := argString(args, "prompt")
	if prompt == "" {
		return nil, imageErrorf("'prompt' is required.")
	}
	image := argString(args, "image")
	video := argString(args, "video")
	lastImage := argString(args, "last_image")
	audio := argString(args, "audio")
	images := argStringList(args, "images")
	model := argString(args, "model")

	if len(images) > 0 && image == "" {
		image, images = images[0], images[1:]
	}
	needs := map[string]bool{}
	if image != "" {
		needs["image"] = true
	}
	if video != "" {
		needs["video"] = true
	}
	if len(images) > 0 {
		needs["images"] = true
	}
	if lastImage != "" {
		needs["last_image"] = true
	}
	if audio != "" {
		needs["audio"] = true
	}
	kind := "text"
	switch {
	case video != "":
		kind = "video"
	case image != "" || lastImage != "":
		kind = "image"
	case audio != "":
		kind = "audio"
	}
	if len(needs) == 0 {
		needs = map[string]bool{"text": true}
	}
	backend, err := s.pickVideoBackend(ctx, needs, model)
	if err != nil {
		return nil, err
	}

	conditionsModel := videoTakesConditions(backend.defaults)
	if image != "" && video != "" && !conditionsModel {
		return nil, imageErrorf("Give either a starting image or an input video, not both.")
	}
	aspectRatio := argString(args, "aspect_ratio")
	if aspectRatio != "" && !contains(backend.aspectRatios(), aspectRatio) {
		return nil, imageErrorf("%s renders %s, not %s.", backend.slug,
			strings.Join(backend.aspectRatios(), ", "), aspectRatio)
	}
	seconds := argInt(args, "seconds")
	if seconds != nil && *seconds > backend.maxSeconds() {
		return nil, imageErrorf("%s renders clips up to %d s.", backend.slug, backend.maxSeconds())
	}
	task := ""
	if backend.defaults != nil && backend.defaults.ExtraParams != nil {
		task, _ = backend.defaults.ExtraParams["task"].(string)
	}
	seed := argInt(args, "seed")
	seedVal := 0
	if seed != nil {
		seedVal = *seed
	} else {
		seedVal = randomSeed()
	}
	steps := argInt(args, "steps")
	keyframes := task == "t2va" && (image != "" || lastImage != "")
	if keyframes {
		task = "fl2va"
	}
	effectiveAspect := aspectRatio
	if effectiveAspect == "" {
		if keyframes {
			effectiveAspect = "auto"
		} else {
			effectiveAspect = "16:9"
		}
	}
	fields := videoFields(backend.defaults, prompt, seconds, effectiveAspect, seedVal, steps)
	if task != "" {
		extra := map[string]any{}
		if fields["extra_params"] != nil {
			extra, _ = fields["extra_params"].(map[string]any)
			extra = cloneMap(extra)
		}
		if extra["task"] != task {
			extra["task"] = task
			fields["extra_params"] = extra
		}
	}

	var refs []videoReference
	if video != "" {
		raw, err := s.loadInput(ctx, video, user, ".mp4", videoMaxInputBytes)
		if err != nil {
			return nil, err
		}
		refs = append(refs, videoReference{kind: "video", raw: raw})
	}
	if image != "" {
		for i, ref := range append([]string{image}, images...) {
			raw, err := s.loadInput(ctx, ref, user, ".png", maxInputBytes)
			if err != nil {
				return nil, err
			}
			pngRaw, err := toPNG(raw)
			if err != nil {
				return nil, err
			}
			var fi *int
			if keyframes && i == 0 {
				zero := 0
				fi = &zero
			}
			refs = append(refs, videoReference{kind: "image", raw: pngRaw, frameIndex: fi})
		}
	}
	if lastImage != "" {
		raw, err := s.loadInput(ctx, lastImage, user, ".png", maxInputBytes)
		if err != nil {
			return nil, err
		}
		pngRaw, err := toPNG(raw)
		if err != nil {
			return nil, err
		}
		minusOne := -1
		refs = append(refs, videoReference{kind: "image", raw: pngRaw, frameIndex: &minusOne})
	}
	if audio != "" {
		raw, err := s.loadInput(ctx, audio, user, ".wav", videoAudioInputBytes)
		if err != nil {
			return nil, err
		}
		refs = append(refs, videoReference{kind: "audio", raw: raw})
	}

	base := s.app.InternalURL(backend.slug) + "/v1/videos"
	req, err := s.videoRequest(ctx, base, fields, refs, conditionsModel, keyframes)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, imageErrorf("%s refused the video job: %v", backend.slug, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imageErrorf("%s refused the video job (HTTP %d): %s",
			backend.slug, resp.StatusCode, truncate(string(body), 500))
	}
	var remote map[string]any
	if err := json.Unmarshal(body, &remote); err != nil {
		return nil, imageErrorf("%s returned an unreadable video job.", backend.slug)
	}
	remoteID, _ := remote["id"].(string)
	if remoteID == "" {
		return nil, imageErrorf("%s did not start a video job.", backend.slug)
	}

	job := &videoJob{
		id: newJobID(), slug: backend.slug, remoteID: remoteID, created: time.Now(),
		seed: seedVal, size: stringField(fields, "size"), steps: steps, kind: kind,
		userID: userIDOf(user),
	}
	if sec := clipSeconds(fields); sec != nil {
		job.seconds = sec
	} else if rs, ok := remote["seconds"]; ok {
		job.seconds = rs
	}
	if job.size == "" {
		if rs, ok := remote["size"].(string); ok {
			job.size = rs
		}
	}
	v := s.videoJobs
	v.mu.Lock()
	v.prune()
	v.jobs[job.id] = job
	v.mu.Unlock()

	status, _ := remote["status"].(string)
	if status == "" {
		status = "queued"
	}
	return &videoInfo{
		JobID: job.id, Model: backend.slug, Status: status,
		Seconds: job.seconds, Size: job.size, Seed: &seedVal, Steps: steps,
	}, nil
}

// videoRequest builds the multipart or JSON request upstream video_service.start
// sends to /v1/videos.
func (s *Server) videoRequest(ctx context.Context, url string, fields map[string]any,
	refs []videoReference, conditionsModel, keyframes bool) (*http.Request, error) {
	if conditionsModel {
		extra, _ := fields["extra_params"].(map[string]any)
		extra = cloneMap(extra)
		conditions := []any{}
		mimes := map[string]string{"image": "image/png", "video": "video/mp4", "audio": "audio/wav"}
		for _, ref := range refs {
			cond := map[string]any{
				"type": ref.kind,
				"uri":  "data:" + mimes[ref.kind] + ";base64," + base64.StdEncoding.EncodeToString(ref.raw),
				"role": "reference",
			}
			if ref.frameIndex != nil {
				cond["role"] = "keyframe"
				cond["frame_index"] = *ref.frameIndex
			}
			conditions = append(conditions, cond)
		}
		extra["conditions"] = conditions
		fields["extra_params"] = extra
		body, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		return s.newAppRequest(ctx, http.MethodPost, url, bytes.NewReader(body))
	}

	if keyframes {
		extra, _ := fields["extra_params"].(map[string]any)
		extra = cloneMap(extra)
		var indices []int
		for _, ref := range refs {
			if ref.frameIndex != nil {
				indices = append(indices, *ref.frameIndex)
			}
		}
		extra["frame_indices"] = indices
		fields["extra_params"] = extra
		delete(fields, "aspect_ratio")
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	slot := "input_reference"
	if len(refs) > 1 {
		slot = "input_references"
	}
	for _, ref := range refs {
		part, err := writer.CreateFormFile(slot, "input."+refExtension(ref.kind))
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(ref.raw); err != nil {
			return nil, err
		}
	}
	for key, value := range fields {
		text := ""
		if m, ok := value.(map[string]any); ok {
			raw, _ := json.Marshal(m)
			text = string(raw)
		} else {
			text = toString(value)
		}
		if err := writer.WriteField(key, text); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req, err := s.newAppRequest(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

// refExtension is the filename extension for one reference input.
func refExtension(kind string) string {
	switch kind {
	case "video":
		return "mp4"
	case "audio":
		return "wav"
	}
	return "png"
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		raw, _ := json.Marshal(t)
		return string(raw)
	}
}

// checkVideo is upstream video_service.check: poll the model server, then copy
// the finished MP4 into the Hub's data dir.
func (s *Server) checkVideo(ctx context.Context, jobID string, wait time.Duration,
	onProgress func(string), user *store.User) (*videoInfo, error) {
	v := s.videoJobs
	v.mu.Lock()
	job, ok := v.jobs[jobID]
	if !ok || !videoOwns(job, user) {
		v.mu.Unlock()
		return nil, imageErrorf("Unknown job_id. Video jobs are forgotten when the Hub restarts; start a new one.")
	}
	if job.videoID != "" {
		info := videoBaseInfo(job)
		info.Status = "completed"
		info.VideoID = job.videoID
		info.Poster = job.poster
		info.InferenceTimeS = job.inferenceTimeS
		v.mu.Unlock()
		return info, nil
	}
	v.mu.Unlock()

	deadline := time.Now().Add(wait)
	if wait > videoMaxWait*time.Second {
		deadline = time.Now().Add(videoMaxWait * time.Second)
	}
	url := s.app.InternalURL(job.slug) + "/v1/videos/" + job.remoteID
	for {
		req, err := s.newAppRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return nil, imageErrorf("%s status check failed: %v", job.slug, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, imageErrorf("%s no longer has this job (was it restarted?).", job.slug)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, imageErrorf("%s status check failed (HTTP %d).", job.slug, resp.StatusCode)
		}
		var remote map[string]any
		_ = json.Unmarshal(body, &remote)
		state, _ := remote["status"].(string)
		switch state {
		case "completed":
			info, err := s.collectVideo(ctx, job, remote)
			if err != nil {
				return nil, err
			}
			return info, nil
		case "failed":
			errObj, _ := remote["error"].(map[string]any)
			msg, _ := errObj["message"].(string)
			if msg == "" {
				msg = "no detail given"
			}
			v.mu.Lock()
			job.failed, job.err = true, msg
			v.mu.Unlock()
			info := videoBaseInfo(job)
			info.Status = "failed"
			info.Err = msg
			return info, nil
		}
		if time.Now().After(deadline) {
			info := videoBaseInfo(job)
			if state == "" {
				state = "queued"
			}
			info.Status = state
			info.Progress = remote["progress"]
			if info.Progress == nil {
				info.Progress = 0
			}
			return info, nil
		}
		if onProgress != nil {
			progress := remote["progress"]
			if progress == nil {
				progress = 0
			}
			if state == "" {
				state = "queued"
			}
			onProgress(state + " " + toString(progress) + "%")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(videoPollSeconds * time.Second):
		}
	}
}

func videoOwns(job *videoJob, user *store.User) bool {
	// Video jobs are per-user when auth is on; the Server passes authEnabled
	// through the job's owner id, exactly like image jobs.
	return job.userID == 0 || (user != nil && job.userID == user.ID)
}

func videoBaseInfo(job *videoJob) *videoInfo {
	info := &videoInfo{
		JobID: job.id, Model: job.slug, Seconds: job.seconds, Size: job.size,
		Seed: intPtrValue(job.seed), Steps: job.steps,
	}
	return info
}

func intPtrValue(v int) *int { return &v }

func (s *Server) collectVideo(ctx context.Context, job *videoJob, remote map[string]any) (*videoInfo, error) {
	dir := filepath.Join(s.cfg.DataDir, "videos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	videoID := newJobID()
	path := filepath.Join(dir, videoID+".mp4")
	contentURL := s.app.InternalURL(job.slug) + "/v1/videos/" + job.remoteID + "/content"
	req, err := s.newAppRequest(ctx, http.MethodGet, contentURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, imageErrorf("Fetching the video from %s failed: %v", job.slug, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imageErrorf("Fetching the video from %s failed (HTTP %d).", job.slug, resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	took := float64(0)
	if completedAt, ok := remote["completed_at"].(float64); ok {
		took = completedAt - float64(job.created.UnixNano())/1e9
	} else {
		took = time.Since(job.created).Seconds()
	}
	seconds := took
	if err := s.media.Record(videoID+".mp4", job.userID, job.slug, &seconds); err != nil {
		s.log.Printf("mcp: record video: %v", err)
	}
	poster := posterJPEG(path)
	var inference *float64
	if v, ok := remote["inference_time_s"].(float64); ok {
		inference = &v
	}

	s.videoJobs.mu.Lock()
	job.videoID, job.poster, job.inferenceTimeS = videoID, poster, inference
	s.videoJobs.mu.Unlock()

	info := videoBaseInfo(job)
	info.Status = "completed"
	info.VideoID = videoID
	info.Poster = poster
	info.InferenceTimeS = inference
	return info, nil
}

// renderingOnVideo counts open jobs still rendering on slug.
func (s *Server) renderingOnVideo(ctx context.Context, slug string) int {
	s.videoJobs.mu.Lock()
	var open []*videoJob
	for _, j := range s.videoJobs.jobs {
		if j.slug == slug && j.videoID == "" && !j.failed {
			open = append(open, j)
		}
	}
	s.videoJobs.mu.Unlock()
	busy := 0
	for _, job := range open {
		url := s.app.InternalURL(slug) + "/v1/videos/" + job.remoteID
		req, err := s.newAppRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := s.http.Do(req)
		if err != nil {
			continue
		}
		var remote map[string]any
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&remote)
		resp.Body.Close()
		state, _ := remote["status"].(string)
		if state == "queued" || state == "in_progress" {
			busy++
		}
	}
	return busy
}

// posterJPEG extracts a still from half a second in, for agents that cannot
// play video. Needs ffmpeg; returns "" when it is unavailable.
func posterJPEG(path string) string {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}
	dir, err := os.MkdirTemp("", "sah-poster-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "poster.jpg")
	cmd := exec.Command(ffmpeg, "-loglevel", "error", "-y", "-ss", "0.5", "-i", path,
		"-frames:v", "1", "-vf", "scale=512:-2", out)
	if err := cmd.Run(); err != nil {
		return ""
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// videoFields is upstream video_service.video_fields.
func videoFields(defaults *registry.RecipeVideoDefaults, prompt string, seconds *int,
	aspectRatio string, seed int, steps *int) map[string]any {
	fields := map[string]any{"prompt": prompt, "seed": seed}
	size := ""
	if defaults != nil && defaults.Size != nil {
		size = *defaults.Size
	}
	if size != "" {
		parts := strings.Split(strings.ToLower(size), "x")
		if len(parts) == 2 {
			w, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			h, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
			long, short := w, h
			if h > w {
				long, short = h, w
			}
			if aspectRatio == "9:16" {
				size = strconv.Itoa(short) + "x" + strconv.Itoa(long)
			} else {
				size = strconv.Itoa(long) + "x" + strconv.Itoa(short)
			}
			fields["size"] = size
		}
	}
	if defaults != nil && defaults.FPS != nil {
		fields["fps"] = *defaults.FPS
	}
	extra := map[string]any{}
	if defaults != nil && defaults.ExtraParams != nil {
		extra = cloneMap(defaults.ExtraParams)
	}
	frames := 0
	if defaults != nil && defaults.NumFrames != nil {
		frames = *defaults.NumFrames
	}
	defaultSeconds := 0
	if defaults != nil && defaults.Seconds != nil {
		defaultSeconds = *defaults.Seconds
	}
	fps := 0
	if defaults != nil && defaults.FPS != nil {
		fps = *defaults.FPS
	}
	if frames > 0 {
		if seconds != nil && fps > 0 {
			frames = maxInt(5, (*seconds*fps-1)/4*4+1)
		}
		fields["num_frames"] = frames
	} else if _, ok := extra["duration"]; ok {
		length := defaultSeconds
		if seconds != nil {
			length = *seconds
		}
		if length == 0 {
			if d, ok := extra["duration"].(float64); ok {
				length = int(d)
			}
		}
		extra["duration"] = float64(length)
	} else if target, ok := extra["target"].(map[string]any); ok {
		target = cloneMap(target)
		length := defaultSeconds
		if seconds != nil {
			length = *seconds
		}
		if length == 0 {
			if d, ok := target["duration_seconds"].(float64); ok {
				length = int(d)
			}
		}
		if length > 0 {
			target["duration_seconds"] = float64(length)
			fields["seconds"] = length
		}
		target["aspect_ratio"] = aspectRatio
		extra["target"] = target
	} else {
		length := defaultSeconds
		if seconds != nil {
			length = *seconds
		}
		if length > 0 {
			fields["seconds"] = length
		}
	}
	if defaults != nil && defaults.SendAspectRatio {
		fields["aspect_ratio"] = aspectRatio
	}
	if defaults != nil && defaults.GuidanceScale != nil {
		fields["guidance_scale"] = *defaults.GuidanceScale
	}
	if defaults != nil && defaults.FlowShift != nil {
		fields["flow_shift"] = *defaults.FlowShift
	}
	if len(extra) > 0 {
		fields["extra_params"] = extra
	}
	defaultSteps := 0
	if defaults != nil && defaults.Steps != nil {
		defaultSteps = *defaults.Steps
	}
	if steps != nil && *steps > 0 {
		fields["num_inference_steps"] = *steps
	} else if defaultSteps > 0 {
		fields["num_inference_steps"] = defaultSteps
	}
	return fields
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// videoTakesConditions reports whether the model reads references from
// extra_params.conditions (MiniMax-H3 semantics).
func videoTakesConditions(defaults *registry.RecipeVideoDefaults) bool {
	if defaults == nil || defaults.ExtraParams == nil {
		return false
	}
	_, ok := defaults.ExtraParams["conditions"].([]any)
	return ok
}

// clipSeconds is upstream clip_seconds.
func clipSeconds(fields map[string]any) any {
	if v, ok := fields["seconds"]; ok && v != nil {
		return v
	}
	if frames, ok := fields["num_frames"]; ok {
		if fps, ok2 := fields["fps"]; ok2 {
			f := toFloat(frames)
			p := toFloat(fps)
			if p != 0 {
				return round1(f / p)
			}
		}
	}
	if extra, ok := fields["extra_params"].(map[string]any); ok {
		if d, ok := extra["duration"]; ok && d != nil {
			return d
		}
	}
	return nil
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	}
	return 0
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
