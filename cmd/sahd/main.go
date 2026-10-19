// sahd — Spark AI Hub daemon, Go port.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	authapi "gospark-hub/internal/api/auth"
	containersapi "gospark-hub/internal/api/containers"
	enginesapi "gospark-hub/internal/api/engines"
	filesapi "gospark-hub/internal/api/files"
	llamaswapapi "gospark-hub/internal/api/llamaswap"
	mcpapi "gospark-hub/internal/api/mcp"
	modelsapi "gospark-hub/internal/api/models"
	oauthapi "gospark-hub/internal/api/oauthapi"
	recipesapi "gospark-hub/internal/api/recipes"
	registriesapi "gospark-hub/internal/api/registries"
	"gospark-hub/internal/api/system"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/dockerx"
	"gospark-hub/internal/engines"
	"gospark-hub/internal/llamaswap"
	"gospark-hub/internal/native"
	"gospark-hub/internal/oauth"
	"gospark-hub/internal/proxy"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// runtime adapts the container service to what the MCP tools need: they ask
// what is installed, what runs, and start/stop an app on demand.
type runtime struct{ d *dockerx.Docker }

func (r runtime) Installed(ctx context.Context) (map[string]bool, error) {
	slugs, err := r.d.InstalledRecipes(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(slugs))
	for slug := range slugs {
		out[slug] = true
	}
	return out, nil
}

func (r runtime) Running(ctx context.Context, slug string) (bool, error) {
	return r.d.IsRecipeRunning(ctx, slug), nil
}

func (r runtime) Ready(slug string) bool       { return r.d.IsReady(slug) }
func (r runtime) StartHealthCheck(slug string) { r.d.StartHealthCheck(context.Background(), slug) }
func (r runtime) Stop(ctx context.Context, slug string) error {
	_, err := r.d.StopRecipe(ctx, slug)
	return err
}

func (r runtime) Launch(ctx context.Context, slug string, user *store.User, origin string) error {
	key := ""
	if user != nil {
		key = user.APIKey
	}
	_, err := r.d.LaunchRecipe(ctx, slug, key, origin, nil)
	return err
}

// runner tells the model proxy which apps are up, so a request lands on the
// container that is actually serving a model.
type runner struct {
	d   *dockerx.Docker
	n   *native.Runner
	reg *registry.Cache
}

func (r runner) RunningSlugs(ctx context.Context) ([]string, error) {
	slugs, err := r.d.InstalledSlugs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		if r.d.IsRecipeRunning(ctx, slug) {
			out = append(out, slug)
		}
	}
	// Native recipes run as systemd --user units, so dockerx cannot see them;
	// without this the proxy's model list and routing skipped every native
	// model (they never showed up in /v1/models or on the Agents page).
	if r.n != nil && r.reg != nil {
		recipes, err := r.reg.All()
		if err != nil {
			return nil, err
		}
		for _, rec := range recipes {
			if !rec.IsNative() {
				continue
			}
			if running, err := r.n.Running(ctx, rec.Slug); err == nil && running {
				out = append(out, rec.Slug)
			}
		}
	}
	return out, nil
}

func main() {
	cfg := config.Load()
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()
	if err := db.InitSchema(); err != nil {
		log.Fatalf("schema: %v", err)
	}

	srv := web.New(cfg)
	r := web.NewRouter()

	// Auth first: it installs the session middleware that wraps every route
	// registered after this point.
	// One OAuth service, shared: it issues tokens for /mcp and resolves them in
	// the session middleware.
	oauthSvc := oauth.NewService(db)
	authapi.Register(r, auth.Deps{Store: db, Config: cfg, Logger: log.Default(), TokenResolver: oauthSvc})

	mgr, err := registry.NewManager(cfg.RegistryHome)
	if err != nil {
		log.Fatalf("registry home: %v", err)
	}
	// No fallback to the repository checkout: recipes come only from the
	// configured sources under the registry home.
	reg := registry.NewCache(mgr.Roots()...)
	reg.SetRootsProvider(mgr.Roots)
	dock := dockerx.New(cfg, reg, db, slog.Default())

	// Host engines: one binary on the machine, installed from the Engines page,
	// that native recipes are launched on through systemd --user.
	enginesMgr := engines.NewManager(cfg.EngineHome)
	nativeRunner := native.NewRunner(filepath.Join(cfg.DataDir, "logs"))

	// llama-swap is a sidecar binary the Hub downloads and runs itself, on the
	// same pattern as the engines: one release archive, one systemd --user
	// unit, one YAML config the page edits.
	swapMgr := llamaswap.NewManager(os.Getenv("LLAMASWAP_HOME"))
	swapService := &llamaswap.Service{Manager: swapMgr}

	recipesapi.Register(r, recipesapi.Deps{Runtime: dock, Cache: reg, Manager: mgr, Engines: enginesMgr, Native: nativeRunner})
	containersapi.Register(r, containersapi.Deps{Config: cfg, Registry: reg, Logger: slog.Default(), DB: db, Docker: dock, Engines: enginesMgr, Native: nativeRunner})
	enginesapi.Register(r, enginesapi.Deps{Engines: enginesMgr, Native: nativeRunner, Reg: reg, Logger: slog.Default()})
	llamaswapapi.Register(r, llamaswapapi.Deps{
		Manager:  swapMgr,
		Service:  swapService,
		Registry: reg,
		Store:    db,
		HubPort:  proxy.ListenPort(cfg),
		Running: func(slug string) bool {
			if rec, ok := reg.Get(slug); ok && rec.IsNative() {
				running, _ := nativeRunner.Running(context.Background(), slug)
				return running
			}
			return dock.IsRecipeRunning(context.Background(), slug)
		},
		Logger: slog.Default(),
	})
	filesapi.Register(r, filesapi.Deps{Config: cfg, Store: db, Logger: log.Default(), Registry: reg})
	mcpapi.Register(r, mcpapi.Deps{Config: cfg, Store: db, Registry: reg, Logger: log.Default(), Runtime: runtime{d: dock}})
	modelsapi.Register(r, modelsapi.Deps{Config: cfg, Registry: reg, Store: db, Logger: log.Default(), Runner: runner{d: dock, n: nativeRunner, reg: reg}})
	oauthapi.Register(r, oauthapi.Deps{Config: cfg, Store: db, Logger: log.Default(), Service: oauthSvc})
	system.Register(r, system.Deps{Config: cfg, Logger: slog.Default(), Registry: reg})
	registriesapi.Register(r, registriesapi.Deps{Manager: mgr, Cache: reg, Runtime: dock, Logger: slog.Default()})
	srv.CoverRoots = mgr.CoverRoots()
	srv.CoverRootsFn = mgr.CoverRoots
	srv.MountStatic(r)

	// The front door (Caddy) publishes the public port and forwards back to the
	// daemon; with proxy_enabled=false the daemon listens on that port itself.
	// Bringing it up never fails the boot: a broken proxy must not remove the
	// recovery door on cfg.Port.
	if err := proxy.Ensure(context.Background(), cfg, reg, slog.Default()); err != nil {
		log.Printf("[proxy] %v", err)
	}

	httpSrv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(proxy.ListenPort(cfg))),
		Handler:           r,
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("sahd listening on %s (public %d, project %s)", httpSrv.Addr, cfg.PublicPort, cfg.Project)

	// Bring up (or hot-reload) the one public port. This never fails the boot:
	// a broken proxy must not remove the recovery door on cfg.Port.

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	_ = httpSrv.Close()
}
