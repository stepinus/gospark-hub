package files

import (
	"net/http"
)

func (h *handler) listMedia(w http.ResponseWriter, r *http.Request) {
	files, err := h.media.List(h.registry, currentUser(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (h *handler) deleteMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !h.media.Delete(name, currentUser(r)) {
		writeErr(w, http.StatusNotFound, "No such file.")
		return
	}
	h.thumbs.Delete(name)
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}
