package system

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hfTokenPath returns the Hub-local token file.
func (s *server) hfTokenPath() string {
	return filepath.Join(s.deps.Config.DataDir, "hf-token")
}

func (s *server) systemTokenPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "huggingface", "token")
}

func (s *server) readHFToken() string {
	for _, p := range []string{s.hfTokenPath(), s.systemTokenPath()} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return ""
}

func (s *server) hasHFToken() bool {
	return s.readHFToken() != ""
}

func (s *server) writeHFToken(token string) error {
	p := s.hfTokenPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(strings.TrimSpace(token)), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

func (s *server) handleHFTokenGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"has_token": s.hasHFToken()})
}

type hfTokenBody struct {
	Token string `json:"token"`
}

func (s *server) handleHFTokenSet(w http.ResponseWriter, r *http.Request) {
	var body hfTokenBody
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"detail": []map[string]any{{
				"type": "json_invalid",
				"loc":  []any{"body"},
				"msg":  "JSON decode error",
			}},
		})
		return
	}
	if err := s.writeHFToken(body.Token); err != nil {
		s.deps.Logger.Error("hf-token write failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *server) handleHFAccess(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	recipe, found := s.deps.Registry.Get(slug)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Recipe not found"})
		return
	}

	repos := checkRepoAccess(r.Context(), s.readHFToken(), recipe.GatedRepos)
	ok := true
	for _, repo := range repos {
		if !repo.Accessible {
			ok = false
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "repos": repos})
}

type repoAccess struct {
	RepoID     string `json:"repo_id"`
	URL        string `json:"url"`
	Accessible bool   `json:"accessible"`
}

func checkRepoAccess(ctx context.Context, token string, repos []string) []repoAccess {
	client := &http.Client{Timeout: 10 * time.Second}
	results := make([]repoAccess, 0, len(repos))
	for _, repo := range repos {
		entry := repoAccess{
			RepoID:     repo,
			URL:        fmt.Sprintf("https://huggingface.co/%s", repo),
			Accessible: true,
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://huggingface.co/%s/resolve/main/config.json", repo), nil)
		if err != nil {
			results = append(results, entry)
			continue
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			results = append(results, entry)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			entry.Accessible = false
		}
		results = append(results, entry)
	}
	return results
}
