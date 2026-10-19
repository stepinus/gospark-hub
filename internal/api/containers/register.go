// Package containers mounts the recipe lifecycle routes: install/update,
// build logs, launch-plan/launch/stop, remove/purge, compose editing and the
// container list. It is a 1:1 port of daemon/routers/containers.py plus the
// build/log websockets.
package containers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/dockerx"
	"gospark-hub/internal/engines"
	"gospark-hub/internal/native"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// Deps is everything the container routes need from the daemon.
type Deps struct {
	Config   config.Config
	Registry *registry.Cache
	Logger   *slog.Logger
	DB       *store.DB
	// Docker is the daemon-wide container service. State it keeps (pending
	// actions, readiness, startup errors) must be the same one the recipes
	// list reads, so the caller passes its instance in.
	Docker *dockerx.Docker
	// Engines and Native serve the recipes that have no container at all.
	// Both are optional: a container-only deployment leaves them nil.
	Engines *engines.Manager
	Native  *native.Runner
}

// Register mounts the container routes. The caller owns the middleware.
func Register(r *web.Router, deps Deps) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	dock := deps.Docker
	if dock == nil {
		dock = dockerx.New(deps.Config, deps.Registry, deps.DB, logger)
	}
	h := &handler{
		cfg:     deps.Config,
		reg:     deps.Registry,
		log:     logger,
		d:       dock,
		engines: deps.Engines,
		natives: deps.Native,
		builds:  map[string]*buildState{},
	}

	r.Handle("POST", "/api/recipes/{slug}/install", h.install)
	r.Handle("DELETE", "/api/recipes/{slug}/install", h.removeInstall)
	r.Handle("POST", "/api/recipes/{slug}/update", h.update)
	r.Handle("GET", "/api/recipes/{slug}/build-status", h.buildStatus)
	r.Handle("GET", "/api/recipes/{slug}/launch-plan", h.launchPlan)
	r.Handle("POST", "/api/recipes/{slug}/launch", h.launch)
	r.Handle("POST", "/api/recipes/{slug}/stop", h.stop)
	r.Handle("POST", "/api/recipes/{slug}/purge", h.purge)
	r.Handle("DELETE", "/api/recipes/{slug}", h.remove)
	r.Handle("GET", "/api/recipes/{slug}/compose", h.getCompose)
	r.Handle("PUT", "/api/recipes/{slug}/compose", h.putCompose)
	r.Handle("POST", "/api/recipes/{slug}/compose/reset", h.resetCompose)
	r.Handle("GET", "/api/containers", h.listContainers)
	r.Handle("GET", "/ws/build/{slug}", h.buildWS)
	r.Handle("GET", "/ws/logs/{slug}", h.logsWS)
}

type buildState struct {
	lines []string
	done  bool
}

// The websocket wait budgets mirror upstream (50×100ms for a build to appear,
// 60×1s for a container, 600×1s for the ready verdict). Tests shrink them.
var (
	buildWaitTicks = 50
	buildWaitTick  = 100 * time.Millisecond
	logsWaitTicks  = 60
	logsWaitTick   = time.Second
	readyWaitTicks = 600
	readyWaitTick  = time.Second
	buildPollTick  = 300 * time.Millisecond
)

type handler struct {
	cfg     config.Config
	reg     *registry.Cache
	log     *slog.Logger
	d       *dockerx.Docker
	engines *engines.Manager
	natives *native.Runner

	mu     sync.Mutex
	builds map[string]*buildState
}

// ── Build-log bookkeeping ───────────────────────────────────────────────────

func (h *handler) startBuild(slug string) {
	h.mu.Lock()
	h.builds[slug] = &buildState{lines: []string{}}
	h.mu.Unlock()
}

func (h *handler) appendBuild(slug, line string) {
	h.mu.Lock()
	if b := h.builds[slug]; b != nil {
		b.lines = append(b.lines, line)
	}
	h.mu.Unlock()
}

func (h *handler) finishBuild(slug string) {
	h.mu.Lock()
	if b := h.builds[slug]; b != nil {
		b.done = true
	}
	h.mu.Unlock()
}

func (h *handler) hasBuild(slug string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.builds[slug]
	return ok
}

// isBuilding reports a build that exists and is not finished — the exact guard
// upstream uses to turn a repeated install into a no-op.
func (h *handler) isBuilding(slug string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.builds[slug]
	return ok && !b.done
}

