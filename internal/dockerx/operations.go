package dockerx

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// InstallRecipe acquires a recipe's artifact (`compose build` for build-type
// recipes, `compose pull` otherwise), prunes the build cache and orphaned
// images, and records the install. Every progress line goes to onLine; the
// error return is only for unexpected failures (the router turns it into an
// `[error] …` log line).
func (d *Docker) InstallRecipe(ctx context.Context, slug, apiKey, hubURL string, onLine func(string)) error {
	dir, ok := d.RecipeDir(slug)
	if !ok {
		onLine(fmt.Sprintf("[error] Recipe directory not found for %s", slug))
		return nil
	}
	if !fileExists(d.composeFile(dir)) {
		onLine(fmt.Sprintf("[error] docker-compose.yml not found in %s", dir))
		return nil
	}
	envFile, created, err := EnsureRuntimeEnv(dir)
	if err != nil {
		onLine(fmt.Sprintf("[error] Failed to prepare runtime env for %s", slug))
		return nil
	}
	if fileExists(dir+"/.env.example") && envFile == "" {
		onLine(fmt.Sprintf("[error] Failed to prepare runtime env for %s", slug))
		return nil
	}
	if created && envFile != "" {
		onLine("[spark-ai-hub] Generated runtime config at " + envFile)
	}

	recipe, _ := d.Reg.Get(slug)
	buildRecipe := recipe.Docker.Build
	onLine(fmt.Sprintf("[spark-ai-hub] Starting install for %s...", slug))

	sub := "pull"
	if buildRecipe {
		sub = "build"
	}
	args := append(d.composeArgs(slug, dir), sub)
	onLine("[spark-ai-hub] Running: " + strings.Join(append([]string{"docker"}, args...), " "))

	rc, err := d.Client.Stream(ctx, dir, d.launchEnv(apiKey, hubURL), true, onLine, args...)
	if err != nil {
		return err
	}
	if rc != 0 {
		onLine(fmt.Sprintf("[spark-ai-hub] Install failed with exit code %d", rc))
		return nil
	}

	if buildRecipe {
		onLine("[spark-ai-hub] Pruning build cache (the image now holds the weights)...")
		onLine("[spark-ai-hub] Build cache " + d.pruneBuildCache(ctx))
	}
	if err := d.DB.UpsertInstalledRecipe(ctx, slug, d.ComposeProject(slug)); err != nil {
		return err
	}
	onLine("[spark-ai-hub] Pruning orphaned images... " + d.pruneOrphanedImages(ctx))
	onLine(fmt.Sprintf("[spark-ai-hub] %s installed successfully!", slug))
	return nil
}

