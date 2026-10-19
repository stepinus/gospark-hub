// Package models implements the OpenAI- and Anthropic-compatible proxy routes
// under /v1.
package models

import (
	"log"
	"net/http"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/modelproxy"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// Deps supplies the runtime state the /v1 routes need.
type Deps struct {
	Config   config.Config
	Registry *registry.Cache
	Store    *store.DB
	Logger   *log.Logger
	Runner   modelproxy.Runner
}

// Register mounts the OpenAI- and Anthropic-compatible routes on r.
// Anthropic-specific routes are registered first so they win over the /v1/{path}
// catch-all.
func Register(r *web.Router, deps Deps) {
	if deps.Logger == nil {
		deps.Logger = log.Default()
	}
	proxy := modelproxy.New(modelproxy.Deps{
		Config:   deps.Config,
		Registry: deps.Registry,
		Store:    deps.Store,
		Logger:   deps.Logger,
		Runner:   deps.Runner,
	})
	h := &handler{proxy: proxy}

	r.Handle("POST", "/v1/messages", h.anthropicMessages)
	r.Handle("POST", "/v1/messages/count_tokens", h.countTokens)

	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		r.Handle(m, "/v1/{path...}", h.openaiProxy)
	}
}

type handler struct {
	proxy *modelproxy.Proxy
}

func (h *handler) openaiProxy(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	uid, source := userAndSource(r)
	h.proxy.OpenAIProxy(w, r, path, uid, source)
}

func (h *handler) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	uid, source := userAndSource(r)
	h.proxy.AnthropicMessages(w, r, uid, source)
}

func (h *handler) countTokens(w http.ResponseWriter, r *http.Request) {
	h.proxy.CountTokens(w, r)
}

func userAndSource(r *http.Request) (int64, string) {
	if u := auth.CurrentUser(r); u != nil {
		return u.ID, auth.AuthSource(r)
	}
	return 0, ""
}
