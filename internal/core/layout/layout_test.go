package layout

import "testing"

func TestStripComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "no comment", line: `key = "value"`, want: `key = "value"`},
		{name: "trailing comment", line: `key = "value" # note`, want: `key = "value" `},
		{name: "hash inside double quotes", line: `key = "a#b" # note`, want: `key = "a#b" `},
		{name: "hash inside single quotes is not quoted", line: `key = 'a#b'`, want: `key = 'a`},
		{name: "escaped quote inside double quotes", line: `key = "a\"#b" # note`, want: `key = "a\"#b" `},
		{name: "escaped backslash closes string", line: `key = "a\\" # note`, want: `key = "a\\" `},
		{name: "backslash outside string does not escape quote", line: `key = a\"#b`, want: `key = a\"#b`},
		{name: "empty line", line: ``, want: ``},
		{name: "only a comment", line: `# comment`, want: ``},
		{name: "indented comment", line: `  # comment`, want: `  `},
		{name: "unterminated string keeps hash", line: `key = "a#b`, want: `key = "a#b`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := StripComment(tt.line); got != tt.want {
				t.Fatalf("StripComment(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}
