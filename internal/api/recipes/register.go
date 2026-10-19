package recipes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"gospark-hub/internal/config"
	"gospark-hub/internal/engines"
	"gospark-hub/internal/native"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

// Runtime is the small amount of live state the registry routes need from
// the rest of the daemon. The consumer (the route package) defines the
// interface, so the concrete store does not need to import HTTP types.
type Runtime interface {
	InstalledRecipes(ctx context.Context) (map[string]struct{}, error)
	HasLeftovers(ctx context.Context, slug string) (bool, error)
	// Runtime state, per recipe. Upstream derives all six flags from the
	// pending action plus "container runs" plus "health check passed".
	IsRecipeRunning(ctx context.Context, slug string) bool
	IsReady(slug string) bool
	GetPending(slug string) string
	GetStartupError(slug string) string
	StartHealthCheck(ctx context.Context, slug string)
}

// Deps supplies the runtime state, the one recipe cache shared with the rest
// of the daemon, and the source manager that owns local/.
type Deps struct {
	Runtime Runtime
	Cache   *registry.Cache
	// Manager owns local/ and the vendored sources. The copy and delete
	// routes need it; without it they are not mounted (tests, embedders).
	Manager *registry.Manager
	// Engines answers "is the engine installed?" for native recipes, and
	// Native answers "is it running?". Both are optional: a container-only
	// daemon leaves them nil and native rows simply show as not installed.
	Engines *engines.Manager
	Native  *native.Runner
}

// Register mounts GET /api/recipes, GET /api/recipes/{slug} and — when the
// source manager is wired in — the copy and delete routes of local.go.
func Register(r *web.Router, deps Deps) {
	cache := deps.Cache
	if cache == nil {
		// Kept for tests and embedders that only care about routes; the
		// daemon always passes the cache built from the registry home.
		cfg := config.Load()
		cache = registry.NewCache(cfg.RegistryPath)
	}
	h := &handler{
		cache:     cache,
		deps:      deps,
		leftovers: map[string]bool{},
	}
	r.API("/api/recipes", h.list)
	r.API("/api/recipes/{slug}", h.detail)
	if deps.Manager != nil {
		r.Handle("POST", "/api/recipes/{slug}/fork", h.fork)
		r.Handle("DELETE", "/api/recipes/{slug}/local", h.deleteLocal)
	}
}

// recipeResponse is registry.Recipe plus where it came from: "local" marks a
// copy under local/, which the UI shows as a badge and which is the only kind
// that can be deleted.
type recipeResponse struct {
	registry.Recipe
	// omitempty keeps the JSON byte-for-byte identical to the upstream Hub for
	// every recipe that comes from a registry; only our own recipes carry it.
	Local bool `json:"local,omitempty"`
}

// MarshalJSON writes the recipe object and, for a local copy, one extra key.
// The manual splice is needed because registry.Recipe has its own MarshalJSON:
// an embedded marshaler would be promoted to this type and swallow the local
// field (the same reason the alias type must not be used here).
func (r recipeResponse) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(r.Recipe)
	if err != nil {
		return nil, err
	}
	if !r.Local {
		return base, nil
	}
	if len(base) < 2 || base[0] != '{' || base[len(base)-1] != '}' {
		return base, nil
	}
	out := make([]byte, 0, len(base)+16)
	out = append(out, base[:len(base)-1]...)
	out = append(out, `,"local":true}`...)
	return out, nil
}

func (h *handler) respond(rec registry.Recipe) recipeResponse {
	return recipeResponse{Recipe: rec, Local: h.cache.IsLocal(rec.Slug)}
}

type handler struct {
	cache *registry.Cache
	deps  Deps

	leftoverMu sync.Mutex
	leftovers  map[string]bool
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	recipes, err := h.cache.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	installed := h.installed(r.Context())

	category := r.URL.Query().Get("category")
	search := r.URL.Query().Get("search")
	q := strings.ToLower(search)

	out := make([]recipeResponse, 0, len(recipes))
	for _, rec := range recipes {
		resp := h.applyRuntime(r.Context(), rec, installed)

		cats := resp.Categories
		if len(cats) == 0 {
			cats = []string{resp.Category}
		}
		if category != "" && category != "all" && !slices.Contains(cats, category) {
			continue
		}
		if search != "" {
			if !strings.Contains(strings.ToLower(resp.Name), q) && !tagContains(resp.Tags, q) {
				continue
			}
		}
		out = append(out, h.respond(resp))
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec, ok := h.cache.Get(slug)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Recipe not found"})
		return
	}
	installed := h.installed(r.Context())
	resp := h.applyRuntime(r.Context(), rec, installed)

	writeJSON(w, http.StatusOK, h.respond(resp))
}

