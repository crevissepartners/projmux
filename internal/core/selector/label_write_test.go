package selector

import (
	"errors"
	"strings"
	"testing"
)

// TestParseLabelChangeSeparatesSetFromRemoveByShape is the write-grammar table.
// Each row states which half of the grammar the operand lands in, and the
// refusals state a distinguishable reason rather than one shared "bad label".
func TestParseLabelChangeSeparatesSetFromRemoveByShape(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		raw        string
		want       LabelChange
		wantReason string
	}{
		{name: "set", raw: "role=worker", want: LabelChange{Key: "role", Value: "worker"}},
		{name: "set to the empty value", raw: "role=", want: LabelChange{Key: "role", Value: ""}},
		{name: "remove", raw: "role-", want: LabelChange{Key: "role", Remove: true}},
		{
			// The key/value split is the first `=`, so a value may carry any
			// number of them. This is the property a URL-shaped value depends on.
			name: "value keeps every later separator", raw: "url=https://example.test/?a=1&b=2",
			want: LabelChange{Key: "url", Value: "https://example.test/?a=1&b=2"},
		},
		{
			// `=` wins over the removal suffix: the operand is a set whose value
			// happens to end in a hyphen, never a removal of the key `a=b`.
			name: "set whose value ends in a hyphen", raw: "phase=task-0-",
			want: LabelChange{Key: "phase", Value: "task-0-"},
		},
		{
			// A key ending in a hyphen is reachable through the set spelling,
			// which is what keeps the grammar total rather than leaving those
			// keys unwritable.
			name: "set of a key ending in a hyphen", raw: "role-=worker",
			want: LabelChange{Key: "role-", Value: "worker"},
		},
		{
			name: "trims both halves the way ParseLabel does", raw: "  role  =  worker  ",
			want: LabelChange{Key: "role", Value: "worker"},
		},
		{name: "bare key", raw: "role", wantReason: "must be key=value to set a label or key- to remove one"},
		{name: "empty operand", raw: "", wantReason: "must be key=value to set a label or key- to remove one"},
		{name: "set with an empty key", raw: "=worker", wantReason: "has an empty key"},
		{name: "remove with an empty key", raw: "-", wantReason: "has an empty key"},
		{name: "whitespace key", raw: "   =worker", wantReason: "has an empty key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLabelChange(test.raw)
			if test.wantReason != "" {
				if err == nil {
					t.Fatalf("ParseLabelChange(%q) = %+v, want a refusal", test.raw, got)
				}
				if !strings.Contains(err.Error(), test.wantReason) {
					t.Fatalf("ParseLabelChange(%q) error = %v, want it to say %q", test.raw, err, test.wantReason)
				}
				var selectorErr *SelectorError
				if !errors.As(err, &selectorErr) || !selectorErr.MetadataUsageError() {
					t.Fatalf("ParseLabelChange(%q) error is not an operator-input error: %v", test.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLabelChange(%q) error = %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ParseLabelChange(%q) = %+v, want %+v", test.raw, got, test.want)
			}
		})
	}
}

