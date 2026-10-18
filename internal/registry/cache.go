package registry

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Cache holds parsed recipes in memory and reloads when any recipe.yaml or
// docker-compose.yml changes. The zero value is not usable: use NewCache.
type Cache struct {
	path    string
	mu      sync.RWMutex
	recipes map[string]Recipe
	dirs    map[string]string
	modTime time.Time
}

// NewCache creates a recipe cache for the given registry/recipes directory.
func NewCache(path string) *Cache {
	return &Cache{path: path}
}

// Load forces an immediate reload from disk.
func (c *Cache) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked()
}

// All returns all recipes in deterministic (slug-sorted) order.
func (c *Cache) All() ([]Recipe, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.recipes))
	for k := range c.recipes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Recipe, len(keys))
	for i, k := range keys {
		out[i] = c.recipes[k]
	}
	return out, nil
}

// Get returns a copy of the recipe with the given slug.
func (c *Cache) Get(slug string) (Recipe, bool) {
	if err := c.ensure(); err != nil {
		return Recipe{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.recipes[slug]
	return r, ok
}

// Dir returns the on-disk directory for a recipe slug.
func (c *Cache) Dir(slug string) (string, bool) {
	if err := c.ensure(); err != nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.dirs[slug]
	return d, ok
}

func (c *Cache) ensure() error {
	maxMod, err := c.maxModTime()
	if err != nil {
		return err
	}
	c.mu.RLock()
	need := c.modTime.IsZero() || maxMod.After(c.modTime)
	c.mu.RUnlock()
	if !need {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.modTime.IsZero() && !maxMod.After(c.modTime) {
		return nil
	}
	if err := c.loadLocked(); err != nil {
		return err
	}
	c.modTime = maxMod
	return nil
}

func (c *Cache) maxModTime() (time.Time, error) {
	entries, err := os.ReadDir(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var max time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(c.path, e.Name())
		for _, fn := range []string{"recipe.yaml", "docker-compose.yml"} {
			st, err := os.Stat(filepath.Join(dir, fn))
			if err == nil && st.ModTime().After(max) {
				max = st.ModTime()
			}
		}
	}
	return max, nil
}

func (c *Cache) loadLocked() error {
	recipes := make(map[string]Recipe)
	dirs := make(map[string]string)

	entries, err := os.ReadDir(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			c.recipes = recipes
			c.dirs = dirs
			return nil
		}
		return fmt.Errorf("read registry path: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirPath := filepath.Join(c.path, e.Name())
		yamlPath := filepath.Join(dirPath, "recipe.yaml")
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			continue
		}
		recipe, err := parseRecipe(data, dirPath)
		if err != nil {
			log.Printf("[registry] Failed to load %s: %v", yamlPath, err)
			continue
		}
		recipes[recipe.Slug] = recipe
		dirs[recipe.Slug] = dirPath
	}

	c.recipes = recipes
	c.dirs = dirs
	log.Printf("[registry] Loaded %d recipes", len(recipes))
	return nil
}
