package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strings"

	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// Image constants mirror daemon/services/image_service.py.
const (
	openAIImagesTag = "openai-images"
	openAIChatTag   = "openai-chat-images"
	hidreamTag      = "hidream-app"
	editSizeMul     = 32
	maxSeed         = 1<<31 - 1
)

// ImageAspectRatios is upstream ASPECT_RATIOS.
var ImageAspectRatios = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}

// openAISizes is upstream OPENAI_SIZES.
var openAISizes = map[string][2]int{
	"1:1": {1024, 1024}, "16:9": {1344, 768}, "9:16": {768, 1344},
	"4:3": {1152, 864}, "3:4": {864, 1152}, "3:2": {1216, 832}, "2:3": {832, 1216},
}

var imageTagKinds = map[string]string{"text-to-image": "generate", "image-edit": "edit"}

type imageBackend struct {
	slug     string
	kinds    map[string]bool
	summary  string
	defaults *registry.RecipeImageDefaults
	protocol string // "images" | "chat" | "hidream"
}

func (b imageBackend) recommended() *orderedMap {
	d := b.defaults
	if d == nil {
		return nil
	}
	out := newOrderedMap()
	if d.Preset != nil {
		out.Set("preset", *d.Preset)
	}
	if d.Steps != nil {
		out.Set("steps", *d.Steps)
	}
	if d.GuidanceScale != nil {
		out.Set("guidance_scale", *d.GuidanceScale)
	}
	if d.TrueCFGScale != nil {
		out.Set("true_cfg_scale", *d.TrueCFGScale)
	}
	if d.Notes != "" {
		out.Set("notes", d.Notes)
	}
	if d.Source != "" {
		out.Set("source", d.Source)
	}
	if len(out.keys) == 0 {
		return nil
	}
	return out
}

func (b imageBackend) tools() []string {
	var out []string
	for _, k := range []string{"generate", "edit"} {
		if b.kinds[k] {
			out = append(out, k+"_image")
		}
	}
	return out
}

// imageBackends lists every catalog image model.
func (s *Server) imageBackends() []imageBackend {
	recipes, err := s.allRecipes()
	if err != nil {
		return nil
	}
	var out []imageBackend
	for _, r := range recipes {
		protocol := ""
		switch {
		case hasTag(r.Tags, openAIImagesTag):
			protocol = "images"
		case hasTag(r.Tags, openAIChatTag):
			protocol = "chat"
		case hasTag(r.Tags, hidreamTag):
			protocol = "hidream"
		default:
			continue
		}
		kinds := map[string]bool{}
		for _, t := range r.Tags {
			if k, ok := imageTagKinds[t]; ok {
				kinds[k] = true
			}
		}
		if len(kinds) == 0 {
			continue
		}
		out = append(out, imageBackend{slug: r.Slug, kinds: kinds, summary: r.Name,
			defaults: r.ImageDefaults, protocol: protocol})
	}
	return out
}

func (s *Server) pickImageBackend(ctx context.Context, kind, model string) (imageBackend, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return imageBackend{}, err
	}
	var candidates []imageBackend
	for _, b := range s.imageBackends() {
		if installed[b.slug] && b.kinds[kind] {
			candidates = append(candidates, b)
		}
	}
	if model != "" {
		found := false
		for _, b := range candidates {
			if b.slug == model {
				candidates, found = []imageBackend{b}, true
				break
			}
		}
		if !found {
			return imageBackend{}, imageErrorf("'%s' is not an installed model that can %s images. "+
				"Installed models that can: %s.", model, kind, imageSlugList(candidates))
		}
	}
	idx, err := s.pickReady(ctx, imageSlugs(candidates), "image model that can "+kind)
	if err != nil {
		return imageBackend{}, err
	}
	return candidates[idx], nil
}

func imageSlugs(bs []imageBackend) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.slug
	}
	return out
}

func imageSlugList(bs []imageBackend) string {
	if len(bs) == 0 {
		return "none installed"
	}
	return strings.Join(imageSlugs(bs), ", ")
}

