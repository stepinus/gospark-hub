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
	filesapi "gospark-hub/internal/api/files"
	recipesapi "gospark-hub/internal/api/recipes"
	"gospark-hub/internal/api/system"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// installed is the recipes.Deps the list endpoint needs. Until the docker phase
// lands nothing can be installed, so every recipe renders as not installed —
// same shape, empty state.
type installed struct{}

func (installed) InstalledRecipes(context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func (installed) HasLeftovers(context.Context, string) (bool, error) { return false, nil }

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
	recipesapi.Register(r, installed{})
	reg := registry.NewCache(cfg.RegistryPath)
	filesapi.Register(r, filesapi.Deps{Config: cfg, Store: db, Logger: log.Default(), Registry: reg})
	system.Register(r, system.Deps{
		Config:   cfg,
		Logger:   slog.Default(),
		Registry: reg,
	})
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