// buildSnapshot returns a copy of the accumulated lines and the done flag.
func (h *handler) buildSnapshot(slug string) (lines []string, done bool, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.builds[slug]
	if !ok {
		return nil, false, false
	}
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out, b.done, true
}

// ── HTTP handlers ───────────────────────────────────────────────────────────

func (h *handler) install(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	// A native recipe has nothing to install unless it declares an engine
	// build: then the client directory is assembled from the model's image.
	// Without one the engine is installed once on the Engines page, and the
	// response keeps the shape the UI expects so its install button does not
	// change behaviour.
	if rec.IsNative() {
		if rec.Native == nil {
			writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "status": "installed"})
			return
		}
		// A vision recipe needs the engine's shared Python runtime; it is one
		// copy per engine, so a second recipe pays nothing for it.
		if err := h.ensureRuntime(r.Context(), rec); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"detail": err.Error()})
			return
		}
		if rec.Native.EngineBuild == nil {
			writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "status": "installed"})
			return
		}
		build, err := h.ensureEngineBuild(r.Context(), rec)
		switch {
		case errors.Is(err, engines.ErrImageNotBuilt):
			writeJSON(w, http.StatusConflict, map[string]string{
				"detail": fmt.Sprintf("Engine image %s is not built", rec.Native.EngineBuild.Image),
			})
		case errors.Is(err, engines.ErrUnknownEngine):
			writeJSON(w, http.StatusConflict, map[string]string{
				"detail": fmt.Sprintf("Engine %s is not installed", rec.Native.Engine),
			})
		case err != nil:
			h.log.Error("install engine build", "slug", slug, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		default:
			writeJSON(w, http.StatusOK, map[string]any{
				"slug": slug, "status": "installed", "engine_build": build.Path,
			})
		}
		return
	}
	// Already building, or left pending=installing by a restored daemon
	// state: report building without starting a second build.
	if h.isBuilding(slug) || h.d.GetPending(slug) == "installing" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "building", "slug": slug})
		return
	}

	h.d.SetPending(slug, "installing")
	h.d.ClearReady(slug)
	h.startBuild(slug)

	apiKey, hubURL := callerContext(r)
	go func() {
		defer h.finishBuild(slug)
		defer h.d.ClearPending(slug)
		if err := h.d.InstallRecipe(context.Background(), slug, apiKey, hubURL, func(line string) {
			h.appendBuild(slug, line)
		}); err != nil {
			h.appendBuild(slug, fmt.Sprintf("[error] %v", err))
		}
	}()

	writeJSON(w, http.StatusOK, map[string]any{"status": "building", "slug": slug})
}

// ensureRuntime makes sure the engine's shared Python runtime exists for a
// recipe that serves images. The image it is extracted from is the recipe's
// runtime_image, falling back to the engine build image: both are the same
// builder in practice.
func (h *handler) ensureRuntime(ctx context.Context, rec registry.Recipe) error {
	if h.engines == nil || rec.Native == nil || !rec.Native.Vision {
		return nil
	}
	image := rec.Native.RuntimeImage
	if image == "" && rec.Native.EngineBuild != nil {
		image = rec.Native.EngineBuild.Image
	}
	if image == "" {
		return errors.New("recipe serves images but names no runtime_image")
	}
	_, err := h.engines.EnsureRuntime(ctx, rec.Native.Engine, image)
	return err
}

// ensureEngineBuild assembles the patched engine client a native recipe runs
// on. The recipe's directory is the build command's working directory and the
// slug names the client directory.
func (h *handler) ensureEngineBuild(ctx context.Context, rec registry.Recipe) (engines.Build, error) {
	if h.engines == nil {
		return engines.Build{}, errors.New("engine builds are not configured")
	}
	eb := rec.Native.EngineBuild
	dir, _ := h.reg.Dir(rec.Slug)
	return h.engines.EnsureBuild(ctx, engines.BuildSpec{
		Recipe:    rec.Slug,
		Engine:    rec.Native.Engine,
		Image:     eb.Image,
		Artifacts: eb.Artifacts,
		Command:   eb.Command,
		Dir:       dir,
	})
}

