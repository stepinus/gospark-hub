package registry

import (
	"bytes"
	"context"
	// The source id is a short identifier, not a security primitive, so the
	// algorithm is fixed by the on-disk format (sha1(url + "#" + subpath)).
	sha1hash "crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultSourceURL     = "https://github.com/WaxacaBytes/spark-ai-hub"
	defaultSourceSubpath = "registry/recipes"
	defaultSourceRef     = "main"
	gitTimeout           = 120 * time.Second
)

var (
	// ErrSlugTaken is returned by Fork when the destination slug already
	// belongs to a local or vendored recipe.
	ErrSlugTaken = errors.New("slug already taken")
	// ErrNotLocal is returned by DeleteLocal for a recipe that does not live
	// under local/.
	ErrNotLocal = errors.New("recipe is not local")
	// ErrSourceNotFound is returned when an id does not name a configured
	// source.
	ErrSourceNotFound = errors.New("registry source not found")
	// ErrSourceExists is returned by Add for a source that is already
	// configured.
	ErrSourceExists = errors.New("registry source already configured")
	// ErrRecipeNotFound is returned by Fork when the source recipe is unknown.
	ErrRecipeNotFound = errors.New("recipe not found")
)

// Source is one entry of sources.yaml. The on-disk layout is
//
//	vendored/<ID>/<Subpath>
//
// and Covers names the sibling directory with original cover images.
type Source struct {
	ID       string    `yaml:"id" json:"id"`
	URL      string    `yaml:"url" json:"url"`
	Ref      string    `yaml:"ref" json:"ref"`
	Subpath  string    `yaml:"subpath" json:"subpath"`
	Covers   string    `yaml:"covers,omitempty" json:"covers"`
	Commit   string    `yaml:"commit,omitempty" json:"commit"`
	SyncedAt time.Time `yaml:"synced_at,omitempty" json:"synced_at"`
	Error    string    `yaml:"error,omitempty" json:"error"`
}

type sourcesFile struct {
	Sources []Source `yaml:"sources"`
}

// Manager owns ~/.config/gospark/registry: sources.yaml, the vendored clones
// and the local/ recipes. It never talks to the network while being
// constructed.
type Manager struct {
	home    string
	mu      sync.Mutex
	sources []Source
}

// NewManager creates the registry home (if needed), reads sources.yaml and
// writes the default source when the file is missing or empty. It performs no
// network calls. A home that cannot be created or written is an error.
func NewManager(home string) (*Manager, error) {
	if strings.TrimSpace(home) == "" {
		home = defaultRegistryHome()
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve registry home: %w", err)
	}
	home = abs
	for _, dir := range []string{home, filepath.Join(home, "vendored"), filepath.Join(home, "local")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create registry dir %s: %w", dir, err)
		}
	}

	m := &Manager{home: home}
	data, err := os.ReadFile(m.sourcesPath())
	switch {
	case err == nil && strings.TrimSpace(string(data)) != "":
		var doc sourcesFile
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", m.sourcesPath(), err)
		}
		m.sources = make([]Source, 0, len(doc.Sources))
		for _, src := range doc.Sources {
			m.sources = append(m.sources, normalizeSource(src))
		}
		return m, nil
	case err == nil:
		// Empty file: fall through and seed the default source.
	case errors.Is(err, os.ErrNotExist):
		// First run.
	default:
		return nil, fmt.Errorf("read %s: %w", m.sourcesPath(), err)
	}

	m.sources = []Source{defaultSource()}
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

// Home is the absolute registry directory.
func (m *Manager) Home() string { return m.home }

// List returns a copy of the configured sources in addition order.
func (m *Manager) List() []Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Source(nil), m.sources...)
}

// Add parses a comma/newline separated list of repository links, adds every
// valid one and syncs it. Bad lines are reported individually; the rest are
// still added.
func (m *Manager) Add(raw string) ([]Source, []error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var added []Source
	var errs []error
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		src, err := ParseSource(part)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", part, err))
			continue
		}
		if m.indexOfLocked(src.ID) >= 0 {
			errs = append(errs, fmt.Errorf("%s: %w", part, ErrSourceExists))
			continue
		}
		m.sources = append(m.sources, src)
		if err := m.saveLocked(); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := m.syncLocked(src.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", part, err))
		}
		if i := m.indexOfLocked(src.ID); i >= 0 {
			added = append(added, m.sources[i])
		}
	}
	return added, errs
}

