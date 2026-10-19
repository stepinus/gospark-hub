// Package registries mounts the admin-only routes that manage registry
// sources: the list of configured repositories, adding/syncing/removing them
// and the local recipes that were copied out of a source. It is a thin HTTP
// layer over registry.Manager — every disk and git operation stays there.
package registries

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gospark-hub/internal/api/recipes"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

// Runtime is the small piece of live state this package needs: a recipe that
// is still installed or running must keep its files on disk.
type Runtime interface {
	InstalledRecipes(ctx context.Context) (map[string]struct{}, error)
	IsRecipeRunning(ctx context.Context, slug string) bool
}

// Deps is everything the registry routes need from the daemon. Manager and
// Cache must be the instances the rest of the daemon uses, so a sync or a
// delete is visible without a restart.
type Deps struct {
	Manager *registry.Manager
	Cache   *registry.Cache
	Runtime Runtime
	Logger  *slog.Logger
}

// Register mounts the registry source routes. The caller owns middleware.
func Register(r *web.Router, deps Deps) {
	h := &handler{deps: deps}

	r.Handle("GET", "/api/registries", h.list)
	r.Handle("POST", "/api/registries", h.add)
	r.Handle("POST", "/api/registries/sync", h.syncAll)
	r.Handle("POST", "/api/registries/{id}/sync", h.syncOne)
	r.Handle("DELETE", "/api/registries/{id}", h.remove)
	r.Handle("GET", "/api/local-recipes", h.listLocal)
}

type handler struct {
	deps Deps

	mu      sync.Mutex
	syncing bool
}

// sourceView is one configured source plus the number of recipes the checkout
// actually holds, which is what the settings page shows next to the commit.
type sourceView struct {
	registry.Source
	Recipes int `json:"recipes"`
}

// listResponse is the shape of GET /api/registries and the source part of the
// mutating responses: the caller always receives the full state it should
// render, so one round trip is enough.
type listResponse struct {
	Sources    []sourceView `json:"sources"`
	LocalCount int          `json:"local_count"`
	Syncing    bool         `json:"syncing"`
}

// addResponse keeps the source state and adds what this request did, line by
// line: successful additions and one error per rejected line.
type addResponse struct {
	listResponse
	Added  []sourceView `json:"added"`
	Errors []string     `json:"errors"`
	Detail string       `json:"detail,omitempty"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, h.snapshot())
}

// add parses the textarea ("one link per line or comma separated"), adds every
// valid source and syncs it. Partial success is a 200 with per-line errors; a
// request where nothing could be added is a 400.
func (h *handler) add(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var body struct {
		URLs string `json:"urls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, addResponse{
			listResponse: h.snapshot(),
			Added:        []sourceView{},
			Errors:       []string{},
			Detail:       "Invalid JSON body",
		})
		return
	}
	if strings.TrimSpace(body.URLs) == "" {
		writeJSON(w, http.StatusBadRequest, addResponse{
			listResponse: h.snapshot(),
			Added:        []sourceView{},
			Errors:       []string{},
			Detail:       "urls is required",
		})
		return
	}

	h.setSyncing(true)
	added, errs := h.deps.Manager.Add(body.URLs)
	h.setSyncing(false)
	h.reload()

	status := http.StatusOK
	resp := addResponse{
		listResponse: h.snapshot(),
		Added:        h.views(added),
		Errors:       errorStrings(errs),
	}
	if len(added) == 0 {
		// Nothing landed: the request as a whole failed, so report the joined
		// per-line errors as the detail too.
		status = http.StatusBadRequest
		resp.Detail = strings.Join(resp.Errors, "; ")
	}
	writeJSON(w, status, resp)
}

func (h *handler) syncAll(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	h.setSyncing(true)
	errs := h.deps.Manager.SyncAll()
	h.setSyncing(false)
	h.reload()

	snap := h.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"sources":     snap.Sources,
		"local_count": snap.LocalCount,
		"syncing":     snap.Syncing,
		"errors":      errorStrings(errs),
	})
}