// removeInstall deletes the engine client a native recipe was built with: the
// process runs the binary that is about to disappear, so it stops first.
func (h *handler) removeInstall(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	if !rec.IsNative() || rec.Native == nil || rec.Native.EngineBuild == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"detail": "Recipe has no engine build"})
		return
	}
	if h.engines == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Engine builds are not configured"})
		return
	}
	if err := h.stopNative(r.Context(), slug); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		return
	}
	if err := h.engines.RemoveBuild(rec.Native.Engine, slug); err != nil {
		h.log.Error("remove engine build", "slug", slug, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	// Updating a native recipe means updating its engine, which the Engines
	// page does; nothing to rebuild here.
	if rec.IsNative() {
		writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "status": "installed"})
		return
	}
	// clear_ready happens before the guard upstream, deliberately.
	h.d.ClearReady(slug)
	if h.isBuilding(slug) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "building", "slug": slug})
		return
	}

	h.d.SetPending(slug, "installing")
	h.startBuild(slug)

	apiKey, hubURL := callerContext(r)
	go func() {
		defer func() {
			h.finishBuild(slug)
			// `up -d` recreated the container; it must earn ready afresh.
			h.d.ClearReady(slug)
			h.d.ClearPending(slug)
			if h.d.IsRecipeRunning(context.Background(), slug) {
				h.d.StartHealthCheck(context.Background(), slug)
			}
		}()
		if err := h.d.UpdateRecipe(context.Background(), slug, apiKey, hubURL, func(line string) {
			h.appendBuild(slug, line)
		}); err != nil {
			h.appendBuild(slug, fmt.Sprintf("[error] %v", err))
		}
	}()

	writeJSON(w, http.StatusOK, map[string]any{"status": "building", "slug": slug})
}

func (h *handler) buildStatus(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	lines, done, ok := h.buildSnapshot(slug)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle", "lines": []string{}})
		return
	}
	status := "building"
	if done {
		status = "done"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "lines": lines})
}