// imageParams is upstream image_service.Params.
type imageParams struct {
	prompt         string
	negativePrompt string
	aspectRatio    string
	seed           *int
	steps          *int
}

// withDefaults fills unset steps from the model card.
func withDefaults(b imageBackend, p imageParams) imageParams {
	if b.defaults != nil && b.defaults.Preset != nil && *b.defaults.Preset != "" {
		p.steps = nil
		return p
	}
	if p.steps == nil && b.defaults != nil && b.defaults.Steps != nil {
		s := *b.defaults.Steps
		p.steps = &s
	}
	return p
}

// generationBody is upstream image_service.generation_body.
func generationBody(p imageParams, seed int, defaults *registry.RecipeImageDefaults) map[string]any {
	size := openAISizes[p.aspectRatio]
	if size == [2]int{} {
		size = openAISizes["1:1"]
	}
	body := map[string]any{
		"prompt":          p.prompt,
		"n":               1,
		"size":            fmt.Sprintf("%dx%d", size[0], size[1]),
		"response_format": "b64_json",
		"output_format":   "png",
		"seed":            seed,
	}
	if defaults != nil {
		if defaults.GuidanceScale != nil {
			body["guidance_scale"] = *defaults.GuidanceScale
		}
		if defaults.TrueCFGScale != nil {
			body["true_cfg_scale"] = *defaults.TrueCFGScale
		}
	}
	if defaults != nil && defaults.Preset != nil && *defaults.Preset != "" {
		body["preset"] = *defaults.Preset
	} else if p.steps != nil {
		body["num_inference_steps"] = *p.steps
	}
	if p.negativePrompt != "" {
		body["negative_prompt"] = p.negativePrompt
	}
	return body
}

// runImage is upstream image_service.run: render, store, record.
func (s *Server) runImage(ctx context.Context, kind string, p imageParams, images []string,
	model string, onProgress func(string), user *store.User) (*imageResult, error) {
	backend, err := s.pickImageBackend(ctx, kind, model)
	if err != nil {
		return nil, err
	}
	p = withDefaults(backend, p)
	seed := 0
	if p.seed != nil {
		seed = *p.seed
	} else {
		seed = randomSeed()
	}

	var inputs [][]byte
	for _, ref := range images {
		raw, err := s.loadInput(ctx, ref, user, ".png", maxInputBytes)
		if err != nil {
			return nil, err
		}
		pngRaw, err := toPNG(raw)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, pngRaw)
	}
	if onProgress != nil {
		onProgress("rendering on " + backend.slug)
	}

	var raw []byte
	switch backend.protocol {
	case "chat":
		raw, err = s.renderChat(ctx, backend, p, inputs, seed)
	case "hidream":
		raw, err = s.renderHidream(ctx, backend, p, inputs, seed)
	default:
		raw, err = s.renderImages(ctx, backend, kind, p, inputs, seed)
	}
	if err != nil {
		return nil, err
	}

	result, err := s.storeImage(raw, backend.slug, &seed)
	if err != nil {
		return nil, err
	}
	result.Steps = p.steps
	if backend.defaults != nil && backend.defaults.Preset != nil {
		result.Preset = *backend.defaults.Preset
	}
	return result, nil
}

