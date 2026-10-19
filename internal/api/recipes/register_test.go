package recipes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

type fakeDeps struct{}

func (fakeDeps) InstalledRecipes(context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{"anythingllm": {}}, nil
}

func (fakeDeps) HasLeftovers(context.Context, string) (bool, error) { return false, nil }
func (fakeDeps) IsRecipeRunning(context.Context, string) bool       { return false }
func (fakeDeps) IsReady(string) bool                                { return false }
func (fakeDeps) GetPending(string) string                           { return "" }
func (fakeDeps) GetStartupError(string) string                      { return "" }
func (fakeDeps) StartHealthCheck(context.Context, string)           {}

func setBaseDir(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}
	t.Setenv("SPARK_AI_HUB_BASE_DIR", root)
	t.Setenv("SPARK_AI_HUB_REGISTRY_PATH", filepath.Join(root, "registry", "recipes"))
}

func TestListAndDetail(t *testing.T) {
	setBaseDir(t)
	r := web.NewRouter()
	Register(r, fakeDeps{})

	// List
	req := httptest.NewRequest(http.MethodGet, "/api/recipes", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("list status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("list content-type: got %q, want application/json", ct)
	}

	var list []registry.Recipe
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 148 {
		t.Fatalf("list length: got %d, want 148", len(list))
	}

	// Detail
	req = httptest.NewRequest(http.MethodGet, "/api/recipes/anythingllm", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status: got %d, want %d", rec.Code, http.StatusOK)
	}
	var detail registry.Recipe
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if detail.Slug != "anythingllm" {
		t.Fatalf("detail slug: got %q, want anythingllm", detail.Slug)
	}
	if !detail.Installed {
		t.Fatalf("detail installed should be true because fakeDeps reports it")
	}

	// 404
	req = httptest.NewRequest(http.MethodGet, "/api/recipes/no-such-slug", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug status: got %d, want %d", rec.Code, http.StatusNotFound)
	}
	var errBody map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("unmarshal 404: %v", err)
	}
	if errBody["detail"] != "Recipe not found" {
		t.Fatalf("404 detail: got %q, want %q", errBody["detail"], "Recipe not found")
	}
}

func TestListCategoryAndSearch(t *testing.T) {
	setBaseDir(t)
	r := web.NewRouter()
	Register(r, fakeDeps{})

	req := httptest.NewRequest(http.MethodGet, "/api/recipes?category=image-gen", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var list []registry.Recipe
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, item := range list {
		cats := item.Categories
		if len(cats) == 0 {
			cats = []string{item.Category}
		}
		found := false
		for _, c := range cats {
			if c == "image-gen" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("recipe %q does not match category filter", item.Slug)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/recipes?search=vision", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal search: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("search for vision returned no results")
	}
}