// UpdateRecipe pulls or rebuilds the recipe in place. Unlike install it does
// not touch installed_recipes (the row already exists).
func (d *Docker) UpdateRecipe(ctx context.Context, slug, apiKey, hubURL string, onLine func(string)) error {
	dir, ok := d.RecipeDir(slug)
	if !ok {
		onLine(fmt.Sprintf("[error] Recipe directory not found for %s", slug))
		return nil
	}
	if !fileExists(d.composeFile(dir)) {
		onLine(fmt.Sprintf("[error] docker-compose.yml not found in %s", dir))
		return nil
	}
	envFile, created, err := EnsureRuntimeEnv(dir)
	if err != nil {
		onLine(fmt.Sprintf("[error] Failed to prepare runtime env for %s", slug))
		return nil
	}
	if fileExists(dir+"/.env.example") && envFile == "" {
		onLine(fmt.Sprintf("[error] Failed to prepare runtime env for %s", slug))
		return nil
	}
	if created && envFile != "" {
		onLine("[spark-ai-hub] Generated runtime config at " + envFile)
	}

	recipe, _ := d.Reg.Get(slug)
	env := d.launchEnv(apiKey, hubURL)

	if recipe.Docker.Build {
		onLine(fmt.Sprintf("[spark-ai-hub] Rebuilding local image for %s...", slug))
		upArgs := append(d.composeArgs(slug, dir), "up", "-d", "--build")
		onLine("[spark-ai-hub] Running: " + strings.Join(append([]string{"docker"}, upArgs...), " "))
		rc, err := d.Client.Stream(ctx, dir, env, true, onLine, upArgs...)
		if err != nil {
			return err
		}
		if rc != 0 {
			onLine(fmt.Sprintf("[spark-ai-hub] Rebuild failed with exit code %d", rc))
			return nil
		}
		onLine("[spark-ai-hub] Pruning build cache (the image now holds the weights)...")
		onLine("[spark-ai-hub] Build cache " + d.pruneBuildCache(ctx))
		onLine("[spark-ai-hub] Pruning orphaned images... " + d.pruneOrphanedImages(ctx))
		onLine(fmt.Sprintf("[spark-ai-hub] %s rebuilt successfully!", slug))
		return nil
	}

	onLine(fmt.Sprintf("[spark-ai-hub] Pulling latest images for %s...", slug))
	pullArgs := append(d.composeArgs(slug, dir), "pull")
	onLine("[spark-ai-hub] Running: " + strings.Join(append([]string{"docker"}, pullArgs...), " "))
	rc, err := d.Client.Stream(ctx, dir, env, true, onLine, pullArgs...)
	if err != nil {
		return err
	}
	if rc != 0 {
		onLine(fmt.Sprintf("[spark-ai-hub] Pull failed with exit code %d", rc))
		return nil
	}

	onLine(fmt.Sprintf("[spark-ai-hub] Recreating containers for %s...", slug))
	upArgs := append(d.composeArgs(slug, dir), "up", "-d")
	onLine("[spark-ai-hub] Running: " + strings.Join(append([]string{"docker"}, upArgs...), " "))
	rc, err = d.Client.Stream(ctx, dir, env, true, onLine, upArgs...)
	if err != nil {
		return err
	}
	if rc == 0 {
		onLine("[spark-ai-hub] Pruning orphaned images... " + d.pruneOrphanedImages(ctx))
		onLine(fmt.Sprintf("[spark-ai-hub] %s updated successfully!", slug))
	} else {
		onLine(fmt.Sprintf("[spark-ai-hub] Update failed with exit code %d", rc))
	}
	return nil
}

// LaunchRecipe runs `compose up -d`. It returns "launched" on success, or the
// raw compose output (or a precondition message) on failure — the router maps
// anything but "launched" to HTTP 500 with that text as `detail`. When onLine
// is set every output line is mirrored into the build log the UI streams.
func (d *Docker) LaunchRecipe(ctx context.Context, slug, apiKey, hubURL string, onLine func(string)) (string, error) {
	dir, ok := d.RecipeDir(slug)
	if !ok {
		return fmt.Sprintf("Recipe directory not found for %s", slug), nil
	}
	envFile, _, err := EnsureRuntimeEnv(dir)
	if err != nil {
		return fmt.Sprintf("Failed to prepare runtime env for %s", slug), nil
	}
	if fileExists(dir+"/.env.example") && envFile == "" {
		return fmt.Sprintf("Failed to prepare runtime env for %s", slug), nil
	}

	// The shared app network has to exist before compose attaches to it.
	d.EnsureNetwork(ctx)

	args := append(d.composeArgs(slug, dir), "up", "-d")
	var full strings.Builder
	rc, err := d.Client.Stream(ctx, dir, d.launchEnv(apiKey, hubURL), false, func(line string) {
		full.WriteString(line)
		full.WriteByte('\n')
		if onLine != nil {
			onLine(line)
		}
	}, args...)
	d.invalidateLists()
	if err != nil {
		return "", err
	}
	if rc == 0 {
		if err := d.DB.UpsertInstalledRecipe(ctx, slug, d.ComposeProject(slug)); err != nil {
			return "", err
		}
		text := full.String()
		if strings.Contains(text, "Built") || strings.Contains(text, "DONE") {
			d.pruneBuildCache(ctx)
		}
		return "launched", nil
	}
	return full.String(), nil
}

// StopRecipe runs `compose down` (containers and network go, images and named
// volumes stay) and returns "stopped" or "failed".
func (d *Docker) StopRecipe(ctx context.Context, slug string) (string, error) {
	dir, ok := d.RecipeDir(slug)
	if !ok {
		return fmt.Sprintf("Recipe directory not found for %s", slug), nil
	}
	args := append(d.composeArgs(slug, dir), "down")
	_, _, code := d.Client.Output(ctx, args...)
	d.invalidateLists()
	if code == 0 {
		return "stopped", nil
	}
	return "failed", nil
}

