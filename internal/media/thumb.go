package media

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"sync"

	"golang.org/x/image/draw"
)

const thumbEdge = 320

// ThumbCache is an LRU cache of generated JPEG thumbnails.
type ThumbCache struct {
	mu    sync.Mutex
	order []string
	data  map[string][]byte
	max   int
}

// NewThumbCache creates a new thumbnail LRU cache.
func NewThumbCache(max int) *ThumbCache {
	return &ThumbCache{data: map[string][]byte{}, max: max}
}

func (c *ThumbCache) Get(name string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[name]
	if !ok {
		return nil, false
	}
	// move to front
	for i, n := range c.order {
		if n == name {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, name)
	return b, true
}

func (c *ThumbCache) Set(name string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[name]; !ok {
		c.order = append(c.order, name)
	}
	c.data[name] = b
	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.data, oldest)
	}
}

func (c *ThumbCache) Delete(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, name)
	for i, n := range c.order {
		if n == name {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// MakeThumb generates a small JPEG thumbnail for an image path.
func MakeThumb(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return thumbJPEG(img)
}

func thumbJPEG(img image.Image) ([]byte, error) {
	// Flatten onto light grey so transparent pixels do not pick up a random background.
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, image.NewUniform(color.RGBA{236, 236, 236, 255}), image.Point{}, draw.Src)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Over)

	dx, dy := bounds.Dx(), bounds.Dy()
	if dx > thumbEdge || dy > thumbEdge {
		if dx > dy {
			dy = dy * thumbEdge / dx
			dx = thumbEdge
		} else {
			dx = dx * thumbEdge / dy
			dy = thumbEdge
		}
		dst := image.NewRGBA(image.Rect(0, 0, dx, dy))
		draw.BiLinear.Scale(dst, dst.Bounds(), rgba, bounds, draw.Src, nil)
		rgba = dst
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SaveRender saves a browser-rendered PNG still for a 3D model.
func SaveRender(path string, raw []byte) error {
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if max(img.Bounds().Dx(), img.Bounds().Dy()) > 2048 {
		return os.ErrInvalid
	}
	f, err := os.Create(renderOf(path))
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
