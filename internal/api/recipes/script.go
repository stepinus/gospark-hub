package recipes

import (
	"context"
	"net"
	"strconv"
	"time"

	"gospark-hub/internal/registry"
)

// applyScriptRuntime fills the runtime flags for a recipe whose app is started
// by its own script. The Hub does not own the container, so "installed" is
// always true and "running" is the app answering on its own port: that is the
// same signal the browser uses, and it stays correct whether the script, the
// Hub or a reboot started it.
func (h *handler) applyScriptRuntime(_ context.Context, rec registry.Recipe) registry.Recipe {
	rec.Error = nil
	rec.Installed = true
	running := portOpen(rec.UI.Port)
	rec.Running = running
	rec.Ready = running
	rec.Starting = false
	return rec
}

// portOpen reports whether something accepts TCP connections on the loopback
// port. A 200 ms budget is enough for a local listener and keeps the recipe
// list from stalling on a stopped app.
func portOpen(port int) bool {
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
