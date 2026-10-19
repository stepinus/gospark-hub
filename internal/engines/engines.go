// Package engines installs and inspects the inference engines that native
// recipes run on. There is exactly one engine for now — TensorFold — and it is
// baked into the code rather than configured: the GitHub repository, the asset
// naming and the binary inside the release are as much a part of the engine as
// its name.
//
// Layout under Manager.Home:
//
//	tensorfold/
//	  1.1.1/              unpacked release: bin/tensorfold-native, lib/, VERSION
//	  current -> 1.1.1    the installed version
package engines

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
	"sort"
	"strings"
	"sync"
	"time"
)

// TensorFold is the only engine the Hub knows about.
const TensorFold = "tensorfold"

const (
	defaultGitHubAPI    = "https://api.github.com"
	defaultDownloadBase = "https://github.com"

	// latestTTL is how long a release lookup is trusted. GitHub's anonymous
	// rate limit is low and the answer changes once a release, so five minutes
	// is generous in both directions.
	latestTTL = 5 * time.Minute
)

var (
	// ErrNotInstalled is returned by Path and Path-like calls when no version
	// of the engine is unpacked.
	ErrNotInstalled = errors.New("engine is not installed")
	// ErrAlreadyInstalled is returned by Install when the requested version is
	// the one already linked. The API turns it into a 409.
	ErrAlreadyInstalled = errors.New("engine version is already installed")
	// ErrUnknownEngine is returned for any name that is not in the built-in
	// table, so the API can answer 404 without matching strings.
	ErrUnknownEngine = errors.New("unknown engine")

	// errNoLatest marks a release lookup that failed; it is cached like a
	// successful one so an offline daemon does not retry on every request.
	errNoLatest = errors.New("no latest release")
)

// Engine describes one built-in engine. AssetPattern is filled with the
// version and the architecture slug ("linux-aarch64").
type Engine struct {
	Name         string
	Repo         string
	AssetPattern string
	Binary       string
}

// known is the whole engine catalogue. A slice, not a map, so List is
// deterministic.
var known = []Engine{{
	Name:         TensorFold,
	Repo:         "ashhart/TensorFold",
	AssetPattern: "tensorfold-%s-%s.tar.gz",
	Binary:       "tensorfold-native",
}}

// Status is one row of GET /api/engines.
type Status struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Path      string `json:"path,omitempty"`
}

// Manager owns the on-disk engine home and talks to GitHub. The zero HTTP and
// GitHubAPI fields mean defaults; tests replace both with an httptest server.
type Manager struct {
	Home string
	HTTP *http.Client
	// GitHubAPI is the REST base for release lookups (default api.github.com).
	GitHubAPI string
	// DownloadBase is the web base release assets are fetched from (default
	// github.com). It is a field so a test can serve a tarball locally.
	DownloadBase string

	mu     sync.Mutex
	latest map[string]latestEntry
}

type latestEntry struct {
	version string
	at      time.Time
}

// NewManager returns a Manager rooted at home.
func NewManager(home string) *Manager { return &Manager{Home: home} }

func (m *Manager) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (m *Manager) apiBase() string {
	if v := strings.TrimRight(m.GitHubAPI, "/"); v != "" {
		return v
	}
	return defaultGitHubAPI
}

func (m *Manager) downloadBase() string {
	if v := strings.TrimRight(m.DownloadBase, "/"); v != "" {
		return v
	}
	return defaultDownloadBase
}

func engineByName(name string) (Engine, error) {
	for _, e := range known {
		if e.Name == name {
			return e, nil
		}
	}
	return Engine{}, fmt.Errorf("%w: %s", ErrUnknownEngine, name)
}

// DetectArch maps the Go architecture onto the suffix the release assets use.
func (m *Manager) DetectArch() (string, error) {
	switch runtime.GOARCH {
	case "arm64":
		return "linux-aarch64", nil
	case "amd64":
		return "linux-x86_64", nil
	}
	return "", fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
}

