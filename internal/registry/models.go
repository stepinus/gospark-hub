package registry

import "encoding/json"

// Recipe mirrors daemon/models/recipe.py:Recipe as closely as possible.
// Computed fields are populated by the loader so the JSON response is a
// plain struct marshal with no extra logic in the HTTP handler.
type Recipe struct {
	Name                    string               `json:"name"`
	Slug                    string               `json:"slug"`
	Version                 string               `json:"version"`
	Description             string               `json:"description"`
	Author                  string               `json:"author"`
	Website                 string               `json:"website"`
	Upstream                string               `json:"upstream"`
	Fork                    string               `json:"fork"`
	Category                string               `json:"category"`
	Categories              []string             `json:"categories"`
	Tags                    []string             `json:"tags"`
	Icon                    string               `json:"icon"`
	Logo                    string               `json:"logo"`
	Cover                   RecipeCover          `json:"cover"`
	Requirements            RecipeRequirements   `json:"requirements"`
	UI                      RecipeUI             `json:"ui"`
	Docker                  RecipeDocker         `json:"docker"`
	Integration             *RecipeIntegration   `json:"integration"`
	ImageDefaults           *RecipeImageDefaults `json:"image_defaults"`
	VideoDefaults           *RecipeVideoDefaults `json:"video_defaults"`
	AudioDefaults           *RecipeAudioDefaults `json:"audio_defaults"`
	Source                  string               `json:"source"`
	Status                  string               `json:"status"`
	ReleaseDate             string               `json:"release_date"`
	Engine                  string               `json:"engine"`
	ParamsB                 *float64             `json:"params_b"`
	ActiveParamsB           *float64             `json:"active_params_b"`
	Arch                    string               `json:"arch"`
	Quantization            string               `json:"quantization"`
	SpeculativeMethod       string               `json:"speculative_method"`
	ArtificialAnalysisIndex *float64             `json:"artificial_analysis_index"`
	WeightsGB               *float64             `json:"weights_gb"`
	DependsOn               []string             `json:"depends_on"`
	RequiresHFToken         bool                 `json:"requires_hf_token"`
	GatedRepos              []string             `json:"gated_repos"`
	RuntimeEnvPath          string               `json:"runtime_env_path"`
	TokensPerSecond         *float64             `json:"tokens_per_second"`
	TokensPerSecondEditing  *float64             `json:"tokens_per_second_editing"`
	EditingWorkload         string               `json:"editing_workload"`
	Benchmarks              map[string]float64   `json:"benchmarks"`
	RenderSeconds           *float64             `json:"render_seconds"`
	RenderWorkload          string               `json:"render_workload"`
	LoadSeconds             *float64             `json:"load_seconds"`
	Arena                   []RecipeArenaScore   `json:"arena"`
	ContextTokens           *int                 `json:"context_tokens"`
	ReservedGB              float64              `json:"reserved_gb"`

	// Kind is "container" (the default) or "native". It is serialized only
	// for native recipes so container recipes keep upstream's JSON.
	Kind string `json:"kind,omitempty"`
	// Native is the launch config for a native recipe; nil for containers.
	Native *RecipeNative `json:"native,omitempty"`

	// Computed fields (Pydantic computed_field in the upstream model).
	MemoryGB          float64  `json:"memory_gb"`
	Capabilities      []string `json:"capabilities"`
	MediaCapabilities []string `json:"media_capabilities"`
	AppURL            string   `json:"app_url"`

	// Runtime state populated by the HTTP handler on every request.
	Installed    bool    `json:"installed"`
	Running      bool    `json:"running"`
	Ready        bool    `json:"ready"`
	Starting     bool    `json:"starting"`
	Installing   bool    `json:"installing"`
	HasLeftovers bool    `json:"has_leftovers"`
	Error        *string `json:"error"`
}

// IsLLM is the upstream is_llm property; it is intentionally not serialized.
func (r Recipe) IsLLM() bool {
	return r.Category == "llm" && r.UI.Type == "api-only"
}

// IsNative reports whether the recipe runs on a host engine instead of in a
// container (no docker-compose.yml, a `native:` block).
func (r Recipe) IsNative() bool { return r.Kind == "native" }

