package llamaswap

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Measurements is the last measurement per model id, so the groups panel can
// show an expected load without re-measuring every time it renders. It is a
// cache of facts, never a source of truth about what is running.
func (m *Manager) Measurements() map[string]Measurement {
	out := map[string]Measurement{}
	data, err := os.ReadFile(filepath.Join(m.Home, "measurements.json"))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// SaveMeasurement records one measurement.
func (m *Manager) SaveMeasurement(measurement Measurement) error {
	all := m.Measurements()
	all[measurement.Model] = measurement
	if err := os.MkdirAll(m.Home, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.Home, "measurements.json"), append(data, '\n'), 0o644)
}
