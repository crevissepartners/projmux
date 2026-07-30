package terminaltext

import "testing"

func TestEscapeControlsBlocksTerminalSequencesAndPreservesUnicode(t *testing.T) {
	t.Parallel()

	input := "팀\x1b]52;c;secret\a\x1b[31m\u009bred\u009dtitle\u200e\nnext\t"
	got := EscapeControls(input)
	want := `팀\x1b]52;c;secret\x07\x1b[31m\x9bred\x9dtitle\u200e\nnext\t`
	if got != want {
		t.Fatalf("EscapeControls() = %q, want %q", got, want)
	}
}
