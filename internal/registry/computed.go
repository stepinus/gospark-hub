package registry

import (
	"math"
	"sort"
)

// TAG_CAPABILITIES maps recipe tags to API capabilities.
var tagCapabilities = map[string]string{
	"vision":     "vision",
	"multimodal": "vision",
	"video":      "video",
	"tool-use":   "tools",
	"reasoning":  "thinking",
}

// mediaCapabilitiesOrder preserves the upstream dictionary order.
var mediaCapabilitiesOrder = []struct {
	tag  string
	name string
}{
	{"text-to-image", "image-generation"},
	{"image-edit", "image-editing"},
	{"multi-image", "multi-image-input"},
	{"text-to-video", "text-to-video"},
	{"image-to-video", "image-to-video"},
	{"last-frame", "first-last-frame"},
	{"multi-reference", "multi-reference"},
	{"audio-reference", "audio-reference"},
	{"video-edit", "video-editing"},
	{"faceswap", "face-swap"},
	{"video-audio", "video-with-audio"},
	{"orbit-360", "orbit-360"},
	{"image-to-3d", "image-to-3d"},
	{"multi-view", "multi-view"},
	{"text-to-music", "music-generation"},
}

// computeDerivedFields populates MemoryGB, Capabilities, MediaCapabilities and AppURL.
// It must be called after ContextTokens and ReservedGB are already resolved.
func (r *Recipe) computeDerivedFields() {
	r.MemoryGB = math.Max(float64(r.Requirements.MinMemoryGB), r.ReservedGB)
	r.Capabilities = r.computeCapabilities()
	r.MediaCapabilities = r.computeMediaCapabilities()
	r.AppURL = r.computeAppURL()
}

func (r *Recipe) computeCapabilities() []string {
	if r.Integration == nil {
		return []string{}
	}
	found := map[string]struct{}{"completion": {}}
	for _, tag := range r.Tags {
		if c, ok := tagCapabilities[tag]; ok {
			found[c] = struct{}{}
		}
	}
	out := make([]string, 0, len(found))
	for c := range found {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (r *Recipe) computeMediaCapabilities() []string {
	out := make([]string, 0, len(mediaCapabilitiesOrder))
	for _, item := range mediaCapabilitiesOrder {
		if contains(r.Tags, item.tag) {
			out = append(out, item.name)
		}
	}
	return out
}

func (r *Recipe) computeAppURL() string {
	if r.UI.Proxy && r.UI.Type == "web" {
		return "/run/" + r.Slug + "/"
	}
	if r.UI.Proxy && r.UI.Type == "api-only" {
		return "/run/" + r.Slug + r.UI.Path
	}
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
