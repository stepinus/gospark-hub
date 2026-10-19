package proxy

import (
	"strings"
	"testing"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
)

// nativeTestConfig avoids testutil.RecipesDir: this test builds its own recipe
// set, so it must not skip when no registry is synced.
func nativeTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Host:         "0.0.0.0",
		Port:         9110,
		PublicPort:   9100,
		ProxyEnabled: true,
		AuthEnabled:  false,
		Project:      "gospark-hub",
		DataDir:      t.TempDir(),
	}
}

func TestRenderNativeRecipeUpstream(t *testing.T) {
	cfg := nativeTestConfig(t)
	recipes := []registry.Recipe{
		{
			Name: "Native One",
			Slug: "native-one",
			Kind: "native",
			UI:   registry.RecipeUI{Type: "api-only", Port: 8888, Path: "/v1", Proxy: true, StripPrefix: true},
			Native: &registry.RecipeNative{
				Engine: "tensorfold",
				Model:  "hf:x/y@rev",
				Port:   9999,
			},
		},
		{
			Name: "Container One",
			Slug: "container-one",
			UI:   registry.RecipeUI{Type: "web", Port: 7860, Path: "/", Proxy: true, StripPrefix: true},
		},
	}

	text := Render(cfg, recipes)
	routes := parseRoutes(text)

	if got := routes["native-one"]; got != "host.docker.internal:9999" {
		t.Fatalf("native route = %q, want host.docker.internal:9999\n%s", got, text)
	}
	if got := routes["container-one"]; got != "gospark-hub-container-one:7860" {
		t.Fatalf("container route = %q, want the container name\n%s", got, text)
	}
	if strings.Contains(text, "gospark-hub-native-one") {
		t.Fatalf("native recipe rendered a container upstream:\n%s", text)
	}
}

func TestRenderNativeRecipeFallsBackToUIPort(t *testing.T) {
	cfg := nativeTestConfig(t)
	recipes := []registry.Recipe{{
		Name:   "Native Two",
		Slug:   "native-two",
		Kind:   "native",
		UI:     registry.RecipeUI{Type: "web", Port: 8000, Path: "/", Proxy: true},
		Native: &registry.RecipeNative{Engine: "tensorfold", Model: "hf:x/y@rev"},
	}}

	if got := parseRoutes(Render(cfg, recipes))["native-two"]; got != "host.docker.internal:8000" {
		t.Fatalf("native route = %q, want host.docker.internal:8000", got)
	}
}
