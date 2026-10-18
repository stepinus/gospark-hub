package dockerx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gospark-hub/internal/registry"
)

// Startup-failure classification, copied from upstream so the log panel says
// the same sentence a user can act on instead of a bare traceback.
var (
	vllmFreeMemRE = regexp.MustCompile(`Free memory on device \S+ \(([\d.]+)/([\d.]+) GiB\) on startup is less than desired GPU memory utilization \(([\d.]+), ([\d.]+) GiB\)`)
	cudaOOMRE     = regexp.MustCompile(`CUDA out of memory|torch\.OutOfMemoryError|cudaErrorMemoryAllocation`)
	kvCacheRE     = regexp.MustCompile(`To serve at least one request with the models's max seq len|No available memory for the cache blocks`)
)

const restartLimit = 2

// ClassifyStartupFailure turns a dead container's logs into one actionable
// sentence, or "" when the failure is not recognised.
func ClassifyStartupFailure(logs string) string {
	if m := vllmFreeMemRE.FindStringSubmatch(logs); m != nil {
		free, _ := strconv.ParseFloat(m[1], 64)
		wanted, _ := strconv.ParseFloat(m[4], 64)
		short := wanted - free
		return fmt.Sprintf(
			"Not enough free memory to start. The engine asked for %s GiB "+
				"(%s of %s GiB) but only %s GiB was free -- "+
				"%.2f GiB short. CPU and GPU share one memory pool on this "+
				"machine, and this check counts only memory that is free right "+
				"now: cached file data and the driver's own reserve count against "+
				"it, so it can fail even with no other app running. Stop any other "+
				"running app, or lower GPU_MEMORY_UTILIZATION in this recipe's "+
				"docker-compose.yml.",
			m[4], m[3], m[2], m[1], short)
	}
	if kvCacheRE.MatchString(logs) {
		return "Not enough free memory for the context window. The weights loaded " +
			"but there was no room left for the KV cache. Lower MAX_MODEL_LEN " +
			"or MAX_NUM_SEQS in this recipe's docker-compose.yml, or stop " +
			"another running app and launch again."
	}
	if cudaOOMRE.MatchString(logs) {
		return "Ran out of memory while loading the model. This machine shares one " +
			"memory pool between CPU and GPU, so other running apps count " +
			"against it. Stop any other running app and launch again."
	}
	return ""
}

// StartHealthCheck launches a background poll for the recipe's readiness,
// unless it is already ready or a check is already running.
func (d *Docker) StartHealthCheck(ctx context.Context, slug string) {
	if d.IsReady(slug) {
		return
	}
	rec, ok := d.Reg.Get(slug)
	if !ok {
		return
	}
	d.stateMu.Lock()
	if _, running := d.health[slug]; running {
		d.stateMu.Unlock()
		return
	}
	hctx, cancel := context.WithCancel(ctx)
	d.health[slug] = cancel
	d.stateMu.Unlock()
	go d.healthLoop(hctx, slug, rec)
}

func (d *Docker) healthLoop(ctx context.Context, slug string, rec registry.Recipe) {
	url, headers, follow := d.healthTarget(slug, rec)
	interval := d.HealthInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticks := d.HealthTicks
	if ticks <= 0 {
		ticks = 300
	}

	for tick := 0; tick < ticks; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if d.probe(ctx, url, headers, follow) {
			d.MarkReady(slug)
			d.Log.Info("recipe ready", "slug", slug, "url", url)
			return
		}
		if tick%3 == 2 {
			if failed, why := d.containerExitInfo(ctx, slug); failed {
				d.ReportStartupFailure(ctx, slug, why)
				return
			}
		}
	}
	d.Log.Info("health check timed out", "slug", slug)
}

// healthTarget builds the probe URL: proxied recipes are polled through the
// front door (public port + probe token), everything else on its own port.
func (d *Docker) healthTarget(slug string, rec registry.Recipe) (string, map[string]string, bool) {
	uiPath := rec.UI.Path
	if uiPath == "" {
		uiPath = "/"
	}
	healthPath := uiPath
	if rec.UI.HealthPath != nil && *rec.UI.HealthPath != "" {
		healthPath = *rec.UI.HealthPath
	}
	headers := map[string]string{}
	if rec.UI.Proxy {
		token := d.probeToken()
		if token != "" {
			headers["X-Sah-Probe"] = token
		}
		return fmt.Sprintf("http://127.0.0.1:%d/run/%s%s", d.Config.PublicPort, slug, healthPath), headers, false
	}
	port := rec.UI.Port
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath), headers, true
}