// Remove deletes a source and its vendored clone. local/ is never touched.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.indexOfLocked(id)
	if i < 0 {
		return ErrSourceNotFound
	}
	m.sources = append(m.sources[:i], m.sources[i+1:]...)
	if err := m.saveLocked(); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(m.home, "vendored", id)); err != nil {
		return fmt.Errorf("remove vendored %s: %w", id, err)
	}
	return nil
}

// Sync clones or updates one source and records its commit, time and error.
func (m *Manager) Sync(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.syncLocked(id)
}

// SyncAll syncs every source. A broken source does not stop the others.
func (m *Manager) SyncAll() []error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for i := range m.sources {
		if err := m.syncLocked(m.sources[i].ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Roots returns the recipe directories in merge order: vendored sources in
// addition order, then local/. Directories that do not exist are skipped.
func (m *Manager) Roots() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rootsLocked()
}

// CoverRoots returns the directories that may hold original cover images:
// local/ first (it has priority), then the covers directory of every source.
func (m *Manager) CoverRoots() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var roots []string
	if local := m.localRootLocked(); isDir(local) {
		roots = append(roots, local)
	}
	for _, src := range m.sources {
		if src.Covers == "" {
			continue
		}
		dir := filepath.Join(m.home, "vendored", src.ID, filepath.FromSlash(cleanSubpath(src.Covers)))
		if isDir(dir) {
			roots = append(roots, dir)
		}
	}
	return roots
}

// LocalDir is the destination of a local recipe with the given slug.
func (m *Manager) LocalDir(slug string) string {
	return filepath.Join(m.home, "local", slug)
}

// Fork copies recipe srcSlug to local/newSlug. A destination that already
// exists locally or is occupied in any vendored source yields ErrSlugTaken.
func (m *Manager) Fork(srcSlug, newSlug string) (string, error) {
	if err := validateSlug(newSlug); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	localDir := m.LocalDir(newSlug)
	if _, err := os.Stat(localDir); err == nil {
		return "", ErrSlugTaken
	}
	for _, src := range m.sources {
		root := filepath.Join(m.home, "vendored", src.ID)
		if sub := cleanSubpath(src.Subpath); sub != "" {
			root = filepath.Join(root, filepath.FromSlash(sub))
		}
		if _, err := os.Stat(filepath.Join(root, newSlug)); err == nil {
			return "", ErrSlugTaken
		}
	}

	if err := validateSlug(srcSlug); err != nil {
		return "", ErrRecipeNotFound
	}
	srcDir, ok := m.findRecipeLocked(srcSlug)
	if !ok {
		return "", ErrRecipeNotFound
	}
	if err := os.MkdirAll(filepath.Dir(localDir), 0o755); err != nil {
		return "", fmt.Errorf("create local dir: %w", err)
	}
	if err := os.CopyFS(localDir, os.DirFS(srcDir)); err != nil {
		return "", cleanupWith(fmt.Errorf("copy recipe %s: %w", srcSlug, err), localDir)
	}
	if err := rewriteSlug(localDir, newSlug); err != nil {
		return "", cleanupWith(err, localDir)
	}
	return localDir, nil
}

// DeleteLocal removes local/slug. Recipes that only exist in a vendored
// source are reported with ErrNotLocal and left alone.
func (m *Manager) DeleteLocal(slug string) error {
	if err := validateSlug(slug); err != nil {
		return ErrNotLocal
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dir := m.LocalDir(slug)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ErrNotLocal
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete local recipe %s: %w", slug, err)
	}
	return nil
}

