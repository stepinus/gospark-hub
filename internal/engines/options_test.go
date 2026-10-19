package engines

import (
	"errors"
	"slices"
	"testing"
)

// TestOptionsRoundTrip pins where the flags live: the engine directory, a
// missing file means "everything off", and an unknown engine is an error.
func TestOptionsRoundTrip(t *testing.T) {
	m := NewManager(t.TempDir())

	got, err := m.Options(TensorFold)
	if err != nil || got != (Options{}) {
		t.Fatalf("fresh options = %+v, %v; want zero", got, err)
	}
	if flags := got.Flags(); len(flags) != 0 {
		t.Fatalf("fresh flags = %q, want none", flags)
	}

	want := Options{Cortheon: true, Dashboard: true}
	if err := m.SetOptions(TensorFold, want); err != nil {
		t.Fatalf("set options: %v", err)
	}
	got, err = m.Options(TensorFold)
	if err != nil || got != want {
		t.Fatalf("options = %+v, %v; want %+v", got, err, want)
	}
	if flags := got.Flags(); !slices.Equal(flags, []string{"--cortheon", "--dashboard"}) {
		t.Fatalf("flags = %q", flags)
	}

	if err := m.SetOptions("nope", want); !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("unknown engine: %v", err)
	}
	if _, err := m.Options("nope"); !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("unknown engine read: %v", err)
	}
}
