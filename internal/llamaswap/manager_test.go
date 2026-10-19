package llamaswap

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// tarGz builds the flat one-binary archive a release ships.
func tarGz(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: Binary, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestInstallVerifiesAndUnpacks(t *testing.T) {
	archive := tarGz(t, "#!/bin/sh\necho llama-swap 265\n")
	sum := sha256Hex(archive)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v265"}`))
	})
	mux.HandleFunc("/mostlygeek/llama-swap/releases/download/v265/llama-swap_265_linux_arm64.tar.gz",
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/mostlygeek/llama-swap/releases/download/v265/llama-swap_265_checksums.txt",
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(sum + "  llama-swap_265_linux_arm64.tar.gz\n"))
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	home := t.TempDir()
	m := NewManager(home)
	m.GitHubAPI = srv.URL
	m.DownloadBase = srv.URL

	version, err := m.Install(context.Background(), "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if version != "265" {
		t.Fatalf("version = %q", version)
	}
	if got, ok := m.Installed(); !ok || got != "265" {
		t.Fatalf("installed = %q, %v", got, ok)
	}
	if _, err := os.Stat(m.Path()); err != nil {
		t.Fatalf("binary: %v", err)
	}
	if _, err := m.Install(context.Background(), "265"); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("second install: %v", err)
	}
}

const sampleConfig = `# live config, comments must survive a groups edit
logLevel: info
apiKeys:
  - "k1"
models:
  a5b:
    name: "A5B"
    proxy: http://127.0.0.1:8000
    cmd: docker start a5b
  comet:
    name: "Comet"
    proxy: http://127.0.0.1:8020
groups:
  heavy:
    swap: true
    members:
      - a5b
`

func TestSetGroupsKeepsTheRestOfTheFile(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ConfigPath(), []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	persistent := true
	err := m.SetGroups(map[string]GroupConfig{
		"heavy": {Persistent: &persistent, Members: []string{"a5b", "comet"}},
		"small": {Members: []string{}},
	})
	if err != nil {
		t.Fatalf("set groups: %v", err)
	}

	text, err := m.ConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "# live config, comments must survive a groups edit") {
		t.Fatalf("comment lost:\n%s", text)
	}
	if !strings.Contains(text, "apiKeys:") {
		t.Fatalf("api keys lost:\n%s", text)
	}
	cfg, err := ParseConfig([]byte(text))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if got := cfg.Groups["heavy"].Members; len(got) != 2 || got[1] != "comet" {
		t.Fatalf("heavy members = %q", got)
	}
	if _, ok := cfg.Groups["small"]; !ok {
		t.Fatalf("empty group dropped: %v", cfg.Groups)
	}

	// A member that names no model is refused before anything is written.
	before, _ := m.ConfigYAML()
	if err := m.SetGroups(map[string]GroupConfig{
		"heavy": {Members: []string{"nope"}},
	}); err == nil {
		t.Fatal("unknown member accepted")
	}
	if after, _ := m.ConfigYAML(); after != before {
		t.Fatal("config changed after a rejected edit")
	}
}

func TestSettingsDefaultAndRoundTrip(t *testing.T) {
	m := NewManager(t.TempDir())
	if got := m.Settings().Port; got != DefaultPort {
		t.Fatalf("default port = %d", got)
	}
	if err := m.SetSettings(Settings{Port: 8091}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := m.Settings().Port; got != 8091 {
		t.Fatalf("port = %d", got)
	}
	if err := m.SetSettings(Settings{Port: 0}); err == nil {
		t.Fatal("port 0 accepted")
	}
}
