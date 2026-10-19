package dockerx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// fakeDockerScript is a `docker` stand-in driven entirely by env vars. Every
// invocation appends its argv to $FAKE_DOCKER_LOG, so a test can assert the
// exact command the Hub would have run.
const fakeDockerScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG" 2>/dev/null
cmd="$1"
shift
case "$cmd" in
  ps)
    if [ "$1" = "-a" ]; then
      printf '%s\n' "$FAKE_PS_ANY"
    else
      case "$*" in
        *Status*) printf '%s\n' "$FAKE_PS_LIST" ;;
        *Label*) printf '%s\n' "$FAKE_PS_PROJECTS" ;;
        *) printf '%s\n' "$FAKE_PS_PROJECTS" ;;
      esac
    fi
    ;;
  inspect) printf '%s\n' "$FAKE_INSPECT" ;;
  image)
    if [ "$1" = "inspect" ]; then
      if [ -n "$FAKE_IMAGE_PRESENT" ]; then exit 0; else exit 1; fi
    fi
    ;;

  logs) printf '%s\n' "$FAKE_LOGS" ;;
  images) printf '%s\n' "$FAKE_IMAGES" ;;
  volume)
    if [ "$1" = "ls" ]; then printf '%s\n' "$FAKE_VOLUMES"; fi
    ;;
  builder) echo "Total: 0B" ;;
  image) echo "Total reclaimed space: 0B" ;;
  compose) printf '%s\n' "$FAKE_COMPOSE_OUT" ;;
