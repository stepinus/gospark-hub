// Package bench measures a running model the honest way: one real streaming
// request with a per-run nonce (so a warm prompt cache cannot fake prefill),
// prefill from prompt tokens over time-to-first-token, decode from completion
// tokens over the time after it, and the memory the process actually holds.
//
// It knows nothing about which hub runs the model: hand it an
// OpenAI-compatible base URL and it reports numbers that compare across
// backends (vLLM, llama.cpp, TensorFold).
package bench

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// verifyPrompt is long enough for prefill to dominate the first token.
const verifyPrompt = `Explain, concisely, why a fixed prompt makes two benchmark runs comparable. ` +
	`A steady prompt is a steady load: the same number of tokens hits the same kernels, the same ` +
	`attention window, and the same cache behaviour, so the only thing that moves between runs is the ` +
	`machine. Network jitter, tokenizer warm-up and the first-touch page faults belong to the first ` +
	`second; measure them once, then keep them out of the numbers you compare. When the prompt drifts, ` +
	`the decode rate drifts with it: longer prompts spend longer in prefill, a cold cache spends time ` +
	`copying weights, and a different language changes how many bytes each token carries. So pin the ` +
	`prompt, pin the sample settings, pin the maximum length, and say what you pinned next to the ` +
	`number. A benchmark without the prompt is a rumour with decimals. Now summarise the argument in ` +
	`one short paragraph, using plain words, and do not restate this instruction.`

// Measurement is one benchmark: two rates plus the memory held afterwards.
type Measurement struct {
	Target       string  `json:"target,omitempty"`
	Model        string  `json:"model"`
	PromptTokens int     `json:"prompt_tokens"`
	DecodeTokens int     `json:"decode_tokens"`
	TTFTms       int64   `json:"ttft_ms"`
	TotalMs      int64   `json:"total_ms"`
	PrefillTPS   float64 `json:"prefill_tps"`
	// PrefillNaiveTPS is prompt tokens over the whole TTFT — what a single
	// short request appears to do. It understates prefill because the fixed
	// cost (tokenize, HTTP, first kernel) is charged to the tokens.
	PrefillNaiveTPS float64 `json:"prefill_naive_tps,omitempty"`
	// OverheadMs is that fixed cost, measured as the intercept of the two
	// runs.
	OverheadMs   float64 `json:"overhead_ms,omitempty"`
	DecodeTPS    float64 `json:"decode_tps"`
	MemoryBytes  int64   `json:"memory_bytes"`
	MemorySource string  `json:"memory_source,omitempty"`
	MeasuredAt   string  `json:"measured_at"`
}

