package sah

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSynthetic(t *testing.T) {
	src := `class Integration:
    name = ""
    display = ""
    kind = "CLI"
    launchable = True
    writes_config = False
    opens_folder = False


class Alpha(Integration):
    name, display, binary = "alpha", "Alpha Code", "alpha"


class Beta(Integration):
    name, display = "beta", "Beta"
    kind = "Desktop"
    launchable = False
    writes_config = True


class Editor(Beta):
    name, display = "editor", "Editor"
    opens_folder = True


INTEGRATIONS: list[Integration] = [
    Alpha(), Editor(),
]

BY_NAME: dict[str, Integration] = {}
`
	got := Parse([]byte(src))
	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2: %+v", len(got), got)
	}
	if got[0] != (Agent{ID: "alpha", Name: "Alpha Code", Kind: "CLI", Command: "sah alpha"}) {
		t.Fatalf("alpha = %+v", got[0])
	}
	// Editor inherits kind/launchable/writes_config from Beta and sets opens_folder.
	if got[1] != (Agent{
		ID: "editor", Name: "Editor", Kind: "Desktop",
		Command: "sah editor --install", WritesConfig: true,
	}) {
		t.Fatalf("editor = %+v", got[1])
	}
}

// TestParseShippedSah pins the parser to the CLI we actually ship. If upstream
// reshapes INTEGRATIONS this fails loudly instead of silently emptying the
// Connect page.
func TestParseShippedSah(t *testing.T) {
	path := filepath.Join("..", "..", "sah", "sah")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	agents := Parse(src)
	if len(agents) == 0 {
		t.Fatal("no agents parsed from the shipped sah")
	}
	if len(agents) != 21 {
		t.Fatalf("parsed %d agents, want 21: %+v", len(agents), agents)
	}

	byID := make(map[string]Agent, len(agents))
	for _, a := range agents {
		byID[a.ID] = a
	}
	for _, id := range []string{"claude", "chatgpt", "opencode", "codex", "zed", "opencode-desktop"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing agent %q in %v", id, agents)
		}
	}

	if a := byID["claude"]; a.Name != "Claude Code" || a.Kind != "CLI" || a.Command != "sah claude" {
		t.Fatalf("claude = %+v", a)
	}
	if a := byID["chatgpt"]; a.Kind != "Desktop" || a.Command != "sah chatgpt --install" {
		t.Fatalf("chatgpt = %+v", a)
	}
	if a := byID["zed"]; a.Command != "sah zed ." || !a.WritesConfig {
		t.Fatalf("zed = %+v", a)
	}
	if a := byID["opencode-desktop"]; a.Kind != "Desktop" || a.Command != "sah opencode-desktop --install" {
		t.Fatalf("opencode-desktop = %+v", a)
	}
	if agents[0].ID != "claude" {
		t.Fatalf("registry order lost: first = %q", agents[0].ID)
	}
}

func TestLoadMissingFile(t *testing.T) {
	// Load caches globally; missing file must yield a usable empty slice.
	if got := Load(filepath.Join(t.TempDir(), "absent")); got != nil && len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}
