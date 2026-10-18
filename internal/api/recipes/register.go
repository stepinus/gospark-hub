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

// Deps supplies the small amount of runtime state the registry routes need
// from the rest of the daemon. The consumer (the route package) defines the
// interface, so the concrete store does not need to import HTTP types.
type Deps interface {
	InstalledRecipes(ctx context.Context) (map[string]struct{}, error)
	HasLeftovers(ctx context.Context, slug string) (bool, error)
}

// Register mounts GET /api/recipes and GET /api/recipes/{slug}.
func Register(r *web.Router, deps Deps) {
	cfg := config.Load()
	h := &handler{
		cache:     registry.NewCache(cfg.RegistryPath),
		deps:      deps,
		leftovers: map[string]bool{},
	}
	r.API("/api/recipes", h.list)
	r.API("/api/recipes/{slug}", h.detail)
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

	out := make([]registry.Recipe, 0, len(recipes))
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
		out = append(out, resp)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *handler) installed(ctx context.Context) map[string]struct{} {
	if h.deps == nil {
		return nil
	}
	m, err := h.deps.InstalledRecipes(ctx)
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
	if _, ok := installed[rec.Slug]; ok {
		rec.Installed = true
		rec.Installing = false
		rec.Running = false
		rec.Ready = false
		rec.Starting = false
		// has_leftovers is intentionally left untouched (sticky).
	} else {
		rec.Installed = false
		rec.Running = false
		rec.Ready = false
		rec.Starting = false
		rec.Installing = false
		if !rec.Starting && !rec.Installing {
			h.leftoverMu.Lock()
			if !h.leftovers[rec.Slug] {
				if h.deps != nil {
					if has, _ := h.deps.HasLeftovers(ctx, rec.Slug); has {
						h.leftovers[rec.Slug] = true
					}
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

func tagContains(tags []string, q string) bool {
	for _, t := range tags {
		if strings.Contains(t, q) {
			return true
		}
	}
	return false
}