func (h *handler) launchPlan(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	stop := []string{}
	if rec.IsLLM() {
		ctx := r.Context()
		need, free, running := h.d.MemoryPlan(ctx, slug)
		type llm = memEntry
		var llms []memEntry
		for _, other := range running {
			o, ok := h.reg.Get(other)
			if !ok || !o.IsLLM() {
				continue
			}
			llms = append(llms, memEntry{slug: other, mem: o.MemoryGB})
		}
		stableSortByMem(llms)
		if free < need {
			// The smallest model that frees enough on its own; failing that,
			// the largest ones until it fits.
			found := ""
			for _, l := range llms {
				if free+l.mem >= need {
					found = l.slug
					break
				}
			}
			if found != "" {
				stop = []string{found}
			} else {
				for _, l := range slices.Backward(llms) {
					stop = append(stop, l.slug)
					free += l.mem
					if free >= need {
						break
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stop": stop})
}

func (h *handler) launch(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	if rec.IsNative() {
		h.launchNative(w, r, rec)
		return
	}
	h.d.SetPending(slug, "launching")
	h.d.ClearReady(slug)
	h.d.ClearStartupError(slug)

	// A build-type recipe whose image is missing gets built by `compose up`
	// itself; mirror that output into the build log the UI already watches.
	buildRecipe := rec.Docker.Build
	var onLine func(string)
	if buildRecipe && !h.isBuilding(slug) {
		h.startBuild(slug)
		onLine = func(line string) { h.appendBuild(slug, line) }
	}

	apiKey, hubURL := callerContext(r)
	result, err := h.d.LaunchRecipe(r.Context(), slug, apiKey, hubURL, onLine)
	if onLine != nil {
		h.finishBuild(slug)
	}
	if err != nil {
		h.d.ClearPending(slug)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		return
	}
	if result == "launched" {
		h.d.StartHealthCheck(context.Background(), slug)
		writeJSON(w, http.StatusOK, map[string]any{"status": "launched", "slug": slug})
		return
	}
	h.d.ClearPending(slug)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": result})
}

// launchNative starts a recipe on its host engine. The unit survives the
// daemon, so there is no pending action, no build log and no health check: the
// first connection either works or the log explains why.
func (h *handler) launchNative(w http.ResponseWriter, r *http.Request, rec registry.Recipe) {
	if rec.Native == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"detail": "Recipe has no native block"})
		return
	}
	if h.engines == nil || h.natives == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Native runtime is not configured"})
		return
	}
	// A recipe with an engine build runs the client built for it, not the
	// engine release under current: the two are different binaries.
	enginePath := ""
	env := make(map[string]string, len(rec.Native.Env))
	for k, v := range rec.Native.Env {
		env[k] = v
	}
	if eb := rec.Native.EngineBuild; eb != nil {
		path, err := h.engines.BuildPath(rec.Native.Engine, rec.Slug)
		if errors.Is(err, engines.ErrClientNotBuilt) || errors.Is(err, engines.ErrUnknownEngine) {
			writeJSON(w, http.StatusConflict, map[string]string{"detail": "Engine client is not built"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
			return
		}
		enginePath = path
		// The block's env values are paths inside the client directory; the
		// runner needs them absolute.
		for k, v := range engines.ExpandEnv(eb.Env, h.engines.BuildDir(rec.Native.Engine, rec.Slug)) {
			env[k] = v
		}
	} else {
		path, err := h.engines.Path(rec.Native.Engine)
		if errors.Is(err, engines.ErrNotInstalled) || errors.Is(err, engines.ErrUnknownEngine) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"detail": fmt.Sprintf("Engine %s is not installed", rec.Native.Engine),
			})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
			return
		}
		enginePath = path
	}
	// A vision recipe needs the engine's shared runtime: the patched engine runs
	// its tower as a Python helper on it, the stock engine only needs the CUDA
	// libraries it carries. One runtime serves every recipe of the engine.
	if rec.Native.Vision {
		if _, err := h.engines.RuntimePath(rec.Native.Engine); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"detail": "Engine runtime is not installed"})
			return
		}
		clientPython := ""
		if rec.Native.EngineBuild != nil {
			candidate := filepath.Join(h.engines.BuildDir(rec.Native.Engine, rec.Slug), "python")
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				clientPython = candidate
			}
		}
		for k, v := range h.engines.RuntimeEnv(rec.Native.Engine, clientPython) {
			env[k] = v
		}
	}

	model, err := h.natives.ResolveModel(rec.Native.Model)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
		return
	}

	port := rec.Native.Port
	if port == 0 {
		port = rec.UI.Port
	}
	served := rec.Name
	if served == "" {
		served = rec.Slug
	}
	spec := native.Spec{
		EnginePath: enginePath,
		Model:      model,
		Args:       rec.Native.Args,
		Env:        env,
		Port:       port,
		ServedName: served,
		LogPath:    filepath.Join(h.cfg.DataDir, "logs", rec.Slug+".log"),
	}
	if len(spec.Env) == 0 {
		spec.Env = nil
	}
	// systemd-run talks to the user manager and returns at once; the request
	// context must not kill it halfway through the handoff.
	if err := h.natives.Start(context.WithoutCancel(r.Context()), rec.Slug, spec); err != nil {
		if errors.Is(err, native.ErrAlreadyRunning) {
			writeJSON(w, http.StatusConflict, map[string]string{"detail": "Recipe is already running"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": rec.Slug, "status": "launched"})
}

// stopNative stops the unit. Stopping what is already stopped is a no-op.
func (h *handler) stopNative(ctx context.Context, slug string) error {
	if h.natives == nil {
		return errors.New("native runtime is not configured")
	}
	return h.natives.Stop(ctx, slug)
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	if rec.IsNative() {
		if err := h.stopNative(r.Context(), slug); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "stopped", "slug": slug})
		return
	}
	h.d.SetPending(slug, "stopping")
	h.d.ClearReady(slug)
	h.d.ClearStartupError(slug)
	result, _ := h.d.StopRecipe(r.Context(), slug)
	h.d.ClearPending(slug)
	writeJSON(w, http.StatusOK, map[string]any{"status": result, "slug": slug})
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	if rec.IsNative() {
		// No image, no volume, no compose project: stopping the unit is the
		// whole of "remove" for a native recipe.
		if err := h.stopNative(r.Context(), slug); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
		return
	}
	h.d.SetPending(slug, "removing")
	h.d.ClearReady(slug)
	result, _ := h.d.RemoveRecipe(r.Context(), slug)
	h.d.ClearPending(slug)
	writeJSON(w, http.StatusOK, map[string]any{"status": result, "slug": slug})
}

func (h *handler) purge(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.reg.Get(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return
	}
	if rec.IsNative() {
		if err := h.stopNative(r.Context(), slug); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
		return
	}
	result, _ := h.d.PurgeRecipe(r.Context(), slug)
	writeJSON(w, http.StatusOK, map[string]any{"status": result, "slug": slug})
}

func (h *handler) listContainers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.d.Client.List(r.Context()))
}