func (s *Server) renderImages(ctx context.Context, backend imageBackend, kind string,
	p imageParams, inputs [][]byte, seed int) ([]byte, error) {
	base := s.app.InternalURL(backend.slug) + "/v1/images"
	if kind == "generate" {
		body, _ := json.Marshal(generationBody(p, seed, backend.defaults))
		req, err := s.newAppRequest(ctx, http.MethodPost, base+"/generations", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return s.openAIImage(ctx, req, backend.slug)
	}

	width, height := editSize(inputs[0])
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for i, png := range inputs {
		part, err := writer.CreateFormFile("image", fmt.Sprintf("input%d.png", i))
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(png); err != nil {
			return nil, err
		}
	}
	fields := map[string]any{
		"prompt": p.prompt, "n": "1", "size": fmt.Sprintf("%dx%d", width, height),
		"response_format": "b64_json", "output_format": "png", "seed": fmt.Sprint(seed),
	}
	if backend.defaults != nil {
		if backend.defaults.GuidanceScale != nil {
			fields["guidance_scale"] = fmt.Sprint(*backend.defaults.GuidanceScale)
		}
		if backend.defaults.TrueCFGScale != nil {
			fields["true_cfg_scale"] = fmt.Sprint(*backend.defaults.TrueCFGScale)
		}
	}
	if p.steps != nil {
		fields["num_inference_steps"] = fmt.Sprint(*p.steps)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, toString(value)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req, err := s.newAppRequest(ctx, http.MethodPost, base+"/edits", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return s.openAIImage(ctx, req, backend.slug)
}

func (s *Server) renderChat(ctx context.Context, backend imageBackend, p imageParams,
	inputs [][]byte, seed int) ([]byte, error) {
	size := openAISizes[p.aspectRatio]
	if size == [2]int{} {
		size = openAISizes["1:1"]
	}
	width, height := size[0], size[1]
	if len(inputs) > 0 {
		width, height = editSize(inputs[0])
	}
	content := []any{map[string]any{"type": "text", "text": p.prompt}}
	for _, png := range inputs {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}})
	}
	body := map[string]any{
		"messages":   []any{map[string]any{"role": "user", "content": content}},
		"modalities": []string{"image"},
		"height":     height,
		"width":      width,
		"seed":       seed,
	}
	if p.steps != nil {
		body["num_inference_steps"] = *p.steps
	}
	if backend.defaults != nil && backend.defaults.GuidanceScale != nil {
		body["cfg_scale"] = *backend.defaults.GuidanceScale
	}
	if p.negativePrompt != "" {
		body["negative_prompt"] = p.negativePrompt
	}
	raw, _ := json.Marshal(body)
	req, err := s.newAppRequest(ctx, http.MethodPost,
		s.app.InternalURL(backend.slug)+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	status, respBody, err := s.doJSON(req, nil)
	if err != nil {
		return nil, imageErrorf("%s failed: %v", backend.slug, err)
	}
	if status != http.StatusOK {
		return nil, imageErrorf("%s failed (HTTP %d): %s", backend.slug, status, truncate(string(respBody), 500))
	}
	var payload map[string]any
	_ = json.Unmarshal(respBody, &payload)
	for _, choiceAny := range asList(payload["choices"]) {
		choice, _ := choiceAny.(map[string]any)
		message, _ := choice["message"].(map[string]any)
		for _, item := range asList(message["content"]) {
			block, _ := item.(map[string]any)
			url, _ := block["image_url"].(map[string]any)
			dataURL, _ := url["url"].(string)
			if strings.HasPrefix(dataURL, "data:") {
				if idx := strings.Index(dataURL, ","); idx >= 0 {
					return base64.StdEncoding.DecodeString(dataURL[idx+1:])
				}
			}
		}
	}
	return nil, imageErrorf("%s returned no image.", backend.slug)
}

func (s *Server) renderHidream(ctx context.Context, backend imageBackend, p imageParams,
	inputs [][]byte, seed int) ([]byte, error) {
	size := openAISizes[p.aspectRatio]
	if size == [2]int{} {
		size = openAISizes["1:1"]
	}
	mode := "t2i"
	if len(inputs) == 1 {
		mode = "edit"
	} else if len(inputs) > 1 {
		mode = "subject"
	}
	body := map[string]any{"prompt": p.prompt, "mode": mode,
		"width": size[0] * 2, "height": size[1] * 2, "seed": seed}
	if len(inputs) > 0 {
		refs := make([]string, len(inputs))
		for i, png := range inputs {
			refs[i] = base64.StdEncoding.EncodeToString(png)
		}
		body["refs_b64"] = refs
		body["keep_original_aspect"] = len(inputs) == 1
	}
	raw, _ := json.Marshal(body)
	base := s.app.InternalURL(backend.slug)
	req, err := s.newAppRequest(ctx, http.MethodPost, base+"/api/generate/start", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var started map[string]any
	status, respBody, err := s.doJSON(req, &started)
	if err != nil {
		return nil, imageErrorf("%s failed: %v", backend.slug, err)
	}
	if status != http.StatusOK {
		return nil, imageErrorf("%s failed (HTTP %d): %s", backend.slug, status, truncate(string(respBody), 500))
	}
	jobID, _ := started["job_id"].(string)
	if jobID == "" {
		return nil, imageErrorf("%s did not start a job.", backend.slug)
	}
	streamReq, err := s.newAppRequest(ctx, http.MethodGet, base+"/api/generate/stream/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(streamReq)
	if err != nil {
		return nil, imageErrorf("%s failed: %v", backend.slug, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imageErrorf("%s failed (HTTP %d).", backend.slug, resp.StatusCode)
	}
	buffer := []byte{}
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			buffer = append(buffer, buf[:n]...)
			events, rest := parseSSE(buffer)
			buffer = rest
			for _, event := range events {
				kind, _ := event["type"].(string)
				if kind == "done" {
					image, _ := event["image"].(string)
					if image == "" {
						return nil, imageErrorf("%s returned no image.", backend.slug)
					}
					return base64.StdEncoding.DecodeString(image)
				}
				if kind == "error" {
					msg, _ := event["message"].(string)
					if msg == "" {
						msg = "unknown error"
					}
					return nil, imageErrorf("%s failed: %s", backend.slug, msg)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, imageErrorf("%s failed: %v", backend.slug, err)
		}
	}
	return nil, imageErrorf("%s returned no image.", backend.slug)
}

// openAIImage runs an OpenAI-images request and decodes data[0].b64_json.
func (s *Server) openAIImage(ctx context.Context, req *http.Request, slug string) ([]byte, error) {
	var payload struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	status, body, err := s.doJSON(req, &payload)
	if err != nil {
		return nil, imageErrorf("%s failed: %v", slug, err)
	}
	if status != http.StatusOK {
		return nil, imageErrorf("%s failed (HTTP %d): %s", slug, status, truncate(string(body), 500))
	}
	if len(payload.Data) == 0 || payload.Data[0].B64 == "" {
		return nil, imageErrorf("%s returned no image.", slug)
	}
	return base64.StdEncoding.DecodeString(payload.Data[0].B64)
}

// editSize is upstream edit_size: ~1 megapixel at the input's aspect, sides
// multiples of 32.
func editSize(png []byte) (int, int) {
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return 1024, 1024
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w == 0 || h == 0 {
		return 1024, 1024
	}
	scale := math.Sqrt(1024.0 * 1024.0 / float64(w*h))
	rw := roundTo(w, scale)
	rh := roundTo(h, scale)
	if rw < 256 {
		rw = 256
	}
	if rh < 256 {
		rh = 256
	}
	return rw, rh
}

func roundTo(v int, scale float64) int {
	n := int(float64(v)*scale/float64(editSizeMul)+0.5) * editSizeMul
	if n < editSizeMul {
		n = editSizeMul
	}
	return n
}

// parseSSE splits complete server-sent events out of buffer, mirroring
// image_service.parse_sse.
func parseSSE(buffer []byte) ([]map[string]any, []byte) {
	normalized := bytes.ReplaceAll(buffer, []byte("\r\n"), []byte("\n"))
	parts := bytes.Split(normalized, []byte("\n\n"))
	if len(parts) <= 1 {
		return nil, buffer
	}
	complete := parts[:len(parts)-1]
	rest := parts[len(parts)-1]
	var events []map[string]any
	for _, block := range complete {
		var data bytes.Buffer
		for _, line := range bytes.Split(block, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				data.Write(bytes.TrimSpace(line[5:]))
			}
		}
		if data.Len() == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(data.Bytes(), &event); err == nil {
			events = append(events, event)
		}
	}
	return events, rest
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}
