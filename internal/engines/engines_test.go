package engines

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeGitHub serves the release metadata, the tarball and its checksum, so the
// whole install path runs without touching github.com.
type fakeGitHub struct {
	tag       string
	asset     string
	tarball   []byte
	checksum  string
	latest404 bool
	latestHit atomic.Int32
	assetHit  atomic.Int32
}

func newFakeGitHub(t *testing.T, version, arch string) (*fakeGitHub, *httptest.Server) {
	t.Helper()
	asset := fmt.Sprintf("tensorfold-%s-%s.tar.gz", version, arch)
	dir := fmt.Sprintf("tensorfold-%s-%s", version, arch)
	tarball := buildTarGz(t, map[string]string{
		dir + "/bin/tensorfold-native": "#!/bin/sh\necho " + version + "\n",
		dir + "/VERSION":               version + "\n",
	})
	sum := sha256.Sum256(tarball)

	f := &fakeGitHub{
		tag:      "v" + version,
		asset:    asset,
		tarball:  tarball,
		checksum: hex.EncodeToString(sum[:]),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/ashhart/TensorFold/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		f.latestHit.Add(1)
		if f.latest404 {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q}`, f.tag)
	})
	assetPath := "/ashhart/TensorFold/releases/download/" + f.tag + "/" + asset
	mux.HandleFunc(assetPath, func(w http.ResponseWriter, r *http.Request) {
		f.assetHit.Add(1)
		_, _ = w.Write(f.tarball)
	})
	mux.HandleFunc(assetPath+".sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", f.checksum, asset)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func newTestManager(t *testing.T, srv *httptest.Server) *Manager {
	t.Helper()
	return &Manager{
		Home:         filepath.Join(t.TempDir(), "engines"),
		HTTP:         srv.Client(),
		GitHubAPI:    srv.URL,
		DownloadBase: srv.URL,
	}
}

func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestListBeforeInstall(t *testing.T) {
	arch, err := (&Manager{}).DetectArch()
	if err != nil {
		t.Fatalf("arch: %v", err)
	}
	_, srv := newFakeGitHub(t, "1.1.1", arch)
	m := newTestManager(t, srv)

	list, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %+v, want one engine", list)
	}
	got := list[0]
	if got.Name != TensorFold || got.Installed || got.Latest != "1.1.1" || got.Version != "" {
		t.Fatalf("status = %+v, want tensorfold not installed with latest 1.1.1", got)
	}
}

func TestListToleratesGitHubFailure(t *testing.T) {
	arch, _ := (&Manager{}).DetectArch()
	f, srv := newFakeGitHub(t, "1.1.1", arch)
	f.latest404 = true
	m := newTestManager(t, srv)

	list, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list[0].Latest != "" {
		t.Fatalf("latest = %q, want empty on a failed lookup", list[0].Latest)
	}
	// The failure is cached: a second call must not hit GitHub again.
	if _, err := m.List(context.Background()); err != nil {
		t.Fatalf("second list: %v", err)
	}
	if n := f.latestHit.Load(); n != 1 {
		t.Fatalf("latest lookups = %d, want 1 (cached)", n)
	}
}

func TestInstallLifecycle(t *testing.T) {
	arch, err := (&Manager{}).DetectArch()
	if err != nil {
		t.Fatalf("arch: %v", err)
	}
	_, srv := newFakeGitHub(t, "1.1.1", arch)
	m := newTestManager(t, srv)
	ctx := context.Background()

	st, err := m.Install(ctx, TensorFold, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if st.Version != "1.1.1" || !st.Installed {
		t.Fatalf("status = %+v", st)
	}
	bin, err := m.Path(TensorFold)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	body, err := os.ReadFile(bin)
	if err != nil || !strings.Contains(string(body), "1.1.1") {
		t.Fatalf("binary = %q, %v", body, err)
	}
	link, err := os.Readlink(filepath.Join(m.Home, TensorFold, "current"))
	if err != nil || link != "1.1.1" {
		t.Fatalf("current -> %q, %v; want 1.1.1", link, err)
	}
	if v, ok := m.Installed(TensorFold); !ok || v != "1.1.1" {
		t.Fatalf("Installed = %q, %v", v, ok)
	}

	// Installing the same version again is a conflict with the version named.
	st, err = m.Install(ctx, TensorFold, "1.1.1")
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("second install err = %v, want ErrAlreadyInstalled", err)
	}
	if st.Version != "1.1.1" {
		t.Fatalf("conflict status = %+v, want version 1.1.1", st)
	}

	// Update to the same latest release changes nothing.
	upd, changed, err := m.Update(ctx, TensorFold)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if changed || upd.Version != "1.1.1" {
		t.Fatalf("update = %+v changed=%v, want 1.1.1 unchanged", upd, changed)
	}

	// Delete removes the tree and the engine is no longer installed.
	if err := m.Delete(TensorFold); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Path(TensorFold); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("path after delete = %v, want ErrNotInstalled", err)
	}
	if _, err := os.Stat(filepath.Join(m.Home, TensorFold)); !os.IsNotExist(err) {
		t.Fatalf("engine dir still exists: %v", err)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	arch, _ := (&Manager{}).DetectArch()
	f, srv := newFakeGitHub(t, "1.1.1", arch)
	f.checksum = strings.Repeat("0", 64)
	m := newTestManager(t, srv)

	if _, err := m.Install(context.Background(), TensorFold, "1.1.1"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("install err = %v, want checksum mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(m.Home, TensorFold, "1.1.1")); !os.IsNotExist(err) {
		t.Fatalf("a failed install left a version behind: %v", err)
	}
}

func TestInstallUnknownEngine(t *testing.T) {
	_, srv := newFakeGitHub(t, "1.1.1", "linux-aarch64")
	m := newTestManager(t, srv)

	if _, err := m.Install(context.Background(), "nope", ""); !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("install err = %v, want ErrUnknownEngine", err)
	}
	if _, err := m.Path("nope"); !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("path err = %v, want ErrUnknownEngine", err)
	}
	if err := m.Delete("nope"); !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("delete err = %v, want ErrUnknownEngine", err)
	}
}

func TestDetectArch(t *testing.T) {
	arch, err := (&Manager{}).DetectArch()
	if err != nil {
		t.Fatalf("detect arch: %v", err)
	}
	if arch != "linux-aarch64" && arch != "linux-x86_64" {
		t.Fatalf("arch = %q", arch)
	}
}
