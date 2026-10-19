package registry

import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// parseRecipe converts a recipe.yaml body into a Recipe value.
// Unknown keys are ignored, and the small set of weak-typing rules that
// Pydantic applies (e.g. params_b: "7" -> 7.0) are reproduced.
func parseRecipe(data []byte, dir string) (Recipe, error) {
	raw := make(map[string]any)
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Recipe{}, fmt.Errorf("unmarshal yaml: %w", err)
	}

	r := Recipe{
		Name:        stringField(raw, "name", ""),
		Slug:        stringField(raw, "slug", ""),
		Version:     stringField(raw, "version", "1.0.0"),
		Description: stringField(raw, "description", ""),
		Author:      stringField(raw, "author", ""),
		Website:     stringField(raw, "website", ""),
		Upstream:    stringField(raw, "upstream", ""),
		Fork:        stringField(raw, "fork", ""),
		Category:    stringField(raw, "category", "llm"),
		Categories:  stringSlice(raw, "categories"),
		Tags:        stringSlice(raw, "tags"),
		Icon:        stringField(raw, "icon", ""),
		Logo:        stringField(raw, "logo", ""),
		Cover:       parseCover(raw["cover"]),
		// Upstream reads these from the `requirements` block, not the top level.
		Requirements: RecipeRequirements{
			MinMemoryGB:         intField(nestedMap(raw, "requirements"), "min_memory_gb", 8),
			RecommendedMemoryGB: intPtrField(nestedMap(raw, "requirements"), "recommended_memory_gb"),
			DiskGB:              intField(nestedMap(raw, "requirements"), "disk_gb", 10),
			CudaCompute:         stringField(nestedMap(raw, "requirements"), "cuda_compute", "12.1"),
		},
		UI: RecipeUI{
			Type:        stringField(nestedMap(raw, "ui"), "type", "web"),
			Port:        intField(nestedMap(raw, "ui"), "port", 8080),
			Path:        stringField(nestedMap(raw, "ui"), "path", "/"),
			HealthPath:  stringPtrField(nestedMap(raw, "ui"), "health_path"),
			Proxy:       boolField(nestedMap(raw, "ui"), "proxy", false),
			StripPrefix: boolField(nestedMap(raw, "ui"), "strip_prefix", true),
		},
		Docker: RecipeDocker{
			Build: boolField(nestedMap(raw, "docker"), "build", false),
			GPU:   boolField(nestedMap(raw, "docker"), "gpu", true),
		},
		Integration:             parseIntegration(raw["integration"]),
		ImageDefaults:           parseImageDefaults(raw["image_defaults"]),
		VideoDefaults:           parseVideoDefaults(raw["video_defaults"]),
		AudioDefaults:           parseAudioDefaults(raw["audio_defaults"]),
		Source:                  stringField(raw, "source", "community"),
		Status:                  stringField(raw, "status", "experimental"),
		ReleaseDate:             stringField(raw, "release_date", ""),
		Engine:                  stringField(raw, "engine", ""),
		ParamsB:                 floatPtrField(raw, "params_b"),
		ActiveParamsB:           floatPtrField(raw, "active_params_b"),
		Arch:                    stringField(raw, "arch", ""),
		Quantization:            stringField(raw, "quantization", ""),
		SpeculativeMethod:       stringField(raw, "speculative_method", ""),
		ArtificialAnalysisIndex: floatPtrField(raw, "artificial_analysis_index"),
		WeightsGB:               floatPtrField(raw, "weights_gb"),
		DependsOn:               stringSlice(raw, "depends_on"),
		RequiresHFToken:         boolField(raw, "requires_hf_token", false),
		GatedRepos:              stringSlice(raw, "gated_repos"),
		TokensPerSecond:         floatPtrField(raw, "tokens_per_second"),
		TokensPerSecondEditing:  floatPtrField(raw, "tokens_per_second_editing"),
		EditingWorkload:         stringField(raw, "editing_workload", ""),
		Benchmarks:              stringFloatMap(raw, "benchmarks"),
		RenderSeconds:           floatPtrField(raw, "render_seconds"),
		RenderWorkload:          stringField(raw, "render_workload", ""),
		LoadSeconds:             floatPtrField(raw, "load_seconds"),
		Arena:                   parseArena(raw["arena"]),
		ContextTokens:           intPtrField(raw, "context_tokens"),
		ReservedGB:              0.0,
		Kind:                    stringField(raw, "kind", "container"),
		Native:                  parseNative(raw["native"], intField(nestedMap(raw, "ui"), "port", 8080)),
	}

	// context_tokens: first the raw context_length key (unknown to Pydantic),
	// then the compose file.
	if r.ContextTokens == nil {
		if v := raw["context_length"]; v != nil {
			r.ContextTokens = intPtrValue(v)
		}
	}
	if r.ContextTokens == nil {
		r.ContextTokens = contextFromCompose(dir)
	}

	if r.Name == "" || r.Slug == "" {
		return Recipe{}, fmt.Errorf("name and slug are required")
	}

	r.ReservedGB = reservedFromCompose(dir)
	r.computeDerivedFields()
	return r, nil
}