// Measure times two requests of different prompt sizes and reports the
// marginal prefill rate: (tokens_big - tokens_small) / (ttft_big - ttft_small).
//
// A single short request is a trap. The first token pays a fixed cost —
// tokenize, HTTP, stream start, the first kernel launch — that has nothing to
// do with the tokens, so 250 tokens divided by the whole TTFT reads ~800 tok/s
// on a box that actually prefillls at ~2500. The difference cancels that cost
// and reports what a long prompt really costs, while the naive number stays
// next to it so the gap is visible.
func Measure(ctx context.Context, base, key, model string) (Measurement, error) {
	small, err := runOnce(ctx, base, key, model, 1)
	if err != nil {
		return Measurement{Model: model, MeasuredAt: nowISO()}, err
	}
	large, err := runOnce(ctx, base, key, model, 5)
	if err != nil {
		return Measurement{Model: model, MeasuredAt: nowISO()}, err
	}

	m := Measurement{
		Model:        model,
		PromptTokens: large.promptTokens,
		DecodeTokens: large.decodeTokens,
		TTFTms:       large.ttft.Milliseconds(),
		TotalMs:      large.total.Milliseconds(),
		MeasuredAt:   nowISO(),
	}

	// Prefill from the difference between the runs.
	if span := large.ttft - small.ttft; span > 0 && large.promptTokens > small.promptTokens {
		m.PrefillTPS = round1(float64(large.promptTokens-small.promptTokens) / span.Seconds())
		// Intercept = small TTFT minus what its own tokens should have cost.
		m.OverheadMs = round1(float64(small.ttft.Milliseconds()) -
			float64(small.promptTokens)/m.PrefillTPS*1000)
	}
	if large.promptTokens > 0 && large.ttft > 0 {
		m.PrefillNaiveTPS = round1(float64(large.promptTokens) / large.ttft.Seconds())
	}
	if m.PrefillTPS <= 0 {
		m.PrefillTPS = m.PrefillNaiveTPS
	}
	if window := large.total - large.ttft; large.decodeTokens > 0 && window > 0 {
		m.DecodeTPS = round1(float64(large.decodeTokens) / window.Seconds())
	}
	return m, nil
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

// runResult is one timed request.
type runResult struct {
	promptTokens int
	decodeTokens int
	ttft         time.Duration
	total        time.Duration
}

// runOnce sends one streaming request whose prompt is `repeats` copies of the
// filler text (plus a nonce, so the prompt cache cannot answer for us) and
// times the first content token and the end of the stream.
func runOnce(ctx context.Context, base, key, model string, repeats int) (runResult, error) {
	body := map[string]any{
		"model":          model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"temperature":    0,
		"max_tokens":     128,
		"messages": []map[string]any{
			{"role": "user", "content": fmt.Sprintf("Run %s (ignore it).\n%s\nReply with one short sentence.",
				nonce(), strings.Repeat(verifyPrompt, repeats))},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return runResult{}, err
	}

	client := &http.Client{Timeout: 3 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(base, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return runResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	var out runResult
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("chat completions: %s", resp.Status)
	}

	var firstToken time.Time
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta map[string]any `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if firstToken.IsZero() && len(chunk.Choices) > 0 {
			for _, field := range []string{"content", "reasoning_content"} {
				if text, ok := chunk.Choices[0].Delta[field].(string); ok && text != "" {
					firstToken = time.Now()
					break
				}
			}
		}
		if chunk.Usage != nil {
			out.promptTokens = chunk.Usage.PromptTokens
			out.decodeTokens = chunk.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	end := time.Now()
	if firstToken.IsZero() {
		firstToken = end
	}
	out.ttft = firstToken.Sub(start)
	out.total = end.Sub(start)
	return out, nil
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

func nonce() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0"
	}
	return hex.EncodeToString(b[:])
}

var (
	pidRE         = regexp.MustCompile(`pid=(\d+)`)
	dockerStartRE = regexp.MustCompile(`docker\s+start\s+([A-Za-z0-9_.-]+)`)
	dockerRunName = regexp.MustCompile(`--name[= ]([A-Za-z0-9_.-]+)`)
)

// ProxyPort turns an upstream URL into its port.
func ProxyPort(proxy string) int {
	_, after, ok := strings.Cut(proxy, "://")
	if !ok {
		after = proxy
	}
	hostPort := strings.TrimSuffix(after, "/")
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		if port, err := strconv.Atoi(hostPort[i+1:]); err == nil {
			return port
		}
	}
	return 0
}

// PIDForPort finds the process listening on a host port.
func PIDForPort(port int) string {
	out, err := exec.Command("ss", "-ltnpH", fmt.Sprintf("sport = :%d", port)).Output()
	if err != nil {
		return ""
	}
	if match := pidRE.FindStringSubmatch(string(out)); match != nil {
		return match[1]
	}
	return ""
}

// ContainerFromCmd names the container a model's cmd starts, if any.
func ContainerFromCmd(cmd string) string {
	if match := dockerStartRE.FindStringSubmatch(cmd); match != nil {
		return match[1]
	}
	if match := dockerRunName.FindStringSubmatch(cmd); match != nil {
		return match[1]
	}
	return ""
}

// ProcessMemory reports what a pid holds. On this box the weights live in the
// GPU allocator, so that number wins when the driver knows the process; the
// cgroup is the fallback for a process the driver does not account (mmap'd
// page cache shows up there), then plain RSS.
func ProcessMemory(pid string) (int64, string) {
	if pid == "" {
		return 0, ""
	}
	if size := GPUProcessMemory(pid); size > 0 {
		return size, "pid:" + pid + " (GPU, unified memory)"
	}
	var rss int64
	if data, err := os.ReadFile("/proc/" + pid + "/status"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "VmRSS:") {
				continue
			}
			if fields := strings.Fields(line); len(fields) >= 2 {
				if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					rss = kb * 1024
				}
			}
			break
		}
	}
	if data, err := os.ReadFile("/proc/" + pid + "/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			_, path, ok := strings.Cut(line, "::")
			if !ok || path == "" || path == "/" {
				continue
			}
			if current, err := os.ReadFile(cgroupMemoryPath(path)); err == nil {
				if value, err := strconv.ParseInt(strings.TrimSpace(string(current)), 10, 64); err == nil && value > rss {
					return value, "pid:" + pid + " (cgroup, unified memory)"
				}
			}
		}
	}
	if rss > 0 {
		return rss, "pid:" + pid + " (host process, RSS)"
	}
	return 0, ""
}

// cgroupMemoryPath maps a /proc/<pid>/cgroup path onto the mounted hierarchy.
func cgroupMemoryPath(procPath string) string {
	return "/sys/fs/cgroup" + strings.TrimSuffix(procPath, "/") + "/memory.current"
}

// GPUProcessMemory asks the driver how much a pid holds. GB10 is a unified
// box: this is the model's weights and KV cache, the number `free` alone hides.
func GPUProcessMemory(pid string) int64 {
	out, err := exec.Command("nvidia-smi",
		"--query-compute-apps=pid,used_memory",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) != 2 {
			continue
		}
		if strings.TrimSpace(fields[0]) != pid {
			continue
		}
		mib, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		if err != nil {
			return 0
		}
		return mib << 20
	}
	return 0
}

// ContainerMemory reads docker's own accounting for a container.
func ContainerMemory(ctx context.Context, name string) (int64, string) {
	if name == "" {
		return 0, ""
	}
	out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream",
		"--format", "{{.MemUsage}}", name).Output()
	if err != nil {
		return 0, ""
	}
	if size := parseMemUsage(string(out)); size > 0 {
		return size, "docker:" + name + " (container, unified memory)"
	}
	return 0, ""
}

// parseMemUsage reads docker's "1.2GiB / 119GiB" into bytes.
func parseMemUsage(text string) int64 {
	value, _, _ := strings.Cut(strings.TrimSpace(text), "/")
	value = strings.TrimSpace(value)
	i := 0
	for i < len(value) && (value[i] >= '0' && value[i] <= '9' || value[i] == '.') {
		i++
	}
	if i == 0 {
		return 0
	}
	number, err := strconv.ParseFloat(value[:i], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(strings.TrimSpace(value[i:]))
	mult := map[string]float64{
		"b": 1, "kib": 1 << 10, "kb": 1e3, "mib": 1 << 20, "mb": 1e6,
		"gib": 1 << 30, "gb": 1e9, "tib": 1 << 40, "tb": 1e12,
	}[unit]
	if mult == 0 {
		return 0
	}
	return int64(number * mult)
}

// MemoryInfo is what the machine looks like right now: total, what other
// processes already hold, and what is still allocatable.
type MemoryInfo struct {
	TotalGB     float64 `json:"total_gb"`
	UsedGB      float64 `json:"used_gb"`
	AvailableGB float64 `json:"available_gb"`
}

// Memory reads /proc/meminfo. Used is total minus available, so it counts
// every foreign process, container and page cache the kernel is willing to
// reclaim — the same number earlyoom acts on.
func Memory() MemoryInfo {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return MemoryInfo{}
	}
	values := map[string]float64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		values[key] = value / 1024 / 1024 // kB → GiB
	}
	info := MemoryInfo{TotalGB: values["MemTotal"], AvailableGB: values["MemAvailable"]}
	if values["MemTotal"] > 0 {
		info.UsedGB = round1(values["MemTotal"] - values["MemAvailable"])
	}
	info.TotalGB = round1(info.TotalGB)
	info.AvailableGB = round1(info.AvailableGB)
	return info
}
