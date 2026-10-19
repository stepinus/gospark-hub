package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LoadAll reads the last measurement per target. A missing or broken file is
// simply "nothing measured yet": a cache, never a source of truth.
func LoadAll(path string) map[string]Measurement {
	out := map[string]Measurement{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// Save records one measurement under key (the recipe slug), keeping the rest.
func Save(path, key string, m Measurement) error {
	all := LoadAll(path)
	all[key] = m
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
