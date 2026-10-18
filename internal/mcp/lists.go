package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"

	"gospark-hub/internal/registry"
)

// memoryFigures reads MemTotal and MemAvailable in GiB from /proc/meminfo.
func memoryFigures() (totalGB, freeGB float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			totalGB = val / (1 << 20)
		case "MemAvailable":
			freeGB = val / (1 << 20)
		}
	}
	return totalGB, freeGB
}

// mediaFigures is upstream models.recipe.media_figures.
func mediaFigures(r registry.Recipe) *orderedMap {
	out := newOrderedMap()
	if r.RenderSeconds != nil && *r.RenderSeconds != 0 {
		out.Set("render_seconds", *r.RenderSeconds)
	}
	if r.RenderWorkload != "" {
		out.Set("render_workload", r.RenderWorkload)
	}
	if r.LoadSeconds != nil && *r.LoadSeconds != 0 {
		out.Set("load_seconds", *r.LoadSeconds)
	}
	if len(r.Arena) > 0 {
		arena := make([]any, 0, len(r.Arena))
		for _, a := range r.Arena {
			arena = append(arena, arenaScore(a))
		}
		out.Set("arena", arena)
	}
	if len(out.keys) == 0 {
		return nil
	}
	return out
}

// arenaScore is RecipeArenaScore.model_dump(exclude_defaults=True).
func arenaScore(a registry.RecipeArenaScore) map[string]any {
	out := map[string]any{}
	if a.Board != "" {
		out["board"] = a.Board
	}
	if a.Task != "" {
		out["task"] = a.Task
	}
	if a.Elo != 0 {
		out["elo"] = a.Elo
	}
	if a.Entry != "" {
		out["entry"] = a.Entry
	}
	if a.Votes != nil {
		out["votes"] = *a.Votes
	}
	if a.Read != "" {
		out["read"] = a.Read
	}
	if a.URL != "" {
		out["url"] = a.URL
	}
	if a.Base {
		out["base"] = true
	}
	return out
}

// stateOf is the shared running/ready state label.
func (s *Server) stateOf(ctx context.Context, slug string) string {
	running, err := s.rt.Running(ctx, slug)
	if err != nil || !running {
		return "stopped"
	}
	if s.rt.Ready(slug) {
		return "ready"
	}
	return "starting"
}

// listImageModels is upstream image_service.list_models.
func (s *Server) listImageModels(ctx context.Context) ([]any, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, b := range s.imageBackends() {
		if !installed[b.slug] {
			continue
		}
		recipe, ok := s.recipe(b.slug)
		if !ok {
			continue
		}
		m := newOrderedMap()
		m.Set("model", b.slug)
		m.Set("name", recipe.Name)
		m.Set("tools", stringsToAny(b.tools()))
		m.Set("summary", b.summary)
		m.Set("state", s.stateOf(ctx, b.slug))
		m.Set("min_memory_gb", recipe.Requirements.MinMemoryGB)
		mergeFigures(m, recipe)
		if rec := b.recommended(); rec != nil {
			m.Set("recommended", rec)
		}
		out = append(out, m)
	}
	return out, nil
}

// listVideoModels is upstream video_service.list_models.
func (s *Server) listVideoModels(ctx context.Context) ([]any, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, b := range s.videoBackends() {
		if !installed[b.slug] {
			continue
		}
		recipe, ok := s.recipe(b.slug)
		if !ok {
			continue
		}
		m := newOrderedMap()
		m.Set("model", b.slug)
		m.Set("name", recipe.Name)
		m.Set("accepts", stringsToAny(b.acceptsList()))
		m.Set("aspect_ratios", stringsToAny(b.aspectRatios()))
		m.Set("max_seconds", b.maxSeconds())
		m.Set("state", s.stateOf(ctx, b.slug))
		m.Set("min_memory_gb", recipe.Requirements.MinMemoryGB)
		mergeFigures(m, recipe)
		if rec := videoRecommended(b); rec != nil {
			m.Set("recommended", rec)
		}
		out = append(out, m)
	}
	return out, nil
}

