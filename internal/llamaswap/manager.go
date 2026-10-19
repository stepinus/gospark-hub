// Package llamaswap owns the llama-swap sidecar: the release binary the Hub
// downloads, the YAML config it serves from, and the systemd --user unit that
// runs it next to the daemon.
//
// llama-swap keeps models warm behind one OpenAI-compatible address and swaps
// them on demand. The Hub does not implement swapping itself; it installs the
// upstream binary, edits its config, and reads its runtime state.
package llamaswap

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// Repo is the upstream project.
	Repo = "mostlygeek/llama-swap"
	// Binary is the executable inside the release archive.
	Binary = "llama-swap"
	// DefaultPort is what llama-swap listens on unless the settings change it.
	DefaultPort = 8080
)

var (
	// ErrNotInstalled is returned when no binary is unpacked yet.
	ErrNotInstalled = errors.New("llama-swap is not installed")
)

// Manager owns the on-disk sidecar home:
//
//	<home>/bin/llama-swap       the release binary (current)
//	<home>/VERSION              its version, "265"
//	<home>/config.yaml          the config it serves
type Manager struct {
	Home string
	// HTTP is the client used for GitHub and the downloads.
	HTTP *http.Client
	// GitHubAPI and DownloadBase let a test point at httptest.
	GitHubAPI    string
	DownloadBase string
}

// NewManager returns a manager for home (empty means the default).
func NewManager(home string) *Manager {
	if strings.TrimSpace(home) == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			home = filepath.Join(dir, "gospark", "llamaswap")
		} else {
			home = filepath.Join(".config", "gospark", "llamaswap")
		}
	}
	return &Manager{Home: home, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

func (m *Manager) apiBase() string {
	if m.GitHubAPI != "" {
		return strings.TrimSuffix(m.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

func (m *Manager) downloadBase() string {
	if m.DownloadBase != "" {
		return strings.TrimSuffix(m.DownloadBase, "/")
	}
	return "https://github.com"
}

func (m *Manager) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return http.DefaultClient
}

// Path is the installed binary.
func (m *Manager) Path() string { return filepath.Join(m.Home, "bin", Binary) }

// ConfigPath is the YAML config the sidecar is started with.
func (m *Manager) ConfigPath() string { return filepath.Join(m.Home, "config.yaml") }

// Installed reports the unpacked version, if any.
func (m *Manager) Installed() (string, bool) {
	version, err := os.ReadFile(filepath.Join(m.Home, "VERSION"))
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(m.Path()); err != nil {
		return "", false
	}
	return strings.TrimSpace(string(version)), true
}

// Latest asks GitHub for the newest release tag, without the "v".
func (m *Manager) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		m.apiBase()+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(rel.Tag), "v"), nil
}

// Install downloads, verifies and unpacks a release. An empty version means
// the latest one; installing the version already in place is ErrAlreadyInstalled.
func (m *Manager) Install(ctx context.Context, version string) (string, error) {
	if version == "" {
		v, err := m.Latest(ctx)
		if err != nil {
			return "", fmt.Errorf("latest version: %w", err)
		}
		version = v
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if current, ok := m.Installed(); ok && current == version {
		return current, ErrAlreadyInstalled
	}

	asset := fmt.Sprintf("llama-swap_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	url := fmt.Sprintf("%s/%s/releases/download/v%s/%s", m.downloadBase(), Repo, version, asset)

	// Everything lands in a temp dir first: a dropped download must not leave
	// a half-unpacked binary behind.
	tmp, err := os.MkdirTemp(m.Home, ".install-")
	if err != nil {
		if err := os.MkdirAll(m.Home, 0o755); err != nil {
			return "", err
		}
		if tmp, err = os.MkdirTemp(m.Home, ".install-"); err != nil {
			return "", err
		}
	}
	defer os.RemoveAll(tmp)

	archive := filepath.Join(tmp, asset)
	sum, err := m.download(ctx, url, archive)
	if err != nil {
		return "", err
	}
	if err := m.verify(ctx, version, asset, sum); err != nil {
		return "", err
	}
	if err := m.unpack(archive, tmp); err != nil {
		return "", err
	}
	binDir := filepath.Join(m.Home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(tmp, Binary), m.Path()); err != nil {
		return "", fmt.Errorf("install binary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(m.Home, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		return "", err
	}
	return version, nil
}

func (m *Manager) download(ctx context.Context, url, dest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// verify checks the asset against the release's checksums file. A checksums
// file without our asset is an error, not a pass.
func (m *Manager) verify(ctx context.Context, version, asset, sum string) error {
	url := fmt.Sprintf("%s/%s/releases/download/v%s/llama-swap_%s_checksums.txt",
		m.downloadBase(), Repo, version, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			if !strings.EqualFold(fields[0], sum) {
				return fmt.Errorf("checksum mismatch for %s", asset)
			}
			return nil
		}
	}
	return fmt.Errorf("checksums list has no %s", asset)
}

func (m *Manager) unpack(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		// The archive is flat: one binary at its root.
		if filepath.Base(hdr.Name) != Binary || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, Binary), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("archive has no %s", Binary)
}

// ErrAlreadyInstalled reports the requested version is already unpacked.
var ErrAlreadyInstalled = errors.New("llama-swap version is already installed")
