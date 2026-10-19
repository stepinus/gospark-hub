// Package proxy owns the Hub's single public front door.
//
// Apps no longer publish host ports: they join one shared Docker network and
// keep whatever port they like *inside* their container. A Caddy container in
// front of them maps
//
//	:public/run/{slug}/...  ->  {container_name}:{ui.port}
//
// addressing them by container name. Nothing is published, nothing can
// collide, and a tunnel needs exactly one origin.
//
// The daemon itself moves to cfg.Port (9110 by default) and Caddy proxies
// everything that is not an app back to it, so the Hub stays reachable at
// cfg.PublicPort (9100 by default) exactly as before. If Caddy is dead or
// misconfigured the daemon is still directly reachable on cfg.Port -- that is
// the recovery path, deliberately kept.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
)

const (
	// AppPrefix is where an app is served. `/run/{slug}/` is stripped before
	// the request is forwarded, so the container sees the paths it would serve
	// at the root. Not `/app/`: that is the Hub's own SPA route for a recipe
	// detail page, and proxying over it would mean reloading a detail page
	// served you the app instead.
	AppPrefix = "/run"

	// ProbeHeader carries the shared secret that lets the daemon's health
	// check reach an app in bypass of forward_auth.
	ProbeHeader = "X-Sah-Probe"

	probeTokenName = "proxy-probe-token"
	caddyfileName  = "Caddyfile"
	proxyImage     = "caddy:2-alpine"
)

// lookPath is exec.LookPath behind a variable so tests can pretend docker is
// (or is not) installed without touching PATH.
var lookPath = exec.LookPath

// runDocker runs the docker CLI and returns its combined output and exit code.
// It is a variable so tests can observe (or replace) container management
// without a docker daemon. A non-zero exit is reported through code, not err;
// err is reserved for the binary being absent or the context being cancelled.
var runDocker = func(ctx context.Context, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return strings.TrimRight(buf.String(), "\n"), 0, nil
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return strings.TrimRight(buf.String(), "\n"), exit.ExitCode(), nil
	}
	return strings.TrimRight(buf.String(), "\n"), -1, err
}

// ListenPort is the port the daemon's HTTP server binds.
//
// With the front door enabled it is the private port (Caddy publishes the
// public one). With the front door disabled (`proxy_enabled=false`) the daemon
// itself must answer on the public port, or nothing at all listens there --
// the upstream bug recorded as R3 in docs/inventory/containers.md. Parity is
// deliberately not kept here: the Hub has to stay reachable.
func ListenPort(cfg config.Config) int {
	if !cfg.ProxyEnabled {
		return cfg.PublicPort
	}
	return cfg.Port
}

// Network is the shared external docker network all recipes attach to. It is
// scoped to cfg.Project so a test Hub cannot touch the production network.
func Network(cfg config.Config) string {
	if cfg.AppNetwork == "" {
		return "spark-ai-hub-net"
	}
	return cfg.AppNetwork
}

// Container is the name of the Caddy container, also scoped to cfg.Project.
func Container(cfg config.Config) string { return cfg.Project + "-proxy" }

// Ensure brings the front door up, or reloads it if it is already up.
//
// Called at daemon startup and safe to call again whenever the registry
// changes. Failure is logged, never returned: a broken proxy must not stop the
// daemon from booting, or there would be nothing left to fix it from.
func Ensure(ctx context.Context, cfg config.Config, cache *registry.Cache, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if !cfg.ProxyEnabled {
		// No network, no container, no Caddyfile. main binds cfg.PublicPort
		// itself through ListenPort so the Hub remains reachable; apps publish
		// no host ports, so they stay unreachable in this mode by design.
		logger.Info("front door disabled, daemon serves the public port itself",
			"public_port", cfg.PublicPort)
		return nil
	}
	if _, err := lookPath("docker"); err != nil {
		logger.Warn("docker not found — apps will not be reachable under /run/")
		return nil
	}

	var recipes []registry.Recipe
	if cache != nil {
		all, err := cache.All()
		if err != nil {
			logger.Error("could not read recipe registry", "error", err)
			return nil
		}
		recipes = all
	}

	if err := ensureNetwork(ctx, cfg); err != nil {
		logger.Error("could not create docker network", "network", Network(cfg), "error", err)
	}

	changed, err := writeCaddyfile(cfg, recipes)
	if err != nil {
		logger.Error("could not write Caddyfile", "error", err)
		return nil
	}

	name := Container(cfg)
	switch state := containerState(ctx, cfg); state {
	case "running":
		if !changed {
			return nil
		}
		// Hot-reload in place: no dropped connections, no restart.
		out, code, err := runDocker(ctx, "exec", name,
			"caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
		if err != nil || code != 0 {
			logger.Error("caddy reload failed", "error", err, "output", out)
			return nil
		}
		logger.Info("front door config changed, reloaded", "container", name)
		return nil
	case "":
		// Absent: fall through to creation.
	default:
		// Exists but stopped -- its config may be stale, so recreate it.
		if out, code, err := runDocker(ctx, "rm", "-f", name); err != nil || code != 0 {
			logger.Warn("could not remove stopped proxy container", "error", err, "output", out)
		}
	}

	args := []string{
		"run", "-d",
		"--name", name,
		"--restart", "unless-stopped",
		"--network", Network(cfg),
		// host-gateway is how the container reaches the daemon on the host.
		"--add-host", "host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("%d:%d", cfg.PublicPort, cfg.PublicPort),
		"-v", filepath.Join(cfg.DataDir, caddyfileName) + ":/etc/caddy/Caddyfile:ro",
		proxyImage,
	}
	out, code, err := runDocker(ctx, args...)
	if err != nil || code != 0 {
		logger.Error("failed to start proxy", "error", err, "output", out)
		return nil
	}
	logger.Info("front door listening", "port", cfg.PublicPort, "container", name)
	return nil
}

func ensureNetwork(ctx context.Context, cfg config.Config) error {
	network := Network(cfg)
	if _, code, err := runDocker(ctx, "network", "inspect", network); err != nil {
		return err
	} else if code == 0 {
		return nil
	}
	_, _, err := runDocker(ctx, "network", "create", network)
	return err
}

// containerState is "running", "exited", … or "" when the container is absent.
func containerState(ctx context.Context, cfg config.Config) string {
	out, code, err := runDocker(ctx, "inspect", "-f", "{{.State.Status}}", Container(cfg))
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// writeCaddyfile writes cfg.DataDir/Caddyfile and reports whether its contents
// changed. An unchanged file means Ensure can skip the reload entirely.
func writeCaddyfile(cfg config.Config, recipes []registry.Recipe) (bool, error) {
	text := Render(cfg, recipes)
	path := filepath.Join(cfg.DataDir, caddyfileName)
	if old, err := os.ReadFile(path); err == nil && string(old) == text {
		return false, nil
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// probeToken returns the shared secret that lets the daemon's health check
// reach an app. It is reused between restarts (the file wins when non-empty)
// and created with 0600 on first use.
func probeToken(cfg config.Config) string {
	path := filepath.Join(cfg.DataDir, probeTokenName)
	if data, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return token
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return token
		}
	}
	return token
}
