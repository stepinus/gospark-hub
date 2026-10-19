// Package llamaswap mounts the routes that manage the llama-swap sidecar:
// install/update of the release binary, its systemd unit, the YAML config and
// the on-demand measurement of a running model.
package llamaswap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gospark-hub/internal/auth"
	swap "gospark-hub/internal/llamaswap"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"

	"gopkg.in/yaml.v3"
)

// Deps is what the routes need. Recipes are read to show what a group would
// load: a model id that matches a recipe's served id borrows its memory.
type Deps struct {
	Manager  *swap.Manager
	Service  *swap.Service
	Registry *registry.Cache
	Store    *store.DB
	// HubPort is the daemon's own port: the generated start/stop scripts call
	// the Hub on loopback.
	HubPort int
	// Running answers whether a recipe is up, for the recipes shelf.
	Running func(slug string) bool
	Logger  *slog.Logger
}

// Register mounts the llama-swap routes. Admin only: the sidecar is one binary
// for the whole machine.
func Register(r *web.Router, deps Deps) {
	h := &handler{deps: deps}
	r.Handle("GET", "/api/llamaswap", h.status)
	r.Handle("POST", "/api/llamaswap/install", h.install)
	r.Handle("POST", "/api/llamaswap/update", h.update)
	r.Handle("POST", "/api/llamaswap/start", h.start)
	r.Handle("POST", "/api/llamaswap/stop", h.stop)
	r.Handle("GET", "/api/llamaswap/config", h.getConfig)
	r.Handle("PUT", "/api/llamaswap/config", h.putConfig)
	r.Handle("PUT", "/api/llamaswap/groups", h.putGroups)
	r.Handle("PUT", "/api/llamaswap/settings", h.putSettings)
	r.Handle("POST", "/api/llamaswap/models/{model}/measure", h.measure)
}

type handler struct{ deps Deps }

// status is everything the page renders: binary, unit, config and runtime.
func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	m := h.deps.Manager
	settings := m.Settings()
	installed, hasBinary := m.Installed()
	out := map[string]any{
		"installed":      hasBinary,
		"version":        installed,
		"port":           settings.Port,
		"config_path":    m.ConfigPath(),
		"binary_path":    m.Path(),
		"measurements":   m.Measurements(),
		"running_models": []swap.RunningModel{},
		"config":         nil,
	}
	if latest, err := m.Latest(r.Context()); err == nil {
		out["latest"] = latest
	}
	if h.deps.Service != nil {
		if running, err := h.deps.Service.Running(r.Context()); err == nil {
			out["running"] = running
		}
	}
	if cfg, err := m.Config(); err == nil {
		out["config"] = cfg
		out["config_exists"] = true
	} else {
		out["config_exists"] = false
	}
	if h.deps.Service != nil {
		if running, _ := h.deps.Service.Running(r.Context()); running {
			client := swap.NewClient(settings.Port, h.apiKey())
			if models, err := client.Running(r.Context()); err == nil {
				out["running_models"] = models
			}
			if version, err := client.Version(r.Context()); err == nil {
				out["sidecar_version"] = version
			}
		}
	}
	out["recipes"] = h.recipes(r.Context())
	out["total_memory_gb"] = totalMemoryGB()
	writeJSON(w, http.StatusOK, out)
}

// apiKey is the first key in the sidecar config; the Hub is one of its clients.
func (h *handler) apiKey() string {
	text, err := h.deps.Manager.ConfigYAML()
	if err != nil {
		return ""
	}
	var raw struct {
		APIKeys []string `yaml:"apiKeys" json:"apiKeys"`
	}
	if err := yaml.Unmarshal([]byte(text), &raw); err != nil {
		return ""
	}
	if len(raw.APIKeys) > 0 {
		return raw.APIKeys[0]
	}
	return ""
}

func (h *handler) install(w http.ResponseWriter, r *http.Request) {
	h.installVersion(w, r, r.URL.Query().Get("version"))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	h.installVersion(w, r, "")
}

func (h *handler) installVersion(w http.ResponseWriter, r *http.Request, version string) {
	if !h.admin(w, r) {
		return
	}
	got, err := h.deps.Manager.Install(r.Context(), version)
	switch {
	case errors.Is(err, swap.ErrAlreadyInstalled):
		writeJSON(w, http.StatusConflict, map[string]string{
			"detail": "llama-swap " + got + " is already installed",
		})
	case err != nil:
		h.fail(w, "install llama-swap", err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"version": got, "installed": true})
	}
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	err := h.deps.Service.Start(r.Context())
	switch {
	case errors.Is(err, swap.ErrNotInstalled):
		writeJSON(w, http.StatusConflict, map[string]string{"detail": "llama-swap is not installed"})
	case errors.Is(err, swap.ErrAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"detail": "llama-swap is already running"})
	case err != nil:
		h.fail(w, "start llama-swap", err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "started"})
	}
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	if err := h.deps.Service.Stop(r.Context()); err != nil {
		h.fail(w, "stop llama-swap", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "stopped"})
}

