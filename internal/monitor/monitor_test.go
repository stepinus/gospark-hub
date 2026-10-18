package monitor

import (
	"strings"
	"testing"
)

func TestParseNvidiaSmi(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Metrics
	}{
		{
			name: "full",
			in:   "35, 4096, 24576, 62, NVIDIA RTX A6000, 87, Not Active, Not Active",
			want: Metrics{
				GPUUtilization:   35,
				GPUMemoryUsedMB:  4096,
				GPUMemoryTotalMB: 24576,
				GPUTemperature:   62,
				GPUName:          "NVIDIA RTX A6000",
				GPUTempHeadroom:  ptrInt(87),
				// Upstream uses substring "Active", so "Not Active" evaluates true.
				GPUThermalSlowdown: true,
			},
		},
		{
			name: "thermal slowdown",
			in:   "99, 16384, 24576, 83, NVIDIA RTX A6000, 6, Active, Not Active",
			want: Metrics{
				GPUUtilization:     99,
				GPUMemoryUsedMB:    16384,
				GPUMemoryTotalMB:   24576,
				GPUTemperature:     83,
				GPUName:            "NVIDIA RTX A6000",
				GPUTempHeadroom:    ptrInt(6),
				GPUThermalSlowdown: true,
			},
		},
		{
			name: "short",
			in:   "0, 0, 0, 0, ",
			want: Metrics{
				GPUName: "",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseNvidiaSmi(tc.in)
			if !ok {
				t.Fatal("expected ok")
			}
			if got.GPUUtilization != tc.want.GPUUtilization ||
				got.GPUMemoryUsedMB != tc.want.GPUMemoryUsedMB ||
				got.GPUMemoryTotalMB != tc.want.GPUMemoryTotalMB ||
				got.GPUTemperature != tc.want.GPUTemperature ||
				got.GPUName != tc.want.GPUName ||
				got.GPUThermalSlowdown != tc.want.GPUThermalSlowdown ||
				!ptrEqual(got.GPUTempHeadroom, tc.want.GPUTempHeadroom) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseNvidiaSmiIgnoresExtraLines(t *testing.T) {
	in := strings.Join([]string{
		"10, 1024, 8192, 40, GPU0, 75, Not Active, Not Active",
		"20, 2048, 8192, 45, GPU1, 70, Not Active, Not Active",
	}, "\n")
	got, ok := parseNvidiaSmi(in)
	if !ok {
		t.Fatal("expected ok")
	}
	if got.GPUName != "GPU0" {
		t.Fatalf("expected first GPU, got %q", got.GPUName)
	}
}

func TestParseMeminfoToBytes(t *testing.T) {
	const sample = `MemTotal:       16000000 kB
MemFree:          2000000 kB
MemAvailable:     8000000 kB
Buffers:           100000 kB
`
	mi, ok := parseMeminfo(strings.NewReader(sample))
	if !ok {
		t.Fatal("expected ok")
	}
	total, avail := mi.ToBytes()
	if total != 16000000*1024 {
		t.Fatalf("total bytes: got %d want %d", total, 16000000*1024)
	}
	if avail != 8000000*1024 {
		t.Fatalf("avail bytes: got %d want %d", avail, 8000000*1024)
	}
}

func ptrInt(v int) *int { return &v }

func ptrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
