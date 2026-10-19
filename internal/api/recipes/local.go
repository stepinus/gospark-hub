package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/registry"
)

// ForkOriginFile records which recipe a local copy was made from. It lives in
// the local recipe directory, is ignored by the loader, and is read by
// GET /api/local-recipes so the settings page can say "copied from <slug>".
const ForkOriginFile = ".forked-from"

// forkSlugRE is the slug grammar of a copied recipe: lowercase alphanumerics
// and inner hyphens. The manager's own check is looser; the API is the place
// that keeps the catalogue tidy.
var forkSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// fork is POST /api/recipes/{slug}/fork: copy the recipe into local/<newSlug>.
// The original is never modified, and the copy stops following the source.
func (h *handler) fork(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	slug := r.PathValue("slug")

	var body struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	newSlug := strings.TrimSpace(body.Slug)
	if !forkSlugRE.MatchString(newSlug) {
		writeErr(w, http.StatusBadRequest, "Slug must match ^[a-z0-9][a-z0-9-]{1,62}$")
		return
	}

	dir, err := h.deps.Manager.Fork(slug, newSlug)
	switch {
	case errors.Is(err, registry.ErrSlugTaken):
		writeErr(w, http.StatusConflict, "Slug already taken")
		return
	case errors.Is(err, registry.ErrRecipeNotFound):
		writeErr(w, http.StatusNotFound, "Recipe not found")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Provenance for the settings page; a failure here is not fatal for the
	// copy, which is already complete.
	_ = os.WriteFile(filepath.Join(dir, ForkOriginFile), []byte(slug+"\n"), 0o644)
	if err := h.cache.Load(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"slug":        newSlug,
		"copied_from": slug,
		"path":        dir,
	})
}

// deleteLocal is DELETE /api/recipes/{slug}/local: remove a recipe that lives
// under local/. An installed or running app keeps its recipe — the user has to
// stop and remove the app first.
func (h *handler) deleteLocal(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	slug := r.PathValue("slug")

	if _, ok := h.cache.Get(slug); !ok {
		writeErr(w, http.StatusNotFound, "Recipe not found")
		return
	}
	if !h.cache.IsLocal(slug) {
		// Only copies are deletable; a recipe from a source is removed by
		// dropping the source, which is a different (admin) action.
		writeErr(w, http.StatusNotFound, "Recipe is not local")
		return
	}
	if h.recipeInUse(r.Context(), slug) {
		writeErr(w, http.StatusConflict, "Recipe is installed, stop and remove the app first")
		return
	}
	if err := h.deps.Manager.DeleteLocal(slug); err != nil {
		if errors.Is(err, registry.ErrNotLocal) {
			writeErr(w, http.StatusNotFound, "Recipe is not local")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.cache.Load(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// admin gates the copy and delete routes on the admin role, the same rule the
// registry routes follow.
func (h *handler) admin(w http.ResponseWriter, r *http.Request) bool {
	if _, err := auth.RequireAdmin(r); err != nil {
		writeErr(w, http.StatusForbidden, "Admin only")
		return false
	}
	return true
}

func (h *handler) recipeInUse(ctx context.Context, slug string) bool {
	if h.deps.Runtime == nil {
		return false
	}
	if installed, err := h.deps.Runtime.InstalledRecipes(ctx); err == nil {
		if _, ok := installed[slug]; ok {
			return true
		}
	}
	return h.deps.Runtime.IsRecipeRunning(ctx, slug)
}
