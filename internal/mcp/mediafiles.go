package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/image/draw"

	"gospark-hub/internal/store"
)

// encodeBase64 is the standard base64 used for inline previews and icon data.
func encodeBase64(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

// previewEdge is upstream image_service.PREVIEW_EDGE.
const previewEdge = 512

// maxInputBytes is upstream image_service.MAX_INPUT_BYTES.
const maxInputBytes = 20 * 1024 * 1024

// mediaNameRE mirrors media_store._URL_NAME_RE, plus the "/uploads/" segment
// the file routes use for uploaded files.
var mediaNameRE = regexp.MustCompile(`/(?:images|videos|audio|meshes)/(?:uploads/)?([0-9a-f]{32}\.(?:png|mp4|wav|glb))(?:$|[?#])`)

// nameInURL returns "<id>.<ext>" for a Hub media URL, else "".
func nameInURL(raw string) string {
	m := mediaNameRE.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return ""
	}
	return m[1]
}

// mediaTypes mirrors media_store.MEDIA_TYPES.
var mediaTypes = map[string]string{
	".png": "image/png", ".mp4": "video/mp4", ".wav": "audio/wav", ".glb": "model/gltf-binary",
}

// isUploadPath reports whether path sits in an "uploads" subfolder, which
// decides the artifact cache header (upstream upload_service.is_upload).
func isUploadPath(path string) bool {
	return filepath.Base(filepath.Dir(path)) == "uploads"
}

// imageResult is upstream image_service.Result.
type imageResult struct {
	ImageID     string
	Model       string
	Width       int
	Height      int
	Seed        *int
	PreviewJPEG string
	Steps       *int
	Preset      string
}

// path is the public artifact path.
func (r *imageResult) path() string { return "/images/" + r.ImageID + ".png" }

// storeImage writes `raw` as data/images/<id>.png and builds the inline preview,
// mirroring image_service.store.
func (s *Server) storeImage(raw []byte, model string, seed *int) (*imageResult, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, imageErrorf("Not a readable image: %v", err)
	}
	dir := filepath.Join(s.cfg.DataDir, "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	id := newJobID()
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	preview, err := previewJPEG(img)
	if err != nil {
		return nil, err
	}

	f, err := os.Create(filepath.Join(dir, id+".png"))
	if err != nil {
		return nil, err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &imageResult{
		ImageID: id, Model: model, Width: width, Height: height,
		Seed: seed, PreviewJPEG: preview,
	}, nil
}

// previewJPEG scales img to the preview edge and encodes it as JPEG, on white
// when the picture has transparency.
func previewJPEG(img image.Image) (string, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	scale := 1.0
	if w > previewEdge || h > previewEdge {
		if w >= h {
			scale = float64(previewEdge) / float64(w)
		} else {
			scale = float64(previewEdge) / float64(h)
		}
	}
	tw, th := int(float64(w)*scale+0.5), int(float64(h)*scale+0.5)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	canvas := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(canvas, canvas.Bounds(), img, img.Bounds(), draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	return encodeBase64(buf.Bytes()), nil
}

// toPNG re-encodes any readable image as PNG (upstream image_service.to_png).
func toPNG(raw []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, imageErrorf("Not a readable image: %v", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// loadInput returns the bytes behind a tool's URL argument: a Hub file (only
// for its owner) or a public http(s) URL. Mirrors image_service.load_input.
func (s *Server) loadInput(ctx context.Context, ref string, user *store.User, suffix string, maxBytes int) ([]byte, error) {
	noun := map[string]string{".mp4": "video", ".wav": "sound"}[suffix]
	if noun == "" {
		noun = "image"
	}
	ref = strings.TrimSpace(ref)
	if name := nameInURL(ref); name != "" && strings.HasSuffix(name, suffix) {
		path := s.media.Find(name)
		if path != "" && s.media.CanRead(name, user) {
			return os.ReadFile(path)
		}
		return nil, imageErrorf("No Hub %s %s for this account (it may have been deleted).", noun, name)
	}

	if strings.HasPrefix(ref, "data:") {
		return nil, imageErrorf("Inline file data isn't accepted: tools take URLs. Call create_upload, " +
			"upload the file with the curl command it gives you, and pass its url.")
	}

	parts, err := url.Parse(ref)
	if err == nil && (parts.Scheme == "http" || parts.Scheme == "https") && parts.Hostname() != "" {
		port := 80
		if parts.Scheme == "https" {
			port = 443
		}
		if parts.Port() != "" {
			fmt.Sscanf(parts.Port(), "%d", &port)
		}
		if reach(parts.Hostname(), port, user) == "" {
			return nil, imageErrorf("Only public http(s) %s URLs can be fetched.", noun)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			return nil, imageErrorf("Fetching %s failed (%v).", ref, err)
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return nil, imageErrorf("Fetching %s failed (%v).", ref, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, imageErrorf("Fetching %s failed (HTTP %d).", ref, resp.StatusCode)
		}
		body, err := readCapped(resp.Body, maxBytes)
		if err != nil {
			return nil, err
		}
		if len(body) > maxBytes {
			return nil, imageErrorf("Input %s is larger than %d MB.", noun, maxBytes/(1<<20))
		}
		return body, nil
	}

	return nil, imageErrorf("%ss must be URLs: one returned by the Hub's tools, an "+
		"upload's url (create_upload), or a public http(s) URL. File paths do not "+
		"work: upload the file first.", strings.ToUpper(noun[:1])+noun[1:])
}

// readCapped reads at most limit+1 bytes, so callers can tell the body ran over.
func readCapped(r io.Reader, limit int) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, r, int64(limit)+1); err != nil && err != io.EOF {
		return nil, err
	}
	return buf.Bytes(), nil
}

// reach reports where fetching host:port for user lands: "public", "lan", or
// "" when refused. Everyone reaches the public internet; only an admin reaches
// private addresses; nobody reaches loopback or the Spark itself.
func reach(host string, port int, user *store.User) string {
	addrs, err := net.LookupIP(host)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	public := true
	for _, ip := range addrs {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return ""
		}
		if ip.IsPrivate() {
			public = false
		}
	}
	if public {
		return "public"
	}
	if user == nil || user.Role != "admin" {
		return ""
	}
	return "lan"
}