func (h *handler) syncOne(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id := r.PathValue("id")
	h.setSyncing(true)
	err := h.deps.Manager.Sync(id)
	h.setSyncing(false)
	if errors.Is(err, registry.ErrSourceNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Registry source not found"})
		return
	}
	h.reload()

	// A git failure is recorded on the source itself (Source.Error), so the
	// response is still a 200 with the refreshed state.
	errs := []string{}
	if err != nil {
		errs = append(errs, err.Error())
	}
	snap := h.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"sources":     snap.Sources,
		"local_count": snap.LocalCount,
		"syncing":     snap.Syncing,
		"errors":      errs,
	})
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id := r.PathValue("id")
	err := h.deps.Manager.Remove(id)
	if errors.Is(err, registry.ErrSourceNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Registry source not found"})
		return
	}
	if err != nil {
		h.logf("remove source %s: %v", id, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	h.reload()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// localRecipe is one row of the "local recipes" block in the settings page.
type localRecipe struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	CopiedFrom string `json:"copied_from"`
	MTime      string `json:"mtime"`
}

// listLocal returns the recipes under local/. "copied_from" is the slug the
// fork came from, recorded by the fork route in ForkOriginFile.
func (h *handler) listLocal(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	localRoot := filepath.Join(h.deps.Manager.Home(), "local")
	entries, err := os.ReadDir(localRoot)
	if err != nil && !os.IsNotExist(err) {
		h.logf("read local recipes: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}

	out := make([]localRecipe, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(localRoot, e.Name())
		info, err := os.Stat(filepath.Join(dir, "recipe.yaml"))
		if err != nil || info.IsDir() {
			continue
		}
		row := localRecipe{Slug: e.Name(), MTime: info.ModTime().UTC().Format(time.RFC3339)}
		if h.deps.Cache != nil {
			if rec, ok := h.deps.Cache.Get(e.Name()); ok {
				row.Slug = rec.Slug
				row.Name = rec.Name
			}
		}
		if data, err := os.ReadFile(filepath.Join(dir, recipes.ForkOriginFile)); err == nil {
			row.CopiedFrom = strings.TrimSpace(string(data))
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })

	writeJSON(w, http.StatusOK, out)
}

// ── Helpers ────────────────────────────────────────────────────────────────

// admin guards every route in this package: the registry is the recipe supply
// chain, so only administrators may change it.
func (h *handler) admin(w http.ResponseWriter, r *http.Request) bool {
	if _, err := auth.RequireAdmin(r); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"detail": "Admin only"})
		return false
	}
	return true
}

func (h *handler) setSyncing(v bool) {
	h.mu.Lock()
	h.syncing = v
	h.mu.Unlock()
}

func (h *handler) isSyncing() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.syncing
}

// reload refreshes the shared cache so the next request sees what just changed
// on disk. A broken recipe is logged by the cache, not fatal here.
func (h *handler) reload() {
	if h.deps.Cache == nil {
		return
	}
	if err := h.deps.Cache.Load(); err != nil {
		h.logf("reload recipes: %v", err)
	}
}

func (h *handler) snapshot() listResponse {
	return listResponse{
		Sources:    h.views(h.deps.Manager.List()),
		LocalCount: countRecipes(filepath.Join(h.deps.Manager.Home(), "local")),
		Syncing:    h.isSyncing(),
	}
}

func (h *handler) views(sources []registry.Source) []sourceView {
	out := make([]sourceView, 0, len(sources))
	for _, src := range sources {
		out = append(out, sourceView{Source: src, Recipes: countRecipes(h.sourceRoot(src))})
	}
	return out
}

// sourceRoot is where a source's recipes are checked out:
// vendored/<id>/<subpath>.
func (h *handler) sourceRoot(src registry.Source) string {
	root := filepath.Join(h.deps.Manager.Home(), "vendored", src.ID)
	if src.Subpath != "" {
		root = filepath.Join(root, filepath.FromSlash(src.Subpath))
	}
	return root
}

// countRecipes counts the directories that actually hold a recipe.yaml.
func countRecipes(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if st, err := os.Stat(filepath.Join(root, e.Name(), "recipe.yaml")); err == nil && !st.IsDir() {
			n++
		}
	}
	return n
}

func errorStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			out = append(out, err.Error())
		}
	}
	return out
}

func (h *handler) logf(format string, args ...any) {
	if h.deps.Logger != nil {
		h.deps.Logger.Error("registries: "+format, args...)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
