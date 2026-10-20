package engines

// A patched engine client.
//
// The released binary knows nothing about a particular checkpoint: a model
// that needs an engine feature the release does not have (fp8 KV, a vision
// tower under another name, ModelOpt NVFP4) ships the engine patches and the
// image they build. That image is the model repository's knowledge; the Hub
// only runs its build command and copies the artifacts out.
//
// Layout under Manager.Home:
//
//	tensorfold/
//	  builds/
//	    qwen38-flash-next-native/
//	      bin/tensorfold-native
//	      share/  python/  fatbin/
//	      build.json
//
// The engine release under tensorfold/current stays untouched: a recipe
// without native.engine_build keeps running on it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	buildsDirName = "builds"
	buildFileName = "build.json"
	// buildMarkerDir names the staging directory a build is assembled in;
	// like .install-, it never becomes a version.
	buildMarkerDir = ".build-"
)

var (
	// ErrImageNotBuilt is returned by EnsureBuild when the declared image is
	// absent and there is no command to build it. The API turns it into a 409
	// that names the image.
	ErrImageNotBuilt = errors.New("engine image is not built")
	// ErrClientNotBuilt is returned for a recipe that declares a build but
	// has no client directory yet, or whose binary is gone.
	ErrClientNotBuilt = errors.New("engine client is not built")
)

// BuildSpec is one `native.engine_build` block, resolved by the caller: the
// recipe's slug and directory come from the registry cache, the rest from the
// recipe YAML.
type BuildSpec struct {
	// Recipe is the recipe slug; it names the client directory.
	Recipe string
	// Engine is the engine the client is built for ("tensorfold").
	Engine string
	// Image holds the already-patched engine. It may be produced by Command.
	Image string
	// Artifacts are the paths inside the image that make up the client;
	// each one is copied to the client directory under its base name, so
	// "native/bin" becomes bin/.
	Artifacts []string
	// Command is the model repository's build script, relative to Dir. It is
	// run with bash -lc only when the image is missing.
	Command string
	// Dir is the recipe directory, the working directory of Command.
	Dir string
}

// Build is one client directory on disk. Path is the directory, not part of
// build.json.
type Build struct {
	Recipe  string `json:"recipe"`
	Engine  string `json:"engine"`
	Image   string `json:"image,omitempty"`
	BuiltAt string `json:"built_at,omitempty"`
	Path    string `json:"path,omitempty"`
}

// buildRecord is the on-disk build.json: the build without its path, which is
// implied by where the file lives.
type buildRecord struct {
	Recipe  string `json:"recipe"`
	Engine  string `json:"engine"`
	Image   string `json:"image,omitempty"`
	BuiltAt string `json:"built_at,omitempty"`
}

// BuildDir is the client directory of a recipe: <Home>/<engine>/builds/<slug>.
func (m *Manager) BuildDir(engine, slug string) string {
	return filepath.Join(m.Home, engine, buildsDirName, slug)
}

// BuildPath is the patched engine executable the native runner starts, or
// ErrClientNotBuilt when the recipe has no usable client directory.
func (m *Manager) BuildPath(engine, slug string) (string, error) {
	eng, err := engineByName(engine)
	if err != nil {
		return "", err
	}
	if err := checkSlug(slug); err != nil {
		return "", err
	}
	dir := m.BuildDir(engine, slug)
	if _, err := os.Stat(filepath.Join(dir, buildFileName)); err != nil {
		return "", fmt.Errorf("%w: %s", ErrClientNotBuilt, slug)
	}
	bin := filepath.Join(dir, "bin", eng.Binary)
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrClientNotBuilt, slug)
	}
	return bin, nil
}

// Build reads the client directory of a recipe. It reports false when there is
// no build.json, so a half-copied directory is never mistaken for a build.
func (m *Manager) Build(engine, slug string) (Build, bool) {
	if _, err := engineByName(engine); err != nil {
		return Build{}, false
	}
	if err := checkSlug(slug); err != nil {
		return Build{}, false
	}
	dir := m.BuildDir(engine, slug)
	rec, ok := readBuildRecord(dir)
	if !ok {
		return Build{}, false
	}
	return rec.build(dir), true
}

// ListBuilds reports every client directory of an engine, sorted by recipe. A
// missing builds/ directory is an empty list, not an error: the engine may not
// have any patched client yet.
func (m *Manager) ListBuilds(engine string) []Build {
	out := []Build{}
	if _, err := engineByName(engine); err != nil {
		return out
	}
	root := filepath.Join(m.Home, engine, buildsDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), buildMarkerDir) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		rec, ok := readBuildRecord(dir)
		if !ok {
			continue
		}
		out = append(out, rec.build(dir))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Recipe < out[j].Recipe })
	return out
}

