package containers

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"gospark-hub/internal/engines"
)

// TestNativeLaunchCarriesEngineOptions pins that the engine-wide flags reach
// the unit's command line: they are saved on the engine, not in each recipe.
func TestNativeLaunchCarriesEngineOptions(t *testing.T) {
	cacheHome := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cacheHome)
	engineHome := t.TempDir()
	installFakeEngine(t, engineHome, "1.1.1")
	srv, stub := newNativeServer(t, engineHome)

	if err := engines.NewManager(engineHome).SetOptions(
		engines.TensorFold, engines.Options{Cortheon: true, Dashboard: true},
	); err != nil {
		t.Fatalf("set options: %v", err)
	}

	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native/launch", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"launched"`) {
		t.Fatalf("launch: %d %s", code, body)
	}
	args := stub.last()
	for _, flag := range []string{"--cortheon", "--dashboard"} {
		if !slices.Contains(args, flag) {
			t.Fatalf("launch args %q missing %s", args, flag)
		}
		if n := count(args, flag); n != 1 {
			t.Fatalf("%s appears %d times in %q", flag, n, args)
		}
	}
}

func count(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}
