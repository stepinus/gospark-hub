package engines

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeDocker answers the commands a client build issues: `image inspect`,
// `create`, `cp`, `rm`, plus the model repository's own build script. It
// materialises cp destinations the way docker does, so the copied layout can
// be asserted without a daemon.
type fakeDocker struct {
	images   map[string]bool // image name -> present
	files    map[string]bool // artifact path inside the image -> exists
	failCP   string          // artifact whose docker cp fails
	builds   string          // image the build script produces
	buildErr int             // exit code of the build script
	calls    []dockerCall
}

type dockerCall struct {
	dir  string
	args []string
}

func (f *fakeDocker) manager(home string) *Manager {
	m := NewManager(home)
	m.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	m.Run = f.run
	return m
}

func (f *fakeDocker) run(_ context.Context, dir string, args ...string) (string, int, error) {
	f.calls = append(f.calls, dockerCall{dir: dir, args: args})
	switch {
	case len(args) >= 4 && args[0] == "docker" && args[1] == "image" && args[2] == "inspect":
		if f.images[args[3]] {
			return "sha256:abc", 0, nil
		}
		return "no such image", 1, nil
	case len(args) >= 3 && args[0] == "docker" && args[1] == "create":
		return "container123\n", 0, nil
	case len(args) >= 4 && args[0] == "docker" && args[1] == "cp":
		src := strings.TrimPrefix(strings.TrimPrefix(args[2], "container123:"), "/")
		if src == f.failCP || !f.files[src] {
			return "no such file or directory: " + src, 1, nil
		}
		if err := os.MkdirAll(args[3], 0o755); err != nil {
			return "", -1, err
		}
		name := "marker"
		if filepath.Base(src) == "bin" {
			name = TensorFoldBinary
		}
		if err := os.WriteFile(filepath.Join(args[3], name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			return "", -1, err
		}
		return "", 0, nil
	case len(args) >= 3 && args[0] == "docker" && args[1] == "rm":
		return args[2], 0, nil
	case len(args) >= 3 && args[0] == "bash" && args[1] == "-lc":
		if f.buildErr != 0 {
			return "prepare.sh failed", f.buildErr, nil
		}
		if f.images == nil {
			f.images = map[string]bool{}
		}
		f.images[f.builds] = true
		return "", 0, nil
	}
	return "", 0, nil
}

// imageRemoved reports whether the builder image was dropped after its
// artifacts were copied out.
func (f *fakeDocker) imageRemoved(image string) bool {
	for _, c := range f.calls {
		if len(c.args) >= 4 && c.args[0] == "docker" && c.args[1] == "image" && c.args[2] == "rm" && c.args[3] == image {
			return true
		}
	}
	return false
}

// counts reports how many calls matched a docker subcommand, e.g. "cp".
func (f *fakeDocker) counts(sub string) int {
	n := 0
	for _, c := range f.calls {
		if len(c.args) > 1 && c.args[1] == sub {
			n++
		}
	}
	return n
}

const buildImage = "tensorfold-qwen38:zig-db28187"

func artifacts() []string { return []string{"native/bin", "share", "python", "fatbin"} }

func allFiles() map[string]bool {
	return map[string]bool{
		"native/bin": true,
		"share":      true,
		"python":     true,
		"fatbin":     true,
	}
}

func spec(dir string) BuildSpec {
	return BuildSpec{
		Recipe:    "qwen38-flash-next-native",
		Engine:    TensorFold,
		Image:     buildImage,
		Artifacts: artifacts(),
		Dir:       dir,
	}
}

func TestExpandEnvResolvesClientPaths(t *testing.T) {
	root := filepath.Join("/home", "spark", "tensorfold", "builds", "qwen")
	got := ExpandEnv(map[string]string{
		"TENSORFOLD_CUDA_KERNELS": "share/tensorfold/cuda/sm121",
		"PYTHONPATH":              "python",
		"TENSORFOLD_HOME":         "/opt/tensorfold",
		"HF_ENDPOINT":             "https://hf.example",
		"EMPTY":                   "",
	}, root)

	want := map[string]string{
		"TENSORFOLD_CUDA_KERNELS": filepath.Join(root, "share/tensorfold/cuda/sm121"),
		"PYTHONPATH":              filepath.Join(root, "python"),
		"TENSORFOLD_HOME":         "/opt/tensorfold",
		"HF_ENDPOINT":             "https://hf.example",
		"EMPTY":                   "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if ExpandEnv(nil, root) == nil {
		t.Error("ExpandEnv(nil) is nil")
	}
}

func TestEnsureBuildCopiesArtifacts(t *testing.T) {
	home := t.TempDir()
	f := &fakeDocker{images: map[string]bool{buildImage: true}, files: allFiles()}
	m := f.manager(home)

	build, err := m.EnsureBuild(context.Background(), spec(t.TempDir()))
	if err != nil {
		t.Fatalf("EnsureBuild: %v", err)
	}
	wantDir := filepath.Join(home, TensorFold, "builds", "qwen38-flash-next-native")
	if build.Path != wantDir || build.Recipe != "qwen38-flash-next-native" || build.Image != buildImage {
		t.Fatalf("build = %+v, want path %s", build, wantDir)
	}
	if build.BuiltAt == "" {
		t.Error("built_at is empty")
	}

	// The binary and every directory artifact landed under their base name.
	bin := filepath.Join(wantDir, "bin", TensorFoldBinary)
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("binary: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("binary mode = %v, want executable", st.Mode())
	}
	for _, dir := range []string{"share", "python", "fatbin"} {
		if _, err := os.Stat(filepath.Join(wantDir, dir, "marker")); err != nil {
			t.Errorf("%s artifact: %v", dir, err)
		}
	}

	// build.json is what the API reads back.
	data, err := os.ReadFile(filepath.Join(wantDir, buildFileName))
	if err != nil {
		t.Fatalf("build.json: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("build.json: %v", err)
	}
	if rec["recipe"] != "qwen38-flash-next-native" || rec["engine"] != TensorFold || rec["image"] != buildImage {
		t.Fatalf("build.json = %s", data)
	}
	if _, ok := rec["built_at"]; !ok {
		t.Fatalf("build.json has no built_at: %s", data)
	}
	if _, ok := rec["path"]; ok {
		t.Fatalf("build.json carries the path: %s", data)
	}

	// Every artifact was copied once, out of one container that was removed.
	if got := f.counts("cp"); got != len(artifacts()) {
		t.Errorf("docker cp calls = %d, want %d", got, len(artifacts()))
	}
	if got := f.counts("rm"); got != 1 {
		t.Errorf("docker rm calls = %d, want 1", got)
	}
	// The image is a build environment, not an artifact: it must be gone once
	// the client exists, or every recipe keeps tens of gigabytes around.
	if !f.imageRemoved(buildImage) {
		t.Error("builder image was not removed after a successful build")
	}

	if path, err := m.BuildPath(TensorFold, "qwen38-flash-next-native"); err != nil || path != bin {
		t.Fatalf("BuildPath = %q, %v", path, err)
	}
	builds := m.ListBuilds(TensorFold)
	if len(builds) != 1 || builds[0].Recipe != "qwen38-flash-next-native" || builds[0].Image != buildImage || builds[0].Path != wantDir {
		t.Fatalf("ListBuilds = %+v", builds)
	}

	// A second install is a no-op: the directory already answers.
	before := len(f.calls)
	if _, err := m.EnsureBuild(context.Background(), spec(t.TempDir())); err != nil {
		t.Fatalf("second EnsureBuild: %v", err)
	}
	if len(f.calls) != before {
		t.Fatalf("second EnsureBuild ran %d more docker calls", len(f.calls)-before)
	}

	// A directory without build.json is not a build.
	stray := filepath.Join(home, TensorFold, "builds", "stray")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}
	if got := len(m.ListBuilds(TensorFold)); got != 1 {
		t.Fatalf("ListBuilds after stray = %d entries", got)
	}

	if err := m.RemoveBuild(TensorFold, "qwen38-flash-next-native"); err != nil {
		t.Fatalf("RemoveBuild: %v", err)
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Fatalf("client dir survived removal: %v", err)
	}
	if _, err := m.BuildPath(TensorFold, "qwen38-flash-next-native"); !errors.Is(err, ErrClientNotBuilt) {
		t.Fatalf("BuildPath after remove = %v, want ErrClientNotBuilt", err)
	}
	// Removing twice is the UI's second click, not an error.
	if err := m.RemoveBuild(TensorFold, "qwen38-flash-next-native"); err != nil {
		t.Fatalf("second RemoveBuild: %v", err)
	}
}

func TestEnsureBuildWithoutImage(t *testing.T) {
	home := t.TempDir()
	f := &fakeDocker{files: allFiles()}
	m := f.manager(home)

	_, err := m.EnsureBuild(context.Background(), spec(t.TempDir()))
	if !errors.Is(err, ErrImageNotBuilt) {
		t.Fatalf("EnsureBuild = %v, want ErrImageNotBuilt", err)
	}
	if !strings.Contains(err.Error(), buildImage) {
		t.Fatalf("error does not name the image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, TensorFold, "builds", "qwen38-flash-next-native")); !os.IsNotExist(err) {
		t.Fatalf("client dir created without an image: %v", err)
	}
	if got := f.counts("create"); got != 0 {
		t.Fatalf("docker create calls = %d, want 0", got)
	}
}

func TestEnsureBuildRunsCommandInRecipeDir(t *testing.T) {
	home := t.TempDir()
	recipeDir := t.TempDir()
	f := &fakeDocker{files: allFiles(), builds: buildImage}
	m := f.manager(home)

	s := spec(recipeDir)
	s.Command = "scripts/prepare.sh"
	if _, err := m.EnsureBuild(context.Background(), s); err != nil {
		t.Fatalf("EnsureBuild: %v", err)
	}

	var script *dockerCall
	for i, c := range f.calls {
		if len(c.args) > 1 && c.args[0] == "bash" {
			script = &f.calls[i]
		}
	}
	if script == nil {
		t.Fatalf("build command did not run: %+v", f.calls)
	}
	if script.dir != recipeDir {
		t.Errorf("command cwd = %q, want %q", script.dir, recipeDir)
	}
	if want := []string{"bash", "-lc", "scripts/prepare.sh"}; !slices.Equal(script.args, want) {
		t.Errorf("command = %q, want %q", script.args, want)
	}
	// The image was only inspected once: the command is not run over an image
	// that already exists.
	if got := strings.Count(f.calls[0].args[0]+" "+strings.Join(f.calls[0].args[1:], " "), "inspect"); got != 1 {
		t.Errorf("first call = %q", f.calls[0].args)
	}
}

func TestEnsureBuildCommandFailure(t *testing.T) {
	home := t.TempDir()
	f := &fakeDocker{files: allFiles(), buildErr: 3}
	m := f.manager(home)

	s := spec(t.TempDir())
	s.Command = "scripts/prepare.sh"
	_, err := m.EnsureBuild(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Fatalf("EnsureBuild = %v, want the command's exit code", err)
	}
	if _, err := os.Stat(filepath.Join(home, TensorFold, "builds", "qwen38-flash-next-native")); !os.IsNotExist(err) {
		t.Fatalf("client dir created by a failed command: %v", err)
	}
}

func TestEnsureBuildCopyFailureLeavesNothing(t *testing.T) {
	home := t.TempDir()
	f := &fakeDocker{images: map[string]bool{buildImage: true}, files: allFiles(), failCP: "share"}
	m := f.manager(home)

	_, err := m.EnsureBuild(context.Background(), spec(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "share") {
		t.Fatalf("EnsureBuild = %v, want a cp failure naming share", err)
	}
	root := filepath.Join(home, TensorFold, "builds")
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read builds: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("builds dir holds %d entries after a failed copy, want 0", len(entries))
	}
	if got := f.counts("rm"); got != 1 {
		t.Errorf("docker rm calls = %d, want 1", got)
	}
	// A failed build keeps its image: a retry must not rebuild it from zero.
	if f.imageRemoved(buildImage) {
		t.Error("builder image was removed after a failed copy")
	}
}

func TestEnsureBuildWithoutBinary(t *testing.T) {
	home := t.TempDir()
	// The copies succeed, but none of them brings bin/tensorfold-native.
	f := &fakeDocker{
		images: map[string]bool{buildImage: true},
		files:  map[string]bool{"native/lib": true},
	}
	m := f.manager(home)
	s := spec(t.TempDir())
	s.Artifacts = []string{"native/lib"}

	_, err := m.EnsureBuild(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), TensorFoldBinary) {
		t.Fatalf("EnsureBuild = %v, want a missing binary", err)
	}
	if _, err := os.Stat(filepath.Join(home, TensorFold, "builds", "qwen38-flash-next-native")); !os.IsNotExist(err) {
		t.Fatalf("client dir created without the binary: %v", err)
	}
}

func TestEnsureBuildRejectsIncompleteSpec(t *testing.T) {
	m := (&fakeDocker{}).manager(t.TempDir())
	cases := map[string]BuildSpec{
		"recipe":    {Engine: TensorFold, Image: buildImage, Artifacts: artifacts()},
		"image":     {Recipe: "r", Engine: TensorFold, Artifacts: artifacts()},
		"artifacts": {Recipe: "r", Engine: TensorFold, Image: buildImage},
		"engine":    {Recipe: "r", Engine: "nope", Image: buildImage, Artifacts: artifacts()},
		"slug":      {Recipe: "../escape", Engine: TensorFold, Image: buildImage, Artifacts: artifacts()},
	}
	for name, s := range cases {
		if _, err := m.EnsureBuild(context.Background(), s); err == nil {
			t.Errorf("%s: EnsureBuild accepted an incomplete spec", name)
		}
	}
	// A slug is a directory name, not a path: a recipe must not be able to
	// write outside builds/.
	if err := m.RemoveBuild(TensorFold, "../escape"); err == nil {
		t.Error("RemoveBuild accepted a path slug")
	}
	if _, err := m.BuildPath(TensorFold, "../escape"); err == nil {
		t.Error("BuildPath accepted a path slug")
	}
}

// TestSplitArtifact pins both artifact forms: the flat one keeps the image
// basename, the explicit one puts the files where the vision runtime needs them.
func TestSplitArtifact(t *testing.T) {
	cases := []struct{ in, dest, src string }{
		{"/opt/tensorfold/share", "", "/opt/tensorfold/share"},
		{"runtime/bin/python3.12=/usr/bin/python3.12", "runtime/bin/python3.12", "/usr/bin/python3.12"},
	}
	for _, c := range cases {
		dest, src := splitArtifact(c.in)
		if dest != c.dest || src != c.src {
			t.Fatalf("splitArtifact(%q) = %q, %q; want %q, %q", c.in, dest, src, c.dest, c.src)
		}
	}
}
