package llamaswap

import (
	"path/filepath"

	"gospark-hub/internal/bench"
)

func (m *Manager) measurementsPath() string { return filepath.Join(m.Home, "measurements.json") }

// Measurements is the last measurement per model id, so the page shows the
// last numbers without re-running the benchmark every time it renders.
func (m *Manager) Measurements() map[string]bench.Measurement {
	return bench.LoadAll(m.measurementsPath())
}

// SaveMeasurement records one measurement.
func (m *Manager) SaveMeasurement(measurement bench.Measurement) error {
	return bench.Save(m.measurementsPath(), measurement.Model, measurement)
}
