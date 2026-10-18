package files

import (
	"io"
	"net/http"
	"strings"

	"gospark-hub/internal/media"
)

const maxRenderBytes = 8 * 1024 * 1024

func (h *handler) getThumb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !strings.HasSuffix(name, ".png") && !strings.HasSuffix(name, ".glb") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	path := h.media.Find(name)
	if path == "" || !h.media.CanRead(name, currentUser(r)) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	jpeg, ok := h.thumbs.Get(name)
	if !ok {
		var err error
		jpeg, err = media.MakeThumb(media.Picture(path))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.thumbs.Set(name, jpeg)
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(jpeg)
}

func (h *handler) putThumb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !strings.HasSuffix(name, ".glb") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	path := h.media.Find(name)
	if path == "" || !h.media.CanRead(name, currentUser(r)) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 || len(raw) > maxRenderBytes {
		writeErr(w, http.StatusBadRequest, "Send one PNG of the model.")
		return
	}
	if err := media.SaveRender(path, raw); err != nil {
		writeErr(w, http.StatusBadRequest, "Not a readable image of a sensible size.")
		return
	}
	h.thumbs.Delete(name)
	writeJSON(w, http.StatusOK, map[string]string{"rendered": name})
}