func parseCover(v any) RecipeCover {
	m, ok := v.(map[string]any)
	if !ok {
		return RecipeCover{Fit: "cover", FocusX: 0.5, FocusY: 0.5, Grade: true}
	}
	return RecipeCover{
		Image:       stringField(m, "image", ""),
		Caption:     stringField(m, "caption", ""),
		Fit:         stringField(m, "fit", "cover"),
		BackdropFit: stringField(m, "backdrop_fit", ""),
		FocusX:      floatField(m, "focus_x", 0.5),
		FocusY:      floatField(m, "focus_y", 0.5),
		Credit:      stringField(m, "credit", ""),
		Source:      stringField(m, "source", ""),
		Grade:       boolField(m, "grade", true),
		Backdrop:    parseBackdrop(m["backdrop"]),
	}
}

func parseBackdrop(v any) *RecipeBackdrop {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RecipeBackdrop{
		Image:  stringField(m, "image", ""),
		Fit:    stringField(m, "fit", ""),
		FocusX: floatPtrField(m, "focus_x"),
		FocusY: floatPtrField(m, "focus_y"),
	}
}

func parseIntegration(v any) *RecipeIntegration {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RecipeIntegration{
		APIURL:          stringField(m, "api_url", ""),
		ModelID:         stringField(m, "model_id", ""),
		APIKey:          stringField(m, "api_key", ""),
		MaxContext:      stringField(m, "max_context", ""),
		MaxOutputTokens: stringField(m, "max_output_tokens", ""),
		CurlExample:     stringField(m, "curl_example", ""),
	}
}

func parseImageDefaults(v any) *RecipeImageDefaults {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RecipeImageDefaults{
		Steps:         intPtrField(m, "steps"),
		GuidanceScale: floatPtrField(m, "guidance_scale"),
		TrueCFGScale:  floatPtrField(m, "true_cfg_scale"),
		Preset:        stringPtrField(m, "preset"),
		Notes:         stringField(m, "notes", ""),
		Source:        stringField(m, "source", ""),
	}
}

func parseVideoDefaults(v any) *RecipeVideoDefaults {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RecipeVideoDefaults{
		Steps:           intPtrField(m, "steps"),
		GuidanceScale:   floatPtrField(m, "guidance_scale"),
		FPS:             intPtrField(m, "fps"),
		Seconds:         intPtrField(m, "seconds"),
		NumFrames:       intPtrField(m, "num_frames"),
		Size:            stringPtrField(m, "size"),
		FlowShift:       floatPtrField(m, "flow_shift"),
		ExtraParams:     anyMap(m, "extra_params"),
		SendAspectRatio: boolField(m, "send_aspect_ratio", false),
		AspectRatios:    stringSliceOrNil(m, "aspect_ratios"),
		MaxSeconds:      intPtrField(m, "max_seconds"),
		Notes:           stringField(m, "notes", ""),
		Source:          stringField(m, "source", ""),
	}
}