esac
exit 0
`

// installFakeDocker writes the fake docker binary, prepends it to PATH and
// returns the argv log path.
func installFakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(fakeDockerScript), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	logPath := filepath.Join(dir, "args.log")
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readArgs(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func writeRecipe(t *testing.T, root, slug, recipeYAML, composeYAML string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir recipe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe.yaml: %v", err)
	}
	if composeYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeYAML), 0o644); err != nil {
			t.Fatalf("write compose: %v", err)
		}
	}
}

func newTestDocker(t *testing.T, regDir string) (*Docker, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.Config{Project: "testproj", AppProjectPrefix: "testproj", AppNetwork: "testproj-net", DataDir: t.TempDir(), RegistryPath: regDir}
	d := New(cfg, registry.NewCache(regDir), db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return d, db
}

func TestParsePorts(t *testing.T) {
	got := ParsePorts("0.0.0.0:8188->8188/tcp, 7860/tcp, [::]:9000->9000/tcp")
	if got["8188"] != 8188 || got["9000"] != 9000 {
		t.Fatalf("published ports: %#v", got)
	}
	if _, ok := got["7860"]; ok {
		t.Fatalf("unpublished 7860 should be dropped: %#v", got)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %#v", got)
	}
}

func TestClassifyStartupFailure(t *testing.T) {
	logs := "Free memory on device cuda:0 (94.49/121.69 GiB) on startup is less than desired GPU memory utilization (0.93, 113.17 GiB)."
	msg := ClassifyStartupFailure(logs)
	if !strings.Contains(msg, "Not enough free memory to start") {
		t.Fatalf("vLLM classification: %q", msg)
	}
	if !strings.Contains(msg, "18.68 GiB short") {
		t.Fatalf("shortfall not computed: %q", msg)
	}
	if got := ClassifyStartupFailure("everything is fine"); got != "" {
		t.Fatalf("unknown failure should classify to empty, got %q", got)
	}
	if !strings.Contains(ClassifyStartupFailure("CUDA out of memory"), "Ran out of memory") {
		t.Fatal("CUDA OOM not classified")
	}
}

func TestParseComposeImages(t *testing.T) {
	dir := t.TempDir()
	writeRecipe(t, dir, "demo", "name: Demo\nslug: demo\n", "services:\n  app:\n    image: testproj-demo\n  b:\n    build: .\n")
	images := ParseComposeImages(filepath.Join(dir, "demo"))
	if len(images) != 1 || images[0] != "testproj-demo" {
		t.Fatalf("images: %#v", images)
	}
}

func TestEnsureRuntimeEnvFillsSecrets(t *testing.T) {
	dir := t.TempDir()
	tmpl := "# comment\nUSER_AUTH_SECRET=\nPOSTGRES_PASSWORD=already\nMINIO_ROOT_PASSWORD=\nS3_AWS_SECRET_ACCESS_KEY=\n"
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	path, created, err := EnsureRuntimeEnv(dir)
	if err != nil || !created {
		t.Fatalf("EnsureRuntimeEnv: path=%q created=%v err=%v", path, created, err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if strings.Contains(text, "USER_AUTH_SECRET=\n") {
		t.Fatalf("USER_AUTH_SECRET not filled: %q", text)
	}
	if !strings.Contains(text, "POSTGRES_PASSWORD=already") {
		t.Fatalf("existing value overwritten: %q", text)
	}
	// MinIO and S3 share one generated secret.
	var minio, s3 string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "MINIO_ROOT_PASSWORD=") {
			minio = strings.TrimPrefix(line, "MINIO_ROOT_PASSWORD=")
		}
		if strings.HasPrefix(line, "S3_AWS_SECRET_ACCESS_KEY=") {
			s3 = strings.TrimPrefix(line, "S3_AWS_SECRET_ACCESS_KEY=")
		}
	}
	if minio == "" || minio != s3 {
		t.Fatalf("shared minio/s3 secret mismatch: %q vs %q", minio, s3)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("env mode: %v, want 0600", st.Mode().Perm())
	}

	// Second call is a no-op.
	_, createdAgain, _ := EnsureRuntimeEnv(dir)
	if createdAgain {
		t.Fatal("EnsureRuntimeEnv should not recreate an existing .env")
	}
}

func TestListContainers(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_PS_LIST", "spark-comfyui\tUp 2 hours\tspark-comfyui:latest\t0.0.0.0:8188->8188/tcp")
	d, _ := newTestDocker(t, t.TempDir())

	list := d.Client.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("containers: %#v", list)
	}
	c := list[0]
	if c.Name != "spark-comfyui" || c.Image != "spark-comfyui:latest" || c.Ports["8188"] != 8188 {
		t.Fatalf("container: %#v", c)
	}
	if c.CPUPercent != 0 || c.MemoryMB != 0 {
		t.Fatalf("cpu/memory should stay 0: %#v", c)
	}
}

func TestInstallBuildRecipe(t *testing.T) {
	logPath := installFakeDocker(t)
	t.Setenv("FAKE_COMPOSE_OUT", "Building app\n#8 DONE 12.3s")
	regDir := t.TempDir()
	writeRecipe(t, regDir, "demo",
		"name: Demo\nslug: demo\ndocker:\n  build: true\n",
		"services:\n  app:\n    image: testproj-demo\n")
	d, db := newTestDocker(t, regDir)

	var lines []string
	if err := d.InstallRecipe(context.Background(), "demo", "", "", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("install: %v", err)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"[spark-ai-hub] Starting install for demo...",
		"[spark-ai-hub] Running: docker compose -p testproj-demo -f ",
		" build",
		"#8 DONE 12.3s",
		"Pruning build cache",
		"Pruning orphaned images...",
		"[spark-ai-hub] demo installed successfully!",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("install log missing %q:\n%s", want, joined)
		}
	}

	installed, err := db.InstalledRecipes(context.Background())
	if err != nil || installed["demo"] != struct{}{} {
		t.Fatalf("installed set: %#v err=%v", installed, err)
	}
	args := readArgs(t, logPath)
	if !strings.Contains(args, "compose -p testproj-demo -f ") {
		t.Fatalf("compose project prefix not used:\n%s", args)
	}
	if !strings.Contains(args, "build") {
		t.Fatalf("compose build not invoked:\n%s", args)
	}
}

func TestInstallPullRecipe(t *testing.T) {
	logPath := installFakeDocker(t)
	t.Setenv("FAKE_COMPOSE_OUT", "pulling layer")
	regDir := t.TempDir()
	writeRecipe(t, regDir, "pullme",
		"name: Pull\nslug: pullme\n",
		"services:\n  app:\n    image: testproj-pull\n")
	d, _ := newTestDocker(t, regDir)

	var lines []string
	if err := d.InstallRecipe(context.Background(), "pullme", "", "", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("install: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Running: docker compose -p testproj-pullme -f ") || !strings.Contains(joined, " pull") {
		t.Fatalf("pull command wrong:\n%s", joined)
	}
	if strings.Contains(joined, "Pruning build cache") {
		t.Fatalf("pull install must not prune the build cache:\n%s", joined)
	}
	args := readArgs(t, logPath)
	if !strings.Contains(args, " pull") {
		t.Fatalf("compose pull not invoked:\n%s", args)
	}
}

func TestInstallFailureKeepsDBEmpty(t *testing.T) {
	// A compose command that exits non-zero: the wrapper script flips the exit
	// code when FAKE_COMPOSE_FAIL is set.
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_DOCKER_LOG\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(dir, "args.log"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	regDir := t.TempDir()
	writeRecipe(t, regDir, "boom", "name: Boom\nslug: boom\n", "services:\n  app:\n    image: testproj-boom\n")
	d, db := newTestDocker(t, regDir)

	var lines []string
	_ = d.InstallRecipe(context.Background(), "boom", "", "", func(l string) { lines = append(lines, l) })
	if got := strings.Join(lines, "\n"); !strings.Contains(got, "Install failed with exit code 1") {
		t.Fatalf("failure line missing:\n%s", got)
	}
	installed, _ := db.InstalledRecipes(context.Background())
	if len(installed) != 0 {
		t.Fatalf("failed install must not record the recipe: %#v", installed)
	}
}

func TestUpdateBuildFailureReportsExitCode(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_DOCKER_LOG\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(dir, "args.log"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	regDir := t.TempDir()
	writeRecipe(t, regDir, "demo", "name: Demo\nslug: demo\ndocker:\n  build: true\n", "services:\n  app:\n    image: testproj-demo\n")
	d, _ := newTestDocker(t, regDir)

	var lines []string
	_ = d.UpdateRecipe(context.Background(), "demo", "", "", func(l string) { lines = append(lines, l) })
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "Rebuild failed with exit code 1") {
		t.Fatalf("missing rebuild failure line:\n%s", got)
	}
	if strings.Contains(got, "rebuilt successfully") {
		t.Fatalf("failed rebuild must not report success:\n%s", got)
	}
}

func TestStopRecipe(t *testing.T) {
	logPath := installFakeDocker(t)
	regDir := t.TempDir()
	writeRecipe(t, regDir, "demo", "name: Demo\nslug: demo\n", "services:\n  app:\n    image: testproj-demo\n")
	d, _ := newTestDocker(t, regDir)
	result, err := d.StopRecipe(context.Background(), "demo")
	if err != nil || result != "stopped" {
		t.Fatalf("stop: %q %v", result, err)
	}
	if !strings.Contains(readArgs(t, logPath), "compose -p testproj-demo -f ") {
		t.Fatalf("stop did not use the project prefix")
	}
}

func TestRemoveScopesSharedImages(t *testing.T) {
	logPath := installFakeDocker(t)
	regDir := t.TempDir()
	writeRecipe(t, regDir, "alpha", "name: A\nslug: alpha\n", "services:\n  app:\n    image: testproj-shared\n")
	writeRecipe(t, regDir, "beta", "name: B\nslug: beta\n", "services:\n  app:\n    image: testproj-shared\n")
	d, db := newTestDocker(t, regDir)
	ctx := context.Background()
	_ = db.UpsertInstalledRecipe(ctx, "alpha", "testproj-alpha")
	_ = db.UpsertInstalledRecipe(ctx, "beta", "testproj-beta")

	result, err := d.RemoveRecipe(ctx, "alpha")
	if err != nil || result != "removed" {
		t.Fatalf("remove: %q %v", result, err)
	}
	args := readArgs(t, logPath)
	if strings.Contains(args, "rmi -f testproj-shared") {
		t.Fatalf("shared image must not be removed:\n%s", args)
	}
	installed, _ := db.InstalledRecipes(ctx)
	if len(installed) != 1 || installed["beta"] != struct{}{} {
		t.Fatalf("DB rows after remove: %#v", installed)
	}
}

func TestPurgeRemovesProjectVolumeAndOwnImage(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_VOLUMES", "testproj-demo_data\nsomeone-else_data")
	t.Setenv("FAKE_IMAGES", "testproj-demo:latest\nspark-ai-hub-proxy:latest")
	regDir := t.TempDir()
	writeRecipe(t, regDir, "demo", "name: Demo\nslug: demo\n", "services:\n  app:\n    image: testproj-demo\n")
	d, _ := newTestDocker(t, regDir)

	result, err := d.PurgeRecipe(context.Background(), "demo")
	if err != nil || result != "purged" {
		t.Fatalf("purge: %q %v", result, err)
	}
}

func TestMemoryPlan(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_PS_PROJECTS", "testproj-b\ntestproj-c")
	meminfo := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(meminfo, []byte("MemTotal: 104857600 kB\nMemAvailable: 10485760 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := MeminfoPath
	MeminfoPath = meminfo
	t.Cleanup(func() { MeminfoPath = old })

	regDir := t.TempDir()
	// Memory lives under `requirements`, exactly as upstream's Recipe model reads it.
	llm := func(slug string, mem int) string {
		return "name: " + slug + "\nslug: " + slug + "\ncategory: llm\nrequirements:\n  min_memory_gb: " + itoa(mem) + "\nui:\n  type: api-only\n"
	}
	writeRecipe(t, regDir, "a", llm("a", 8), "services:\n  app:\n    image: testproj-a\n")
	writeRecipe(t, regDir, "b", llm("b", 20), "services:\n  app:\n    image: testproj-b\n")
	writeRecipe(t, regDir, "c", llm("c", 40), "services:\n  app:\n    image: testproj-c\n")
	d, db := newTestDocker(t, regDir)
	ctx := context.Background()
	_ = db.UpsertInstalledRecipe(ctx, "b", "testproj-b")
	_ = db.UpsertInstalledRecipe(ctx, "c", "testproj-c")

	need, free, running := d.MemoryPlan(ctx, "a")
	if need != 8 {
		t.Fatalf("need = %v, want 8", need)
	}
	// 10 GiB free minus the two not-yet-ready apps (20+40).
	if free != -50 {
		t.Fatalf("free = %v, want -50", free)
	}
	if len(running) != 2 {
		t.Fatalf("running = %#v", running)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