func (h *handler) getConfig(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	text, err := h.deps.Manager.ConfigYAML()
	if errors.Is(err, swap.ErrNoConfig) {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "No config yet"})
		return
	}
	if err != nil {
		h.fail(w, "read llama-swap config", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"yaml": text})
}

func (h *handler) putConfig(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "Invalid body"})
		return
	}
	if err := h.deps.Manager.SaveConfigYAML(body.YAML); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

func (h *handler) putGroups(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var groups map[string]swap.GroupConfig
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&groups); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "Invalid body"})
		return
	}
	if err := h.ensureModels(groups); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
		return
	}
	if err := h.deps.Manager.SetGroups(groups); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var settings swap.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "Invalid body"})
		return
	}
	if err := h.deps.Manager.SetSettings(settings); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// measure answers what the model actually does right now: prefill and decode
// around a fixed prompt, and the memory it holds afterwards.
func (h *handler) measure(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	model := r.PathValue("model")
	settings := h.deps.Manager.Settings()
	client := swap.NewClient(settings.Port, h.apiKey())
	measurement, err := client.Measure(r.Context(), model)
	if err != nil {
		h.fail(w, "measure "+model, err)
		return
	}
	if err := h.deps.Manager.SaveMeasurement(measurement); err != nil {
		h.logf("save measurement: %v", err)
	}
	writeJSON(w, http.StatusOK, measurement)
}


// recipes lists the Hub recipes llama-swap can front: the language models, with
// the memory a group would need and the port the sidecar should proxy to.
func (h *handler) recipes(ctx context.Context) []map[string]any {
	if h.deps.Registry == nil {
		return []map[string]any{}
	}
	all, err := h.deps.Registry.All()
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(all))
	for _, rec := range all {
		if !rec.IsLLM() {
			continue
		}
		port := rec.UI.Port
		if rec.IsNative() && rec.Native != nil && rec.Native.Port != 0 {
			port = rec.Native.Port
		}
		running := false
		if h.deps.Running != nil {
			running = h.deps.Running(rec.Slug)
		}
		out = append(out, map[string]any{
			"slug":      rec.Slug,
			"name":      rec.Name,
			"memory_gb": rec.MemoryGB,
			"port":      port,
			"kind":      rec.Kind,
			"running":   running,
		})
	}
	return out
}

// ensureModels gives every group member a model entry. A member that is not in
// the config yet is a Hub recipe the user just dragged in: its entry is
// generated to start and stop that recipe on demand.
func (h *handler) ensureModels(groups map[string]swap.GroupConfig) error {
	cfg, err := h.deps.Manager.Config()
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, member := range group.Members {
			// A member that is a Hub recipe is regenerated every save, so an
			// improved script or a rotated session reaches existing entries.
			rec, isRecipe := h.deps.Registry.Get(member)
			if _, inConfig := cfg.Models[member]; inConfig && !isRecipe {
				continue
			}
			if !isRecipe {
				return fmt.Errorf("group lists model %q, which is neither in the config nor a Hub recipe", member)
			}
			port := rec.UI.Port
			if rec.IsNative() && rec.Native != nil && rec.Native.Port != 0 {
				port = rec.Native.Port
			}
			token, err := h.sidecarToken()
			if err != nil {
				return err
			}
			model, err := h.deps.Manager.RecipeModel(member, rec.Name,
				"Hub recipe, started on demand through the Hub", port, h.deps.HubPort, token)
			if err != nil {
				return err
			}
			if err := h.deps.Manager.SetModel(member, model); err != nil {
				return err
			}
			cfg.Models[member] = model
		}
	}
	return nil
}

// sidecarToken is the session the generated scripts act with. It is minted
// once, kept 0600 next to the config, and reused until it expires.
func (h *handler) sidecarToken() (string, error) {
	path := filepath.Join(h.deps.Manager.Home, "sidecar-token")
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data)), nil
	}
	if h.deps.Store == nil {
		return "", errors.New("no store to mint a sidecar session")
	}
	id, ok := h.deps.Store.FirstAdminID()
	if !ok {
		return "", errors.New("no active administrator to act as")
	}
	token, err := h.deps.Store.CreateSession(id, "gospark-llamaswap", "127.0.0.1", 3650)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(h.deps.Manager.Home, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// totalMemoryGB is the machine's RAM, the denominator of the group fill bars.
func totalMemoryGB() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0
		}
		return math.Round(kb / 1024 / 1024 * 10) / 10
	}
	return 0
}

func (h *handler) admin(w http.ResponseWriter, r *http.Request) bool {
	if _, err := auth.RequireAdmin(r); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"detail": "Admin only"})
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, what string, err error) {
	h.logf("%s: %v", what, err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
}

func (h *handler) logf(format string, args ...any) {
	if h.deps.Logger != nil {
		h.deps.Logger.Error("llamaswap: "+format, args...)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
