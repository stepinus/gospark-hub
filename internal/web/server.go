// Package web is the net/http front door: JSON routes first, then static
// assets, then the SPA fallback. Same order and same cache rules as
// daemon/main.py, because the copied frontend depends on them.
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gospark-hub/internal/config"
)

// Router is where every feature package hangs its routes. Feature packages get
// Register(r *Router) so nobody else has to edit this file.
type Router struct {
	mux  *http.ServeMux
	auth func(http.Handler) http.Handler
}

func NewRouter() *Router { return &Router{mux: http.NewServeMux()} }

// API mounts a handler under /api, behind the session middleware.
func (r *Router) API(pattern string, h http.HandlerFunc) {
	r.mux.Handle("GET "+pattern, r.wrap(h))
}

func (r *Router) Handle(method, pattern string, h http.HandlerFunc) {
	r.mux.Handle(method+" "+pattern, r.wrap(h))
}

func (r *Router) HandleFunc(pattern string, h http.HandlerFunc) { r.mux.HandleFunc(pattern, h) }

// Mount hangs a whole http.Handler (file server, proxy, websocket) at a prefix.
func (r *Router) Mount(pattern string, h http.Handler) { r.mux.Handle(pattern, h) }

func (r *Router) wrap(h http.Handler) http.Handler {
	if r.auth == nil {
		return h
	}
	return r.auth(h)
}

func (r *Router) Use(mw func(http.Handler) http.Handler) { r.auth = mw }

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }

// Server owns the paths that come from the checkout rather than from a feature:
// the built SPA, the sah CLI, and the untouched cover sources.
type Server struct {
	Cfg  config.Config
	Dist string
}

func New(cfg config.Config) *Server {
	return &Server{Cfg: cfg, Dist: filepath.Join(cfg.BaseDir, "frontend", "dist")}
}

// assetSuffixes: a missing file with one of these must 404, never fall through
// to index.html — an HTML 200 for a missing .png poisons the browser cache.
var assetSuffixes = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
	".svg": true, ".ico": true, ".avif": true, ".css": true, ".js": true,
	".mjs": true, ".map": true, ".json": true, ".woff": true, ".woff2": true,
	".ttf": true,
}

func (s *Server) MountStatic(r *Router) {
	// Original cover sources, served whole.
	covers := filepath.Join(s.Cfg.BaseDir, "registry", "covers")
	if st, err := os.Stat(covers); err == nil && st.IsDir() {
		r.Mount("/cover-sources/", http.StripPrefix("/cover-sources/", http.FileServer(http.Dir(covers))))
	}

	// The sah CLI, shipped as-is; /sah/install.sh and /sah/version are dynamic
	// and win over the static mount because they are registered first.
	sahDir := filepath.Join(s.Cfg.BaseDir, "sah")
	if st, err := os.Stat(sahDir); err == nil && st.IsDir() {
		r.HandleFunc("GET /sah/install.sh", func(w http.ResponseWriter, req *http.Request) {
			http.ServeFile(w, req, filepath.Join(sahDir, "install.sh"))
		})
		r.HandleFunc("GET /sah/version", func(w http.ResponseWriter, req *http.Request) {
			sum, err := fileSHA256(filepath.Join(sahDir, "sah"))
			if err != nil {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte(sum))
		})
		r.Mount("/sah/", http.StripPrefix("/sah/", http.FileServer(http.Dir(sahDir))))
	}

	assets := filepath.Join(s.Dist, "assets")
	if st, err := os.Stat(assets); err == nil && st.IsDir() {
		r.Mount("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(assets))))
	}

	r.HandleFunc("GET /healthz", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	r.HandleFunc("/", s.serveSPA)
}

func (s *Server) serveSPA(w http.ResponseWriter, req *http.Request) {
	clean := path.Clean("/" + req.URL.Path)
	rel := strings.TrimPrefix(clean, "/")
	if rel != "" {
		full := filepath.Join(s.Dist, filepath.FromSlash(rel))
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			http.ServeFile(w, req, full)
			return
		}
		if assetSuffixes[strings.ToLower(path.Ext(rel))] {
			http.NotFound(w, req)
			return
		}
	}
	index := filepath.Join(s.Dist, "index.html")
	if _, err := os.Stat(index); err != nil {
		log.Printf("spa: %s missing — run `npm run build` in frontend/", index)
		http.NotFound(w, req)
		return
	}
	// index.html names the hashed bundle, so it must always revalidate.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, req, index)
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