// ParseSource turns one user-supplied repository link into a Source. Accepted
// forms include github.com/o/r, …/o/r/<sub>, …/tree/<ref>/<sub>, a trailing
// .git and the scp-like git@host:o/r.git. Only github.com, gitlab.com and
// codeberg.org are allowed; localhost, private addresses and file:// are not.
func ParseSource(raw string) (Source, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Source{}, errors.New("empty source")
	}
	if strings.Contains(strings.ToLower(s), "file://") {
		return Source{}, fmt.Errorf("file sources are not allowed: %q", raw)
	}

	var u *url.URL
	if strings.HasPrefix(s, "git@") {
		rest := strings.TrimPrefix(s, "git@")
		colon := strings.Index(rest, ":")
		if colon <= 0 || colon == len(rest)-1 {
			return Source{}, fmt.Errorf("invalid scp-like address %q", raw)
		}
		host, repoPath := rest[:colon], rest[colon+1:]
		if err := checkHost(host); err != nil {
			return Source{}, err
		}
		u = &url.URL{Scheme: "https", Host: host, Path: "/" + strings.TrimPrefix(repoPath, "/")}
	} else {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		parsed, err := url.Parse(s)
		if err != nil {
			return Source{}, fmt.Errorf("parse url: %w", err)
		}
		if parsed.Scheme != "https" && parsed.Scheme != "http" {
			return Source{}, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
		}
		if err := checkHost(parsed.Hostname()); err != nil {
			return Source{}, err
		}
		u = parsed
	}

	segments := splitPath(u.Path)
	if len(segments) < 2 {
		return Source{}, fmt.Errorf("url must point at a repository: %q", raw)
	}
	owner := segments[0]
	repo := strings.TrimSuffix(segments[1], ".git")
	if owner == "" || repo == "" || strings.ContainsAny(owner+repo, `/\`) {
		return Source{}, fmt.Errorf("invalid repository in %q", raw)
	}

	ref := defaultSourceRef
	subpath := ""
	rest := segments[2:]
	// GitLab puts tree URLs behind "-": /o/r/-/tree/<ref>/<sub>.
	if len(rest) > 1 && rest[0] == "-" && rest[1] == "tree" {
		rest = rest[1:]
	}
	if len(rest) > 0 {
		if rest[0] == "tree" {
			if len(rest) < 2 || rest[1] == "" {
				return Source{}, fmt.Errorf("tree url without a ref: %q", raw)
			}
			ref = rest[1]
			subpath = strings.Join(rest[2:], "/")
		} else {
			subpath = strings.Join(rest, "/")
		}
	}
	if strings.HasPrefix(ref, "-") {
		return Source{}, fmt.Errorf("invalid ref %q", ref)
	}

	src := Source{
		URL:     "https://" + strings.ToLower(u.Hostname()) + "/" + owner + "/" + repo,
		Ref:     ref,
		Subpath: cleanSubpath(subpath),
	}
	src.ID = sourceID(src.URL, src.Subpath)
	return src, nil
}

func (m *Manager) sourcesPath() string { return filepath.Join(m.home, "sources.yaml") }

func (m *Manager) localRootLocked() string { return filepath.Join(m.home, "local") }

func (m *Manager) indexOfLocked(id string) int {
	for i := range m.sources {
		if m.sources[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *Manager) rootsLocked() []string {
	var roots []string
	for _, src := range m.sources {
		dir := filepath.Join(m.home, "vendored", src.ID)
		if sub := cleanSubpath(src.Subpath); sub != "" {
			dir = filepath.Join(dir, filepath.FromSlash(sub))
		}
		if isDir(dir) {
			roots = append(roots, dir)
		}
	}
	if local := m.localRootLocked(); isDir(local) {
		roots = append(roots, local)
	}
	return roots
}

func (m *Manager) findRecipeLocked(slug string) (string, bool) {
	var (
		found string
		ok    bool
	)
	for _, root := range m.rootsLocked() {
		dir := filepath.Join(root, slug)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, "recipe.yaml")); err == nil {
				found, ok = dir, true
			}
		}
	}
	return found, ok
}

func (m *Manager) syncLocked(id string) error {
	i := m.indexOfLocked(id)
	if i < 0 {
		return ErrSourceNotFound
	}
	src := &m.sources[i]
	err := m.syncOne(src)
	src.SyncedAt = time.Now().UTC()
	if err != nil {
		src.Error = err.Error()
	} else {
		src.Error = ""
	}
	if saveErr := m.saveLocked(); saveErr != nil && err == nil {
		err = saveErr
	}
	return err
}

func (m *Manager) syncOne(src *Source) error {
	if strings.HasPrefix(src.URL, "-") || strings.HasPrefix(src.Ref, "-") {
		return fmt.Errorf("invalid url or ref")
	}
	dest := filepath.Join(m.home, "vendored", src.ID)

	if isDir(filepath.Join(dest, ".git")) {
		if err := runGit("", "-C", dest, "fetch", "--depth", "1", "origin", src.Ref); err != nil {
			return err
		}
		if err := runGit("", "-C", dest, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return err
		}
	} else {
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("clean vendored %s: %w", src.ID, err)
		}
		if err := runGit(m.home, "clone", "--depth", "1", "--filter=blob:none", "--sparse",
			"--branch", src.Ref, src.URL, dest); err != nil {
			return err
		}
	}

	sub := cleanSubpath(src.Subpath)
	covers := cleanSubpath(src.Covers)
	if sub == "" {
		// A repository added without a subpath is checked out whole; there is
		// no directory to name in sparse-checkout.
		if err := runGit("", "-C", dest, "sparse-checkout", "disable"); err != nil {
			return err
		}
		if covers == "" && isDir(filepath.Join(dest, "covers")) {
			covers = "covers"
		}
	} else {
		if covers == "" {
			covers = deriveCovers(sub)
		}
		args := []string{"-C", dest, "sparse-checkout", "set", sub}
		if covers != "" {
			args = append(args, covers)
		}
		if err := runGit("", args...); err != nil {
			return err
		}
	}

	commit, err := gitOutput("", "-C", dest, "rev-parse", "--short", "HEAD")
	if err != nil {
		return err
	}
	src.Commit = strings.TrimSpace(commit)
	if covers != "" && isDir(filepath.Join(dest, filepath.FromSlash(covers))) {
		src.Covers = covers
	} else {
		src.Covers = ""
	}
	return nil
}

func (m *Manager) saveLocked() error {
	data, err := yaml.Marshal(sourcesFile{Sources: m.sources})
	if err != nil {
		return fmt.Errorf("marshal sources: %w", err)
	}
	tmp, err := os.CreateTemp(m.home, "sources-*.yaml")
	if err != nil {
		return fmt.Errorf("write sources: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write sources: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write sources: %w", err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("write sources: %w", err)
	}
	if err := os.Rename(name, m.sourcesPath()); err != nil {
		return fmt.Errorf("write sources: %w", err)
	}
	return nil
}

func normalizeSource(src Source) Source {
	src.Subpath = cleanSubpath(src.Subpath)
	src.Covers = cleanSubpath(src.Covers)
	if src.Ref == "" {
		src.Ref = defaultSourceRef
	}
	if !validSourceID(src.ID) {
		src.ID = sourceID(src.URL, src.Subpath)
	}
	return src
}

func validSourceID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func defaultSource() Source {
	return Source{
		ID:      sourceID(defaultSourceURL, defaultSourceSubpath),
		URL:     defaultSourceURL,
		Ref:     defaultSourceRef,
		Subpath: defaultSourceSubpath,
	}
}

// cleanupWith removes a half-written local recipe and keeps the original
// error, reporting the cleanup failure alongside it when there is one.
func cleanupWith(err error, dir string) error {
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		return fmt.Errorf("%w (cleanup %s: %v)", err, dir, rmErr)
	}
	return err
}

func defaultRegistryHome() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "gospark", "registry")
	}
	return filepath.Join(home, ".config", "gospark", "registry")
}

func sourceID(rawURL, subpath string) string {
	sum := sha1hash.Sum([]byte(rawURL + "#" + cleanSubpath(subpath)))
	return hex.EncodeToString(sum[:])[:8]
}

func cleanSubpath(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	s = strings.Trim(s, "/")
	if s == "" || s == "." {
		return ""
	}
	cleaned := path.Clean(s)
	for cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		cleaned = strings.TrimPrefix(strings.TrimPrefix(cleaned, ".."), "/")
	}
	if cleaned == "" || cleaned == "." {
		return ""
	}
	return cleaned
}

func deriveCovers(subpath string) string {
	if subpath == "" {
		return ""
	}
	parent := path.Dir(subpath)
	if parent == "." {
		return "covers"
	}
	return path.Join(parent, "covers")
}

func splitPath(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
}

func checkHost(host string) error {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	switch h {
	case "":
		return errors.New("source has no host")
	case "localhost":
		return fmt.Errorf("host %q is not allowed", host)
	}
	if strings.HasSuffix(h, ".localhost") {
		return fmt.Errorf("host %q is not allowed", host)
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("host %q is not allowed", host)
		}
		return fmt.Errorf("host %q is not allowed", host)
	}
	switch h {
	case "github.com", "gitlab.com", "codeberg.org":
		return nil
	}
	return fmt.Errorf("host %q is not allowed", host)
}

func validateSlug(slug string) error {
	if slug == "" || slug == "." || slug == ".." || len(slug) > 128 {
		return fmt.Errorf("invalid slug %q", slug)
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("invalid slug %q", slug)
		}
	}
	return nil
}

func rewriteSlug(dir, slug string) error {
	file := filepath.Join(dir, "recipe.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read recipe.yaml: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "slug:") {
			lines[i] = "slug: " + slug
			found = true
			break
		}
	}
	if !found {
		lines = append([]string{"slug: " + slug}, lines...)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("rewrite slug: %w", err)
	}
	return nil
}

func runGit(dir string, args ...string) error {
	_, err := gitOutput(dir, args...)
	return err
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git %s: timed out after %s", gitName(args), gitTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return "", fmt.Errorf("git %s: %w: %s", gitName(args), err, msg)
	}
	return out.String(), nil
}

func gitName(args []string) string {
	for _, arg := range args {
		if arg != "-C" && arg != "" && arg != "." {
			return arg
		}
	}
	return "git"
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
