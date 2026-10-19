package dockerx

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// Docker is the recipe-lifecycle service: a docker CLI client plus the
// in-memory state (readiness, pending actions, startup errors, build-cache
// list) that upstream keeps in module globals and never persists.
type Docker struct {
	Client *Client
	Config config.Config
	Reg    *registry.Cache
	DB     *store.DB
	Log    *slog.Logger

	stateMu       sync.Mutex
	ready         map[string]bool
	health        map[string]context.CancelFunc
	pending       map[string]string
	startupErrors map[string]string
	failureLogs   map[string][]string

	listMu    sync.Mutex
	listCache map[string]listEntry
	listGen   int

	HTTPClient     *http.Client
	HealthInterval time.Duration
	HealthTicks    int
}

type listEntry struct {
	at    time.Time
	lines []string
}

// DefaultHealthInterval and DefaultHealthTicks are the readiness poll cadence.
// They are variables so tests can shrink the wait.
var (
	DefaultHealthInterval = time.Second
	DefaultHealthTicks    = 300
)

// New builds the recipe service. logger may be nil.
func New(cfg config.Config, reg *registry.Cache, db *store.DB, logger *slog.Logger) *Docker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Docker{
		Client:         NewClient(),
		Config:         cfg,
		Reg:            reg,
		DB:             db,
		Log:            logger,
		ready:          map[string]bool{},
		health:         map[string]context.CancelFunc{},
		pending:        map[string]string{},
		startupErrors:  map[string]string{},
		failureLogs:    map[string][]string{},
		listCache:      map[string]listEntry{},
		HTTPClient:     &http.Client{Timeout: 3 * time.Second},
		HealthInterval: DefaultHealthInterval,
		HealthTicks:    DefaultHealthTicks,
	}
}

// ComposeProject is the per-recipe compose project name, prefixed by
// cfg.Project so two Hubs never share one.
func (d *Docker) ComposeProject(slug string) string {
	prefix := d.Config.AppProjectPrefix
	if prefix == "" {
		prefix = "spark-ai-hub"
	}
	return prefix + "-" + slug
}

// Network is the shared external docker network all recipe composes attach to.
func (d *Docker) Network() string {
	if d.Config.AppNetwork == "" {
		return "spark-ai-hub-net"
	}
	return d.Config.AppNetwork
}

// RecipeDir returns the on-disk recipe directory.
func (d *Docker) RecipeDir(slug string) (string, bool) {
	if d.Reg == nil {
		return "", false
	}
	return d.Reg.Dir(slug)
}

func (d *Docker) composeFile(dir string) string {
	return filepath.Join(dir, "docker-compose.yml")
}

func (d *Docker) composeArgs(slug, dir string) []string {
	return d.Client.ComposeArgs(d.ComposeProject(slug), d.composeFile(dir))
}

// ── In-memory action state ──────────────────────────────────────────────────

func (d *Docker) SetPending(slug, action string) {
	d.stateMu.Lock()
	d.pending[slug] = action
	d.stateMu.Unlock()
	d.invalidateLists()
}

func (d *Docker) ClearPending(slug string) {
	d.stateMu.Lock()
	delete(d.pending, slug)
	d.stateMu.Unlock()
	d.invalidateLists()
}

func (d *Docker) GetPending(slug string) string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.pending[slug]
}

func (d *Docker) MarkReady(slug string) {
	d.stateMu.Lock()
	d.ready[slug] = true
	delete(d.pending, slug)
	if cancel, ok := d.health[slug]; ok {
		delete(d.health, slug)
		cancel()
	}
	d.stateMu.Unlock()
	d.invalidateLists()
}

func (d *Docker) ClearReady(slug string) {
	d.stateMu.Lock()
	delete(d.ready, slug)
	if cancel, ok := d.health[slug]; ok {
		delete(d.health, slug)
		cancel()
	}
	d.stateMu.Unlock()
}

func (d *Docker) IsReady(slug string) bool {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.ready[slug]
}

func (d *Docker) SetStartupError(slug, message string) {
	d.stateMu.Lock()
	d.startupErrors[slug] = message
	d.stateMu.Unlock()
}

func (d *Docker) GetStartupError(slug string) string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.startupErrors[slug]
}

func (d *Docker) ClearStartupError(slug string) {
	d.stateMu.Lock()
	delete(d.startupErrors, slug)
	delete(d.failureLogs, slug)
	d.stateMu.Unlock()
}

// LastFailureLogs is the tail kept from the last failed launch. A copy is
// returned so callers cannot mutate the cache.
func (d *Docker) LastFailureLogs(slug string) []string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	src := d.failureLogs[slug]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// ── Docker listing cache (upstream's 3s TTL) ────────────────────────────────

func (d *Docker) dockerLines(ctx context.Context, key string, args ...string) []string {
	d.listMu.Lock()
	if e, ok := d.listCache[key]; ok && time.Since(e.at) < 3*time.Second {
		d.listMu.Unlock()
		return e.lines
	}
	d.listMu.Unlock()

	out, _, code := d.Client.Output(ctx, args...)
	if code != 0 {
		out = ""
	}
	lines := nonEmptyLines(strings.TrimRight(out, "\n"))
	d.listMu.Lock()
	d.listCache[key] = listEntry{at: time.Now(), lines: lines}
	d.listMu.Unlock()
	return lines
}

func (d *Docker) invalidateLists() {
	d.listMu.Lock()
	d.listGen++
	d.listCache = map[string]listEntry{}
	d.listMu.Unlock()
}

// ── Environment for child processes ─────────────────────────────────────────

// launchEnv is the environment handed to compose build/up: the process
// environment plus HF_TOKEN (from data/hf-token or the HF cache), the caller's
// SAH_API_KEY and the origin they reached the Hub on.
func (d *Docker) launchEnv(apiKey, hubURL string) []string {
	env := os.Environ()
	hasHFToken := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "HF_TOKEN=") {
			hasHFToken = true
			break
		}
	}
	if !hasHFToken {
		if token := d.readHFToken(); token != "" {
			env = append(env, "HF_TOKEN="+token)
		}
	}
	if apiKey != "" {
		env = append(env, "SAH_API_KEY="+apiKey)
	}
	if hubURL != "" {
		env = append(env, "SAH_HUB_URL="+strings.TrimRight(hubURL, "/"))
	}
	return env
}

func (d *Docker) readHFToken() string {
	hub := filepath.Join(d.Config.DataDir, "hf-token")
	candidates := []string{hub}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".cache", "huggingface", "token"))
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	return ""
}

// ── Installed recipes ───────────────────────────────────────────────────────

// InstalledRecipes implements the recipes route package's state dependency.
func (d *Docker) InstalledRecipes(ctx context.Context) (map[string]struct{}, error) {
	return d.DB.InstalledRecipes(ctx)
}

// InstalledSlugs lists the installed slugs in ascending order.
func (d *Docker) InstalledSlugs(ctx context.Context) ([]string, error) {
	slugs, err := d.DB.InstalledSlugs(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(slugs)
	return slugs, nil
}