// ── Compose file editing ────────────────────────────────────────────────────

func (h *handler) composeFileForSlug(slug string, w http.ResponseWriter) (string, bool) {
	dir, ok := h.reg.Dir(slug)
	if !ok {
		notFound(w, "Recipe not found")
		return "", false
	}
	compose := filepath.Join(dir, "docker-compose.yml")
	if st, err := os.Stat(compose); err != nil || st.IsDir() {
		notFound(w, "docker-compose.yml not found")
		return "", false
	}
	return compose, true
}

func (h *handler) getCompose(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	compose, ok := h.composeFileForSlug(slug, w)
	if !ok {
		return
	}
	content, err := os.ReadFile(compose)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defaultContent, err := h.defaultComposeContent(slug)
	if err != nil {
		defaultContent = string(content)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"content":         string(content),
		"default_content": defaultContent,
	})
}

func (h *handler) putCompose(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	compose, ok := h.composeFileForSlug(slug, w)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := os.WriteFile(compose, []byte(body.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (h *handler) resetCompose(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	compose, ok := h.composeFileForSlug(slug, w)
	if !ok {
		return
	}
	defaultContent, err := h.defaultComposeContent(slug)
	switch {
	case errors.Is(err, errNoSource):
		writeJSON(w, http.StatusConflict, map[string]string{"detail": "Local recipe has no source, reset is not available"})
		return
	case err != nil:
		notFound(w, "Default docker-compose.yml not available")
		return
	}
	if err := os.WriteFile(compose, []byte(defaultContent), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset", "content": defaultContent})
}

// errNoSource marks a local recipe: there is no checkout to restore a file
// from, so reset is refused instead of silently doing nothing.
var errNoSource = errors.New("local recipe has no source")

// defaultComposeContent returns the file as committed at HEAD of the checkout
// that owns the recipe. The root comes from the cache origin — the nearest
// git checkout above the recipe — so recipes under any source subpath, and a
// local recipe that inherited a repository, both resolve without counting
// parent directories.
func (h *handler) defaultComposeContent(slug string) (string, error) {
	root, dir, ok := h.reg.Origin(slug)
	if !ok {
		return "", fmt.Errorf("recipe %s not found", slug)
	}
	if h.reg.IsLocal(slug) {
		return "", errNoSource
	}
	rel, err := filepath.Rel(root, filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "show", "HEAD:"+filepath.ToSlash(rel))
	cmd.Dir = root
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git show: %w", err)
	}
	return out.String(), nil
}

// ── WebSockets ──────────────────────────────────────────────────────────────

func (h *handler) buildWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := conn.CloseRead(context.Background())
	slug := r.PathValue("slug")

	// Wait up to 5s for the build to appear (it may start right after the
	// POST that opened this socket).
	found := false
	for range buildWaitTicks {
		if h.hasBuild(slug) {
			found = true
			break
		}
		if !sleepCtx(ctx, buildWaitTick) {
			return
		}
	}
	if !found {
		_ = conn.Write(ctx, websocket.MessageText, []byte("[done]"))
		return
	}

	seen := 0
	for {
		lines, done, ok := h.buildSnapshot(slug)
		if !ok {
			if !sleepCtx(ctx, buildPollTick) {
				return
			}
			continue
		}
		for seen < len(lines) {
			if err := conn.Write(ctx, websocket.MessageText, []byte(lines[seen])); err != nil {
				return
			}
			seen++
		}
		if done {
			_ = conn.Write(ctx, websocket.MessageText, []byte("[done]"))
			return
		}
		if !sleepCtx(ctx, buildPollTick) {
			return
		}
	}
}

func (h *handler) logsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	baseCtx := conn.CloseRead(context.Background())
	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()
	slug := r.PathValue("slug")

	write := func(text string) error {
		return conn.Write(ctx, websocket.MessageText, []byte(text))
	}

	// Phase 1: wait for the container (running OR exited).
	container := ""
	deadline := logsWaitTicks
	announcedWait := false
	buildSeen := 0
	for deadline > 0 {
		container = h.d.AnyContainerName(ctx, slug)
		if container != "" {
			break
		}
		lines, done, ok := h.buildSnapshot(slug)
		if ok && (!done || buildSeen > 0) {
			for buildSeen < len(lines) {
				if err := write(lines[buildSeen]); err != nil {
					return
				}
				buildSeen++
			}
			if !done {
				deadline = logsWaitTicks
			}
		} else if !announcedWait {
			if err := write("[spark-ai-hub] Waiting for container to start..."); err != nil {
				return
			}
			announcedWait = true
		}
		if !sleepCtx(ctx, logsWaitTick) {
			return
		}
		deadline--
	}
	if container == "" {
		// A failed launch runs `compose down`, deleting the container; the
		// preserved tail is the only place the reason still exists.
		saved := h.d.LastFailureLogs(slug)
		if len(saved) > 0 {
			for _, line := range saved {
				if err := write(line); err != nil {
					return
				}
			}
			if msg := h.d.GetStartupError(slug); msg != "" {
				_ = write("[spark-ai-hub:error] " + msg)
			}
		} else {
			_ = write("[spark-ai-hub] Container not running")
		}
		return
	}

	// The container appearing ended the wait, so a build may still have its
	// last lines unsent.
	if lines, _, ok := h.buildSnapshot(slug); ok && buildSeen > 0 {
		for buildSeen < len(lines) {
			if err := write(lines[buildSeen]); err != nil {
				return
			}
			buildSeen++
		}
	}

	h.d.StartHealthCheck(ctx, slug)

	notifyCtx, notifyCancel := context.WithCancel(ctx)
	defer notifyCancel()
	notifyDone := make(chan struct{})
	go func() {
		defer close(notifyDone)
		for range readyWaitTicks {
			if h.d.IsReady(slug) {
				_ = write("[spark-ai-hub:ready]")
				return
			}
			if msg := h.d.GetStartupError(slug); msg != "" {
				_ = write("[spark-ai-hub:error] " + msg)
				return
			}
			if !sleepCtx(notifyCtx, readyWaitTick) {
				return
			}
		}
	}()

	// `logging: driver: none` leaves nothing for `docker logs` to read; say so
	// and still deliver the ready/error verdict the health check produces,
	// exactly like upstream's `await health_task` before close.
	if driver, err := h.d.Client.Inspect(ctx, "{{.HostConfig.LogConfig.Type}}", container); err == nil {
		if len(driver) > 0 && strings.TrimSpace(driver[0]) == "none" {
			_ = write("[spark-ai-hub] This app keeps no logs: logging is turned off in its docker-compose.yml.")
			select {
			case <-notifyDone:
			case <-ctx.Done():
			}
			return
		}
	}

	streamContainerLogs(ctx, container, func(line string) error { return write(line) })

	if msg := h.d.GetStartupError(slug); msg != "" {
		_ = write("[spark-ai-hub:error] " + msg)
	}
}

// streamContainerLogs runs `docker logs -f --tail 200` and forwards each line
// the way upstream does: strip the trailing whitespace, keep only the last
// carriage-return segment (progress bars), and skip empty lines.
func streamContainerLogs(ctx context.Context, container string, emit func(string) error) {
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--tail", "200", container)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		text := strings.TrimRight(sc.Text(), " \t\r\n\v\f")
		if idx := strings.LastIndex(text, "\r"); idx >= 0 {
			text = text[idx+1:]
		}
		if text != "" {
			if err := emit(text); err != nil {
				break
			}
		}
	}
	_ = sc.Err()
	_ = cmd.Wait()
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func callerContext(r *http.Request) (apiKey, hubURL string) {
	if u := auth.CurrentUser(r); u != nil {
		apiKey = u.APIKey
	}
	return apiKey, requestOrigin(r)
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if h := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); h != "" {
		host = h
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

type memEntry struct {
	slug string
	mem  float64
}

func stableSortByMem(llms []memEntry) {
	for i := 1; i < len(llms); i++ {
		for j := i; j > 0 && llms[j].mem < llms[j-1].mem; j-- {
			llms[j], llms[j-1] = llms[j-1], llms[j]
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter, detail string) {
	writeJSON(w, http.StatusNotFound, map[string]string{"detail": detail})
}
