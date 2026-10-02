package app

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/setupcmd"
)

func TestSuggestedPlainChordForSequence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		seq  []byte
		want string
		ok   bool
	}{
		{name: "alt printable", seq: []byte("\x1ba"), want: "M-a", ok: true},
		{name: "alt digit", seq: []byte("\x1b7"), want: "M-7", ok: true},
		{name: "control byte", seq: []byte{0x01}, want: "C-a", ok: true},
		{name: "printable key", seq: []byte("p"), want: "p", ok: true},
		{name: "printable uppercase key", seq: []byte("P"), want: "P", ok: true},
		{name: "catalog plain sequence", seq: []byte("\x1b[1;4D"), want: "M-S-Left", ok: true},
		{name: "enter is ambiguous", seq: []byte("\r"), ok: false},
		{name: "space is not a keymap chord", seq: []byte(" "), ok: false},
		{name: "unsupported printable config char", seq: []byte(`"`), ok: false},
		{name: "raw multi-byte printable sequence", seq: []byte("pa"), ok: false},
		{name: "arrow is not a plain tmux chord", seq: []byte("\x1b[A"), ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := suggestedPlainChordForSequence(tc.seq)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("suggestedPlainChordForSequence(%q) = %q, %v; want %q, %v", tc.seq, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestSetupCommandRunNonInteractive(t *testing.T) {
	t.Parallel()

	cmd := setupcmd.New(probeKeysFromCatalog(), nil, func(string) string { return "" })
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--non-interactive"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run --non-interactive error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"Detected terminal:",
		"Expected key sequences:",
		"Alt-1",
		"Ctrl-N",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("non-interactive output missing %q\nfull:\n%s", want, out)
		}
	}
	if strings.Contains(out, "9900u") || strings.Contains(out, "User") {
		t.Fatalf("non-interactive output should not mention app escape/User keys:\n%s", out)
	}
}

func TestDefaultProbeKeysCoverSpec(t *testing.T) {
	t.Parallel()

	keys := probeKeysFromCatalog()
	got := sortedProbeLabels(keys)
	want := []string{
		"Alt-1", "Alt-2", "Alt-3", "Alt-4", "Alt-5", "Alt-6", "Alt-7",
		"Alt-Shift-Left", "Alt-Shift-Right",
		"Ctrl-M", "Ctrl-N", "Ctrl-Shift-L", "Ctrl-Shift-M", "Ctrl-Shift-R",
	}
	if len(got) != len(want) {
		t.Fatalf("default probe key count mismatch: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default probe key[%d] = %q, want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
	alt3 := probeKeyByLabel(keys, "Alt-3")
	if alt3.Action != "Recent windows" || alt3.ActionID != "RecentWindows:Open" || alt3.PlainChord != "M-3" {
		t.Fatalf("Alt-3 probe = %#v, want RecentWindows:Open recent windows M-3 probe", alt3)
	}
	cmd := setupcmd.New(keys, nil, func(string) string { return "" })
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--non-interactive"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run --non-interactive error = %v", err)
	}
	out := stdout.String()
	for _, banned := range []string{"9900u", "User", "CSI-u"} {
		if strings.Contains(out, banned) {
			t.Fatalf("default probe key output should not include legacy route %q:\n%s", banned, out)
		}
	}
}

func probeKeyByLabel(keys []setupcmd.ProbeKey, label string) setupcmd.ProbeKey {
	for _, key := range keys {
		if key.Label == label {
			return key
		}
	}
	return setupcmd.ProbeKey{}
}

// sortedProbeLabels returns the probe key labels in sorted order, so the
// catalog coverage assertion does not depend on probe order.
func sortedProbeLabels(keys []setupcmd.ProbeKey) []string {
	labels := make([]string, 0, len(keys))
	for _, k := range keys {
		labels = append(labels, k.Label)
	}
	sort.Strings(labels)
	return labels
}
