package containers

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gospark-hub/internal/registry"
)

// scriptTimeout caps one start or stop command. A ComfyUI start rebuilds
// nothing but still waits for the container to accept connections, so a couple
// of minutes is normal; anything past this is a hang, not a slow launch.
const scriptTimeout = 15 * time.Minute

// runScript executes one of the recipe's `script:` commands through the user's
// shell, so the app keeps its own launcher (mounts, image, flags) as the single
// source of truth and the Hub only flips the switch.
//
// The command runs with the request's values but not its cancellation: a
// browser tab closing mid-launch must not leave a half-started container.
func runScript(ctx context.Context, rec registry.Recipe, command string) error {
	if rec.Script == nil || command == "" {
		return fmt.Errorf("recipe %s has no script command", rec.Slug)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	if rec.Script.Dir != "" {
		cmd.Dir = rec.Script.Dir
	}
	cmd.Env = scriptEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		if msg == "" {
			return fmt.Errorf("%s: %v", command, err)
		}
		return fmt.Errorf("%s: %v: %s", command, err, msg)
	}
	return nil
}

// scriptRunning reports whether a script recipe's app is up. A `status:`
// command wins when the recipe gives one (exit code 0 = running); otherwise the
// app's own UI port is probed, which is what the browser would use anyway.
func scriptRunning(ctx context.Context, rec registry.Recipe) bool {
	if rec.Script != nil && rec.Script.Status != "" {
		probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probe, "bash", "-lc", rec.Script.Status)
		if rec.Script.Dir != "" {
			cmd.Dir = rec.Script.Dir
		}
		return cmd.Run() == nil
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return runtimePortOpen(probe, rec.UI.Port)
}

// runtimePortOpen reports whether something listens on the loopback port.
func runtimePortOpen(ctx context.Context, port int) bool {
	if port <= 0 {
		return false
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// hubOnlyEnv are the variables the daemon sets for itself that a recipe's
// script must not inherit: spark-comfyui.sh reads PORT (default 8188) and would
// try to publish the Hub's own port 9106, failing with "address already in use".
var hubOnlyEnv = map[string]bool{
	"PORT":           true,
	"PUBLIC_PORT":    true,
	"PROXY_ENABLED":  true,
	"REGISTRY_HOME":  true,
	"ENGINE_HOME":    true,
	"DATA_DIR":       true,
	"DB_PATH":        true,
	"HUB_API":        true,
	"SESSION_SECRET": true,
}

// scriptEnv is the daemon's environment minus the Hub's own knobs.
func scriptEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if hubOnlyEnv[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
