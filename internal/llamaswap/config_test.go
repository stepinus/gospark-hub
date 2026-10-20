package llamaswap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Deleting a model has to remove the entry AND its group membership in one
// write, otherwise Validate rejects the result and the file never shrinks.
func TestDeleteModelRemovesEntryAndGroupMember(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home)
	before := `# keep this comment
apiKeys:
  - "k"
models:
  dead:
    name: Dead model
    cmd: sleep 1
  live:
    name: Live model
    cmd: sleep 2
groups:
  pair:
    swap: true
    members:
      - dead
      - live
`
	if err := os.WriteFile(m.ConfigPath(), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteModel("dead"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	text, err := m.ConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "dead") {
		t.Fatalf("deleted model still in the config:\n%s", text)
	}
	if !strings.Contains(text, "# keep this comment") {
		t.Fatalf("comments were lost:\n%s", text)
	}
	cfg, err := m.Config()
	if err != nil {
		t.Fatalf("config no longer parses: %v", err)
	}
	if _, ok := cfg.Models["live"]; !ok {
		t.Fatalf("the surviving model disappeared:\n%s", text)
	}
	if got := cfg.Groups["pair"].Members; len(got) != 1 || got[0] != "live" {
		t.Fatalf("group members after delete = %v, want [live]", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("result does not validate: %v", err)
	}
	if err := m.DeleteModel("dead"); !errors.Is(err, ErrNoSuchModel) {
		t.Fatalf("second delete = %v, want ErrNoSuchModel", err)
	}
}

// The manager writes its config through an atomic rename; a failed delete must
// leave the file alone.
func TestDeleteModelUnknownLeavesFileAlone(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home)
	before := "models:\n  real:\n    name: Real\n"
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteModel("ghost"); !errors.Is(err, ErrNoSuchModel) {
		t.Fatalf("DeleteModel(ghost) = %v, want ErrNoSuchModel", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != before {
		t.Fatalf("config changed on a failed delete:\n%s", data)
	}
}
