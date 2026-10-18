package media

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// MediaStore owns file scanning, ownership and metadata.
type MediaStore struct {
	cfg     config.Config
	db      *store.DB
	shapes  *shapeCache
	mu      sync.Mutex
}

// NewStore builds a MediaStore.
func NewStore(cfg config.Config, db *store.DB) *MediaStore {
	return &MediaStore{cfg: cfg, db: db, shapes: newShapeCache()}
}

// location describes one on-disk store.
type location struct {
	folder   string
	pattern  *regexp.Regexp
	kind     string
	prefix   string
	upload   bool
}

func (s *MediaStore) locations() []location {
	return []location{
		{folder: filepath.Join(s.cfg.DataDir, "images"), pattern: regexp.MustCompile(`^([0-9a-f]{32})\.png$`), kind: "image", prefix: "/images", upload: false},
		{folder: filepath.Join(s.cfg.DataDir, "images", "uploads"), pattern: regexp.MustCompile(`^([0-9a-f]{32})\.png$`), kind: "image", prefix: "/images", upload: true},
		{folder: filepath.Join(s.cfg.DataDir, "videos"), pattern: regexp.MustCompile(`^([0-9a-f]{32})\.mp4$`), kind: "video", prefix: "/videos", upload: false},
		{folder: filepath.Join(s.cfg.DataDir, "videos", "uploads"), pattern: regexp.MustCompile(`^([0-9a-f]{32})\.mp4$`), kind: "video", prefix: "/videos", upload: true},
		{folder: filepath.Join(s.cfg.DataDir, "audio"), pattern: regexp.MustCompile(`^([0-9a-f]{32})\.wav$`), kind: "audio", prefix: "/audio", upload: false},
		{folder: filepath.Join(s.cfg.DataDir, "audio", "uploads"), pattern: regexp.MustCompile(`^([0-9a-f]{32})\.wav$`), kind: "audio", prefix: "/audio", upload: true},
		{folder: filepath.Join(s.cfg.DataDir, "meshes"), pattern: regexp.MustCompile(`^([0-9a-f]{32})\.glb$`), kind: "mesh", prefix: "/meshes", upload: false},
	}
}

// Record notes that userID made a file. A zero userID is ignored.
func (s *MediaStore) Record(name string, userID int64, model string, seconds *float64) error {
	if userID == 0 {
		return nil
	}
	return s.db.RecordMedia(name, userID, model, seconds)
}

// CanRead reports whether user may read the named file.
func (s *MediaStore) CanRead(name string, user *store.User) bool {
	if !s.cfg.AuthEnabled {
		return true
	}
	if user == nil {
		return false
	}
	owner, ok, _ := s.db.MediaOwner(name)
	if !ok {
		return user.Role == "admin"
	}
	return owner == user.ID
}

func (s *MediaStore) visible(owner sql.NullInt64, user *store.User) bool {
	if !s.cfg.AuthEnabled {
		return true
	}
	if user == nil {
		return false
	}
	if owner.Valid {
		return owner.Int64 == user.ID
	}
	return user.Role == "admin"
}

// entry is a file on disk before merging with DB metadata.
type entry struct {
	Name    string
	Kind    string
	Upload  bool
	Path    string
	Bytes   int64
	Created int64
	File    string
}

func (s *MediaStore) entries() []entry {
	var out []entry
	for _, loc := range s.locations() {
		files, err := os.ReadDir(loc.folder)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if !loc.pattern.MatchString(name) {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			out = append(out, entry{
				Name:    name,
				Kind:    loc.kind,
				Upload:  loc.upload,
				Path:    loc.prefix + "/" + name,
				Bytes:   info.Size(),
				Created: info.ModTime().Unix(),
				File:    filepath.Join(loc.folder, name),
			})
		}
	}
	return out
}

// modelFields returns the display name and orbit flag for a recipe slug.
func modelFields(reg *registry.Cache, slug string) (string, bool) {
	if slug == "" {
		return "", false
	}
	if reg == nil {
		return slug, false
	}
	rec, ok := reg.Get(slug)
	if !ok {
		return slug, false
	}
	orbit := false
	for _, t := range rec.Tags {
		if t == "orbit-360" {
			orbit = true
			break
		}
	}
	return rec.Name, orbit
}

