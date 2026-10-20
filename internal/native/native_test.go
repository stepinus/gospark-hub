package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// recorder captures every command the runner executes and answers
// `systemctl --user is-active` from active.
type recorder struct {
	active bool
	calls  [][]string
}

func (rec *recorder) runner() *Runner {
	return &Runner{
		UnitPrefix: "gospark-native",
		LookPath:   func(string) (string, error) { return "/bin/true", nil },
		Run: func(_ context.Context, args ...string) (string, int, error) {
			rec.calls = append(rec.calls, args)
			if len(args) >= 3 && args[0] == "systemctl" && args[2] == "is-active" {
				if rec.active {
					return "active", 0, nil
				}
				return "inactive", 3, nil
			}
			return "", 0, nil
		},
	}
}

func (rec *recorder) last() []string {
	if len(rec.calls) == 0 {
		return nil
	}
	return rec.calls[len(rec.calls)-1]
}

func TestStartBuildsSystemdRunArgs(t *testing.T) {
	rec := &recorder{}
	logPath := filepath.Join(t.TempDir(), "qwen.log")
	spec := Spec{
		EnginePath: "/engines/tensorfold/current/bin/tensorfold-native",
		Model:      "/models/qwen",
		Args:       []string{"--context", "262144", "--vision"},
		Env:        map[string]string{"TF_FLASHNEXT_DRAFTS": "6", "ALT": "x"},
		Port:       8888,
		ServedName: "qwen38-flash-next",
		LogPath:    logPath,
	}
	if err := rec.runner().Start(context.Background(), "qwen-native", spec); err != nil {
		t.Fatalf("start: %v", err)
	}

	want := []string{
		"systemd-run",
		"--user",
		"--unit=gospark-native-qwen-native",
		"--collect",
		"--quiet",
		"--property=StandardOutput=append:" + logPath,
		"--property=StandardError=append:" + logPath,
		"--setenv=ALT=x",
		"--setenv=TF_FLASHNEXT_DRAFTS=6",
		"/engines/tensorfold/current/bin/tensorfold-native",
		"serve",
		"/models/qwen",
		"--context",
		"262144",
		"--vision",
		"--port",
		"8888",
		"--name",
		"qwen38-flash-next",
	}
	if got := rec.last(); !slices.Equal(got, want) {
		t.Fatalf("systemd-run args:\n got %q\nwant %q", got, want)
	}
}

func TestStartRunsCommandInsteadOfBinary(t *testing.T) {
	rec := &recorder{}
	logPath := filepath.Join(t.TempDir(), "minicpm.log")
	spec := Spec{
		// A module engine names its argv prefix; the runner appends serve/model.
		Command: []string{"/runtime/bin/python3.12", "-m", "tensorfold"},
		Model:   "/models/minicpm",
		Args:    []string{"--vision", "--parallel", "1"},
		Env:     map[string]string{"PYTHONPATH": "/src:/pylibs"},
		Port:    8892,
		LogPath: logPath,
	}
	if err := rec.runner().Start(context.Background(), "minicpm-native", spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	want := []string{
		"systemd-run",
		"--user",
		"--unit=gospark-native-minicpm-native",
		"--collect",
		"--quiet",
		"--property=StandardOutput=append:" + logPath,
		"--property=StandardError=append:" + logPath,
		"--setenv=PYTHONPATH=/src:/pylibs",
		"/runtime/bin/python3.12",
		"-m",
		"tensorfold",
		"serve",
		"/models/minicpm",
		"--vision",
		"--parallel",
		"1",
		"--port",
		"8892",
	}
	if got := rec.last(); !slices.Equal(got, want) {
		t.Fatalf("systemd-run args:\n got %q\nwant %q", got, want)
	}
}

func TestStartDoesNotDuplicateRecipeFlags(t *testing.T) {
	rec := &recorder{}
	spec := Spec{
		EnginePath: "/bin/true",
		Model:      "/m",
		Args:       []string{"--port", "1234", "--name=already"},
		Port:       8888,
		ServedName: "other",
		LogPath:    filepath.Join(t.TempDir(), "x.log"),
	}
	if err := rec.runner().Start(context.Background(), "slug", spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	args := rec.last()
	if slices.Contains(args, "8888") || slices.Contains(args, "other") {
		t.Fatalf("recipe flags were duplicated: %q", args)
	}
}

func TestStartRefusesRunningUnit(t *testing.T) {
	rec := &recorder{active: true}
	err := rec.runner().Start(context.Background(), "slug", Spec{EnginePath: "/bin/true", LogPath: t.TempDir() + "/x.log"})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	for _, call := range rec.calls {
		if call[0] == "systemd-run" {
			t.Fatalf("systemd-run was called for an active unit: %q", call)
		}
	}
}

func TestRunningAndStop(t *testing.T) {
	rec := &recorder{}
	r := rec.runner()
	ctx := context.Background()

	running, err := r.Running(ctx, "slug")
	if err != nil || running {
		t.Fatalf("running = %v, %v; want false, nil", running, err)
	}
	if got := rec.last(); !slices.Equal(got, []string{"systemctl", "--user", "is-active", "gospark-native-slug"}) {
		t.Fatalf("is-active args = %q", got)
	}

	if err := r.Stop(ctx, "slug"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := rec.last(); !slices.Equal(got, []string{"systemctl", "--user", "stop", "gospark-native-slug"}) {
		t.Fatalf("stop args = %q", got)
	}

	rec.active = true
	running, err = r.Running(ctx, "slug")
	if err != nil || !running {
		t.Fatalf("running = %v, %v; want true, nil", running, err)
	}
}

func TestStopInactiveIsNotAnError(t *testing.T) {
	// systemctl exits 3 for an inactive unit; Stop must still return nil.
	r := &Runner{Run: func(context.Context, ...string) (string, int, error) { return "inactive", 3, nil }}
	if err := r.Stop(context.Background(), "slug"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestResolveModel(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cache)
	r := &Runner{}

	got, err := r.ResolveModel("hf:nvidia/Qwen3.8-Flash-Next-FP4-Local@b9ce18a6")
	if err != nil {
		t.Fatalf("hf resolve: %v", err)
	}
	want := filepath.Join(cache, "models--nvidia--Qwen3.8-Flash-Next-FP4-Local", "snapshots", "b9ce18a6")
	if got != want {
		t.Fatalf("hf resolve = %q, want %q", got, want)
	}

	if got, err := r.ResolveModel("/models/local-checkpoint"); err != nil || got != "/models/local-checkpoint" {
		t.Fatalf("plain path = %q, %v", got, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if got, err := r.ResolveModel("~/checkpoints/x"); err != nil || got != filepath.Join(home, "checkpoints", "x") {
		t.Fatalf("tilde path = %q, %v", got, err)
	}

	for _, bad := range []string{"", "hf:repo-without-rev", "hf:@rev", "hf:repo@"} {
		if _, err := r.ResolveModel(bad); err == nil {
			t.Errorf("ResolveModel(%q) = nil error, want failure", bad)
		}
	}
}
