package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

const nativeRecipeYAML = `name: Qwen3.8 Flash Next Native
slug: qwen38-flash-next-native
kind: native
category: llm
ui:
  type: api-only
  port: 8888
native:
  engine: tensorfold
  model: "hf:nvidia/Qwen3.8-Flash-Next-FP4-Local@b9ce18a6887e18c089fdea8509a33e63429a3300"
  args: ["--context", "262144", "--parallel", "4", "--vision"]
  env: {TF_FLASHNEXT_DRAFTS: "6"}
`

const containerRecipeYAML = `name: Demo
slug: demo
category: llm
ui:
  type: web
  port: 9000
`

func TestParseNativeRecipe(t *testing.T) {
	rec, err := parseRecipe([]byte(nativeRecipeYAML), t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !rec.IsNative() {
		t.Fatalf("IsNative() = false, kind = %q", rec.Kind)
	}
	if rec.Kind != "native" {
		t.Fatalf("kind = %q, want native", rec.Kind)
	}
	if rec.Native == nil {
		t.Fatal("native block is nil")
	}
	if rec.Native.Engine != "tensorfold" {
		t.Errorf("engine = %q", rec.Native.Engine)
	}
	if !strings.HasPrefix(rec.Native.Model, "hf:nvidia/") {
		t.Errorf("model = %q", rec.Native.Model)
	}
	if len(rec.Native.Args) != 5 || rec.Native.Args[0] != "--context" {
		t.Errorf("args = %v", rec.Native.Args)
	}
	if rec.Native.Env["TF_FLASHNEXT_DRAFTS"] != "6" {
		t.Errorf("env = %v", rec.Native.Env)
	}
	// port comes from ui.port when the native block does not set one.
	if rec.Native.Port != 8888 {
		t.Errorf("port = %d, want ui.port 8888", rec.Native.Port)
	}
}

func TestParseNativePortOverridesUI(t *testing.T) {
	yaml := strings.Replace(nativeRecipeYAML, "  engine: tensorfold", "  engine: tensorfold\n  port: 9999", 1)
	rec, err := parseRecipe([]byte(yaml), t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rec.Native.Port != 9999 {
		t.Fatalf("port = %d, want 9999", rec.Native.Port)
	}
}

func TestParseContainerRecipeKeepsUpstreamShape(t *testing.T) {
	rec, err := parseRecipe([]byte(containerRecipeYAML), t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rec.IsNative() {
		t.Fatal("container recipe reported as native")
	}
	if rec.Kind != "container" {
		t.Fatalf("kind = %q, want container", rec.Kind)
	}
	if rec.Native != nil {
		t.Fatalf("native = %+v, want nil", rec.Native)
	}

	// The JSON contract: neither field may appear for a container recipe, or
	// the daemon stops being byte-compatible with upstream.
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), `"kind"`) {
		t.Errorf("container JSON carries kind: %s", body)
	}
	if strings.Contains(string(body), `"native"`) {
		t.Errorf("container JSON carries native: %s", body)
	}
}

func TestNativeRecipeJSONHasKindAndBlock(t *testing.T) {
	rec, err := parseRecipe([]byte(nativeRecipeYAML), t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Kind   string `json:"kind"`
		Native *struct {
			Engine string            `json:"engine"`
			Model  string            `json:"model"`
			Args   []string          `json:"args"`
			Env    map[string]string `json:"env"`
			Port   int               `json:"port"`
		} `json:"native"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != "native" || got.Native == nil {
		t.Fatalf("native JSON = %s", body)
	}
	if got.Native.Engine != "tensorfold" || got.Native.Port != 8888 || got.Native.Env["TF_FLASHNEXT_DRAFTS"] != "6" {
		t.Fatalf("native block = %+v", got.Native)
	}
}