// List returns the files visible to user, newest first.
func (s *MediaStore) List(reg *registry.Cache, user *store.User) ([]map[string]any, error) {
	entries := s.entries()
	rows, err := s.mediaRows()
	if err != nil {
		return nil, err
	}

	files := []map[string]any{}
	for _, e := range entries {
		row := rows[e.Name]
		var owner sql.NullInt64
		if row != nil {
			owner = sql.NullInt64{Int64: row.UserID, Valid: true}
		}
		if !s.visible(owner, user) {
			continue
		}

		modelName, orbit := modelFields(reg, rowModel(row))
		m := map[string]any{
			"name":   e.Name,
			"kind":   e.Kind,
			"upload": e.Upload,
			"path":   e.Path,
			"bytes":  e.Bytes,
			"created": e.Created,
			"model":  modelName,
			"orbit":  orbit,
		}
		if row != nil && row.Seconds.Valid {
			m["seconds"] = round(row.Seconds.Float64, 1)
		}
		if e.Kind == "mesh" {
			m["rendered"] = fileExists(renderOf(e.File))
		}
		shape := s.Shape(e.File, e.Bytes)
		for k, v := range shape {
			m[k] = v
		}
		files = append(files, m)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i]["created"].(int64) > files[j]["created"].(int64)
	})
	return files, nil
}

func rowModel(row *mediaRow) string {
	if row == nil {
		return ""
	}
	return row.Model
}

type mediaRow struct {
	UserID  int64
	Model   string
	Seconds sql.NullFloat64
}

func (s *MediaStore) mediaRows() (map[string]*mediaRow, error) {
	rows, err := s.db.MediaRows()
	if err != nil {
		return nil, err
	}
	out := map[string]*mediaRow{}
	for _, r := range rows {
		out[r.Name] = &mediaRow{UserID: r.UserID, Model: r.Model, Seconds: r.Seconds}
	}
	return out, nil
}

// Find returns the absolute path of a media file, or "" if absent.
func (s *MediaStore) Find(name string) string {
	for _, loc := range s.locations() {
		if loc.pattern.MatchString(name) {
			path := filepath.Join(loc.folder, name)
			if fileExists(path) {
				return path
			}
		}
	}
	return ""
}

// Picture returns the still that represents a file.
func Picture(path string) string {
	if filepath.Ext(path) != ".glb" {
		return path
	}
	render := renderOf(path)
	if fileExists(render) {
		return render
	}
	base := path[:len(path)-len(filepath.Ext(path))]
	return base + ".png"
}

// RenderOf returns the render path for a 3D model.
func renderOf(path string) string {
	base := path[:len(path)-len(filepath.Ext(path))]
	return base + ".render.png"
}

// Delete removes a file and its DB row if user may see it.
func (s *MediaStore) Delete(name string, user *store.User) bool {
	path := s.Find(name)
	if path == "" || !s.CanRead(name, user) {
		return false
	}
	_ = os.Remove(path)
	if filepath.Ext(path) == ".glb" {
		_ = os.Remove(pngOf(path))
		_ = os.Remove(renderOf(path))
	}
	_ = s.db.DeleteMedia(name)
	return true
}

func pngOf(path string) string {
	base := path[:len(path)-len(filepath.Ext(path))]
	return base + ".png"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Shape returns width/height/duration metadata for a file.
func (s *MediaStore) Shape(path string, size int64) map[string]any {
	return s.shapes.get(path, size)
}

type shapeCache struct {
	mu   sync.Mutex
	data map[string]map[string]any
}

func newShapeCache() *shapeCache {
	return &shapeCache{data: map[string]map[string]any{}}
}

func (c *shapeCache) get(path string, size int64) map[string]any {
	key := fmt.Sprintf("%s|%d", path, size)
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.data[key]; ok {
		return v
	}
	shape := computeShape(path)
	c.data[key] = shape
	return shape
}

func computeShape(path string) map[string]any {
	ext := filepath.Ext(path)
	shape := map[string]any{}
	switch ext {
	case ".png":
		f, err := os.Open(path)
		if err != nil {
			return shape
		}
		defer f.Close()
		cfg, _, err := image.DecodeConfig(f)
		if err != nil {
			return shape
		}
		shape["width"] = cfg.Width
		shape["height"] = cfg.Height
	case ".mp4", ".wav":
		ffprobe, err := exec.LookPath("ffprobe")
		if err != nil {
			return shape
		}
		out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=width,height:format=duration",
			"-of", "json", path).Output()
		if err != nil {
			return shape
		}
		var info struct {
			Streams []struct {
				Width  int     `json:"width"`
				Height int     `json:"height"`
			} `json:"streams"`
			Format struct {
				Duration float64 `json:"duration"`
			} `json:"format"`
		}
		if err := json.Unmarshal(out, &info); err != nil {
			return shape
		}
		if len(info.Streams) > 0 && info.Streams[0].Width > 0 {
			shape["width"] = info.Streams[0].Width
			shape["height"] = info.Streams[0].Height
		}
		if info.Format.Duration > 0 {
			shape["duration"] = round(info.Format.Duration, 2)
		}
	}
	return shape
}

func round(v float64, places int) float64 {
	p := mathPow10(places)
	return float64(int64(v*p+0.5)) / p
}

func mathPow10(n int) float64 {
	return map[int]float64{0: 1, 1: 10, 2: 100, 3: 1000, 4: 10000}[n]
}