func (d *Docker) probeToken() string {
	data, err := os.ReadFile(filepath.Join(d.Config.DataDir, "proxy-probe-token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (d *Docker) probe(ctx context.Context, url string, headers map[string]string, follow bool) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := d.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	if !follow {
		noRedirect := *client
		noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &noRedirect
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// ReportStartupFailure records why a launch died (classifying the logs) and
// then takes the app down, so a corpse is not inherited by the next attempt.
func (d *Docker) ReportStartupFailure(ctx context.Context, slug, why string) {
	if d.GetStartupError(slug) != "" {
		return
	}
	logs := d.containerLogsTail(ctx, slug)
	message := ClassifyStartupFailure(logs)
	if message == "" {
		tail := nonEmptyLines(strings.TrimRight(logs, "\n"))
		if len(tail) > 3 {
			tail = tail[len(tail)-3:]
		}
		detail := "no output"
		if len(tail) > 0 {
			detail = truncate(strings.Join(tail, " "), 300)
		}
		message = fmt.Sprintf("The app stopped before it finished starting (%s): %s", why, detail)
	}
	d.SetStartupError(slug, message)
	failureLines := nonEmptyLines(strings.TrimRight(logs, "\n"))
	if len(failureLines) > 400 {
		failureLines = failureLines[len(failureLines)-400:]
	}
	d.stateMu.Lock()
	d.failureLogs[slug] = failureLines
	// The health task is what is running this; drop its bookkeeping by hand
	// instead of calling ClearReady, which would cancel it.
	delete(d.ready, slug)
	delete(d.health, slug)
	d.stateMu.Unlock()
	d.SetPending(slug, "stopping")
	defer d.ClearPending(slug)
	_, _ = d.StopRecipe(ctx, slug)
	d.Log.Info("recipe failed to start", "slug", slug, "error", message)
}

// containerExitInfo reports (failed, why) for a recipe's containers: a
// crash-loop past restartLimit, or every container exited with a non-zero
// code. A sidecar that exits 0 is not a failure.
func (d *Docker) containerExitInfo(ctx context.Context, slug string) (bool, string) {
	names := d.dockerLines(ctx, "ps_any:"+slug, "ps", "-a",
		"--filter", "label=com.docker.compose.project="+d.ComposeProject(slug),
		"--format", "{{.Names}}")
	if len(names) == 0 {
		return false, ""
	}
	args := append([]string{"inspect", "--format", "{{.State.Status}}|{{.State.ExitCode}}|{{.RestartCount}}"}, names...)
	out, _, code := d.Client.Output(ctx, args...)
	if code != 0 {
		return false, ""
	}
	type row struct {
		status   string
		exitCode int
		restarts int
	}
	var rows []row
	for _, line := range nonEmptyLines(strings.TrimRight(out, "\n")) {
		fields := strings.Split(line, "|")
		if len(fields) != 3 {
			continue
		}
		exit, err1 := strconv.Atoi(strings.TrimSpace(fields[1]))
		restarts, err2 := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err1 != nil || err2 != nil {
			continue
		}
		rows = append(rows, row{status: strings.TrimSpace(fields[0]), exitCode: exit, restarts: restarts})
	}
	if len(rows) == 0 {
		return false, ""
	}
	for _, r := range rows {
		if r.restarts >= restartLimit {
			return true, fmt.Sprintf("it crashed and restarted %d times", r.restarts)
		}
	}
	for _, r := range rows {
		if r.status != "exited" {
			return false, ""
		}
	}
	for _, r := range rows {
		if r.exitCode != 0 {
			return true, fmt.Sprintf("exit code %d", r.exitCode)
		}
	}
	return false, ""
}

func (d *Docker) containerLogsTail(ctx context.Context, slug string) string {
	container := d.AnyContainerName(ctx, slug)
	if container == "" {
		return ""
	}
	out, code := d.Client.Logs(ctx, container, false, 400)
	if code != 0 {
		return ""
	}
	return out
}

// AnyContainerName returns the recipe's first container, running OR exited,
// so a container that died still has readable logs.
func (d *Docker) AnyContainerName(ctx context.Context, slug string) string {
	names := d.dockerLines(ctx, "ps_any:"+slug, "ps", "-a",
		"--filter", "label=com.docker.compose.project="+d.ComposeProject(slug),
		"--format", "{{.Names}}")
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
