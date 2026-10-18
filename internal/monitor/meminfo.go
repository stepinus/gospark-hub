package monitor

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Meminfo holds the values we care about from /proc/meminfo, in kilobytes.
type Meminfo struct {
	TotalKB     int64
	AvailableKB int64
}

// ToBytes converts kilobytes to bytes.
func (m Meminfo) ToBytes() (total, available int64) {
	return m.TotalKB * 1024, m.AvailableKB * 1024
}

// readMeminfo parses /proc/meminfo and returns MemTotal and MemAvailable in KB.
func readMeminfo(path string) (totalKB, availableKB int64, ok bool) {
	mi, ok := parseMeminfoFile(path)
	if !ok {
		return 0, 0, false
	}
	return mi.TotalKB, mi.AvailableKB, true
}

func parseMeminfoFile(path string) (Meminfo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Meminfo{}, false
	}
	defer f.Close()
	return parseMeminfo(f)
}

func parseMeminfo(r interface{ Read([]byte) (int, error) }) (Meminfo, bool) {
	var mi Meminfo
	found := 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		val, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			mi.TotalKB = val
			found++
		case "MemAvailable":
			mi.AvailableKB = val
			found++
		}
		if found == 2 {
			break
		}
	}
	if found < 1 {
		return Meminfo{}, false
	}
	return mi, true
}
