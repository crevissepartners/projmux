package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

func TestParsePercent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{"42%", 42, true},
		{"42", 42, true},
		{"42.5 %", 42.5, true},
		{"  0% ", 0, true},
		{"", 0, false},
		{"n/a", 0, false},
		{"%", 0, false},
		{"-3%", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParsePercent(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("ParsePercent(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestCollectMissingFile(t *testing.T) {
	t.Parallel()
	a := New(t.TempDir())
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("Collect with no sidecar = %v, want empty", snaps)
	}
}

func TestCollectEmptyBaseDir(t *testing.T) {
	t.Parallel()
	a := New("")
	snaps, err := a.Collect(context.Background())
	if err != nil || len(snaps) != 0 {
		t.Fatalf("Collect with empty baseDir = (%v, %v), want (nil, nil)", snaps, err)
	}
}

func TestWriteThenCollect(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	updated := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if err := WriteContext(dir, ContextRecord{Pct: 42, UpdatedAt: updated}); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}
	a := New(dir)
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("Collect = %d snapshots, want 1", len(snaps))
	}
	s := snaps[0]
	if s.Model != Name || s.Window != usage.WindowContext || s.Pct != 42 {
		t.Fatalf("snapshot = %+v, want model=%s window=context pct=42", s, Name)
	}
	if !s.UpdatedAt.Equal(updated) {
		t.Fatalf("UpdatedAt = %v, want %v", s.UpdatedAt, updated)
	}
	if !s.ResetsAt.IsZero() {
		t.Fatalf("ResetsAt = %v, want zero (context window has no reset)", s.ResetsAt)
	}
}

func TestCollectMalformedFileDegrades(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ContextFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := New(dir)
	snaps, err := a.Collect(context.Background())
	if err == nil {
		t.Fatalf("Collect on malformed file: want diagnostic error, got nil")
	}
	if len(snaps) != 0 {
		t.Fatalf("Collect on malformed file = %v, want no snapshots", snaps)
	}
}

func TestCollectClampsNegativePct(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := WriteContext(dir, ContextRecord{Pct: -5, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}
	snaps, err := New(dir).Collect(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("Collect = (%v, %v), want 1 snapshot", snaps, err)
	}
	if snaps[0].Pct != 0 {
		t.Fatalf("Pct = %v, want clamped to 0", snaps[0].Pct)
	}
}
