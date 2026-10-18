package files

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"gospark-hub/internal/media"
	"gospark-hub/internal/store"
)

func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	result, status := h.receive(r, user)
	if status != http.StatusOK {
		writeErr(w, status, result["detail"].(string))
		return
	}
	writeJSON(w, status, result)
}

// receive reads the raw body as a single media upload and records ownership.
func (h *handler) receive(r *http.Request, user *store.User) (map[string]any, int) {
	var size int64
	limit := int64(media.MaxBytes)
	body, err := io.ReadAll(&limitedReader{r: r.Body, limit: limit, seen: &size})
	if err != nil {
		return map[string]any{"detail": fmt.Sprintf("Uploads can be at most %d MB.", media.MaxBytes/(1024*1024))}, http.StatusRequestEntityTooLarge
	}
	if size > limit {
		return map[string]any{"detail": fmt.Sprintf("Uploads can be at most %d MB.", media.MaxBytes/(1024*1024))}, http.StatusRequestEntityTooLarge
	}

	info, err := media.Save(h.cfg, body)
	if err != nil {
		return map[string]any{"detail": err.Error()}, http.StatusBadRequest
	}

	path := info["path"].(string)
	name := path[strings.LastIndex(path, "/")+1:]
	var uid int64
	if user != nil {
		uid = user.ID
	}
	if err := h.media.Record(name, uid, "", nil); err != nil {
		return map[string]any{"detail": err.Error()}, http.StatusInternalServerError
	}

	origin := requestOrigin(r)
	if origin == "" {
		origin = "http://" + r.Host
	}
	info["url"] = origin + path
	delete(info, "path")
	return info, http.StatusOK
}

type limitedReader struct {
	r     io.Reader
	limit int64
	seen  *int64
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	n, err := lr.r.Read(p)
	*lr.seen += int64(n)
	if *lr.seen > lr.limit {
		return n, fmt.Errorf("upload exceeds limit")
	}
	return n, err
}