func (h *handler) installed(ctx context.Context) map[string]struct{} {
	if h.deps.Runtime == nil {
		return nil
	}
	m, err := h.deps.Runtime.InstalledRecipes(ctx)
	if err != nil {
		return nil
	}
	return m
}

func (h *handler) applyRuntime(ctx context.Context, rec registry.Recipe, installed map[string]struct{}) registry.Recipe {
	if dir, ok := h.cache.Dir(rec.Slug); ok {
		rec.RuntimeEnvPath = runtimeEnvPath(dir)
	}
	if rec.IsNative() {
		return h.applyNativeRuntime(ctx, rec)
	}

	rec.Error = nil
	pending := ""
	running := false
	if h.deps.Runtime != nil {
		pending = h.deps.Runtime.GetPending(rec.Slug)
		if msg := h.deps.Runtime.GetStartupError(rec.Slug); msg != "" {
			rec.Error = &msg
		}
	}

	if _, ok := installed[rec.Slug]; ok {
		rec.Installed = true
		rec.Installing = pending == "installing"
		running = h.deps.Runtime != nil && h.deps.Runtime.IsRecipeRunning(ctx, rec.Slug)
		rec.Ready = running && h.deps.Runtime.IsReady(rec.Slug)
		switch pending {
		case "launching":
			rec.Starting = true
			rec.Running = false
		case "stopping":
			rec.Running = false
			rec.Starting = false
		default:
			rec.Starting = running && !rec.Ready
			rec.Running = running && rec.Ready
		}
		if rec.Starting || (running && !rec.Ready) {
			go h.deps.Runtime.StartHealthCheck(context.Background(), rec.Slug)
		}
		// has_leftovers keeps its sticky value while installed.
	} else {
		rec.Installed = false
		rec.Running = false
		rec.Ready = false
		rec.Starting = pending == "launching"
		rec.Installing = pending == "installing"
		if !rec.Starting && !rec.Installing {
			h.leftoverMu.Lock()
			if !h.leftovers[rec.Slug] && h.deps.Runtime != nil {
				if has, _ := h.deps.Runtime.HasLeftovers(ctx, rec.Slug); has {
					h.leftovers[rec.Slug] = true
				}
			}
			rec.HasLeftovers = h.leftovers[rec.Slug]
			h.leftoverMu.Unlock()
		}
	}
	return rec
}

// applyNativeRuntime derives the six flags for a recipe that runs on a host
// engine: there is no container to install, no pending action and no health
// check — systemd says whether it is up.
func (h *handler) applyNativeRuntime(ctx context.Context, rec registry.Recipe) registry.Recipe {
	rec.Error = nil
	rec.Installing = false
	rec.Starting = false
	rec.HasLeftovers = false
	rec.Installed = false
	if rec.Native != nil {
		if eb := rec.Native.EngineBuild; eb != nil && h.deps.Engines != nil {
			// This recipe runs a client built for it, so the engine release
			// under current/ is irrelevant: the client directory is what
			// must exist.
			rec.EngineBuild = &registry.RecipeEngineBuildStatus{Image: eb.Image}
			if build, ok := h.deps.Engines.Build(rec.Native.Engine, rec.Slug); ok {
				rec.EngineBuild.Image = build.Image
				rec.EngineBuild.Path = build.Path
			}
			if _, err := h.deps.Engines.BuildPath(rec.Native.Engine, rec.Slug); err == nil {
				rec.EngineBuild.Ready = true
				rec.Installed = true
			}
			if rec.Native.Vision {
				// The patched client alone is not enough: its vision helper
				// runs on the engine's shared runtime.
				_, err := h.deps.Engines.RuntimePath(rec.Native.Engine)
				rec.EngineBuild.Runtime = err == nil
				rec.EngineBuild.Ready = rec.EngineBuild.Ready && rec.EngineBuild.Runtime
			}
		}
		if !rec.Installed && rec.Native.EngineBuild == nil && h.deps.Engines != nil {
			if _, err := h.deps.Engines.Path(rec.Native.Engine); err == nil {
				rec.Installed = true
			}
		}
	}
	running := false
	if rec.Installed && h.deps.Native != nil {
		running, _ = h.deps.Native.Running(ctx, rec.Slug)
	}
	rec.Running = running
	rec.Ready = running
	return rec
}

func runtimeEnvPath(dir string) string {
	envFile := filepath.Join(dir, ".env")
	if _, err := os.Stat(envFile); err == nil {
		return envFile
	}
	if _, err := os.Stat(envFile + ".example"); err == nil {
		return envFile
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr is the {"detail": …} shape the frontend expects on every error.
func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

func tagContains(tags []string, q string) bool {
	for _, t := range tags {
		if strings.Contains(t, q) {
			return true
		}
	}
	return false
}
