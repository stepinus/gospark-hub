package engines

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runtimeDocker answers the docker calls EnsureRuntime makes and materialises
// each copied tree, so the runtime layout can be asserted without a daemon.
type runtimeDocker struct {
	image   string
	missing bool
	copies  int
}

func (f *runtimeDocker) manager(home string) *Manager {
	m := NewManager(home)
	m.Run = f.run
	return m
}

func (f *runtimeDocker) run(_ context.Context, _ string, args ...string) (string, int, error) {
	switch {
	case len(args) >= 4 && args[1] == "image" && args[2] == "inspect":
		if f.missing {
			return "no such image", 1, nil
		}
		return "sha256:abc", 0, nil
	case len(args) >= 2 && args[1] == "create":
		return "container123", 0, nil
	case len(args) >= 4 && args[1] == "cp":
		f.copies++
		src := strings.TrimPrefix(args[2], "container123:")
		// The interpreter is the only file; everything else is a directory
		// with a marker inside.
		if filepath.Base(src) == "python3.12" {
			if err := os.MkdirAll(filepath.Dir(args[3]), 0o755); err != nil {
				return "", -1, err
			}
			return "", 0, os.WriteFile(args[3], []byte("#!/bin/sh\n"), 0o755)
		}
		if err := os.MkdirAll(args[3], 0o755); err != nil {
			return "", -1, err
		}
		return "", 0, os.WriteFile(filepath.Join(args[3], "marker"), nil, 0o644)
	case len(args) >= 2 && args[1] == "rm":
		return "", 0, nil
	}
	return "", 0, nil
}

func TestEnsureRuntimeCopiesOnceAndFindsTheInterpreter(t *testing.T) {
	home := t.TempDir()
	fake := &runtimeDocker{}
	m := fake.manager(home)

	path, err := m.EnsureRuntime(context.Background(), TensorFold, buildImage)
	if err != nil {
		t.Fatalf("EnsureRuntime: %v", err)
	}
	want := filepath.Join(home, TensorFold, RuntimeDirName, "bin", "python3.12")
	if path != want {
		t.Fatalf("interpreter = %s, want %s", path, want)
	}
	for _, dir := range []string{"lib/python3.12", "pylibs/dist-packages", "pylibs/system-dist-packages", "vlib/sys", "vlib/cuda", "vlib/locallib", "vlib/hpcx"} {
		if _, err := os.Stat(filepath.Join(home, TensorFold, RuntimeDirName, filepath.FromSlash(dir))); err != nil {
			t.Fatalf("runtime is missing %s: %v", dir, err)
		}
	}
	if fake.copies != len(runtimeLayout) {
		t.Fatalf("copies = %d, want %d", fake.copies, len(runtimeLayout))
	}

	// Second call is a no-op: the runtime is one copy per engine.
	if _, err := m.EnsureRuntime(context.Background(), TensorFold, buildImage); err != nil {
		t.Fatalf("EnsureRuntime again: %v", err)
	}
	if fake.copies != len(runtimeLayout) {
		t.Fatalf("copies after the second call = %d, want %d", fake.copies, len(runtimeLayout))
	}
}

func TestEnsureRuntimeWithoutImage(t *testing.T) {
	m := (&runtimeDocker{missing: true}).manager(t.TempDir())
	if _, err := m.EnsureRuntime(context.Background(), TensorFold, buildImage); err == nil {
		t.Fatal("EnsureRuntime succeeded with a missing image")
	}
	if _, err := m.RuntimePath(TensorFold); err == nil {
		t.Fatal("RuntimePath found a runtime that was never extracted")
	}
}

// TestRuntimeEnv pins the two shapes: a stock engine only needs the libraries,
// a patched one starts its helper on the shared interpreter.
func TestRuntimeEnv(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home)

	stock := m.RuntimeEnv(TensorFold, "")
	if _, ok := stock["TENSORFOLD_VISION_PYTHON"]; ok {
		t.Fatal("stock engine got a Python helper interpreter")
	}
	if !strings.Contains(stock["LD_LIBRARY_PATH"], filepath.Join(home, TensorFold, RuntimeDirName, "vlib", "cuda")) {
		t.Fatalf("LD_LIBRARY_PATH lacks the CUDA libraries: %s", stock["LD_LIBRARY_PATH"])
	}

	client := filepath.Join(home, "client", "python")
	patched := m.RuntimeEnv(TensorFold, client)
	if got := patched["TENSORFOLD_VISION_PYTHON"]; !strings.HasSuffix(got, filepath.Join("bin", "python3.12")) {
		t.Fatalf("vision interpreter = %s", got)
	}
	if !strings.HasPrefix(patched["PYTHONPATH"], client+":") {
		t.Fatalf("client package does not come first: %s", patched["PYTHONPATH"])
	}
	if !strings.Contains(patched["PYTHONPATH"], filepath.Join("pylibs", "system-dist-packages")) {
		t.Fatalf("distribution packages missing from PYTHONPATH: %s", patched["PYTHONPATH"])
	}
}
