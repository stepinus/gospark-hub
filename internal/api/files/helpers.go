package files

import (
	"encoding/json"
	"net/http"
	"strings"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

func currentUser(r *http.Request) *store.User {
	return auth.CurrentUser(r)
}

// requestOrigin returns the public origin for building absolute URLs.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	} else if fwd := parseForwarded(r.Header.Get("Forwarded")); fwd["host"] != "" {
		host = fwd["host"]
	}
	if !isValidHost(host) {
		return ""
	}
	return scheme + "://" + host
}

func parseForwarded(v string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(v, ",") {
		for _, kv := range strings.Split(part, ";") {
			kv = strings.TrimSpace(kv)
			if idx := strings.Index(kv, "="); idx > 0 {
				k, v := strings.TrimSpace(kv[:idx]), strings.TrimSpace(kv[idx+1:])
				v = strings.Trim(v, "\"")
				if out[k] == "" {
					out[k] = v
				}
			}
		}
	}
	return out
}

func isValidHost(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range host {
		if r > 0x7f {
			return false
		}
	}
	return true
}
