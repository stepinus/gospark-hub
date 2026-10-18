// Package system implements the /api/system/* routes and /ws/metrics.
package system

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

// Deps are the external dependencies the system routes need.
type Deps struct {
	Config   config.Config
	Logger   *slog.Logger
	Registry *registry.Registry
}

// Register mounts the system routes on r.
func Register(r *web.Router, deps Deps) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &server{deps: deps}

	r.API("/api/system/metrics", s.handleMetrics)
	r.API("/api/system/connect", s.handleConnect)
	r.API("/api/system/hf-token", s.handleHFTokenGet)
	r.Handle("POST", "/api/system/hf-token", s.handleHFTokenSet)
	r.API("/api/system/hf-access/{slug}", s.handleHFAccess)

	// WebSocket upgrades are handled as a normal GET behind the same auth
	// middleware as the rest of /api.
	r.Handle("GET", "/ws/metrics", s.handleMetricsWS)
}

type server struct {
	deps Deps
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