// MarshalJSON keeps the wire format byte-compatible with upstream: `kind` and
// `native` are native-only fields, even though the Go value carries the
// canonical "container" for ordinary recipes. The alias type is load-bearing —
// marshaling Recipe itself would recurse forever.
func (r Recipe) MarshalJSON() ([]byte, error) {
	type alias Recipe
	a := alias(r)
	if !r.IsNative() {
		a.Kind = ""
		a.Native = nil
	}
	return json.Marshal(a)
}

// RecipeNative is the `native:` block: which host engine serves the recipe,
// where its checkpoint is and which flags the launch adds.
//
// Port is the host port the app listens on; it defaults to ui.port.
type RecipeNative struct {
	Engine string            `json:"engine"`
	Model  string            `json:"model"`
	Args   []string          `json:"args,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Port   int               `json:"port,omitempty"`
}

type RecipeRequirements struct {
	MinMemoryGB         int    `json:"min_memory_gb"`
	RecommendedMemoryGB *int   `json:"recommended_memory_gb"`
	DiskGB              int    `json:"disk_gb"`
	CudaCompute         string `json:"cuda_compute"`
}

type RecipeUI struct {
	Type        string  `json:"type"`
	Port        int     `json:"port"`
	Path        string  `json:"path"`
	HealthPath  *string `json:"health_path"`
	Proxy       bool    `json:"proxy"`
	StripPrefix bool    `json:"strip_prefix"`
}

type RecipeDocker struct {
	Build bool `json:"build"`
	GPU   bool `json:"gpu"`
}

type RecipeIntegration struct {
	APIURL          string `json:"api_url"`
	ModelID         string `json:"model_id"`
	APIKey          string `json:"api_key"`
	MaxContext      string `json:"max_context"`
	MaxOutputTokens string `json:"max_output_tokens"`
	CurlExample     string `json:"curl_example"`
}

type RecipeImageDefaults struct {
	Steps         *int     `json:"steps"`
	GuidanceScale *float64 `json:"guidance_scale"`
	TrueCFGScale  *float64 `json:"true_cfg_scale"`
	Preset        *string  `json:"preset"`
	Notes         string   `json:"notes"`
	Source        string   `json:"source"`
}

type RecipeVideoDefaults struct {
	Steps           *int           `json:"steps"`
	GuidanceScale   *float64       `json:"guidance_scale"`
	FPS             *int           `json:"fps"`
	Seconds         *int           `json:"seconds"`
	NumFrames       *int           `json:"num_frames"`
	Size            *string        `json:"size"`
	FlowShift       *float64       `json:"flow_shift"`
	ExtraParams     map[string]any `json:"extra_params"`
	SendAspectRatio bool           `json:"send_aspect_ratio"`
	AspectRatios    []string       `json:"aspect_ratios"`
	MaxSeconds      *int           `json:"max_seconds"`
	Notes           string         `json:"notes"`
	Source          string         `json:"source"`
}

type RecipeAudioDefaults struct {
	Seconds *int   `json:"seconds"`
	Notes   string `json:"notes"`
	Source  string `json:"source"`
}

type RecipeBackdrop struct {
	Image  string   `json:"image"`
	Fit    string   `json:"fit"`
	FocusX *float64 `json:"focus_x"`
	FocusY *float64 `json:"focus_y"`
}

type RecipeCover struct {
	Image       string          `json:"image"`
	Caption     string          `json:"caption"`
	Fit         string          `json:"fit"`
	BackdropFit string          `json:"backdrop_fit"`
	FocusX      float64         `json:"focus_x"`
	FocusY      float64         `json:"focus_y"`
	Credit      string          `json:"credit"`
	Source      string          `json:"source"`
	Grade       bool            `json:"grade"`
	Backdrop    *RecipeBackdrop `json:"backdrop"`
}

type RecipeArenaScore struct {
	Board string  `json:"board"`
	Task  string  `json:"task"`
	Elo   float64 `json:"elo"`
	Entry string  `json:"entry"`
	Votes *int    `json:"votes"`
	Read  string  `json:"read"`
	URL   string  `json:"url"`
	Base  bool    `json:"base"`
}