// List reports every built-in engine. A failed release lookup is not an error
// of the call: Latest stays empty and the failure is cached for latestTTL.
func (m *Manager) List(ctx context.Context) ([]Status, error) {
	latest := make(map[string]string, len(known))
	for _, e := range known {
		if v, err := m.latestVersion(ctx, e); err == nil {
			latest[e.Name] = v
		}
	}
	out := make([]Status, 0, len(known))
	for _, e := range known {
		st := Status{Name: e.Name, Latest: latest[e.Name]}
		if version, ok := m.Installed(e.Name); ok {
			st.Installed = true
			st.Version = version
			st.Path, _ = m.Path(e.Name)
		}
		out = append(out, st)
	}
	return out, nil
}

// Install downloads, verifies, unpacks and links version. An empty version
// means the latest release. Installing the version that is already linked is
// ErrAlreadyInstalled, with the Status filled in so the API can name it.
func (m *Manager) Install(ctx context.Context, name, version string) (Status, error) {
	eng, err := engineByName(name)
	if err != nil {
		return Status{}, err
	}
	arch, err := m.DetectArch()
	if err != nil {
		return Status{}, err
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		version, err = m.latestVersion(ctx, eng)
		if err != nil {
			return Status{}, fmt.Errorf("latest version: %w", err)
		}
	}
	current, installed := m.Installed(name)
	if installed && current == version {
		return Status{Name: name, Installed: true, Version: current}, fmt.Errorf("%w: %s %s", ErrAlreadyInstalled, name, version)
	}

	root := filepath.Join(m.Home, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Status{}, fmt.Errorf("create engine home: %w", err)
	}
	// Everything happens inside a temp directory under the engine home: a
	// failed download must never leave a half-installed version behind.
	tmp, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return Status{}, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	asset := fmt.Sprintf(eng.AssetPattern, version, arch)
	archive := filepath.Join(tmp, asset)
	sum, err := m.downloadFile(ctx, m.assetURL(eng, version, asset), archive)
	if err != nil {
		return Status{}, err
	}
	want, err := m.fetchText(ctx, m.assetURL(eng, version, asset+".sha256"))
	if err != nil {
		return Status{}, err
	}
	fields := strings.Fields(want)
	if len(fields) == 0 {
		return Status{}, fmt.Errorf("empty checksum for %s", asset)
	}
	if !strings.EqualFold(fields[0], sum) {
		return Status{}, fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, sum, fields[0])
	}

	extractDir := filepath.Join(tmp, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return Status{}, fmt.Errorf("temp extract dir: %w", err)
	}
	if err := extractTarGz(archive, extractDir); err != nil {
		return Status{}, fmt.Errorf("unpack %s: %w", asset, err)
	}
	src, err := topLevelDir(extractDir, fmt.Sprintf("%s-%s-%s", eng.Name, version, arch))
	if err != nil {
		return Status{}, err
	}

	dest := filepath.Join(root, version)
	// A previous attempt may have left a partial version directory.
	if err := os.RemoveAll(dest); err != nil {
		return Status{}, fmt.Errorf("replace %s: %w", dest, err)
	}
	if err := os.Rename(src, dest); err != nil {
		return Status{}, fmt.Errorf("move %s: %w", dest, err)
	}

	// The symlink swap is the atomic switch: current.tmp plus one rename.
	link := filepath.Join(root, "current")
	tmpLink := link + ".tmp"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(version, tmpLink); err != nil {
		return Status{}, fmt.Errorf("link current: %w", err)
	}
	if err := os.Rename(tmpLink, link); err != nil {
		return Status{}, fmt.Errorf("swap current: %w", err)
	}

	m.removeOtherVersions(root, version)

	st := Status{Name: name, Installed: true, Version: version}
	st.Path, _ = m.Path(name)
	return st, nil
}

// Update installs the latest release and reports whether anything changed.
// When the installed version already is the latest it returns changed=false
// without touching the disk.
func (m *Manager) Update(ctx context.Context, name string) (Status, bool, error) {
	eng, err := engineByName(name)
	if err != nil {
		return Status{}, false, err
	}
	latest, err := m.latestVersion(ctx, eng)
	if err != nil {
		return Status{}, false, fmt.Errorf("latest version: %w", err)
	}
	if current, ok := m.Installed(name); ok && current == latest {
		st := Status{Name: name, Installed: true, Version: current, Latest: latest}
		st.Path, _ = m.Path(name)
		return st, false, nil
	}
	st, err := m.Install(ctx, name, latest)
	if err != nil {
		return Status{}, false, err
	}
	st.Latest = latest
	return st, true, nil
}