// TestIsLabelChangeOperandSplitsByShapeOnly proves the reference/operand split
// consults nothing but the token: a name is whatever carries neither `=` nor a
// trailing `-`.
func TestIsLabelChangeOperandSplitsByShapeOnly(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]bool{
		"role=worker":       true,
		"role=":             true,
		"role-":             true,
		"-":                 true,
		"=":                 true,
		"alpha":             false,
		"uid:pane-abc":      false,
		"lead-ship-task0":   false,
		"uid:agent-abc-def": false,
	} {
		if got := IsLabelChangeOperand(raw); got != want {
			t.Fatalf("IsLabelChangeOperand(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestParseLabelChangesRefusesOneKeyChangedTwice keeps argv order from deciding
// a write the operator did not spell out.
func TestParseLabelChangesRefusesOneKeyChangedTwice(t *testing.T) {
	t.Parallel()

	for _, operands := range [][]string{
		{"role=a", "role=b"},
		{"role=a", "role-"},
		{"role-", "role=a"},
		{"role-", "role-"},
	} {
		if _, err := ParseLabelChanges(operands); err == nil {
			t.Fatalf("ParseLabelChanges(%q) accepted one key changed twice", operands)
		} else if !strings.Contains(err.Error(), "is changed twice") {
			t.Fatalf("ParseLabelChanges(%q) error = %v, want it to name the repeat", operands, err)
		}
	}

	changes, err := ParseLabelChanges([]string{"role=worker", "phase-", "tier="})
	if err != nil {
		t.Fatalf("ParseLabelChanges error = %v", err)
	}
	set, remove := LabelChangeSets(changes)
	if len(set) != 2 || set["role"] != "worker" || set["tier"] != "" {
		t.Fatalf("LabelChangeSets set = %v, want role=worker and an empty tier", set)
	}
	if len(remove) != 1 || remove[0] != "phase" {
		t.Fatalf("LabelChangeSets remove = %v, want [phase]", remove)
	}
}

// TestFormatLabelsIsStableAndUsesTheSetSpelling keeps the result line readable
// back as input.
func TestFormatLabelsIsStableAndUsesTheSetSpelling(t *testing.T) {
	t.Parallel()

	if got := FormatLabels(nil); got != "" {
		t.Fatalf("FormatLabels(nil) = %q, want the empty string", got)
	}
	got := FormatLabels(map[string]string{"role": "worker", "phase": "task-0", "tier": ""})
	if want := "phase=task-0 role=worker tier="; got != want {
		t.Fatalf("FormatLabels = %q, want %q", got, want)
	}
}

// TestFormatLabelsQuotesTokensThatWouldBreakTheLine is the quoting table. A
// key or value carrying a rune strconv.IsPrint rejects, invalid UTF-8, or a
// leading `"` renders in strconv.Quote form; everything else, including a space
// and printable non-ASCII text, renders exactly as stored.
func TestFormatLabelsQuotesTokensThatWouldBreakTheLine(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "newline", key: "k", value: "line1\nline2", want: `k="line1\nline2"`},
		{name: "tab", key: "tab", value: "a\tb", want: `tab="a\tb"`},
		{name: "carriage return", key: "cr", value: "before\rafter", want: `cr="before\rafter"`},
		{name: "escape sequence", key: "esc", value: "x\x1b[31mred\x1b[0m", want: `esc="x\x1b[31mred\x1b[0m"`},
		{name: "C0 control", key: "nul", value: "a\x01b", want: `nul="a\x01b"`},
		{name: "C1 next line", key: "c1", value: "a\u0085b", want: `c1="a\u0085b"`},
		{name: "line separator", key: "ls", value: "a\u2028b", want: `ls="a\u2028b"`},
		{name: "invalid UTF-8", key: "bad", value: "a\xffb", want: `bad="a\xffb"`},
		{name: "leading quote", key: "q", value: `"quoted"`, want: `q="\"quoted\""`},
		{name: "inner quote stays", key: "q", value: `say "hi"`, want: `q=say "hi"`},
		{name: "empty value", key: "tier", value: "", want: "tier="},
		{name: "space", key: "note", value: "two words", want: "note=two words"},
		{name: "printable non-ASCII", key: "unicode", value: "레이블", want: "unicode=레이블"},
		{name: "control character in the key", key: "bad\nkey", value: "v", want: `"bad\nkey"=v`},
		{name: "both quoted", key: "k\t", value: "v\r", want: `"k\t"="v\r"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := FormatLabels(map[string]string{test.key: test.value})
			if got != test.want {
				t.Fatalf("FormatLabels(%q=%q) = %s, want %s", test.key, test.value, got, test.want)
			}
			if strings.ContainsAny(got, "\n\r\t\x1b") {
				t.Fatalf("FormatLabels(%q=%q) = %q still carries a raw control character", test.key, test.value, got)
			}
		})
	}
}

// TestFormatLabelsLeavesControlFreePairsByteIdentical pins that the quoting is
// invisible to every map without a token it applies to: the line is the plain
// sorted `key=value` join it was before quoting existed.
func TestFormatLabelsLeavesControlFreePairsByteIdentical(t *testing.T) {
	t.Parallel()

	labels := map[string]string{
		"role":    "epic-worker",
		"note":    "two words",
		"unicode": "레이블",
		"url":     "https://example.test/merge_requests/1?tab=diffs",
		"inner":   `say "hi"`,
		"tier":    "",
	}
	want := `inner=say "hi" note=two words role=epic-worker tier= unicode=레이블 url=https://example.test/merge_requests/1?tab=diffs`
	if got := FormatLabels(labels); got != want {
		t.Fatalf("FormatLabels = %q, want %q", got, want)
	}
}

// TestWriteGrammarAcceptsEveryValueTheReadGrammarProduces pins the one property
// the two grammars must share: a value written by `key=value` is spelled the
// same way `--selector key=value` spells it, so a label is selectable by the
// text that wrote it.
func TestWriteGrammarAcceptsEveryValueTheReadGrammarProduces(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"role=worker", "role=", "  role = worker  ", "url=https://example.test/?a=1",
		"note=one two three", "emoji=✅", "long=" + strings.Repeat("x", 4096),
	} {
		read, readErr := ParseLabel(raw)
		write, writeErr := ParseLabelChange(raw)
		if (readErr == nil) != (writeErr == nil) {
			t.Fatalf("%q: read error = %v, write error = %v; the two grammars disagree on acceptance", raw, readErr, writeErr)
		}
		if readErr != nil {
			continue
		}
		if write.Remove {
			t.Fatalf("%q parsed as a removal on the write path", raw)
		}
		if read.Key != write.Key || read.Value != write.Value {
			t.Fatalf("%q: read %+v, write %+v; a written label would not match the selector that wrote it", raw, read, write)
		}
	}
}
