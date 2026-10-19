package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMeasureDerivesRates pins the arithmetic: prefill is prompt tokens over
// TTFT, decode is completion tokens over the window after the first token.
func TestMeasureDerivesRates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt := ""
		if len(body.Messages) > 0 {
			prompt = body.Messages[0].Content
		}
		// Tokens follow the prompt and TTFT follows the tokens, so the
		// two-run fit has something real to difference.
		tokens := len(prompt) / 4
		time.Sleep(time.Duration(tokens) * 20 * time.Microsecond)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":%d,"completion_tokens":50}}`+"\n\n", tokens)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m, err := Measure(context.Background(), srv.URL, "", "test-model")
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if m.PromptTokens <= 0 || m.DecodeTokens != 50 {
		t.Fatalf("tokens = %d/%d", m.PromptTokens, m.DecodeTokens)
	}
	// An in-process fake answers inside a millisecond, so the rounded fields
	// can legitimately read 0; the rates come from the duration.
	if m.TotalMs < m.TTFTms {
		t.Fatalf("timings = %d/%d", m.TTFTms, m.TotalMs)
	}
	if m.PrefillTPS <= 0 || m.PrefillNaiveTPS <= 0 {
		t.Fatalf("prefill = %v naive = %v", m.PrefillTPS, m.PrefillNaiveTPS)
	}
	if m.DecodeTokens > 0 && m.DecodeTPS <= 0 {
		t.Fatalf("decode = %v", m.DecodeTPS)
	}
}

func TestProxyPortAndContainer(t *testing.T) {
	cases := map[string]int{
		"http://127.0.0.1:8085":  8085,
		"http://127.0.0.1:8085/": 8085,
		"host:9000":              9000,
		"http://127.0.0.1":       0,
	}
	for in, want := range cases {
		if got := ProxyPort(in); got != want {
			t.Fatalf("ProxyPort(%q) = %d, want %d", in, got, want)
		}
	}
	if got := ContainerFromCmd(`sh -c "docker start a5b"`); got != "a5b" {
		t.Fatalf("docker start: %q", got)
	}
	if got := ContainerFromCmd("docker run --rm --name comet img"); got != "comet" {
		t.Fatalf("docker run: %q", got)
	}
	if got := ContainerFromCmd("/bin/hub.sh"); got != "" {
		t.Fatalf("plain command: %q", got)
	}
}

func TestParseMemUsage(t *testing.T) {
	cases := map[string]int64{
		"1.5GiB / 119GiB": 1610612736,
		"512MiB / 1GiB":   536870912,
		"":                0,
		"weird":           0,
	}
	for in, want := range cases {
		if got := parseMemUsage(in); got != want {
			t.Fatalf("parseMemUsage(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMemoryReadsProc(t *testing.T) {
	info := Memory()
	if info.TotalGB <= 0 {
		t.Fatalf("total = %v", info.TotalGB)
	}
	if info.AvailableGB <= 0 || info.AvailableGB > info.TotalGB {
		t.Fatalf("available = %v of %v", info.AvailableGB, info.TotalGB)
	}
}
