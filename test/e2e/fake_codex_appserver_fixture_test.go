package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureStateDirIsFixedPrivateSmokeChild(t *testing.T) {
	root := t.TempDir()
	expected := filepath.Join(root, "codex-lifecycle", "fake-codex-state")
	t.Setenv("PROJMUX_SMOKE_WORKDIR", root)
	t.Setenv("PROJMUX_FAKE_CODEX_STATE", expected)

	got, err := fixtureStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("fixture state dir = %q, want %q", got, expected)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("fixture state dir mode = %04o, want 0700", mode)
	}

	t.Setenv("PROJMUX_FAKE_CODEX_STATE", filepath.Join(root, "sibling"))
	if _, err := fixtureStateDir(); err == nil {
		t.Fatal("fixture accepted a state directory outside its fixed smoke child")
	}
}

func TestNextObserverEpochUsesFixedPrivateFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "codex-lifecycle", "fake-codex-state")
	t.Setenv("PROJMUX_SMOKE_WORKDIR", root)
	t.Setenv("PROJMUX_FAKE_CODEX_STATE", dir)

	for want := 1; want <= 2; want++ {
		got, err := nextObserverEpoch()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("observer epoch = %d, want %d", got, want)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "epoch"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("fixture epoch mode = %04o, want 0600", mode)
	}
}

func TestFixtureFrameHeaderCheckedLengths(t *testing.T) {
	for _, test := range []struct {
		length int
		want   []byte
		ok     bool
	}{
		{length: -1},
		{length: 0, want: []byte{0x81, 0}, ok: true},
		{length: 125, want: []byte{0x81, 125}, ok: true},
		{length: 126, want: []byte{0x81, 126, 0, 126}, ok: true},
		{length: 1<<16 - 1, want: []byte{0x81, 126, 0xff, 0xff}, ok: true},
		{length: 1 << 16},
	} {
		got, err := fixtureFrameHeader(test.length)
		if (err == nil) != test.ok {
			t.Fatalf("length %d error = %v, want ok=%t", test.length, err, test.ok)
		}
		if !bytes.Equal(got, test.want) {
			t.Errorf("length %d header = %v, want %v", test.length, got, test.want)
		}
	}
}
