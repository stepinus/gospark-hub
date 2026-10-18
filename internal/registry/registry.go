// Package registry loads recipe metadata from disk.
package registry

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
	"gospark-hub/internal/config"
)

// Recipe is the subset of a recipe.yaml that the backend needs right now.
type Recipe struct {
	Slug            string   `yaml:"slug"`
	Name            string   `yaml:"name"`
	RequiresHFToken bool     `yaml:"requires_hf_token"`
	GatedRepos      []string `yaml:"gated_repos"`
}

// Registry keeps parsed recipes in memory.
type Registry struct {
	cfg     config.Config
	recipes map[string]*Recipe
	mu      sync.RWMutex
}

// New creates an empty registry.
func New(cfg config.Config) *Registry {
	return &Registry{cfg: cfg, recipes: make(map[string]*Recipe)}
}

// Load scans RegistryPath for recipe.yaml files and parses them.
func (r *Registry) Load() error {
	path := r.cfg.RegistryPath
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	loaded := make(map[string]*Recipe)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		yamlPath := filepath.Join(path, e.Name(), "recipe.yaml")
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			continue
		}
		var rec Recipe
		if err := yaml.Unmarshal(data, &rec); err != nil {
			log.Printf("[registry] failed to parse %s: %v", yamlPath, err)
			continue
		}
		if rec.Slug == "" {
			rec.Slug = e.Name()
		}
		loaded[rec.Slug] = &rec
	}

	r.mu.Lock()
	r.recipes = loaded
	r.mu.Unlock()
	return nil
}

// GetRecipe returns the recipe for slug, or nil if it is not loaded.
func (r *Registry) GetRecipe(slug string) *Recipe {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.recipes[slug]
}

// Len returns the number of loaded recipes.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.recipes)
}
