package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gospark-hub/internal/store"
)

// callTool runs one tool for user; failures come back as an isError result,
// never a panic or a protocol error. Mirrors upstream mcp.call_tool.
func (s *Server) callTool(ctx context.Context, name string, args map[string]any,
	origin string, user *store.User, onProgress func(string)) (out toolResult) {
	defer func() {
		if rec := recover(); rec != nil {
			switch e := rec.(type) {
			case *ImageError:
				out = textResult(e.Msg, true)
			case *WebError:
				out = textResult(e.Msg, true)
			case error:
				out = textResult(e.Error(), true)
			default:
				out = textResult(fmt.Sprint(rec), true)
			}
		}
	}()

	switch name {
	case "web_search":
		query := argString(args, "query")
		if query == "" {
			return textResult("'query' is required.", true)
		}
		return textResult(s.search(ctx, query, argInt(args, "max_results")), false)
	case "web_fetch":
		url := argString(args, "url")
		if url == "" {
			return textResult("'url' is required.", true)
		}
		text, err := s.fetch(ctx, url, user)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return textResult(text, false)
	case "list_image_models":
		models, err := s.listImageModels(ctx)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return textResult(s.withMemory(ctx, models), false)
	case "list_video_models":
		models, err := s.listVideoModels(ctx)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return textResult(s.withMemory(ctx, models), false)
	case "list_3d_models":
		models, err := s.list3DModels(ctx)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return textResult(s.withMemory(ctx, models), false)
	case "list_decision_models":
		models, err := s.listDecisionModels(ctx)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return textResult(s.withMemory(ctx, models), false)
	case "create_upload", "create_download":
		return s.linkTool(name, args, origin, user)
	case "delete_file":
		fileName, err := s.ownFile(args, user)
		if err != nil {
			return textResult(err.Error(), true)
		}
		s.media.Delete(fileName, user)
		return toolResult{
			Content:           []any{textBlock{Type: "text", Text: "Deleted " + fileName + "."}},
			StructuredContent: newOrderedMap().Set("deleted", fileName),
			IsError:           false,
		}
	case "decide":
		answer, err := s.decide(ctx, args, user)
		if err != nil {
			return textResult(err.Error(), true)
		}
		raw, _ := json.MarshalIndent(answer, "", "  ")
		return toolResult{
			Content:           []any{textBlock{Type: "text", Text: string(raw)}},
			StructuredContent: answer,
			IsError:           false,
		}
	case "start_model":
		return s.startModel(ctx, args, origin, onProgress, user)
	case "stop_model":
		return s.stopModel(ctx, args)
	case "generate_music":
		lyrics := argString(args, "lyrics")
		style := argString(args, "style")
		if lyrics == "" || style == "" {
			return textResult("'lyrics' and 'style' are both required.", true)
		}
		backend, err := s.pickAudioBackend(ctx, argString(args, "model"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		jobID := s.jobs.start("music", backend.slug, userIDOf(user), func() (any, error) {
			return s.generateMusic(context.Background(), args, user)
		})
		return s.started("get_music", jobID, backend.slug, nil)
	case "get_image", "get_music":
		info, err := s.jobs.check(requiredJobID(args), CheckWait*time.Second, userIDOf(user), s.cfg.AuthEnabled)
		if err != nil {
			return textResult(err.Error(), true)
		}
		if info.Kind == "music" {
			return s.musicResult(info, origin)
		}
		return s.imageResult(info, origin)
	case "get_3d":
		info, err := s.jobs.check(requiredJobID(args), CheckWait*time.Second, userIDOf(user), s.cfg.AuthEnabled)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return s.meshResult(info, origin)
	case "get_video":
		info, err := s.checkVideo(ctx, requiredJobID(args), CheckWait*time.Second, onProgress, user)
		if err != nil {
			return textResult(err.Error(), true)
		}
		return s.videoResult(*info, origin)
	case "generate_3d":
		imageRef := argString(args, "image")
		if imageRef == "" {
			return textResult("'image' is required: the picture to build the model from.", true)
		}
		views := map[string]string{}
		if raw, ok := args["views"].(map[string]any); ok {
			for k, v := range raw {
				if sv, ok := v.(string); ok && strings.TrimSpace(sv) != "" {
					views[k] = strings.TrimSpace(sv)
				}
			}
		} else if raw, ok := args["views"]; ok && raw != nil {
			return textResult("'views' must be an object: {\"left\": url, ...}.", true)
		}
		backend, err := s.pickMeshBackend(ctx, argString(args, "model"), len(views) > 0)
		if err != nil {
			return textResult(err.Error(), true)
		}
		jobID := s.jobs.start("mesh", backend.slug, userIDOf(user), func() (any, error) {
			return s.generateMesh(context.Background(), args, user)
		})
		return s.started("get_3d", jobID, backend.slug, nil)
	case "generate_video":
		prompt := argString(args, "prompt")
		if prompt == "" {
			return textResult("'prompt' is required.", true)
		}
		info, err := s.generateVideo(ctx, args, user)
		if err != nil {
			return textResult(err.Error(), true)
		}
		extra := newOrderedMap()
		if info.Seconds != nil {
			extra.Set("seconds", info.Seconds)
		}
		if info.Size != "" {
			extra.Set("size", info.Size)
		}
		if info.Seed != nil {
			extra.Set("seed", *info.Seed)
		}
		if info.Steps != nil {
			extra.Set("steps", *info.Steps)
		}
		return s.started("get_video", info.JobID, info.Model, extra)
	}

	// generate_image / edit_image (and any future image tool).
	prompt := argString(args, "prompt")
	if prompt == "" {
		return textResult("'prompt' is required.", true)
	}
	params := imageParams{
		prompt:         prompt,
		negativePrompt: argString(args, "negative_prompt"),
		aspectRatio:    argString(args, "aspect_ratio"),
		seed:           argInt(args, "seed"),
		steps:          argInt(args, "steps"),
	}
	if params.aspectRatio == "" {
		params.aspectRatio = "1:1"
	}
	kind := "generate"
	images := []string{}
	if name == "edit_image" {
		kind = "edit"
		images = argStringList(args, "images")
		if len(images) < 1 || len(images) > 3 {
			return textResult("'images' must list one to three images.", true)
		}
	}
	backend, err := s.pickImageBackend(ctx, kind, argString(args, "model"))
	if err != nil {
		return textResult(err.Error(), true)
	}
	jobID := s.jobs.start(kind, backend.slug, userIDOf(user), func() (any, error) {
		return s.runImage(context.Background(), kind, params, images, backend.slug, nil, user)
	})
	return s.started("get_image", jobID, backend.slug, nil)
}

// requiredJobID is upstream _job_id: a missing id is an ImageError.
func requiredJobID(args map[string]any) string {
	jobID := argString(args, "job_id")
	if jobID == "" {
		panic(imageErrorf("'job_id' is required."))
	}
	return jobID
}

// --- result builders --------------------------------------------------------

// notDone is upstream _not_done.
func (s *Server) notDone(tool, jobID, status string, info *orderedMap) toolResult {
	text := fmt.Sprintf("Not done yet: %s. This is a job, not the result. Now call %s "+
		"with job_id %s, and keep calling it until it returns the result "+
		"or an error. Do not start the render again.", status, tool, jobID)
	structured := newOrderedMap()
	structured.Set("status", "rendering")
	if info != nil {
		for _, k := range info.keys {
			structured.Set(k, info.vals[k])
		}
	}
	structured.Set("job_id", jobID)
	structured.Set("done", false)
	structured.Set("next_call", newOrderedMap().
		Set("tool", tool).
		Set("arguments", newOrderedMap().Set("job_id", jobID)))
	return toolResult{
		Content:           []any{textBlock{Type: "text", Text: text}},
		StructuredContent: structured,
		IsError:           false,
	}
}

// started is upstream _started.
func (s *Server) started(tool, jobID, model string, info *orderedMap) toolResult {
	merged := newOrderedMap()
	if info != nil {
		for _, k := range info.keys {
			merged.Set(k, info.vals[k])
		}
	}
	merged.Set("model", model)
	return s.notDone(tool, jobID, fmt.Sprintf("job %s started on %s", jobID, model), merged)
}

// imageResult is upstream _image_result.
func (s *Server) imageResult(info jobInfo, origin string) toolResult {
	if info.Status == "failed" {
		msg := info.Err
		if msg == "" {
			msg = fmt.Sprintf("Image job %s failed.", info.JobID)
		}
		return textResult(msg, true)
	}
	if info.Status != "completed" {
		extra := newOrderedMap()
		extra.Set("model", info.Model)
		extra.Set("kind", info.Kind)
		if info.Err != "" {
			extra.Set("error", info.Err)
		} else {
			extra.Set("error", nil)
		}
		extra.Set("elapsed", int(info.Elapsed))
		return s.notDone("get_image", info.JobID,
			fmt.Sprintf("still rendering on %s (%ds so far)", info.Model, int(info.Elapsed)), extra)
	}
	result, _ := info.Result.(*imageResult)
	if result == nil {
		return textResult(fmt.Sprintf("Image job %s failed.", info.JobID), true)
	}
	url := origin + result.path()
	lines := []string{
		"Image: " + url,
		"Model: " + result.Model,
		fmt.Sprintf("Size: %dx%d", result.Width, result.Height),
	}
	if result.Seed != nil {
		lines = append(lines, fmt.Sprintf("Seed: %d", *result.Seed))
	}
	if result.Steps != nil {
		lines = append(lines, fmt.Sprintf("Steps: %d", *result.Steps))
	}
	if result.Preset != "" {
		lines = append(lines, "Preset: "+result.Preset)
	}
	lines = append(lines,
		"Pass this URL to edit_image to change it further.",
		"To save it into your workspace, call create_download with this URL; "+
			"the attached JPEG is a small preview, not the file.")
	structured := newOrderedMap()
	structured.Set("url", url)
	structured.Set("model", result.Model)
	structured.Set("width", result.Width)
	structured.Set("height", result.Height)
	structured.Set("seed", result.Seed)
	structured.Set("steps", result.Steps)
	return toolResult{
		Content: []any{
			textBlock{Type: "text", Text: strings.Join(lines, "\n")},
			imageBlock{Type: "image", Data: result.PreviewJPEG, MimeType: "image/jpeg"},
		},
		StructuredContent: structured,
		IsError:           false,
	}
}

// musicResult is upstream _music_result.
func (s *Server) musicResult(info jobInfo, origin string) toolResult {
	if info.Status == "failed" {
		msg := info.Err
		if msg == "" {
			msg = fmt.Sprintf("Music job %s failed.", info.JobID)
		}
		return textResult(msg, true)
	}
	if info.Status != "completed" {
		extra := newOrderedMap()
		extra.Set("model", info.Model)
		extra.Set("kind", info.Kind)
		extra.Set("elapsed", int(info.Elapsed))
		return s.notDone("get_music", info.JobID,
			fmt.Sprintf("still composing on %s (%ds so far)", info.Model, int(info.Elapsed)), extra)
	}
	song, _ := info.Result.(*audioResult)
	if song == nil {
		return textResult(fmt.Sprintf("Music job %s failed.", info.JobID), true)
	}
	url := origin + "/audio/" + song.AudioID + ".wav"
	lines := []string{
		"Music: " + url,
		"Model: " + song.Model,
		fmt.Sprintf("Length: %gs", song.Seconds),
		fmt.Sprintf("Seed: %d", song.Seed),
	}
	structured := newOrderedMap()
	structured.Set("audio_id", song.AudioID)
	structured.Set("model", song.Model)
	structured.Set("seconds", song.Seconds)
	structured.Set("seed", song.Seed)
	structured.Set("url", url)
	return toolResult{
		Content:           []any{textBlock{Type: "text", Text: strings.Join(lines, "\n")}},
		StructuredContent: structured,
		IsError:           false,
	}
}

// meshResult is upstream _mesh_result.
func (s *Server) meshResult(info jobInfo, origin string) toolResult {
	if info.Status == "failed" {
		msg := info.Err
		if msg == "" {
			msg = fmt.Sprintf("3D job %s failed.", info.JobID)
		}
		return textResult(msg, true)
	}
	if info.Status != "completed" {
		extra := newOrderedMap()
		extra.Set("model", info.Model)
		extra.Set("kind", info.Kind)
		extra.Set("elapsed", int(info.Elapsed))
		return s.notDone("get_3d", info.JobID,
			fmt.Sprintf("still building on %s (%ds so far)", info.Model, int(info.Elapsed)), extra)
	}
	mesh, _ := info.Result.(*meshResult)
	if mesh == nil {
		return textResult(fmt.Sprintf("3D job %s failed.", info.JobID), true)
	}
	url := origin + "/meshes/" + mesh.MeshID + ".glb"
	lines := []string{
		"3D model (GLB): " + url,
		"Model: " + mesh.Model,
		fmt.Sprintf("Seed: %d", mesh.Seed),
		fmt.Sprintf("File size: %.1f MB", float64(mesh.Bytes)/1e6),
	}
	if mesh.InferenceTimeS != nil {
		lines = append(lines, fmt.Sprintf("Build time: %ds", int(*mesh.InferenceTimeS+0.5)))
	}
	lines = append(lines, "The user can turn it around in the Hub's My files. To save it into your "+
		"workspace, call create_download with this URL; the attached JPEG is the cut-out "+
		"picture it was built from, not a render of the model.")
	structured := newOrderedMap()
	structured.Set("mesh_id", mesh.MeshID)
	structured.Set("model", mesh.Model)
	structured.Set("seed", mesh.Seed)
	structured.Set("steps", mesh.Steps)
	structured.Set("views", stringsToAny(mesh.Views))
	structured.Set("bytes", mesh.Bytes)
	structured.Set("inference_time_s", mesh.InferenceTimeS)
	structured.Set("url", url)
	return toolResult{
		Content: []any{
			textBlock{Type: "text", Text: strings.Join(lines, "\n")},
			imageBlock{Type: "image", Data: mesh.Preview, MimeType: "image/jpeg"},
		},
		StructuredContent: structured,
		IsError:           false,
	}
}

// videoResult is upstream _video_result.
func (s *Server) videoResult(info videoInfo, origin string) toolResult {
	if info.Status == "failed" {
		return textResult(fmt.Sprintf("Video job %s failed on %s: %s", info.JobID, info.Model, info.Err), true)
	}
	extra := newOrderedMap()
	extra.Set("job_id", info.JobID)
	extra.Set("model", info.Model)
	if info.Seconds != nil {
		extra.Set("seconds", info.Seconds)
	}
	if info.Size != "" {
		extra.Set("size", info.Size)
	}
	if info.Seed != nil {
		extra.Set("seed", *info.Seed)
	}
	if info.Steps != nil {
		extra.Set("steps", *info.Steps)
	}
	if info.Status != "completed" {
		extra.Set("status", info.Status)
		progress := info.Progress
		if progress == nil {
			progress = 0
		}
		extra.Set("progress", progress)
		return s.notDone("get_video", info.JobID,
			fmt.Sprintf("still rendering on %s (%s, %v%% done)", info.Model, info.Status, progress), extra)
	}
	url := origin + "/videos/" + info.VideoID + ".mp4"
	lines := []string{"Video: " + url, "Model: " + info.Model}
	for _, k := range []string{"size", "seconds", "seed", "steps"} {
		if v := extra.vals[k]; v != nil {
			lines = append(lines, strings.ToUpper(k[:1])+k[1:]+": "+toString(v))
		}
	}
	if info.InferenceTimeS != nil {
		lines = append(lines, fmt.Sprintf("Render time: %ds", int(*info.InferenceTimeS+0.5)))
	}
	save := "To save it into your workspace, call create_download with this URL"
	if info.Poster != "" {
		lines = append(lines, save+"; the attached JPEG is only a poster frame, not the video.")
	} else {
		lines = append(lines, save+".")
	}
	content := []any{textBlock{Type: "text", Text: strings.Join(lines, "\n")}}
	if info.Poster != "" {
		content = append(content, imageBlock{Type: "image", Data: info.Poster, MimeType: "image/jpeg"})
	}
	structured := newOrderedMap()
	structured.Set("job_id", info.JobID)
	structured.Set("model", info.Model)
	if info.Seconds != nil {
		structured.Set("seconds", info.Seconds)
	}
	if info.Size != "" {
		structured.Set("size", info.Size)
	}
	if info.Seed != nil {
		structured.Set("seed", *info.Seed)
	}
	if info.Steps != nil {
		structured.Set("steps", *info.Steps)
	}
	structured.Set("status", "completed")
	structured.Set("video_id", info.VideoID)
	if info.InferenceTimeS != nil {
		structured.Set("inference_time_s", *info.InferenceTimeS)
	}
	structured.Set("url", url)
	return toolResult{Content: content, StructuredContent: structured, IsError: false}
}