func parseAudioDefaults(v any) *RecipeAudioDefaults {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RecipeAudioDefaults{
		Seconds: intPtrField(m, "seconds"),
		Notes:   stringField(m, "notes", ""),
		Source:  stringField(m, "source", ""),
	}
}

// parseNative reads the `native:` block. An absent block means a container
// recipe (nil), and a missing port falls back to the UI port, which is what
// the Caddy upstream points at.
func parseNative(v any, defaultPort int) *RecipeNative {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RecipeNative{
		Engine:      stringField(m, "engine", ""),
		Model:       stringField(m, "model", ""),
		Args:        stringSlice(m, "args"),
		Env:         stringMap(m, "env"),
		Port:        intField(m, "port", defaultPort),
		EngineBuild: parseEngineBuild(m["engine_build"]),
	}
}

// parseEngineBuild reads `native.engine_build`: the image holding the patched
// engine, the artifacts copied out of it, the optional build script and the
// env the client needs when it runs. An absent block means "run the engine
// release under current", which is what every recipe did before.
func parseEngineBuild(v any) *RecipeEngineBuild {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &RecipeEngineBuild{
		Image:     stringField(m, "image", ""),
		Artifacts: stringSlice(m, "artifacts"),
		Command:   stringField(m, "command", ""),
		Env:       stringMap(m, "env"),
	}
}

func parseArena(v any) []RecipeArenaScore {
	list, ok := v.([]any)
	if !ok {
		return []RecipeArenaScore{}
	}
	out := make([]RecipeArenaScore, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, RecipeArenaScore{
			Board: stringField(m, "board", ""),
			Task:  stringField(m, "task", ""),
			Elo:   floatField(m, "elo", 0),
			Entry: stringField(m, "entry", ""),
			Votes: intPtrField(m, "votes"),
			Read:  stringField(m, "read", ""),
			URL:   stringField(m, "url", ""),
			Base:  boolField(m, "base", false),
		})
	}
	return out
}

// --- coercion helpers --------------------------------------------------------

func nestedMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if mm, ok := v.(map[string]any); ok {
			return mm
		}
	}
	return map[string]any{}
}

func stringField(m map[string]any, key, def string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func stringPtrField(m map[string]any, key string) *string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return &s
	}
	s := fmt.Sprint(v)
	return &s
}

func intField(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	if n, ok := v.(int); ok {
		return n
	}
	return intValue(v, def)
}

func intPtrField(m map[string]any, key string) *int {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	return intPtrValue(v)
}

func intPtrValue(v any) *int {
	if n, ok := v.(int); ok {
		return &n
	}
	n := intValue(v, 0)
	return &n
}

func intValue(v any, def int) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return def
}

func floatField(m map[string]any, key string, def float64) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	return floatValue(v, def)
}

func floatPtrField(m map[string]any, key string) *float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	return floatPtrValue(v)
}

func floatPtrValue(v any) *float64 {
	f := floatValue(v, 0)
	return &f
}

func floatValue(v any, def float64) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return def
}

func boolField(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// stringSliceOrNil keeps "absent" distinct from "empty": upstream models this
// field as Optional, so null and [] are different answers.
func stringSliceOrNil(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	return stringSlice(m, key)
}

func stringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return []string{}
	}
	list, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringMap(m map[string]any, key string) map[string]string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	src, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, val := range src {
		out[k] = fmt.Sprint(val)
	}
	return out
}

func stringFloatMap(m map[string]any, key string) map[string]float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return map[string]float64{}
	}
	src, ok := v.(map[string]any)
	if !ok {
		return map[string]float64{}
	}
	out := make(map[string]float64, len(src))
	for k, val := range src {
		out[k] = floatValue(val, 0)
	}
	return out
}

func anyMap(m map[string]any, key string) map[string]any {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	if mm, ok := v.(map[string]any); ok {
		return mm
	}
	return nil
}
