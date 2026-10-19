package mcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"gospark-hub/internal/store"
)

// Audio constants mirror daemon/services/audio_service.py.
const (
	audioMaxSeconds    = 360
	audioDefaultSecond = 30
	audioFramesPerSec  = 25
)

type audioBackend struct {
	slug       string
	summary    string
	defaultSec *int
}

func (s *Server) audioBackends() []audioBackend {
	recipes, err := s.allRecipes()
	if err != nil {
		return nil
	}
	var out []audioBackend
	for _, r := range recipes {
		if !hasTag(r.Tags, "openai-music") || !hasTag(r.Tags, "text-to-music") {
			continue
		}
		b := audioBackend{slug: r.Slug, summary: r.Name}
		if r.AudioDefaults != nil {
			b.defaultSec = r.AudioDefaults.Seconds
		}
		out = append(out, b)
	}
	return out
}

func (s *Server) pickAudioBackend(ctx context.Context, model string) (audioBackend, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return audioBackend{}, err
	}
	var candidates []audioBackend
	for _, b := range s.audioBackends() {
		if installed[b.slug] {
			candidates = append(candidates, b)
		}
	}
	if model != "" {
		found := false
		for _, b := range candidates {
			if b.slug == model {
				candidates, found = []audioBackend{b}, true
				break
			}
		}
		if !found {
			names := "none installed"
			if len(candidates) > 0 {
				names = ""
				for i, b := range candidates {
					if i > 0 {
						names += ", "
					}
					names += b.slug
				}
			}
			return audioBackend{}, imageErrorf("'%s' is not an installed music model. "+
				"Installed music models: %s.", model, names)
		}
	}
	slugs := make([]string, len(candidates))
	for i, b := range candidates {
		slugs[i] = b.slug
	}
	idx, err := s.pickReady(ctx, slugs, "music model")
	if err != nil {
		return audioBackend{}, err
	}
	return candidates[idx], nil
}

// audioResult is upstream audio_service.generate's return value.
type audioResult struct {
	AudioID string
	Model   string
	Seconds float64
	Seed    int
}

// musicBody is upstream audio_service.music_body.
func musicBody(lyrics, style string, seconds, seed int) map[string]any {
	seconds = maxInt(5, minInt(seconds, audioMaxSeconds))
	return map[string]any{
		"input":           lyrics,
		"instructions":    style,
		"seed":            seed,
		"max_new_tokens":  seconds * audioFramesPerSec,
		"response_format": "wav",
	}
}

// generateMusic is upstream audio_service.generate.
func (s *Server) generateMusic(ctx context.Context, args map[string]any, user *store.User) (*audioResult, error) {
	lyrics := argString(args, "lyrics")
	style := argString(args, "style")
	if lyrics == "" || style == "" {
		return nil, imageErrorf("'lyrics' and 'style' are both required.")
	}
	backend, err := s.pickAudioBackend(ctx, argString(args, "model"))
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
	seconds := 0
	if sec := argInt(args, "seconds"); sec != nil {
		seconds = *sec
	} else if backend.defaultSec != nil {
		seconds = *backend.defaultSec
	} else {
		seconds = audioDefaultSecond
	}

	body, _ := json.Marshal(musicBody(lyrics, style, seconds, seedVal))
	req, err := s.newAppRequest(ctx, http.MethodPost,
		s.app.InternalURL(backend.slug)+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, imageErrorf("%s failed: %v", backend.slug, err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imageErrorf("%s failed (HTTP %d): %s", backend.slug, resp.StatusCode, truncate(string(raw), 500))
	}
	dir := filepath.Join(s.cfg.DataDir, "audio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	audioID := newJobID()
	if err := os.WriteFile(filepath.Join(dir, audioID+".wav"), raw, 0o644); err != nil {
		return nil, err
	}
	length := wavSeconds(raw)
	recordSeconds := float64(seconds)
	if length > 0 {
		recordSeconds = length
	}
	if err := s.media.Record(audioID+".wav", userIDOf(user), backend.slug, &recordSeconds); err != nil {
		s.log.Printf("mcp: record audio: %v", err)
	}
	if length == 0 {
		length = float64(seconds)
	}
	return &audioResult{AudioID: audioID, Model: backend.slug, Seconds: round1(length), Seed: seedVal}, nil
}

// wavSeconds reads a WAV's duration from its header, or 0 when unreadable.
func wavSeconds(raw []byte) float64 {
	if len(raw) < 44 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return 0
	}
	// Walk the chunks to find fmt (byte rate) and data (size).
	byteRate := 0
	dataSize := 0
	for i := 12; i+8 <= len(raw); {
		id := string(raw[i : i+4])
		size := int(binary.LittleEndian.Uint32(raw[i+4 : i+8]))
		switch id {
		case "fmt ":
			if i+8+16 <= len(raw) {
				byteRate = int(binary.LittleEndian.Uint32(raw[i+8+8 : i+8+12]))
			}
		case "data":
			dataSize = size
		}
		if size <= 0 {
			break
		}
		i += 8 + size + (size & 1)
	}
	if byteRate == 0 || dataSize == 0 {
		return 0
	}
	return float64(dataSize) / float64(byteRate)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
