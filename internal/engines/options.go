package engines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Options are the launch flags that belong to the engine rather than to a
// single recipe: Cortheon's reasoning program (with its web research tools)
// and the live dashboard page the engine serves at /dashboard.
//
// They live in <home>/<engine>/options.json so every native recipe on the
// engine picks them up without editing each recipe.yaml.
type Options struct {
	Cortheon  bool `json:"cortheon"`
	Dashboard bool `json:"dashboard"`
}

// Flags is what these options add to a `serve` command line.
func (o Options) Flags() []string {
	out := make([]string, 0, 2)
	if o.Cortheon {
		out = append(out, "--cortheon")
	}
	if o.Dashboard {
		out = append(out, "--dashboard")
	}
	return out
}

func (m *Manager) optionsPath(name string) string {
	return filepath.Join(m.Home, name, "options.json")
}

// Options reads the engine's launch options. A missing file is the zero value,
// not an error: an engine installed before this setting existed simply has
// every flag off.
func (m *Manager) Options(name string) (Options, error) {
	var o Options
	if _, err := engineByName(name); err != nil {
		return o, err
	}
	data, err := os.ReadFile(m.optionsPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return Options{}, err
	}
	return o, nil
}

// SetOptions writes the engine's launch options. It rewrites the whole file,
// so the caller sends the full set, not a patch.
func (m *Manager) SetOptions(name string, o Options) error {
	if _, err := engineByName(name); err != nil {
		return err
	}
	dir := filepath.Join(m.Home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.optionsPath(name), append(data, '\n'), 0o644)
}
