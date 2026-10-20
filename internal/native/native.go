// Package native runs a recipe on a host engine instead of in a container.
//
// The process is owned by systemd --user (one transient unit per recipe), so
// it survives a daemon restart; like a container, it is not started again
// automatically after the machine reboots. Everything goes through a Run hook
// so the argument line can be asserted in tests without systemd present.
package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// DefaultUnitPrefix namespaces the transient units so the Hub's own processes
// are recognisable in `systemctl --user list-units`.
const DefaultUnitPrefix = "gospark-native"

// ErrAlreadyRunning is returned by Start when the unit is active: launching
// twice would fight over the GPU and the port.
var ErrAlreadyRunning = errors.New("native recipe is already running")

// Spec is everything one launch needs. Zero Port and ServedName mean "do not
// pass the flag"; the caller fills them from the recipe.
type Spec struct {
	EnginePath string
	// Command, when set, is the argv prefix started instead of EnginePath:
	// "serve", the model and Args are appended to it. It exists for engines
	// that are a module rather than one executable (python -m tensorfold).
	Command    []string
	Model      string
	Args       []string
	Env        map[string]string
	Port       int
	ServedName string
	LogPath    string
}

// Runner starts and stops native recipes. The zero value is usable: Run and
// LookPath fall back to the real systemctl/systemd-run and exec.LookPath.
type Runner struct {
	// LogDir is where a recipe's log goes when Spec.LogPath is empty.
	LogDir string
	// UnitPrefix overrides "gospark-native".
	UnitPrefix string
	// LookPath resolves the engine executable before the unit starts.
	LookPath func(string) (string, error)
	// Run executes one command (args[0] is the binary) and returns the
	// combined output and exit code. A non-zero exit is code, not err; err is
	// reserved for the binary being absent or the context being cancelled.
	Run func(ctx context.Context, args ...string) (string, int, error)
}

// NewRunner returns a Runner that logs under logDir.
func NewRunner(logDir string) *Runner { return &Runner{LogDir: logDir} }

func (r *Runner) run() func(context.Context, ...string) (string, int, error) {
	if r.Run != nil {
		return r.Run
	}
	return runCommand
}

func (r *Runner) lookPath() func(string) (string, error) {
	if r.LookPath != nil {
		return r.LookPath
	}
	return exec.LookPath
}

func (r *Runner) unitPrefix() string {
	if r.UnitPrefix != "" {
		return r.UnitPrefix
	}
	return DefaultUnitPrefix
}

// Unit is the systemd unit name for a recipe slug.
func (r *Runner) Unit(slug string) string { return r.unitPrefix() + "-" + slug }

// runCommand is the real implementation: systemctl for queries and control,
// systemd-run for starting a transient unit.
func runCommand(ctx context.Context, args ...string) (string, int, error) {
	if len(args) == 0 {
		return "", -1, errors.New("native: empty command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
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

// ResolveModel turns the recipe's model reference into a path: an `hf:` spec
// becomes the snapshot directory in the Hugging Face cache, `~` expands, and a
// plain path is used as is.
func (r *Runner) ResolveModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("native: empty model")
	}
	if ref, ok := strings.CutPrefix(model, "hf:"); ok {
		repo, rev, ok := strings.Cut(ref, "@")
		if !ok || repo == "" || rev == "" {
			return "", fmt.Errorf("native: model %q must be hf:<repo>@<rev>", model)
		}
		cache := os.Getenv("HF_HUB_CACHE")
		if cache == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("native: huggingface cache: %w", err)
			}
			cache = filepath.Join(home, ".cache", "huggingface", "hub")
		}
		dir := "models--" + strings.ReplaceAll(repo, "/", "--")
		return filepath.Join(cache, dir, "snapshots", rev), nil
	}
	if model == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("native: home dir: %w", err)
		}
		return home, nil
	}
	if rest, ok := strings.CutPrefix(model, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("native: home dir: %w", err)
		}
		return filepath.Join(home, rest), nil
	}
	return model, nil
}

// Start launches the recipe as a transient systemd user unit.
func (r *Runner) Start(ctx context.Context, slug string, spec Spec) error {
	if slug == "" {
		return errors.New("native: empty slug")
	}
	if spec.EnginePath == "" && len(spec.Command) == 0 {
		return errors.New("native: empty engine path")
	}
	if running, err := r.Running(ctx, slug); err != nil {
		return err
	} else if running {
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, slug)
	}
	if bin := spec.binary(); bin != "" {
		if _, err := r.lookPath()(bin); err != nil {
			return fmt.Errorf("native: engine executable %s: %w", bin, err)
		}
	}
	logPath := spec.LogPath
	if logPath == "" {
		if r.LogDir == "" {
			return errors.New("native: no log path")
		}
		logPath = filepath.Join(r.LogDir, slug+".log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("native: log dir: %w", err)
	}
	out, code, err := r.run()(ctx, r.startArgs(slug, spec, logPath)...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("systemd-run exited with %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// startArgs is the exact systemd-run command line, extracted so tests can
// assert it without starting anything.
func (r *Runner) startArgs(slug string, spec Spec, logPath string) []string {
	unit := r.Unit(slug)
	args := []string{
		"systemd-run",
		"--user",
		"--unit=" + unit,
		"--collect",
		"--quiet",
		"--property=StandardOutput=append:" + logPath,
		"--property=StandardError=append:" + logPath,
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--setenv="+k+"="+spec.Env[k])
	}
	args = append(args, spec.prefix()...)
	args = append(args, "serve", spec.Model)
	args = append(args, spec.Args...)
	if spec.Port > 0 && !hasFlag(spec.Args, "--port") {
		args = append(args, "--port", strconv.Itoa(spec.Port))
	}
	if spec.ServedName != "" && !hasFlag(spec.Args, "--name") {
		args = append(args, "--name", spec.ServedName)
	}
	return args
}

// prefix is argv[0:n] of the launched command: the recipe's Command when it
// names one, the engine binary otherwise.
func (s Spec) prefix() []string {
	if len(s.Command) > 0 {
		return slices.Clone(s.Command)
	}
	return []string{s.EnginePath}
}

// binary is the executable to resolve before starting: the first word of the
// command, or the engine binary.
func (s Spec) binary() string {
	if len(s.Command) > 0 {
		return s.Command[0]
	}
	return s.EnginePath
}

// hasFlag reports whether the recipe's own args already set flag, in either
// the "--flag value" or the "--flag=value" form.
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

// Stop stops the unit. An inactive unit is not an error: stopping is
// idempotent from the UI's point of view.
func (r *Runner) Stop(ctx context.Context, slug string) error {
	_, _, err := r.run()(ctx, "systemctl", "--user", "stop", r.Unit(slug))
	return err
}

// Running asks systemd whether the unit is active.
func (r *Runner) Running(ctx context.Context, slug string) (bool, error) {
	_, code, err := r.run()(ctx, "systemctl", "--user", "is-active", r.Unit(slug))
	if err != nil {
		return false, err
	}
	return code == 0, nil
}
