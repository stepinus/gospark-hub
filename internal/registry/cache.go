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
// docker-compose.yml changes. Roots are merged in order, so a later root
// shadows a slug from an earlier one. The zero value is not usable: use
// NewCache.
type Cache struct {
	roots     []string
	localRoot string
	mu        sync.RWMutex
	recipes   map[string]Recipe
	dirs      map[string]string
	origins   map[string]string
	modTime   time.Time
}

// NewCache creates a recipe cache over the given recipe directories. They are
// read in order; the last root that contains a slug wins. An empty list is
// valid and means "no recipes".
func NewCache(roots ...string) *Cache {
	c := &Cache{roots: append([]string(nil), roots...)}
	// The local/ root is the only root that is not a vendored clone. It is
	// identified by its name so IsLocal stays correct when local/ does not
	// exist yet (Roots() then omits it).
	for _, root := range c.roots {
		if filepath.Base(filepath.Clean(root)) == "local" {
			c.localRoot = filepath.Clean(root)
		}
	}
	return c
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

// Origin returns the source checkout root and the recipe's on-disk
// directory. The root is the git repository the recipe belongs to (the
// nearest ancestor with .git), which is what `git show HEAD:<rel>` needs; for
// a local recipe without a checkout it falls back to the registry root. It is
// used where the caller needs the source checkout rather than a fixed number
// of parent directories.
func (c *Cache) Origin(slug string) (root, dir string, ok bool) {
	if err := c.ensure(); err != nil {
		return "", "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	dir, ok = c.dirs[slug]
	if !ok {
		return "", "", false
	}
	return sourceRoot(dir, c.origins[slug]), dir, true
}

// sourceRoot walks up from the recipe directory to the checkout that owns it
// and falls back to the registry root when there is no .git above.
func sourceRoot(dir, fallback string) string {
	for p := dir; ; {
		if info, err := os.Stat(filepath.Join(p, ".git")); err == nil && info != nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return fallback
		}
		p = parent
	}
}

// IsLocal reports whether the winning recipe lives under local/.
func (c *Cache) IsLocal(slug string) bool {
	if err := c.ensure(); err != nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	root, ok := c.origins[slug]
	return ok && c.localRoot != "" && filepath.Clean(root) == c.localRoot
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
	var max time.Time
	for _, root := range c.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return time.Time{}, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			for _, fn := range []string{"recipe.yaml", "docker-compose.yml"} {
				st, err := os.Stat(filepath.Join(dir, fn))
				if err == nil && st.ModTime().After(max) {
					max = st.ModTime()
				}
			}
		}
	}
	return max, nil
}

func (c *Cache) loadLocked() error {
	recipes := make(map[string]Recipe)
	dirs := make(map[string]string)
	origins := make(map[string]string)

	for _, root := range c.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read registry path %s: %w", root, err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dirPath := filepath.Join(root, e.Name())
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
			// Later roots shadow earlier ones (vendored order, then local).
			recipes[recipe.Slug] = recipe
			dirs[recipe.Slug] = dirPath
			origins[recipe.Slug] = root
		}
	}

	c.recipes = recipes
	c.dirs = dirs
	c.origins = origins
	log.Printf("[registry] Loaded %d recipes", len(recipes))
	return nil
}
