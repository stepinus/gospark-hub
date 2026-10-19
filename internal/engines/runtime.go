package engines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The shared runtime is the one part of a native setup that is genuinely bulky:
// the patched engine's vision helper is Python and needs the interpreter, torch,
// cuDNN and MPI that the builder image carries. It is extracted once per engine
// and shared by every recipe — a per-recipe copy would duplicate ~12 GB for no
// benefit, since every recipe of an engine wants the same versions.
//
// Layout under Manager.Home:
//
//	tensorfold/
//	  runtime/
//	    bin/python3.12        the interpreter the helper runs on
//	    lib/python3.12        its standard library
//	    pylibs/               the image's dist-packages (torch, transformers, ...)
//	    vlib/cuda, vlib/ompi, vlib/sys   shared libraries torch links against
const (
	RuntimeDirName = "runtime"

	// runtimeBin is the interpreter a vision helper is started with.
	runtimeBin = "bin/python3.12"
)

// ErrRuntimeMissing means the shared runtime was never extracted for the engine.
var ErrRuntimeMissing = errors.New("engine runtime is not installed")

// runtimeLayout is copied out of the builder image. Destinations are exact:
// each copy lands on a temp name and is renamed, so docker cp's "copy inside
// the directory" behaviour cannot nest the trees.
var runtimeLayout = []struct{ image, dest string }{
	{"/usr/bin/python3.12", "bin/python3.12"},
	{"/usr/lib/python3.12", "lib/python3.12"},
	{"/usr/local/lib/python3.12/dist-packages", "pylibs/dist-packages"},
	{"/usr/lib/aarch64-linux-gnu", "vlib/sys"},
	{"/usr/local/cuda-13.3/targets/sbsa-linux/lib", "vlib/cuda"},
	{"/usr/local/lib", "vlib/locallib"},
	{"/opt/hpcx", "vlib/hpcx"},
}

// RuntimeDir is where an engine's shared runtime lives.
func (m *Manager) RuntimeDir(engine string) string {
	return filepath.Join(m.Home, engine, RuntimeDirName)
}

// RuntimePath returns the interpreter of the shared runtime, or
// ErrRuntimeMissing. It only checks for the interpreter: a half-copied runtime
// fails at the helper's first import, which is loud enough.
func (m *Manager) RuntimePath(engine string) (string, error) {
	path := filepath.Join(m.RuntimeDir(engine), filepath.FromSlash(runtimeBin))
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", ErrRuntimeMissing
	}
	return path, nil
}

// EnsureRuntime extracts the shared runtime out of a builder image. It is
// idempotent: an existing runtime is returned untouched, because the versions
// only change together with the image and the engine client built from it.
func (m *Manager) EnsureRuntime(ctx context.Context, engine, image string) (string, error) {
	if _, err := engineByName(engine); err != nil {
		return "", err
	}
	if image == "" {
		return "", errors.New("engines: runtime build needs an image")
	}
	if path, err := m.RuntimePath(engine); err == nil {
		return path, nil
	}
	if _, _, err := m.run()(ctx, "", "docker", "image", "inspect", image); err != nil {
		return "", fmt.Errorf("engines: docker image inspect %s: %w", image, err)
	}

	dir := m.RuntimeDir(engine)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("engines: runtime dir: %w", err)
	}
	id, err := m.containerFor(ctx, image)
	if err != nil {
		return "", err
	}
	defer func() {
		// Cancelled together with the request if the caller gives up, but the
		// container must not outlive it either way.
		_, _, _ = m.run()(context.WithoutCancel(ctx), "", "docker", "rm", "-f", id)
	}()

	for _, item := range runtimeLayout {
		target := filepath.Join(dir, filepath.FromSlash(item.dest))
		if err := m.copyArtifact(ctx, id, item.image, target); err != nil {
			return "", err
		}
	}
	// The python tree under /usr/local/lib came along with the shared
	// libraries; pylibs already has it.
	_ = os.RemoveAll(filepath.Join(dir, "vlib", "locallib", "python3.12"))

	path, err := m.RuntimePath(engine)
	if err != nil {
		return "", fmt.Errorf("engines: runtime extracted without %s", runtimeBin)
	}
	return path, nil
}

// RuntimeEnv is the environment a native launch needs when the recipe serves
// images. The helper starts on the shared interpreter and sees the client's
// `python/` (the tensorfold package) before the runtime's packages;
// LD_LIBRARY_PATH mirrors what the builder image exports, which is what makes
// torch and the CUDA tower load on the host.
func (m *Manager) RuntimeEnv(engine, clientPython string) map[string]string {
	dir := m.RuntimeDir(engine)
	py := filepath.Join(dir, "pylibs", "dist-packages")

	env := map[string]string{
		"LD_LIBRARY_PATH": strings.Join([]string{
			filepath.Join(dir, "vlib", "sys"),
			filepath.Join(dir, "vlib", "sys", "nvshmem", "13"),
			filepath.Join(dir, "vlib", "cuda"),
			filepath.Join(dir, "vlib", "locallib"),
			filepath.Join(dir, "vlib", "hpcx", "ompi5", "lib"),
			filepath.Join(dir, "vlib", "hpcx", "ucc", "lib"),
			filepath.Join(dir, "vlib", "hpcx", "sharp", "lib"),
			filepath.Join(dir, "vlib", "hpcx", "hcoll", "lib"),
			filepath.Join(dir, "vlib", "hpcx", "ucx", "lib"),
			filepath.Join(py, "torch", "lib"),
		}, ":"),
	}
	// A patched engine runs its vision path as a Python helper; the stock
	// engine reads the tower in CUDA and only needs the libraries above.
	if clientPython != "" {
		env["TENSORFOLD_VISION_PYTHON"] = filepath.Join(dir, filepath.FromSlash(runtimeBin))
		env["TENSORFOLD_VISION_PYTHONPATH"] = clientPython
		env["PYTHONPATH"] = clientPython + ":" + py
	}
	return env
}

// copyArtifact copies one image path onto an exact destination, whatever it is
// (docker cp would nest a directory inside an existing one).
func (m *Manager) copyArtifact(ctx context.Context, containerID, imagePath, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("engines: %s: %w", filepath.Dir(target), err)
	}
	tmp := target + ".tmp"
	_ = os.RemoveAll(tmp)
	out, code, err := m.run()(ctx, "", "docker", "cp", containerID+":"+imagePath, tmp)
	if err != nil {
		return fmt.Errorf("engines: docker cp %s: %w", imagePath, err)
	}
	if code != 0 {
		return fmt.Errorf("engines: docker cp %s: exit %d: %s", imagePath, code, strings.TrimSpace(out))
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("engines: %s: %w", target, err)
	}
	return nil
}

// containerFor creates a throwaway container from the image so its files can be
// copied out; the caller removes it.
func (m *Manager) containerFor(ctx context.Context, image string) (string, error) {
	out, code, err := m.run()(ctx, "", "docker", "create", image)
	if err != nil {
		return "", fmt.Errorf("engines: docker create %s: %w", image, err)
	}
	id := strings.TrimSpace(out)
	if code != 0 || id == "" {
		return "", fmt.Errorf("engines: docker create %s: exit %d: %s", image, code, id)
	}
	return id, nil
}
