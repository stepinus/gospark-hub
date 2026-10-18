package system

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"gospark-hub/internal/monitor"
)

func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	m := monitor.Read(r.Context())
	writeJSON(w, http.StatusOK, m)
}

func (s *server) handleMetricsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.deps.Logger.Debug("metrics ws accept failed", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	for {
		m := monitor.Read(ctx)
		b, err := json.Marshal(m)
		if err != nil {
			s.deps.Logger.Debug("metrics ws marshal failed", "err", err)
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