// Delete removes every version of the engine.
func (m *Manager) Delete(name string) error {
	if _, err := engineByName(name); err != nil {
		return err
	}
	if m.Home == "" {
		return errors.New("engines: empty home")
	}
	return os.RemoveAll(filepath.Join(m.Home, name))
}

// Installed reports the linked version, if any.
func (m *Manager) Installed(name string) (string, bool) {
	if _, err := engineByName(name); err != nil {
		return "", false
	}
	target, err := os.Readlink(filepath.Join(m.Home, name, "current"))
	if err != nil {
		return "", false
	}
	version := filepath.Base(strings.TrimSuffix(target, string(os.PathSeparator)))
	if version == "" || version == "." || version == ".." {
		return "", false
	}
	if _, err := m.Path(name); err != nil {
		return "", false
	}
	return version, true
}

// Path is the engine executable the native runner starts, or ErrNotInstalled.
func (m *Manager) Path(name string) (string, error) {
	eng, err := engineByName(name)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(m.Home, name, "current", "bin", eng.Binary)
	st, err := os.Stat(bin)
	if err != nil || st.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNotInstalled, name)
	}
	return bin, nil
}

// removeOtherVersions keeps the tree to one version so a failed upgrade does
// not fill the disk with the previous release.
func (m *Manager) removeOtherVersions(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		if strings.HasPrefix(e.Name(), ".install-") {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// assetURL is the release download URL for one asset.
func (m *Manager) assetURL(eng Engine, version, asset string) string {
	return fmt.Sprintf("%s/%s/releases/download/v%s/%s", m.downloadBase(), eng.Repo, version, asset)
}

// latestVersion returns the release tag without its leading "v", cached for
// latestTTL per process. Failures are cached too.
func (m *Manager) latestVersion(ctx context.Context, eng Engine) (string, error) {
	m.mu.Lock()
	if m.latest == nil {
		m.latest = map[string]latestEntry{}
	}
	if entry, ok := m.latest[eng.Name]; ok && time.Since(entry.at) < latestTTL {
		m.mu.Unlock()
		if entry.version == "" {
			return "", errNoLatest
		}
		return entry.version, nil
	}
	m.mu.Unlock()

	version, err := m.fetchLatest(ctx, eng)
	m.mu.Lock()
	m.latest[eng.Name] = latestEntry{version: version, at: time.Now()}
	m.mu.Unlock()
	return version, err
}

func (m *Manager) fetchLatest(ctx context.Context, eng Engine) (string, error) {
	url := m.apiBase() + "/repos/" + eng.Repo + "/releases/latest"
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
		return "", fmt.Errorf("releases/latest: status %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	version := strings.TrimPrefix(strings.TrimSpace(body.TagName), "v")
	if version == "" {
		return "", fmt.Errorf("releases/latest: empty tag")
	}
	return version, nil
}

// downloadFile streams url into dest and returns the sha256 of what was
// written, so the caller can compare it with the release checksum.
func (m *Manager) downloadFile(ctx context.Context, url, dest string) (string, error) {
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
		return "", fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Manager) fetchText(ctx context.Context, url string) (string, error) {
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
		return "", fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// extractTarGz unpacks archive into dest, refusing entries that would escape
// it. Symlinks are recreated as they are; the release layout needs them.
func extractTarGz(archive, dest string) error {
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
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes target: %s", hdr.Name)
		}
		target := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// topLevelDir finds the release directory inside the unpacked archive: the
// expected name when it is there, otherwise the single directory present.
func topLevelDir(root, want string) (string, error) {
	if st, err := os.Stat(filepath.Join(root, want)); err == nil && st.IsDir() {
		return filepath.Join(root, want), nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		sort.Strings(dirs)
		return "", fmt.Errorf("archive does not contain %s (found %v)", want, dirs)
	}
	return filepath.Join(root, dirs[0]), nil
}
