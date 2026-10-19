package llamaswap

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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// verifyPrompt is long enough for the prefill phase to dominate the first
// token: a few hundred tokens of plain text. A per-run nonce is appended so
// the engine's prompt cache cannot turn prefill into a zero-cost hit.
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

// Measurement is one on-demand benchmark of a running model: prefill and decode
// rates measured around a real request, plus the memory the process holds
// afterwards.
type Measurement struct {
	Model         string  `json:"model"`
	PromptTokens  int     `json:"prompt_tokens"`
	DecodeTokens  int     `json:"decode_tokens"`
	TTFTms        int64   `json:"ttft_ms"`
	TotalMs       int64   `json:"total_ms"`
	PrefillTPS    float64 `json:"prefill_tps"`
	DecodeTPS     float64 `json:"decode_tps"`
	MemoryBytes   int64   `json:"memory_bytes"`
	MemorySource  string  `json:"memory_source,omitempty"`
	MeasuredAt    string  `json:"measured_at"`
	EngineMetrics string  `json:"engine_metrics,omitempty"`
}

// Measure runs one streaming request against the sidecar and derives the two
// rates from wall-clock: prefill is prompt tokens over time-to-first-token,
// decode is completion tokens over the time after it. That works for every
// backend llama-swap fronts, unlike llama.cpp's own timings block.
func (c *Client) Measure(ctx context.Context, model string) (Measurement, error) {
	m := Measurement{Model: model, MeasuredAt: time.Now().UTC().Format(time.RFC3339)}

	body := map[string]any{
		"model":  model,
		"stream": true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
		"temperature": 0,
		"max_tokens":  96,
		"messages": []map[string]any{
			// The nonce is the point: the same text would hit the prompt cache
			// and report a prefill rate for work that never happened.
			{"role": "user", "content": fmt.Sprintf("Run id %s (ignore it).\n\n%s", nonce(), verifyPrompt)},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return m, err
	}

	timeout := c.client().Timeout
	if timeout < 2*time.Minute {
		timeout = 2 * time.Minute
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.Base+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return m, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}

	start := time.Now()
	resp, err := c.client().Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("chat completions: %s", resp.Status)
	}

	var firstToken time.Time
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
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
			for _, key := range []string{"content", "reasoning_content"} {
				if text, ok := chunk.Choices[0].Delta[key].(string); ok && text != "" {
					firstToken = time.Now()
					break
				}
			}
		}
		if chunk.Usage != nil {
			m.PromptTokens = chunk.Usage.PromptTokens
			m.DecodeTokens = chunk.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return m, err
	}
	end := time.Now()
	if firstToken.IsZero() {
		firstToken = end
	}
	m.TTFTms = firstToken.Sub(start).Milliseconds()
	m.TotalMs = end.Sub(start).Milliseconds()

	if m.PromptTokens > 0 && m.TTFTms > 0 {
		m.PrefillTPS = round1(float64(m.PromptTokens) / (float64(m.TTFTms) / 1000))
	}
	decodeWindow := end.Sub(firstToken).Seconds()
	if m.DecodeTokens > 0 && decodeWindow > 0 {
		m.DecodeTPS = round1(float64(m.DecodeTokens) / decodeWindow)
	}

	m.MemoryBytes, m.MemorySource = c.modelMemory(ctx, model)
	return m, nil
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

var pidRE = regexp.MustCompile(`pid=(\d+)`)

// modelMemory reports what the model's own process or container holds. The
// sidecar knows which one: a model started with `docker start` is a container,
// anything else owns the port its proxy URL names.
func (c *Client) modelMemory(ctx context.Context, model string) (int64, string) {
	running, err := c.Running(ctx)
	if err != nil {
		return 0, ""
	}
	for _, r := range running {
		if r.Model != model {
			continue
		}
		if name := dockerContainer(r.Cmd); name != "" {
			out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream",
				"--format", "{{.MemUsage}}", name).Output()
			if err == nil {
				if bytes := parseMemUsage(string(out)); bytes > 0 {
					return bytes, "docker:" + name + " (container, unified memory)"
				}
			}
		}
		port := ProxyPort(r.Proxy)
		if port == 0 {
			return 0, ""
		}
		out, err := exec.CommandContext(ctx, "ss", "-ltnpH", fmt.Sprintf("sport = :%d", port)).Output()
		if err != nil {
			return 0, ""
		}
		if match := pidRE.FindStringSubmatch(string(out)); match != nil {
			if size, source := processMemory(match[1]); size > 0 {
				return size, source
			}
		}
		return 0, ""
	}
	return 0, ""
}

var dockerStartRE = regexp.MustCompile(`docker\s+start\s+([A-Za-z0-9_.-]+)`)

func dockerContainer(cmd string) string {
	if match := dockerStartRE.FindStringSubmatch(cmd); match != nil {
		return match[1]
	}
	return ""
}

// nonce keeps the measurement prompt unique, so a warm prompt cache cannot
// report a prefill rate for work that never happened.
func nonce() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0"
	}
	return hex.EncodeToString(b[:])
}

// processMemory reports what the model's process holds. The cgroup is the
// honest number on a unified-memory box: mmap'd weights are page cache, which
// RSS under-reports, while the unit's memory.current includes them.
func processMemory(pid string) (int64, string) {
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
			if current, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", path, "memory.current")); err == nil {
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

func rssBytes(pid string) int64 {
	data, err := os.ReadFile("/proc/" + pid + "/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
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
