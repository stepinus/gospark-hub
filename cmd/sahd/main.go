// sahd — Spark AI Hub daemon, Go port.
package main

import (
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

	"gospark-hub/internal/api/system"
	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

func main() {
	cfg := config.Load()
	for _, dir := range []string{cfg.DataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("data dir: %v", err)
		}
	}

	srv := web.New(cfg)
	r := web.NewRouter()
	srv.MountStatic(r)

	reg := registry.New(cfg)
	if err := reg.Load(); err != nil {
		log.Printf("registry load: %v", err)
	}
	system.Register(r, system.Deps{
		Config:   cfg,
		Logger:   slog.Default(),
		Registry: reg,
	})

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
