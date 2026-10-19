package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// Runtime answers the questions the MCP tools ask about installed recipes:
// which are installed, which are running and ready, and how to start or stop
// one. The default implementation is the docker CLI, the same commands the
// Hub's own buttons run; a test supplies a fake.
type Runtime interface {
	Installed(ctx context.Context) (map[string]bool, error)
	Running(ctx context.Context, slug string) (bool, error)
	Ready(slug string) bool
	StartHealthCheck(slug string)
	Launch(ctx context.Context, slug string, user *store.User, origin string) error
	Stop(ctx context.Context, slug string) error
}

// NilRuntime reports nothing installed, so every media tool answers with the
// upstream "nothing is installed" error.
type NilRuntime struct{}

func (NilRuntime) Installed(context.Context) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (NilRuntime) Running(context.Context, string) (bool, error) { return false, nil }
func (NilRuntime) Ready(string) bool                             { return false }
func (NilRuntime) StartHealthCheck(string)                       {}
func (NilRuntime) Launch(context.Context, string, *store.User, string) error {
	return fmt.Errorf("no container runtime available")
}
func (NilRuntime) Stop(context.Context, string) error {
	return fmt.Errorf("no container runtime available")
}

// DockerRuntime is the docker-CLI runtime: installed recipes come from the
// database, running state from `docker ps`, readiness from a health poll of
// the app through the front door, and start/stop from `docker compose`.
type DockerRuntime struct {
	cfg  config.Config
	db   *store.DB
	reg  *registry.Cache
	app  App
	log  *log.Logger
	exec func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu       sync.Mutex
	ready    map[string]bool
	checking map[string]bool
	running  map[string]bool
	runAt    time.Time
}

// NewDockerRuntime builds the default runtime.
func NewDockerRuntime(cfg config.Config, db *store.DB, reg *registry.Cache, app App, logger *log.Logger) *DockerRuntime {
	if logger == nil {
		logger = log.Default()
	}
	return &DockerRuntime{
		cfg: cfg, db: db, reg: reg, app: app, log: logger,
		exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		ready:    map[string]bool{},
		checking: map[string]bool{},
	}
}

func (d *DockerRuntime) project(slug string) string { return d.cfg.Project + "-" + slug }

// Installed reads the installed_recipes table.
func (d *DockerRuntime) Installed(ctx context.Context) (map[string]bool, error) {
	if d.db == nil {
		return map[string]bool{}, nil
	}
	rows, err := d.db.Query("SELECT slug FROM installed_recipes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out[slug] = true
	}
	return out, rows.Err()
}

// Running reports whether the recipe's compose project has a container up.
func (d *DockerRuntime) Running(ctx context.Context, slug string) (bool, error) {
	d.mu.Lock()
	if time.Since(d.runAt) < 2*time.Second && d.running != nil {
		up := d.running[d.project(slug)]
		d.mu.Unlock()
		return up, nil
	}
	d.mu.Unlock()

	out, err := d.exec(ctx, "docker", "ps", "--format", `{{.Label "com.docker.compose.project"}}`)
	if err != nil {
		return false, err
	}
	projects := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			projects[p] = true
		}
	}
	d.mu.Lock()
	d.running = projects
	d.runAt = time.Now()
	up := projects[d.project(slug)]
	d.mu.Unlock()
	return up, nil
}

// Ready reports the health poll's verdict for slug.
func (d *DockerRuntime) Ready(slug string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ready[slug]
}

// StartHealthCheck polls the app through the front door until it answers,
// mirroring docker_service.start_health_check (a 5-minute budget).
func (d *DockerRuntime) StartHealthCheck(slug string) {
	d.mu.Lock()
	if d.ready[slug] || d.checking[slug] {
		d.mu.Unlock()
		return
	}
	d.checking[slug] = true
	d.mu.Unlock()

	healthPath := "/"
	if d.reg != nil {
		if recipe, ok := d.reg.Get(slug); ok {
			if recipe.UI.Path != "" {
				healthPath = recipe.UI.Path
			}
			if recipe.UI.HealthPath != nil && *recipe.UI.HealthPath != "" {
				healthPath = *recipe.UI.HealthPath
			}
		}
	}
	url := d.app.InternalURL(slug) + healthPath
	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.checking, slug)
			d.mu.Unlock()
		}()
		client := &http.Client{Timeout: 3 * time.Second}
		for i := 0; i < 300; i++ {
			time.Sleep(time.Second)
			req, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				continue
			}
			for k, v := range d.app.ProbeHeaders() {
				req.Header.Set(k, v)
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				d.mu.Lock()
				d.ready[slug] = true
				d.mu.Unlock()
				d.log.Printf("[health] %s is ready at %s (%d)", slug, url, resp.StatusCode)
				return
			}
		}
	}()
}

// Launch brings a recipe's compose project up.
func (d *DockerRuntime) Launch(ctx context.Context, slug string, user *store.User, origin string) error {
	dir := ""
	if d.reg != nil {
		if recipeDir, ok := d.reg.Dir(slug); ok {
			dir = recipeDir
		}
	}
	args := []string{"compose", "-p", d.project(slug)}
	if dir != "" {
		args = append(args, "--project-directory", dir)
	}
	args = append(args, "up", "-d")
	if _, err := d.exec(ctx, "docker", args...); err != nil {
		return err
	}
	d.StartHealthCheck(slug)
	return nil
}

// Stop brings a recipe's compose project down.
func (d *DockerRuntime) Stop(ctx context.Context, slug string) error {
	dir := ""
	if d.reg != nil {
		if recipeDir, ok := d.reg.Dir(slug); ok {
			dir = recipeDir
		}
	}
	args := []string{"compose", "-p", d.project(slug)}
	if dir != "" {
		args = append(args, "--project-directory", dir)
	}
	args = append(args, "stop")
	if _, err := d.exec(ctx, "docker", args...); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.ready, slug)
	d.mu.Unlock()
	return nil
}
