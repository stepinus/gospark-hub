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
