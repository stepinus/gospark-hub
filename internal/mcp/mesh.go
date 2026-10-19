package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"

	"gospark-hub/internal/store"
)

// Mesh constants mirror daemon/services/mesh_service.py.
const (
	meshPollSeconds = 3
	meshJobTimeout  = 30 * 60
	cutTolerance    = 35
	edgeStep        = 8
	minBackground   = 0.05
	maxBackground   = 0.97
)

// MeshViews is upstream VIEWS.
var MeshViews = []string{"left", "back", "right"}

type meshBackend struct {
	slug      string
	summary   string
	multiView bool
	cutsOut   bool
}

// meshBackends lists every catalog 3D model (openai-meshes + image-to-3d).
func (s *Server) meshBackends() []meshBackend {
	recipes, err := s.allRecipes()
	if err != nil {
		return nil
	}
	var out []meshBackend
	for _, r := range recipes {
		if !hasTag(r.Tags, "openai-meshes") || !hasTag(r.Tags, "image-to-3d") {
			continue
		}
		out = append(out, meshBackend{slug: r.Slug, summary: r.Name,
			multiView: hasTag(r.Tags, "multi-view"), cutsOut: hasTag(r.Tags, "background-removal")})
	}
	return out
}

func (s *Server) pickMeshBackend(ctx context.Context, model string, multiView bool) (meshBackend, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return meshBackend{}, err
	}
	var candidates []meshBackend
	for _, b := range s.meshBackends() {
		if !installed[b.slug] {
			continue
		}
		if !multiView || b.multiView {
			candidates = append(candidates, b)
		}
	}
	what := "3D model"
	if multiView {
		what = "3D model that takes several views"
	}
	if model != "" {
		found := false
		for _, b := range candidates {
			if b.slug == model {
				candidates, found = []meshBackend{b}, true
				break
			}
		}
		if !found {
			return meshBackend{}, imageErrorf("'%s' is not an installed %s. Installed: %s.",
				model, what, meshSlugList(candidates))
		}
	}
	slugs := make([]string, len(candidates))
	for i, b := range candidates {
		slugs[i] = b.slug
	}
	idx, err := s.pickReady(ctx, slugs, what)
	if err != nil {
		return meshBackend{}, err
	}
	return candidates[idx], nil
}

func meshSlugList(bs []meshBackend) string {
	if len(bs) == 0 {
		return "none installed"
	}
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.slug
	}
	return strings.Join(out, ", ")
}

// meshResult is upstream mesh_service.generate's return value.
type meshResult struct {
	MeshID         string
	Model          string
	Seed           int
	Steps          *int
	Views          []string
	Bytes          int
	InferenceTimeS *float64
	Preview        string
}

