// Package llamaswap mounts the routes that manage the llama-swap sidecar:
// install/update of the release binary, its systemd unit, the YAML config and
// the on-demand measurement of a running model.
package llamaswap

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"gospark-hub/internal/auth"
	swap "gospark-hub/internal/llamaswap"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"

	"gopkg.in/yaml.v3"
)

// Deps is what the routes need. Recipes are read to show what a group would
// load: a model id that matches a recipe's served id borrows its memory.
type Deps struct {
	Manager  *swap.Manager
	Service  *swap.Service
	Registry *registry.Cache
	Logger   *slog.Logger
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
