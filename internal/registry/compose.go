package registry

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// _CONTEXT_FLAGS lists the compose command flags that set the context window.
var contextFlags = map[string]bool{
	"--max-model-len":   true,
	"--max-seq-len":     true,
	"--max-total-tokens": true,
	"--ctx-size":        true,
	"-c":                true,
}

// _RESERVE_FLAGS lists the compose command flags that reserve GPU memory.
var reserveFlags = map[string]bool{
	"--gpu-memory-utilization": true,
	"--mem-fraction-static":    true,
}

func composeCommands(dir string) [][]string {
	path := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	doc := make(map[string]any)
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return nil
	}
	var out [][]string
	for _, svc := range services {
		svcMap, ok := svc.(map[string]any)
		if !ok {
			continue
		}
		cmd, ok := svcMap["command"].([]any)
		if !ok {
			continue
		}
		args := make([]string, 0, len(cmd))
		for _, a := range cmd {
			args = append(args, stringify(a))
		}
		if len(args) > 0 {
			out = append(out, args)
		}
	}
	return out
}

func flagValue(dir string, flags map[string]bool) string {
	for _, args := range composeCommands(dir) {
		for i, arg := range args {
			flag, inline, hadEq := strings.Cut(arg, "=")
			if !flags[flag] {
				continue
			}
			value := inline
			if !hadEq && i+1 < len(args) {
				value = args[i+1]
			}
			if strings.HasPrefix(value, "${") && strings.Contains(value, ":-") {
				parts := strings.SplitN(value, ":-", 2)
				value = strings.TrimSuffix(parts[1], "}")
			}
			return value
		}
	}
	return ""
}

func contextFromCompose(dir string) *int {
	value := flagValue(dir, contextFlags)
	if value == "" || !isAllDigits(value) {
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &n
}

func reservedFromCompose(dir string) float64 {
	value := flagValue(dir, reserveFlags)
	if value == "" {
		return 0.0
	}
	fraction, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0.0
	}
	return round(fraction*totalMemoryGB(), 1)
}

func totalMemoryGB() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0.0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0.0
		}
		kb, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0.0
		}
		return kb / (1024 * 1024)
	}
	if err := sc.Err(); err != nil {
		return 0.0
	}
	return 0.0
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func round(v float64, places int) float64 {
	shift := 1.0
	for range places {
		shift *= 10
	}
	return float64(int64(v*shift+0.5)) / shift
}

func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