// EnsureBuild materialises the client directory for spec. An existing
// build.json is the whole answer: rebuilding would replace a binary under a
// running process. Otherwise:
//
//  1. `docker image inspect` the declared image;
//  2. if it is absent and the recipe has a command, run it with bash -lc in
//     the recipe directory — that script is the model repository's, the Hub
//     never applies patches itself;
//  3. `docker create` one throwaway container, `docker cp` each artifact out
//     of it and `docker rm` it;
//  4. write build.json.
//
// Everything is assembled in a sibling temp directory and renamed into place,
// so a failed copy leaves no half client behind.
func (m *Manager) EnsureBuild(ctx context.Context, spec BuildSpec) (Build, error) {
	eng, err := engineByName(spec.Engine)
	if err != nil {
		return Build{}, err
	}
	if err := checkSlug(spec.Recipe); err != nil {
		return Build{}, err
	}
	switch {
	case spec.Image == "":
		return Build{}, errors.New("engines: engine build has no image")
	case len(spec.Artifacts) == 0:
		return Build{}, errors.New("engines: engine build has no artifacts")
	}
	dir := m.BuildDir(spec.Engine, spec.Recipe)
	if rec, ok := readBuildRecord(dir); ok {
		return rec.build(dir), nil
	}

	if _, err := m.lookPath()("docker"); err != nil {
		return Build{}, fmt.Errorf("docker: %w", err)
	}
	out, code, err := m.run()(ctx, "", "docker", "image", "inspect", spec.Image)
	if err != nil {
		return Build{}, err
	}
	if code != 0 {
		if strings.TrimSpace(spec.Command) == "" {
			return Build{}, fmt.Errorf("%w: %s", ErrImageNotBuilt, spec.Image)
		}
		out, code, err = m.run()(ctx, spec.Dir, "bash", "-lc", spec.Command)
		if err != nil {
			return Build{}, fmt.Errorf("build engine image: %w", err)
		}
		if code != 0 {
			return Build{}, fmt.Errorf("build engine image %s: exit %d: %s", spec.Image, code, strings.TrimSpace(out))
		}
	}

	out, code, err = m.run()(ctx, "", "docker", "create", spec.Image)
	if err != nil {
		return Build{}, err
	}
	if code != 0 {
		return Build{}, fmt.Errorf("docker create %s: exit %d: %s", spec.Image, code, strings.TrimSpace(out))
	}
	id := lastLine(out)
	if id == "" {
		return Build{}, fmt.Errorf("docker create %s: empty container id", spec.Image)
	}
	// The container is a scratch copy source; removing it must happen even
	// when a cp fails, and must not be cancelled with the request.
	defer func() {
		_, _, _ = m.run()(context.WithoutCancel(ctx), "", "docker", "rm", "-f", id)
	}()

	root := filepath.Dir(dir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Build{}, fmt.Errorf("create builds dir: %w", err)
	}
	tmp, err := os.MkdirTemp(root, buildMarkerDir)
	if err != nil {
		return Build{}, fmt.Errorf("temp build dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	for _, artifact := range spec.Artifacts {
		// "dest=src" puts the artifact at an explicit path inside the client
		// (a vision runtime wants runtime/bin/python3.12, not a flat
		// python3.12 beside the engine). A plain path keeps the basename.
		entry, src := splitArtifact(artifact)
		src = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(src)), "/")
		if src == "" || src == "." || src == ".." {
			return Build{}, fmt.Errorf("engines: bad artifact %q", artifact)
		}
		if entry == "" {
			entry = filepath.Base(src)
		}
		if entry == "" || entry == "." || strings.HasPrefix(entry, "..") || filepath.IsAbs(entry) {
			return Build{}, fmt.Errorf("engines: bad artifact destination %q", artifact)
		}
		dst := filepath.Join(tmp, filepath.FromSlash(entry))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Build{}, fmt.Errorf("artifact dir %s: %w", filepath.Dir(dst), err)
		}
		out, code, err := m.run()(ctx, "", "docker", "cp", id+":/"+src, dst)
		if err != nil {
			return Build{}, fmt.Errorf("docker cp %s: %w", src, err)
		}
		if code != 0 {
			return Build{}, fmt.Errorf("docker cp %s: exit %d: %s", src, code, strings.TrimSpace(out))
		}
	}

	// The client is only usable with the engine binary in it: a missing
	// artifact list entry must fail here, not at launch.
	if st, err := os.Stat(filepath.Join(tmp, "bin", eng.Binary)); err != nil || st.IsDir() {
		return Build{}, fmt.Errorf("engine build has no bin/%s (artifacts %v)", eng.Binary, spec.Artifacts)
	}

	rec := buildRecord{
		Recipe:  spec.Recipe,
		Engine:  spec.Engine,
		Image:   spec.Image,
		BuiltAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return Build{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, buildFileName), append(data, '\n'), 0o644); err != nil {
		return Build{}, fmt.Errorf("write build.json: %w", err)
	}
	// A concurrent build may have won the race; its client is as good.
	if err := os.Rename(tmp, dir); err != nil {
		if current, ok := readBuildRecord(dir); ok {
			return current.build(dir), nil
		}
		return Build{}, fmt.Errorf("place engine client: %w", err)
	}
	// The image was a build environment, not an artifact: everything worth
	// keeping is in the client directory now, and a builder image costs tens
	// of gigabytes per recipe. build.json keeps its name, so the client can
	// still be traced back to what produced it.
	m.dropImage(ctx, spec.Image)
	return rec.build(dir), nil
}

