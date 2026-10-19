package dockerx

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseComposeImages returns the `image:` of every service in a recipe's
// docker-compose.yml (services without one, e.g. pure-build services, are
// skipped). A missing or unparsable file yields no images.
func ParseComposeImages(recipeDir string) []string {
	data, err := os.ReadFile(filepath.Join(recipeDir, "docker-compose.yml"))
	if err != nil {
		return nil
	}
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	var images []string
	for _, svc := range doc.Services {
		if svc.Image != "" {
			images = append(images, svc.Image)
		}
	}
	return images
}

// imageRef normalizes a compose image reference the way docker resolves it:
// a missing tag means :latest.
func imageRef(image string) string {
	last := image
	if i := strings.LastIndex(image, "/"); i >= 0 {
		last = image[i+1:]
	}
	if strings.Contains(last, ":") {
		return image
	}
	return image + ":latest"
}

// LocalImage returns the first image a recipe's compose declares that already
// exists on this machine. Recipes bake weights into their image, so a present
// image is a finished artifact: rebuilding it would re-download gigabytes and
// change nothing. Empty second value means "nothing local to reuse".
func (d *Docker) LocalImage(ctx context.Context, slug string) (string, bool) {
	dir, ok := d.RecipeDir(slug)
	if !ok {
		return "", false
	}
	for _, img := range ParseComposeImages(dir) {
		if img == "" {
			continue
		}
		_, _, code := d.Client.Output(ctx, "image", "inspect", imageRef(img))
		if code == 0 {
			return imageRef(img), true
		}
	}
	return "", false
}
