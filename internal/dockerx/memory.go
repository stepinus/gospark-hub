package dockerx

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
)

// MeminfoPath is where the memory reader looks; tests point it at a fixture.
var MeminfoPath = "/proc/meminfo"

// AvailableMemoryGB reads a field of /proc/meminfo in GiB. With the default
// "MemAvailable" it is how much memory a new app can actually claim; with
// "MemTotal" it is the pool CPU and GPU share.
func AvailableMemoryGB(field string) float64 {
	f, err := os.Open(MeminfoPath)
	if err != nil {
		return 0.0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, field+":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0.0
		}
		kb, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0.0
		}
		return kb / (1024 * 1024)
	}
	return 0.0
}

// MemoryPlan answers "(GiB `slug` needs, GiB free for it, other apps up or
// starting)". An app that is still loading has not taken all of its memory
// yet, so its share is counted as spoken for.
func (d *Docker) MemoryPlan(ctx context.Context, slug string) (need, free float64, running []string) {
	installed, err := d.InstalledSlugs(ctx)
	if err != nil {
		installed = nil
	}
	loading := 0.0
	for _, other := range installed {
		if other == slug {
			continue
		}
		rec, ok := d.Reg.Get(other)
		if !ok {
			continue
		}
		if !d.IsRecipeRunning(ctx, other) && d.GetPending(other) != "launching" {
			continue
		}
		if !d.IsReady(other) {
			loading += rec.MemoryGB
		}
		running = append(running, other)
	}
	if rec, ok := d.Reg.Get(slug); ok {
		need = rec.MemoryGB
	}
	return need, AvailableMemoryGB("MemAvailable") - loading, running
}

// IsRecipeRunning reports whether the recipe's compose project has a running
// container; one cached `docker ps` answers this for every recipe.
func (d *Docker) IsRecipeRunning(ctx context.Context, slug string) bool {
	projects := d.dockerLines(ctx, "running_projects",
		"ps", "--format", `{{.Label "com.docker.compose.project"}}`)
	want := d.ComposeProject(slug)
	for _, p := range projects {
		if p == want {
			return true
		}
	}
	return false
}