func videoRecommended(b videoBackend) *orderedMap {
	d := b.defaults
	if d == nil {
		return nil
	}
	out := newOrderedMap()
	if d.Steps != nil {
		out.Set("steps", *d.Steps)
	}
	if d.GuidanceScale != nil {
		out.Set("guidance_scale", *d.GuidanceScale)
	}
	if d.FPS != nil {
		out.Set("fps", *d.FPS)
	}
	if d.Seconds != nil {
		out.Set("seconds", *d.Seconds)
	}
	if d.Size != nil {
		out.Set("size", *d.Size)
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

// list3DModels is upstream mesh_service.list_models.
func (s *Server) list3DModels(ctx context.Context) ([]any, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, b := range s.meshBackends() {
		if !installed[b.slug] {
			continue
		}
		recipe, ok := s.recipe(b.slug)
		if !ok {
			continue
		}
		accepts := []string{"image-to-3d"}
		if b.multiView {
			accepts = append(accepts, "multi-view")
		}
		background := "plain: the Hub cuts it away first"
		if b.cutsOut {
			background = "any: the model cuts the object out itself"
		}
		m := newOrderedMap()
		m.Set("model", b.slug)
		m.Set("name", recipe.Name)
		m.Set("accepts", stringsToAny(accepts))
		if b.multiView {
			views := []string{"front (image)"}
			views = append(views, MeshViews...)
			m.Set("views", stringsToAny(views))
		}
		m.Set("output", "GLB with textures")
		m.Set("background", background)
		m.Set("state", s.stateOf(ctx, b.slug))
		m.Set("min_memory_gb", recipe.Requirements.MinMemoryGB)
		mergeFigures(m, recipe)
		out = append(out, m)
	}
	return out, nil
}

// listDecisionModels is upstream decision_service.list_models.
func (s *Server) listDecisionModels(ctx context.Context) ([]any, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, recipe := range s.decisionRecipes() {
		if !installed[recipe.Slug] {
			continue
		}
		accepts := []string{"text"}
		if hasTag(recipe.Tags, "vision") {
			accepts = append(accepts, "images")
		}
		if hasTag(recipe.Tags, "video") {
			accepts = append(accepts, "videos")
		}
		m := newOrderedMap()
		m.Set("model", recipe.Slug)
		m.Set("name", recipe.Name)
		m.Set("author", recipe.Author)
		m.Set("accepts", stringsToAny(accepts))
		m.Set("state", s.stateOf(ctx, recipe.Slug))
		m.Set("min_memory_gb", recipe.Requirements.MinMemoryGB)
		out = append(out, m)
	}
	return out, nil
}

func (s *Server) recipe(slug string) (registry.Recipe, bool) {
	if s.reg == nil {
		return registry.Recipe{}, false
	}
	return s.reg.Get(slug)
}

func (s *Server) decisionRecipes() []registry.Recipe {
	recipes, err := s.allRecipes()
	if err != nil {
		return nil
	}
	out := []registry.Recipe{}
	for _, r := range recipes {
		if hasTag(r.Tags, "systemone-api") {
			out = append(out, r)
		}
	}
	return out
}

// decisionBackends is upstream decision_service.backends.
func (s *Server) decisionBackends() map[string]bool {
	out := map[string]bool{}
	for _, r := range s.decisionRecipes() {
		out[r.Slug] = true
	}
	return out
}

func mergeFigures(m *orderedMap, recipe registry.Recipe) {
	figures := mediaFigures(recipe)
	if figures == nil {
		return
	}
	for _, k := range figures.keys {
		m.Set(k, figures.vals[k])
	}
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// withMemory is upstream mcp._with_memory: the models plus the memory free to
// start one, as the JSON text a tool returns.
func (s *Server) withMemory(ctx context.Context, models []any) string {
	total, free := memoryFigures()
	if free < 0 {
		free = 0
	}
	for _, item := range models {
		m, ok := item.(*orderedMap)
		if !ok {
			continue
		}
		if m.vals["state"] != "stopped" {
			continue
		}
		min, ok := toFloatOK(m.vals["min_memory_gb"])
		if !ok {
			continue
		}
		m.Set("fits_now", min <= free)
	}
	payload := struct {
		FreeGB  float64 `json:"memory_free_gb"`
		TotalGB float64 `json:"memory_total_gb"`
		Models  []any   `json:"models"`
	}{math.Round(free), math.Round(total), models}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func toFloatOK(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}