// dropImage removes a builder image once its artifacts are on disk. A missing
// image, or one another container still uses, is not an error: the caller
// already has what it needs. It runs with a detached context so a cancelled
// request cannot leave the image behind.
func (m *Manager) dropImage(ctx context.Context, image string) {
	if strings.TrimSpace(image) == "" {
		return
	}
	_, _, _ = m.run()(context.WithoutCancel(ctx), "", "docker", "image", "rm", image)
}

// RemoveBuild deletes the client directory. Removing what is not there is a
// no-op: the UI calls it twice after a failed install.
func (m *Manager) RemoveBuild(engine, slug string) error {
	if _, err := engineByName(engine); err != nil {
		return err
	}
	if err := checkSlug(slug); err != nil {
		return err
	}
	if m.Home == "" {
		return errors.New("engines: empty home")
	}
	return os.RemoveAll(m.BuildDir(engine, slug))
}

// ExpandEnv turns the build's env block into one a process can use: every
// relative value is a path inside the client directory, so it is joined onto
// root. Absolute paths, URLs and anything else that is not a path are left
// alone.
func ExpandEnv(env map[string]string, root string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		switch {
		case v == "", filepath.IsAbs(v), strings.Contains(v, "://"):
			out[k] = v
		default:
			out[k] = filepath.Join(root, v)
		}
	}
	return out
}

// checkSlug keeps a recipe slug from naming anything but one directory under
// builds/: the slug comes from recipe YAML, which is not trusted to be a path.
func checkSlug(slug string) error {
	switch {
	case slug == "", slug == ".", slug == "..", strings.ContainsAny(slug, `/\\`):
		return fmt.Errorf("engines: bad recipe slug %q", slug)
	}
	return nil
}

func readBuildRecord(dir string) (buildRecord, bool) {
	data, err := os.ReadFile(filepath.Join(dir, buildFileName))
	if err != nil {
		return buildRecord{}, false
	}
	var rec buildRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.Recipe == "" {
		return buildRecord{}, false
	}
	return rec, true
}

func (b buildRecord) build(dir string) Build {
	return Build{Recipe: b.Recipe, Engine: b.Engine, Image: b.Image, BuiltAt: b.BuiltAt, Path: dir}
}

// run is the docker/build-command hook; nil means a real process.
func (m *Manager) run() func(context.Context, string, ...string) (string, int, error) {
	if m.Run != nil {
		return m.Run
	}
	return runCommandDir
}

func (m *Manager) lookPath() func(string) (string, error) {
	if m.LookPath != nil {
		return m.LookPath
	}
	return exec.LookPath
}

// runCommandDir executes one command in dir (empty means the daemon's own
// working directory) and returns the combined output and exit code.
func runCommandDir(ctx context.Context, dir string, args ...string) (string, int, error) {
	if len(args) == 0 {
		return "", -1, errors.New("engines: empty command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
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

// lastLine is the container id docker create prints: the last non-empty line
// of its output.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range slices.Backward(lines) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// splitArtifact understands the two artifact forms: "/image/path" (kept under
// its basename in the client) and "client/path=/image/path".
func splitArtifact(artifact string) (dest, src string) {
	if before, after, ok := strings.Cut(artifact, "="); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return "", artifact
}
