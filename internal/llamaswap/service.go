package llamaswap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultUnit is the transient unit the sidecar runs in.
const DefaultUnit = "gospark-llamaswap"

// Settings are the sidecar knobs the page edits. Port is the host port the
// sidecar listens on; the config file path is fixed by the manager.
type Settings struct {
	Port int `json:"port"`
}

func (m *Manager) settingsPath() string { return filepath.Join(m.Home, "settings.json") }

// Settings reads the sidecar settings, defaulting the port.
func (m *Manager) Settings() Settings {
	s := Settings{Port: DefaultPort}
	data, err := os.ReadFile(m.settingsPath())
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil || s.Port <= 0 || s.Port > 65535 {
		return Settings{Port: DefaultPort}
	}
	return s
}

// SetSettings writes the sidecar settings.
func (m *Manager) SetSettings(s Settings) error {
	if s.Port <= 0 || s.Port > 65535 {
		return fmt.Errorf("port %d is out of range", s.Port)
	}
	if err := os.MkdirAll(m.Home, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.settingsPath(), append(data, '\n'), 0o644)
}

// Service runs the installed binary as a transient systemd --user unit, the
// same way native recipes run: it outlives a daemon restart and dies with the
// machine, exactly like a container started with docker run.
type Service struct {
	Manager *Manager
	Unit    string

	// Run executes one command; nil means the real exec. Tests replace it.
	Run func(ctx context.Context, args ...string) (string, int, error)
	// LookPath resolves the binary; nil means exec.LookPath.
	LookPath func(string) (string, error)
}

func (s *Service) unit() string {
	if s.Unit != "" {
		return s.Unit
	}
	return DefaultUnit
}

func (s *Service) run() func(context.Context, ...string) (string, int, error) {
	if s.Run != nil {
		return s.Run
	}
	return func(ctx context.Context, args ...string) (string, int, error) {
		if len(args) == 0 {
			return "", -1, errors.New("llamaswap: empty command")
		}
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode(), nil
		}
		if err != nil {
			return string(out), -1, err
		}
		return string(out), 0, nil
	}
}

func (s *Service) lookPath() func(string) (string, error) {
	if s.LookPath != nil {
		return s.LookPath
	}
	return exec.LookPath
}

// ErrAlreadyRunning means the unit is active; starting twice would fight over
// the port.
var ErrAlreadyRunning = errors.New("llama-swap is already running")

// Start launches the sidecar against the managed config.
func (s *Service) Start(ctx context.Context) error {
	if _, ok := s.Manager.Installed(); !ok {
		return ErrNotInstalled
	}
	if running, _ := s.Running(ctx); running {
		return ErrAlreadyRunning
	}
	if _, err := s.lookPath()(s.Manager.Path()); err != nil {
		return err
	}
	if _, err := os.Stat(s.Manager.ConfigPath()); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	listen := "0.0.0.0:" + strconv.Itoa(s.Manager.Settings().Port)
	_, code, err := s.run()(ctx, "systemd-run", "--user",
		"--unit="+s.unit(), "--collect",
		"--description=llama-swap sidecar (managed by gospark-hub)",
		s.Manager.Path(),
		"-config", s.Manager.ConfigPath(),
		"-watch-config",
		"-listen", listen,
	)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("systemd-run exited %d", code)
	}
	return nil
}

// Stop stops the unit. Stopping what is stopped is a no-op.
func (s *Service) Stop(ctx context.Context) error {
	_, code, err := s.run()(ctx, "systemctl", "--user", "stop", s.unit())
	if err != nil {
		return err
	}
	if code != 0 && code != 5 { // 5 = unit not loaded
		return fmt.Errorf("systemctl stop exited %d", code)
	}
	return nil
}

// Running reports whether the unit is active.
func (s *Service) Running(ctx context.Context) (bool, error) {
	_, code, err := s.run()(ctx, "systemctl", "--user", "is-active", s.unit())
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// BinaryVersion runs the installed binary's -version.
func (s *Service) BinaryVersion(ctx context.Context) (string, error) {
	out, code, err := s.run()(ctx, s.Manager.Path(), "-version")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("%s -version exited %d", Binary, code)
	}
	return strings.TrimSpace(out), nil
}
