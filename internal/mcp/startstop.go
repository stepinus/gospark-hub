package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gospark-hub/internal/store"
)

// readyPollSeconds is upstream READY_POLL_SECONDS.
const readyPollSeconds = 2

// startableSlugs is upstream mcp._startable_slugs: every installed-able media
// model plus SearXNG. LLMs and every other app stay the Hub's.
func (s *Server) startableSlugs() map[string]bool {
	out := map[string]bool{searxngSlug: true}
	for _, b := range s.imageBackends() {
		out[b.slug] = true
	}
	for _, b := range s.videoBackends() {
		out[b.slug] = true
	}
	for _, b := range s.meshBackends() {
		out[b.slug] = true
	}
	for _, b := range s.audioBackends() {
		out[b.slug] = true
	}
	for slug := range s.decisionBackends() {
		out[slug] = true
	}
	return out
}

// startable is upstream mcp._startable: the installed model or SearXNG args names.
func (s *Server) startable(ctx context.Context, args map[string]any) (string, error) {
	slug := argString(args, "model")
	installed, err := s.installedSet(ctx)
	if err != nil {
		return "", err
	}
	startable := s.startableSlugs()
	var names []string
	for candidate := range startable {
		if installed[candidate] {
			names = append(names, candidate)
		}
	}
	sort.Strings(names)
	if !startable[slug] || !installed[slug] {
		return "", imageErrorf("'%s' is not an installed image, video, 3D, music or decision model, or "+
			"SearXNG. Installed: %s.", slug, orNone(names))
	}
	return slug, nil
}

// memoryPlan is upstream docker_service.memory_plan for one slug.
func (s *Server) memoryPlan(ctx context.Context, slug string) (need, free float64, running []string) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		installed = map[string]bool{}
	}
	loading := 0.0
	for candidate := range installed {
		if candidate == slug {
			continue
		}
		recipe, ok := s.recipe(candidate)
		if !ok {
			continue
		}
		up, err := s.rt.Running(ctx, candidate)
		if err != nil || !up {
			continue
		}
		if !s.rt.Ready(candidate) {
			loading += recipe.MemoryGB
		}
		running = append(running, candidate)
	}
	sort.Strings(running)
	_, available := memoryFigures()
	if slug != "" {
		if recipe, ok := s.recipe(slug); ok {
			need = recipe.MemoryGB
		}
	}
	return need, available - loading, running
}

// checkMemory is upstream mcp._check_memory.
func (s *Server) checkMemory(ctx context.Context, slug string) error {
	need, free, running := s.memoryPlan(ctx, slug)
	if free >= need {
		return nil
	}
	startable := s.startableSlugs()
	var runningMedia, runningOther []string
	for _, other := range running {
		if startable[other] {
			runningMedia = append(runningMedia, other)
		} else {
			runningOther = append(runningOther, other)
		}
	}
	lines := []string{fmt.Sprintf("%s needs about %.0f GB and only %.0f GB is free.",
		slug, need, maxFloat(free, 0))}
	if len(runningMedia) > 0 {
		lines = append(lines, "Running models you can stop with stop_model: "+
			strings.Join(runningMedia, ", ")+".")
	}
	if len(runningOther) > 0 {
		var names []string
		for _, other := range runningOther {
			if recipe, ok := s.recipe(other); ok {
				names = append(names, recipe.Name)
			} else {
				names = append(names, other)
			}
		}
		lines = append(lines, "Also running (only the Hub can stop these): "+
			strings.Join(names, ", ")+".")
	}
	return imageErrorf("%s", strings.Join(lines, " "))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// startModel is upstream mcp._start_model.
func (s *Server) startModel(ctx context.Context, args map[string]any, origin string,
	onProgress func(string), user *store.User) toolResult {
	slug, err := s.startable(ctx, args)
	if err != nil {
		return textResult(err.Error(), true)
	}

	s.startMu.Lock()
	running, _ := s.rt.Running(ctx, slug)
	if !running {
		if err := s.checkMemory(ctx, slug); err != nil {
			s.startMu.Unlock()
			return textResult(err.Error(), true)
		}
		if onProgress != nil {
			onProgress("starting " + slug)
		}
		if err := s.rt.Launch(ctx, slug, user, origin); err != nil {
			s.startMu.Unlock()
			return textResult(fmt.Sprintf("%s failed to launch: %s", slug, truncate(err.Error(), 500)), true)
		}
	}
	s.startMu.Unlock()

	deadline := time.Now().Add(CheckWait * time.Second)
	for !s.rt.Ready(slug) {
		running, _ := s.rt.Running(ctx, slug)
		if !running {
			return textResult(fmt.Sprintf("%s stopped before it finished loading. "+
				"Its log is on its page in the Hub.", slug), true)
		}
		if time.Now().After(deadline) {
			text := fmt.Sprintf("Not done yet: %s is still starting up. Now call start_model "+
				"with model %s again, and keep calling it until it is ready.", slug, slug)
			structured := newOrderedMap()
			structured.Set("model", slug)
			structured.Set("status", "starting")
			structured.Set("done", false)
			structured.Set("next_call", newOrderedMap().
				Set("tool", "start_model").
				Set("arguments", newOrderedMap().Set("model", slug)))
			return toolResult{
				Content:           []any{textBlock{Type: "text", Text: text}},
				StructuredContent: structured,
				IsError:           false,
			}
		}
		s.rt.StartHealthCheck(slug)
		time.Sleep(readyPollSeconds * time.Second)
	}
	structured := newOrderedMap()
	structured.Set("model", slug)
	structured.Set("status", "ready")
	structured.Set("done", true)
	return toolResult{
		Content:           []any{textBlock{Type: "text", Text: slug + " is ready."}},
		StructuredContent: structured,
		IsError:           false,
	}
}

// stopModel is upstream mcp._stop_model.
func (s *Server) stopModel(ctx context.Context, args map[string]any) toolResult {
	slug, err := s.startable(ctx, args)
	if err != nil {
		return textResult(err.Error(), true)
	}
	running, _ := s.rt.Running(ctx, slug)
	if !running {
		structured := newOrderedMap()
		structured.Set("model", slug)
		structured.Set("status", "stopped")
		return toolResult{
			Content:           []any{textBlock{Type: "text", Text: slug + " is already stopped."}},
			StructuredContent: structured,
			IsError:           false,
		}
	}
	busy := s.jobs.rendersOn(slug) + s.renderingOnVideo(ctx, slug)
	if busy > 0 {
		plural := ""
		if busy > 1 {
			plural = "s"
		}
		them := "it"
		if busy > 1 {
			them = "them"
		}
		return textResult(fmt.Sprintf("%s is rendering %d job%s right now; stopping it would lose "+
			"%s. Collect the results first, then stop it.", slug, busy, plural, them), true)
	}
	if err := s.rt.Stop(ctx, slug); err != nil {
		return textResult(fmt.Sprintf("Stopping %s failed; try again or stop it in the Hub.", slug), true)
	}
	structured := newOrderedMap()
	structured.Set("model", slug)
	structured.Set("status", "stopped")
	return toolResult{
		Content:           []any{textBlock{Type: "text", Text: fmt.Sprintf("Stopped %s; its memory is free.", slug)}},
		StructuredContent: structured,
		IsError:           false,
	}
}
