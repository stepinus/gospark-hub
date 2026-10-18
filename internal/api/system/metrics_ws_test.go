package system

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

func TestMetricsWebSocket(t *testing.T) {
	r := web.NewRouter()
	Register(r, Deps{Config: config.Load(), Registry: registry.NewCache(config.Load().RegistryPath)})
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/metrics"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	start := time.Now()
	for i := 0; i < 3; i++ {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if !strings.Contains(string(b), "gpu_utilization") {
			t.Fatalf("expected metrics JSON, got %s", b)
		}
	}
	if time.Since(start) < 1500*time.Millisecond {
		t.Fatalf("messages arrived too fast; expected ~1s cadence")
	}
}
