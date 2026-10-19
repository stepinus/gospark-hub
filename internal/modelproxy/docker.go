package modelproxy

import (
	"context"
	"os/exec"
	"strings"
)

// DockerRunner shells out to docker ps to discover running compose projects.
// It is used when the dockerx package is not yet available.
type DockerRunner struct {
	Project string
}

// RunningSlugs returns recipe slugs whose compose project label matches
// {Project}-{slug}.
func (d *DockerRunner) RunningSlugs(ctx context.Context) ([]string, error) {
	if d.Project == "" {
		d.Project = "gospark-hub"
	}
	prefix := d.Project + "-"
	cmd := exec.CommandContext(ctx, "docker", "ps", "--format", `{{.Label "com.docker.compose.project"}}`)
	out, err := cmd.Output()
	if err != nil {
		// Docker missing is not fatal: the proxy simply sees no models.
		if _, ok := err.(*exec.ExitError); !ok {
			return nil, nil
		}
		return nil, err
	}
	var slugs []string
	seen := make(map[string]bool)
	for _, line := range strings.Fields(string(out)) {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		slug := strings.TrimPrefix(line, prefix)
		if slug != "" && !seen[slug] {
			seen[slug] = true
			slugs = append(slugs, slug)
		}
	}
	return slugs, nil
}
