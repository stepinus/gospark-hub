package mcp

import (
	"net/http"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/store"
)

// recordUsage appends a usage_events row for one tools/call, keyed by the
// account the request's API key or session resolved to.
func (s *Server) recordUsage(r *http.Request, tool string, args map[string]any) {
	if s.store == nil {
		return
	}
	user := auth.CurrentUser(r)
	if user == nil {
		return
	}
	model := argStringSafe(args, "model")
	if model == "" {
		model = tool
	}
	ev := store.UsageEvent{
		UserID:   user.ID,
		Model:    model,
		Endpoint: "mcp:" + tool,
		Source:   auth.AuthSource(r),
	}
	if err := s.store.RecordUsage(ev); err != nil {
		s.log.Printf("mcp: record usage: %v", err)
	}
}

// argStringSafe is argString without the panic on a non-string value.
func argStringSafe(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}
