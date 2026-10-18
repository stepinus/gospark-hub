package files

import (
	"net/http"
	"path/filepath"
)

func (h *handler) uploadOnce(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	owner, err := h.links.ClaimUpload(token)
	if err != nil || owner == nil {
		writeErr(w, http.StatusNotFound,
			"This upload link is used, expired or invalid. Call create_upload for a new one.")
		return
	}

	saved := false
	result, status := h.receive(r, owner)
	saved = status == http.StatusOK
	if !saved {
		_ = h.links.ReleaseUpload(token)
	}
	if status != http.StatusOK {
		writeErr(w, status, result["detail"].(string))
		return
	}
	writeJSON(w, status, result)
}

func (h *handler) downloadOnce(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	name, owner, err := h.links.DownloadTarget(token)
	if err != nil || name == "" || owner == nil {
		writeErr(w, http.StatusNotFound,
			"This download link is expired or invalid, or the file was deleted. Call create_download for a new one.")
		return
	}

	path := h.media.Find(name)
	if path == "" || !h.media.CanRead(name, owner) {
		writeErr(w, http.StatusNotFound,
			"This download link is expired or invalid, or the file was deleted. Call create_download for a new one.")
		return
	}

	w.Header().Set("Content-Type", mediaType(filepath.Ext(path)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	http.ServeFile(w, r, path)
}

func mediaType(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".mp4":
		return "video/mp4"
	case ".wav":
		return "audio/wav"
	case ".glb":
		return "model/gltf-binary"
	}
	return "application/octet-stream"
}
