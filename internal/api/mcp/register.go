// Package mcpapi mounts the MCP endpoint and the media artifact routes on the
// Hub's router. The MCP logic lives in internal/mcp; this package is the HTTP
// wiring, mirroring daemon/routers/mcp.py's route table.
package mcpapi

import (
	"log"
	"net/http"

	"gospark-hub/internal/config"
	"gospark-hub/internal/mcp"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// Deps supplies the runtime state the MCP routes need.
type Deps struct {
	Config   config.Config
	Store    *store.DB
	Registry *registry.Cache
	Logger   *log.Logger
	// Runtime answers installed/running/ready and starts or stops a recipe.
	Runtime mcp.Runtime
	// App resolves a recipe's internal URL. Defaults to the front door.
	App  mcp.App
	HTTP *http.Client
}

// Register mounts POST /mcp, the 405 GET/DELETE answer, and the artifact
// routes /images|/videos|/audio|/meshes/{name}.
func Register(r *web.Router, deps Deps) {
	if deps.Logger == nil {
		deps.Logger = log.Default()
	}
	server := mcp.New(mcp.Deps{
		Config:   deps.Config,
		Store:    deps.Store,
		Registry: deps.Registry,
		Logger:   deps.Logger,
		Runtime:  deps.Runtime,
		App:      deps.App,
		HTTP:     deps.HTTP,
	})

	// /mcp sits behind the session/API-key middleware like upstream.
	r.Handle("POST", "/mcp", server.POST)
	r.HandleFunc("GET /mcp", server.NoStream)
	r.HandleFunc("DELETE /mcp", server.NoStream)

	// Artifact routes are outside /api and /mcp, so the middleware lets an
	// anonymous request through and the handler itself answers 401.
	r.HandleFunc("GET /images/{name}", func(w http.ResponseWriter, req *http.Request) {
		server.ServeMedia(w, req, "image")
	})
	r.HandleFunc("GET /videos/{name}", func(w http.ResponseWriter, req *http.Request) {
		server.ServeMedia(w, req, "video")
	})
	r.HandleFunc("GET /audio/{name}", func(w http.ResponseWriter, req *http.Request) {
		server.ServeMedia(w, req, "audio")
	})
	r.HandleFunc("GET /meshes/{name}", func(w http.ResponseWriter, req *http.Request) {
		server.ServeMedia(w, req, "mesh")
	})
}
