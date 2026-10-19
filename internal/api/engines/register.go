// Package engines mounts the admin-only routes that manage the host engines
// native recipes run on: list, install (or update to) a version, and delete.
// It is a thin HTTP layer over engines.Manager, like internal/api/registries
// is for the registry manager.
package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"gospark-hub/internal/auth"
	engine "gospark-hub/internal/engines"
	"gospark-hub/internal/native"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

// Deps is everything the engine routes need. Manager and Cache must be the
// instances the rest of the daemon uses, so a recipe's runtime flags see the
// install immediately. Native answers "is a recipe of this engine running?".
type Deps struct {
	Engines *engine.Manager
	Native  *native.Runner
	Reg     *registry.Cache
	Logger  *slog.Logger
}

// Register mounts the engine routes. The caller owns the middleware, which is
// what answers 401 for a caller without a session.
func Register(r *web.Router, deps Deps) {
	h := &handler{deps: deps}
	r.Handle("GET", "/api/engines", h.list)
	r.Handle("GET", "/api/engines/{name}/settings", h.settings)
	r.Handle("PUT", "/api/engines/{name}/settings", h.saveSettings)
	r.Handle("POST", "/api/engines/{name}/install", h.install)
	r.Handle("POST", "/api/engines/{name}/update", h.update)
	r.Handle("DELETE", "/api/engines/{name}", h.remove)
}

type handler struct {
	deps Deps
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	if h.deps.Engines == nil {
		writeJSON(w, http.StatusOK, map[string]any{"engines": []engine.Status{}})
		return
	}
	list, err := h.deps.Engines.List(r.Context())
	if err != nil {
		h.fail(w, "list engines", err)
		return
	}
	if list == nil {
		list = []engine.Status{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"engines": list})
}

// settings answers the engine-wide serve flags the Engines page toggles.
func (h *handler) settings(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	if h.deps.Engines == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Engine not found"})
		return
	}
	opts, err := h.deps.Engines.Options(r.PathValue("name"))
	switch {
	case errors.Is(err, engine.ErrUnknownEngine):
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Engine not found"})
	case err != nil:
		h.fail(w, "read engine settings", err)
	default:
		writeJSON(w, http.StatusOK, opts)
	}
}

// saveSettings replaces the flags. A launch in flight keeps the flags it
// started with: these are command-line arguments, not a live control surface.
func (h *handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	if h.deps.Engines == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Engine not found"})
		return
	}
	var opts engine.Options
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "Invalid settings"})
		return
	}
	name := r.PathValue("name")
	err := h.deps.Engines.SetOptions(name, opts)
	switch {
	case errors.Is(err, engine.ErrUnknownEngine):
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Engine not found"})
	case err != nil:
		h.fail(w, "write engine settings", err)
	default:
		writeJSON(w, http.StatusOK, opts)
	}
}

func (h *handler) install(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	if h.deps.Engines == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Engines are not configured"})
		return
	}
	name := r.PathValue("name")
	st, err := h.deps.Engines.Install(r.Context(), name, r.URL.Query().Get("version"))
	switch {
	case errors.Is(err, engine.ErrAlreadyInstalled):
		writeJSON(w, http.StatusConflict, map[string]string{
			"detail": fmt.Sprintf("Engine %s %s is already installed", name, st.Version),
		})
	case errors.Is(err, engine.ErrUnknownEngine):
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Engine not found"})
	case err != nil:
		h.fail(w, "install engine "+name, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"name": st.Name, "version": st.Version, "installed": true})
	}
}

// update installs the latest release. It refuses while a native recipe of the
// engine runs: replacing the binary under a live process is a crash, not an
// upgrade.
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	if h.deps.Engines == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Engines are not configured"})
		return
	}
	name := r.PathValue("name")
	if slug, running := h.runningRecipe(r.Context(), name); running {
		h.logf("update engine %s refused: %s is running", name, slug)
		writeJSON(w, http.StatusConflict, map[string]string{"detail": "A native recipe is running on this engine"})
		return
	}
	st, changed, err := h.deps.Engines.Update(r.Context(), name)
	switch {
	case errors.Is(err, engine.ErrUnknownEngine):
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Engine not found"})
	case err != nil:
		h.fail(w, "update engine "+name, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"name": st.Name, "version": st.Version, "changed": changed})
	}
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	if h.deps.Engines == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Engines are not configured"})
		return
	}
	name := r.PathValue("name")
	if slug, running := h.runningRecipe(r.Context(), name); running {
		h.logf("delete engine %s refused: %s is running", name, slug)
		writeJSON(w, http.StatusConflict, map[string]string{"detail": "A native recipe is running on this engine"})
		return
	}
	err := h.deps.Engines.Delete(name)
	switch {
	case errors.Is(err, engine.ErrUnknownEngine):
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Engine not found"})
	case err != nil:
		h.fail(w, "delete engine "+name, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

// runningRecipe reports a native recipe of this engine whose unit is active.
func (h *handler) runningRecipe(ctx context.Context, engineName string) (string, bool) {
	if h.deps.Native == nil || h.deps.Reg == nil {
		return "", false
	}
	recipes, err := h.deps.Reg.All()
	if err != nil {
		return "", false
	}
	for _, rec := range recipes {
		if !rec.IsNative() || rec.Native == nil || rec.Native.Engine != engineName {
			continue
		}
		if running, err := h.deps.Native.Running(ctx, rec.Slug); err == nil && running {
			return rec.Slug, true
		}
	}
	return "", false
}

// admin guards every route in this package — an engine is a machine-wide
// binary, so only administrators may change it.
func (h *handler) admin(w http.ResponseWriter, r *http.Request) bool {
	if _, err := auth.RequireAdmin(r); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"detail": "Admin only"})
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, what string, err error) {
	h.logf("%s: %v", what, err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
}

func (h *handler) logf(format string, args ...any) {
	if h.deps.Logger != nil {
		h.deps.Logger.Error("engines: "+format, args...)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