// RemoveRecipe uninstalls a recipe: `compose down --volumes`, then only the
// images no other installed recipe shares, the project's volumes, the build
// cache and finally the installed_recipes row. A failed `down` keeps the row.
func (d *Docker) RemoveRecipe(ctx context.Context, slug string) (string, error) {
	dir, ok := d.RecipeDir(slug)
	if !ok {
		return fmt.Sprintf("Recipe directory not found for %s", slug), nil
	}

	own := toSet(ParseComposeImages(dir))
	shared := map[string]struct{}{}
	installed, err := d.InstalledSlugs(ctx)
	if err != nil {
		return "", err
	}
	for _, other := range installed {
		if other == slug {
			continue
		}
		otherDir, ok := d.RecipeDir(other)
		if !ok {
			continue
		}
		for _, img := range ParseComposeImages(otherDir) {
			shared[img] = struct{}{}
		}
	}
	var removable []string
	for img := range own {
		if _, isShared := shared[img]; !isShared {
			removable = append(removable, img)
		}
	}

	args := append(d.composeArgs(slug, dir), "down", "--volumes")
	_, _, code := d.Client.Output(ctx, args...)
	d.invalidateLists()
	if code != 0 {
		return "failed", nil
	}

	for _, img := range removable {
		d.Client.RMI(ctx, "-f", img)
	}
	for _, vol := range d.findProjectVolumes(ctx, slug) {
		d.Client.VolumeRM(ctx, "-f", vol)
	}
	d.pruneBuildCache(ctx)
	if err := d.DB.DeleteInstalledRecipe(ctx, slug); err != nil {
		return "", err
	}
	d.pruneOrphanedImages(ctx)
	return "removed", nil
}

// PurgeRecipe is the separate "get rid of the leftovers" action: it removes
// the recipe's project volumes and its unused images and sweeps orphaned
// images, but never touches pending/ready state (and not the build cache).
func (d *Docker) PurgeRecipe(ctx context.Context, slug string) (string, error) {
	var errs []string
	d.invalidateLists()

	for _, vol := range d.findProjectVolumes(ctx, slug) {
		stderr, code := d.Client.VolumeRM(ctx, "-f", vol)
		if code != 0 {
			msg := strings.TrimSpace(stderr)
			if msg != "" && !strings.Contains(msg, "No such volume") {
				errs = append(errs, msg)
			}
		}
	}
	for _, img := range d.findProjectImages(ctx, slug) {
		stderr, code := d.Client.RMI(ctx, "-f", img)
		if code != 0 {
			msg := strings.TrimSpace(stderr)
			if msg != "" && !strings.Contains(msg, "No such image") {
				errs = append(errs, msg)
			}
		}
	}
	d.pruneOrphanedImages(ctx)
	if len(errs) > 0 {
		return "partial: " + strings.Join(errs, "; "), nil
	}
	return "purged", nil
}

// EnsureNetwork creates the shared external network if it is missing. It is
// scoped to cfg.Project, so a test Hub cannot touch the production network.
func (d *Docker) EnsureNetwork(ctx context.Context) {
	_, _, code := d.Client.Output(ctx, "network", "inspect", d.Network())
	if code == 0 {
		return
	}
	if _, err := d.Client.Run(ctx, "network", "create", d.Network()); err != nil {
		d.Log.Warn("could not create docker network", "network", d.Network(), "error", err)
	}
}

// ── Volumes and images (all scoped to cfg.Project) ──────────────────────────

// findProjectVolumes matches volumes whose name starts with this recipe's
// project prefix. A slug ending in a variant segment (`…-dflash`) also matches
// its base recipe's volumes, unless that base is itself a real recipe.
func (d *Docker) findProjectVolumes(ctx context.Context, slug string) []string {
	prefixes := map[string]struct{}{d.ComposeProject(slug) + "_": {}}
	// A slug ending in a variant segment (`…-dflash`) also matches its base
	// recipe's volumes, unless that base is a real recipe of its own.
	if idx := strings.LastIndex(slug, "-"); idx > 0 {
		base := slug[:idx]
		if _, isRecipe := d.Reg.Get(base); !isRecipe {
			prefixes[d.Config.AppProjectPrefix+"-"+base+"_"] = struct{}{}
		}
	}

	volumes := d.dockerLines(ctx, "volumes", "volume", "ls", "-q")
	var matched []string
	for _, v := range volumes {
		for p := range prefixes {
			if strings.HasPrefix(v, p) {
				matched = append(matched, v)
				break
			}
		}
	}
	return matched
}

