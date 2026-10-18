package media

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"

	"gospark-hub/internal/config"
)

// Size limits mirror daemon/services/upload_service.py.
var (
	MaxImageBytes = 20 * 1024 * 1024
	MaxVideoBytes = 200 * 1024 * 1024
	MaxAudioBytes = 50 * 1024 * 1024
	MaxBytes      = MaxVideoBytes
)

// IMAGE_BRANDS marks HEIF/HEIC/AVIF still images that share an MP4 container.
var imageBrands = map[string]bool{
	"mif1": true, "msf1": true, "heic": true, "heix": true,
	"heim": true, "heis": true, "hevc": true, "hevx": true,
	"avif": true, "avis": true,
}

// UploadError is returned when a file cannot be accepted.
type UploadError struct{ Msg string }

func (e *UploadError) Error() string { return e.Msg }

func isMP4(raw []byte) bool {
	if len(raw) < 16 || string(raw[4:8]) != "ftyp" {
		return false
	}
	size := int(raw[0])<<24 | int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	if size > len(raw) {
		size = len(raw)
	}
	brands := []string{string(raw[8:12])}
	for i := 16; i < size && i+4 <= size; i += 4 {
		brands = append(brands, string(raw[i:i+4]))
	}
	for _, b := range brands {
		if imageBrands[b] {
			return false
		}
	}
	return true
}

func isAudio(raw []byte) bool {
	if len(raw) < 12 {
		return false
	}
	if string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WAVE" {
		return true
	}
	if string(raw[:4]) == "fLaC" || string(raw[:4]) == "OggS" {
		return true
	}
	if string(raw[:3]) == "ID3" {
		return true
	}
	if len(raw) > 1 && raw[0] == 0xFF && raw[1]&0xE0 == 0xE0 {
		return true
	}
	return false
}

// Save stores an uploaded image, video or sound and returns its public path info.
func Save(cfg config.Config, raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, &UploadError{Msg: "The upload is empty."}
	}

	if isAudio(raw) {
		return saveAudio(cfg, raw)
	}
	if isMP4(raw) {
		return saveVideo(cfg, raw)
	}

	return saveImage(cfg, raw)
}

func saveAudio(cfg config.Config, raw []byte) (map[string]any, error) {
	if len(raw) > MaxAudioBytes {
		return nil, &UploadError{Msg: fmt.Sprintf("Sounds can be at most %d MB.", MaxAudioBytes/(1024*1024))}
	}
	wav, err := toWAV(raw)
	if err != nil {
		return nil, err
	}
	id := newID()
	dir := filepath.Join(cfg.DataDir, "audio", "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".wav")
	if err := os.WriteFile(path, wav, 0o644); err != nil {
		return nil, err
	}
	return map[string]any{
		"kind": "audio",
		"id":   id,
		"path": "/audio/uploads/" + id + ".wav",
	}, nil
}

// toWAV returns raw WAV data as-is; other formats are converted with ffmpeg if available.
func toWAV(raw []byte) ([]byte, error) {
	if len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WAVE" {
		return raw, nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, &UploadError{Msg: "Not a sound file the Hub can read."}
	}
	dir, err := os.MkdirTemp("", "sah-audio-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "in")
	out := filepath.Join(dir, "out.wav")
	if err := os.WriteFile(src, raw, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-y", "-i", src, "-vn", out)
	if err := cmd.Run(); err != nil {
		return nil, &UploadError{Msg: "Not a sound file the Hub can read."}
	}
	return os.ReadFile(out)
}

func saveVideo(cfg config.Config, raw []byte) (map[string]any, error) {
	if len(raw) > MaxVideoBytes {
		return nil, &UploadError{Msg: fmt.Sprintf("Videos can be at most %d MB.", MaxVideoBytes/(1024*1024))}
	}
	id := newID()
	dir := filepath.Join(cfg.DataDir, "videos", "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".mp4")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return nil, err
	}
	return map[string]any{
		"kind": "video",
		"id":   id,
		"path": "/videos/uploads/" + id + ".mp4",
	}, nil
}

func saveImage(cfg config.Config, raw []byte) (map[string]any, error) {
	if len(raw) > MaxImageBytes {
		return nil, &UploadError{Msg: fmt.Sprintf("Images can be at most %d MB.", MaxImageBytes/(1024*1024))}
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, &UploadError{Msg: "Not an image, MP4/MOV video or sound file the Hub can read."}
	}
	id := newID()
	dir := filepath.Join(cfg.DataDir, "images", "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".png")
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	return map[string]any{
		"kind":   "image",
		"id":     id,
		"width":  bounds.Dx(),
		"height": bounds.Dy(),
		"path":   "/images/uploads/" + id + ".png",
	}, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
