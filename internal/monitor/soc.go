package monitor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readSOCTemperature returns the hottest ACPI thermal zone and the lowest
// critical trip point found under /sys/class/thermal. Values are in °C.
func readSOCTemperature() (hottest, critical *int) {
	zones, err := filepath.Glob("/sys/class/thermal/thermal_zone*")
	if err != nil {
		return nil, nil
	}
	var hot, crit *int
	for _, zone := range zones {
		tempPath := filepath.Join(zone, "temp")
		tempRaw, err := os.ReadFile(tempPath)
		if err != nil {
			continue
		}
		temp, err := strconv.Atoi(strings.TrimSpace(string(tempRaw)))
		if err != nil {
			continue
		}
		temp /= 1000

		trips, err := filepath.Glob(filepath.Join(zone, "trip_point_*_type"))
		if err != nil {
			trips = nil
		}
		for _, trip := range trips {
			typ, err := os.ReadFile(trip)
			if err != nil {
				continue
			}
			if strings.TrimSpace(string(typ)) != "critical" {
				continue
			}
			valuePath := strings.Replace(trip, "_type", "_temp", 1)
			valueRaw, err := os.ReadFile(valuePath)
			if err != nil {
				continue
			}
			value, err := strconv.Atoi(strings.TrimSpace(string(valueRaw)))
			if err != nil {
				continue
			}
			value /= 1000
			if crit == nil || value < *crit {
				crit = &value
			}
		}

		if hot == nil || temp > *hot {
			hot = &temp
		}
	}
	return hot, crit
}