// findProjectImages returns the recipe's compose images that exist locally and
// are not in use by any running container. Only images belonging to
// cfg.Project are considered: the upstream sweeps every image on the box,
// which must never touch a production Hub's store.
func (d *Docker) findProjectImages(ctx context.Context, slug string) []string {
	dir, ok := d.RecipeDir(slug)
	if !ok {
		return nil
	}
	composeImages := ParseComposeImages(dir)
	if len(composeImages) == 0 {
		return nil
	}
	inUse := toSet(d.dockerLines(ctx, "ps_images", "ps", "--format", "{{.Image}}"))
	present := toSet(d.dockerLines(ctx, "images", "images", "--format", "{{.Repository}}:{{.Tag}}"))

	var matched []string
	for _, img := range composeImages {
		// No ownership filter here: the image list comes from the recipe's own
		// compose file, so it IS the app's artifact. Upstream deletes exactly
		// these on remove/purge, which is what makes uninstall total.
		if _, ok := present[imageRef(img)]; !ok {
			continue
		}
		if _, used := inUse[img]; used {
			continue
		}
		matched = append(matched, img)
	}
	return matched
}

// isOwnImage reports whether an image reference belongs to this Hub's project.
// Compose-built images are tagged `<project>-<service>` / `<project>_<service>`;
// pulled base images are not ours and are left alone.
func (d *Docker) isOwnImage(image string) bool {
	return strings.HasPrefix(image, d.Config.AppProjectPrefix+"-") ||
		strings.HasPrefix(image, d.Config.Project+"_") ||
		image == d.Config.Project
}

// HasLeftovers implements the recipes route package's state dependency: true
// when a project volume or an own image for the slug still exists.
func (d *Docker) HasLeftovers(ctx context.Context, slug string) (bool, error) {
	if len(d.findProjectVolumes(ctx, slug)) > 0 {
		return true, nil
	}
	if len(d.findProjectImages(ctx, slug)) > 0 {
		return true, nil
	}
	return false, nil
}

// pruneBuildCache drops BuildKit's copy of a finished build, so removing an
// app really frees the weights. Returns the "Total:" line upstream logs.
func (d *Docker) pruneBuildCache(ctx context.Context) string {
	out, _, code := d.Client.Output(ctx, "builder", "prune", "-a", "-f")
	if code != 0 {
		return "reclaimed 0B"
	}
	lines := nonEmptyLines(strings.TrimRight(out, "\n"))
	for _, line := range slices.Backward(lines) {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "total:") {
			return strings.TrimSpace(line)
		}
	}
	return "reclaimed 0B"
}

// pruneOrphanedImages removes this project's own images that no installed
// recipe or existing container references, then sweeps plain dangling layers.
func (d *Docker) pruneOrphanedImages(ctx context.Context) string {
	keep := map[string]struct{}{}
	installed, _ := d.InstalledSlugs(ctx)
	for _, slug := range installed {
		dir, ok := d.RecipeDir(slug)
		if !ok {
			continue
		}
		for _, img := range ParseComposeImages(dir) {
			keep[img] = struct{}{}
		}
	}
	for _, img := range d.dockerLines(ctx, "ps_images", "ps", "--format", "{{.Image}}") {
		keep[img] = struct{}{}
	}

	all := d.dockerLines(ctx, "images", "images", "--format", "{{.Repository}}:{{.Tag}}")
	removed := 0
	for _, img := range all {
		if strings.Contains(img, "<none>") || !d.isOwnImage(img) {
			continue
		}
		if _, kept := keep[img]; kept {
			continue
		}
		if _, kept := keep[strings.TrimSuffix(img, ":latest")]; kept {
			continue
		}
		if _, code := d.Client.RMI(ctx, "-f", img); code == 0 {
			removed++
		}
	}

	out, _, _ := d.Client.Output(ctx, "image", "prune", "-f")
	dangling := "reclaimed 0B"
	lines := nonEmptyLines(strings.TrimRight(out, "\n"))
	for _, line := range slices.Backward(lines) {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "total reclaimed space:") {
			dangling = strings.TrimSpace(line)
			break
		}
	}
	return fmt.Sprintf("removed %d orphaned image(s); dangling layers: %s", removed, dangling)
}

func toSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}