// generateMesh is upstream mesh_service.generate.
func (s *Server) generateMesh(ctx context.Context, args map[string]any, user *store.User) (*meshResult, error) {
	imageRef := argString(args, "image")
	if imageRef == "" {
		return nil, imageErrorf("'image' is required: the picture to build the model from.")
	}
	views := map[string]string{}
	if raw, ok := args["views"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				views[k] = strings.TrimSpace(s)
			}
		}
	} else if raw, ok := args["views"]; ok && raw != nil {
		return nil, imageErrorf("'views' must be an object: {\"left\": url, ...}.")
	}
	for k := range views {
		if !contains(MeshViews, k) {
			return nil, imageErrorf("Unknown view %s: views are %s.", k, strings.Join(MeshViews, ", "))
		}
	}
	backend, err := s.pickMeshBackend(ctx, argString(args, "model"), len(views) > 0)
	if err != nil {
		return nil, err
	}
	seed := argInt(args, "seed")
	seedVal := 0
	if seed != nil {
		seedVal = *seed
	} else {
		seedVal = randomSeed()
	}
	steps := argInt(args, "steps")

	base := s.app.InternalURL(backend.slug) + "/v1/meshes"
	raw, err := s.loadInput(ctx, imageRef, user, ".png", maxInputBytes)
	if err != nil {
		return nil, err
	}
	cutout := raw
	if backend.cutsOut {
		cutout, err = toPNG(raw)
	} else {
		cutout, err = cutOut(raw)
	}
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(cutout); err != nil {
		return nil, err
	}
	for name, ref := range views {
		viewRaw, err := s.loadInput(ctx, ref, user, ".png", maxInputBytes)
		if err != nil {
			return nil, err
		}
		view, err := cutOut(viewRaw)
		if err != nil {
			return nil, err
		}
		viewPart, err := writer.CreateFormFile(name, name+".png")
		if err != nil {
			return nil, err
		}
		if _, err := viewPart.Write(view); err != nil {
			return nil, err
		}
	}
	writer.WriteField("seed", fmt.Sprint(seedVal))
	writer.WriteField("output_format", "glb")
	if steps != nil {
		writer.WriteField("num_inference_steps", fmt.Sprint(*steps))
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req, err := s.newAppRequest(ctx, http.MethodPost, base, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, imageErrorf("%s refused the 3D job: %v", backend.slug, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imageErrorf("%s refused the 3D job (HTTP %d): %s",
			backend.slug, resp.StatusCode, truncate(string(body), 500))
	}
	var remote map[string]any
	if err := json.Unmarshal(body, &remote); err != nil {
		return nil, imageErrorf("%s returned an unreadable 3D job.", backend.slug)
	}
	remoteID, _ := remote["id"].(string)
	if remoteID == "" {
		return nil, imageErrorf("%s did not start a 3D job.", backend.slug)
	}

	statusURL := base + "/" + remoteID
	deadline := time.Now().Add(meshJobTimeout * time.Second)
	for {
		state, _ := remote["status"].(string)
		if state == "completed" {
			break
		}
		if state == "failed" {
			errObj, _ := remote["error"].(map[string]any)
			msg, _ := errObj["message"].(string)
			if msg == "" {
				msg = "no detail given"
			}
			return nil, imageErrorf("%s failed to make the 3D model: %s", backend.slug, msg)
		}
		if time.Now().After(deadline) {
			return nil, imageErrorf("%s did not finish the 3D model in time.", backend.slug)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(meshPollSeconds * time.Second):
		}
		pollReq, err := s.newAppRequest(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			return nil, err
		}
		pollResp, err := s.http.Do(pollReq)
		if err != nil {
			return nil, imageErrorf("%s lost the 3D job: %v", backend.slug, err)
		}
		pollBody, _ := io.ReadAll(io.LimitReader(pollResp.Body, 1<<20))
		pollResp.Body.Close()
		if pollResp.StatusCode != http.StatusOK {
			return nil, imageErrorf("%s lost the 3D job (HTTP %d); was it restarted?",
				backend.slug, pollResp.StatusCode)
		}
		_ = json.Unmarshal(pollBody, &remote)
	}

	contentReq, err := s.newAppRequest(ctx, http.MethodGet, statusURL+"/content", nil)
	if err != nil {
		return nil, err
	}
	contentResp, err := s.http.Do(contentReq)
	if err != nil {
		return nil, imageErrorf("Fetching the model from %s failed: %v", backend.slug, err)
	}
	glb, _ := io.ReadAll(contentResp.Body)
	contentResp.Body.Close()
	if contentResp.StatusCode != http.StatusOK {
		return nil, imageErrorf("Fetching the model from %s failed (HTTP %d).", backend.slug, contentResp.StatusCode)
	}

	dir := filepath.Join(s.cfg.DataDir, "meshes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	meshID := newJobID()
	if err := os.WriteFile(filepath.Join(dir, meshID+".png"), cutout, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, meshID+".glb"), glb, 0o644); err != nil {
		return nil, err
	}
	seconds := 0.0
	if err := s.media.Record(meshID+".glb", userIDOf(user), backend.slug, &seconds); err != nil {
		s.log.Printf("mcp: record mesh: %v", err)
	}
	var inference *float64
	if v, ok := remote["inference_time_s"].(float64); ok {
		inference = &v
	}
	viewNames := []string{"front"}
	for _, v := range MeshViews {
		if _, ok := views[v]; ok {
			viewNames = append(viewNames, v)
		}
	}
	preview, err := previewGreyJPEG(cutout)
	if err != nil {
		return nil, err
	}
	return &meshResult{
		MeshID: meshID, Model: backend.slug, Seed: seedVal, Steps: steps,
		Views: viewNames, Bytes: len(glb), InferenceTimeS: inference, Preview: preview,
	}, nil
}

// cutOut is upstream mesh_service.cut_out: flood-fill the plain background in
// from the edges and make it transparent.
func cutOut(raw []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, imageErrorf("Not a readable image: %v", err)
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	rgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)

	// Enough of the picture already see-through? Use it as it is.
	transparent := 0
	for i := 3; i < len(rgba.Pix); i += 4 {
		if rgba.Pix[i] < 128 {
			transparent++
		}
	}
	if float64(transparent) >= minBackground*float64(w*h) {
		return encodePNG(rgba)
	}

	bg := make([]bool, w*h)
	queue := make([]int, 0, w*h/4)
	// Sample the edge and grow the background from every edge pixel.
	push := func(x, y int) {
		i := y*w + x
		if bg[i] {
			return
		}
		bg[i] = true
		queue = append(queue, i)
	}
	for x := 0; x < w; x += edgeStep {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y += edgeStep {
		push(0, y)
		push(w-1, y)
	}
	// The background colour is whatever most sampled edge pixels agree on;
	// approximate by seeding tolerance from the top-left corner.
	br, bgc, bb := rgba.Pix[0], rgba.Pix[1], rgba.Pix[2]
	diff := func(i int) int {
		p := i * 4
		d := absInt(int(rgba.Pix[p])-int(br)) + absInt(int(rgba.Pix[p+1])-int(bgc)) + absInt(int(rgba.Pix[p+2])-int(bb))
		// An already transparent pixel is background too.
		if rgba.Pix[p+3] < 128 {
			return 0
		}
		return d
	}
	for head := 0; head < len(queue); head++ {
		i := queue[head]
		x, y := i%w, i/w
		for _, n := range [4]int{north(x, y, w, h), south(x, y, w, h), west(x, y, w, h), east(x, y, w, h)} {
			if n < 0 || bg[n] {
				continue
			}
			if diff(n) <= cutTolerance {
				bg[n] = true
				queue = append(queue, n)
			}
		}
	}
	// A 1px erosion drops the blended halo the fill stops short of.
	cropped := make([]bool, w*h)
	copy(cropped, bg)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			if !bg[i] {
				continue
			}
			if !(bg[i-1] && bg[i+1] && bg[i-w] && bg[i+w]) {
				cropped[i] = false
			}
		}
	}
	share := 0.0
	for i := range cropped {
		if cropped[i] {
			share++
		}
	}
	share /= float64(w * h)
	if share < minBackground || share > maxBackground {
		return nil, imageErrorf("This picture has no plain background to cut the object out of. " +
			"Give one object on a plain background (generate_image can make one: ask for it " +
			"\"centered on a plain white background\"), or a PNG whose background is already transparent.")
	}
	for i := range cropped {
		if cropped[i] {
			rgba.Pix[i*4+3] = 0
		}
	}
	return encodePNG(rgba)
}

func north(x, y, w, h int) int {
	if y == 0 {
		return -1
	}
	return (y-1)*w + x
}
func south(x, y, w, h int) int {
	if y == h-1 {
		return -1
	}
	return (y+1)*w + x
}
func west(x, y, w, h int) int {
	if x == 0 {
		return -1
	}
	return y*w + x - 1
}
func east(x, y, w, h int) int {
	if x == w-1 {
		return -1
	}
	return y*w + x + 1
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// previewGreyJPEG is upstream preview_jpeg_b64: the cut-out on neutral grey.
func previewGreyJPEG(cutout []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(cutout))
	if err != nil {
		return "", err
	}
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
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{R: 128, G: 128, B: 128, A: 255}}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(canvas, canvas.Bounds(), img, img.Bounds(), draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
