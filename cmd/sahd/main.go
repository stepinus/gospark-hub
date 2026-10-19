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
	"strconv"
	"syscall"
	"time"

	authapi "gospark-hub/internal/api/auth"
	containersapi "gospark-hub/internal/api/containers"
	filesapi "gospark-hub/internal/api/files"
	mcpapi "gospark-hub/internal/api/mcp"
	modelsapi "gospark-hub/internal/api/models"
	recipesapi "gospark-hub/internal/api/recipes"
	"gospark-hub/internal/api/system"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/dockerx"
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

func (r runtime) Ready(slug string) bool          { return r.d.IsReady(slug) }
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
type runner struct{ d *dockerx.Docker }

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
	authapi.Register(r, auth.Deps{Store: db, Config: cfg, Logger: log.Default()})

	reg := registry.NewCache(cfg.RegistryPath)
	dock := dockerx.New(cfg, reg, db, slog.Default())

	recipesapi.Register(r, dock)
	containersapi.Register(r, containersapi.Deps{Config: cfg, Registry: reg, Logger: slog.Default(), DB: db})
	filesapi.Register(r, filesapi.Deps{Config: cfg, Store: db, Logger: log.Default(), Registry: reg})
	mcpapi.Register(r, mcpapi.Deps{Config: cfg, Store: db, Registry: reg, Logger: log.Default(), Runtime: runtime{d: dock}})
	modelsapi.Register(r, modelsapi.Deps{Config: cfg, Registry: reg, Store: db, Logger: log.Default(), Runner: runner{d: dock}})
	system.Register(r, system.Deps{Config: cfg, Logger: slog.Default(), Registry: reg})
	srv.MountStatic(r)

	httpSrv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:           r,
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("sahd listening on %s (public %d, project %s)", httpSrv.Addr, cfg.PublicPort, cfg.Project)

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
