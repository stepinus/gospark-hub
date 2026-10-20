// Package web is the net/http front door: JSON routes first, then static
// assets, then the SPA fallback. Same order and same cache rules as
// daemon/main.py, because the copied frontend depends on them.
package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gospark-hub/frontend"
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
	// dist is the SPA tree actually served: the copy embedded in the binary,
	// or the checkout's frontend/dist when the binary carries no index.html.
	dist fs.FS
	// CoverRoots are searched in order for /cover-sources/ files: local recipes
	// first, then every source's covers directory. The daemon fills this from
	// the registry manager; New falls back to the checkout copies.
	CoverRoots []string
	// CoverRootsFn, when set, is asked on every request instead of CoverRoots:
	// a sync adds a source whose covers must be served without a restart.
	CoverRootsFn func() []string
}

func New(cfg config.Config) *Server {
	s := &Server{Cfg: cfg, Dist: filepath.Join(cfg.BaseDir, "frontend", "dist")}
	s.dist = pickDist(s.Dist)
	legacy := filepath.Join(cfg.BaseDir, "registry", "covers")
	if st, err := os.Stat(legacy); err == nil && st.IsDir() {
		s.CoverRoots = []string{legacy}
	}
	return s
}

// pickDist prefers the SPA compiled into the binary, so a release binary runs
// with no frontend/ directory beside it. A binary built without `npm run build`
// carries no index.html and falls through to whatever is on disk.
func pickDist(dir string) fs.FS {
	if sub, err := fs.Sub(frontend.Dist, "dist"); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			return sub
		}
	}
	return os.DirFS(dir)
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
	// Cover sources, served whole from every root that may hold one. A local
	// cover wins over a vendored cover of the same name.
	r.Mount("/cover-sources/", http.StripPrefix("/cover-sources/", http.HandlerFunc(s.serveCover)))

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

	if assets, err := fs.Sub(s.dist, "assets"); err == nil {
		if st, err := fs.Stat(assets, "."); err == nil && st.IsDir() {
			// Asset names carry a content hash: they never change under a name.
			immutable := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				http.FileServer(http.FS(assets)).ServeHTTP(w, req)
			})
			r.Mount("/assets/", http.StripPrefix("/assets/", immutable))
		}
	}

	r.HandleFunc("GET /healthz", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	r.HandleFunc("/", s.serveSPA)
}

// serveCover serves the first matching file across the cover roots. Only
// regular files are served: a directory must not turn into a listing of the
// registry home, and a missing file must stay a 404 for every root.
func (s *Server) serveCover(w http.ResponseWriter, req *http.Request) {
	rel := strings.TrimPrefix(path.Clean("/"+req.URL.Path), "/")
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		http.NotFound(w, req)
		return
	}
	roots := s.CoverRoots
	if s.CoverRootsFn != nil {
		roots = s.CoverRootsFn()
	}
	for _, root := range roots {
		full := filepath.Join(root, filepath.FromSlash(rel))
		st, err := os.Stat(full)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		http.ServeFile(w, req, full)
		return
	}
	http.NotFound(w, req)
}

func (s *Server) serveSPA(w http.ResponseWriter, req *http.Request) {
	clean := path.Clean("/" + req.URL.Path)
	rel := strings.TrimPrefix(clean, "/")
	if rel != "" && rel != "." {
		if st, err := fs.Stat(s.dist, rel); err == nil && !st.IsDir() {
			s.serveDistFile(w, req, rel)
			return
		}
		if assetSuffixes[strings.ToLower(path.Ext(rel))] {
			http.NotFound(w, req)
			return
		}
	}
	if _, err := fs.Stat(s.dist, "index.html"); err != nil {
		log.Printf("spa: %s/index.html missing — run `npm run build` in frontend/", s.Dist)
		http.NotFound(w, req)
		return
	}
	// index.html names the hashed bundle, so it must always revalidate.
	w.Header().Set("Cache-Control", "no-cache")
	s.serveDistFile(w, req, "index.html")
}

// serveDistFile serves one file out of the resolved SPA tree — embedded or on
// disk, the caller does not care which.
func (s *Server) serveDistFile(w http.ResponseWriter, req *http.Request, name string) {
	b, err := fs.ReadFile(s.dist, name)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	http.ServeContent(w, req, path.Base(name), time.Time{}, bytes.NewReader(b))
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
