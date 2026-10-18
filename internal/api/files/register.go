// Package files implements /api/uploads, /api/media and /link/* routes.
package files

import (
	"log"

	"gospark-hub/internal/config"
	"gospark-hub/internal/media"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// Deps supplies the runtime state the file routes need.
type Deps struct {
	Config   config.Config
	Store    *store.DB
	Logger   *log.Logger
	Registry *registry.Cache
}

// Register mounts uploads, media and short link routes on r.
func Register(r *web.Router, deps Deps) {
	if deps.Logger == nil {
		deps.Logger = log.Default()
	}
	ms := media.NewStore(deps.Config, deps.Store)
	ls := media.NewLinkService(deps.Store)
	media.StartJanitor(ls, deps.Logger)

	h := &handler{
		cfg:      deps.Config,
		store:    deps.Store,
		media:    ms,
		links:    ls,
		registry: deps.Registry,
		log:      deps.Logger,
		thumbs:   media.NewThumbCache(500),
	}

	// Authenticated routes.
	r.Handle("POST", "/api/uploads", h.upload)
	r.Handle("GET", "/api/media", h.listMedia)
	r.Handle("DELETE", "/api/media/{name}", h.deleteMedia)
	r.Handle("GET", "/api/media/{name}/thumb", h.getThumb)
	r.Handle("PUT", "/api/media/{name}/thumb", h.putThumb)

	// Token-only routes outside /api so the auth middleware lets them through.
	r.HandleFunc("POST /link/upload/{token}", h.uploadOnce)
	r.HandleFunc("GET /link/download/{token}", h.downloadOnce)
}

type handler struct {
	cfg      config.Config
	store    *store.DB
	media    *media.MediaStore
	links    *media.LinkService
	registry *registry.Cache
	log      *log.Logger
	thumbs   *media.ThumbCache
}
