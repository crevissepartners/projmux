package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentQuestionWindowSecondsFile(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		content string
		missing bool
		dir     bool
		want    int
	}{
		{name: "missing", missing: true, want: 900},
		{name: "default", content: "900", want: 900},
		{name: "minimum", content: "60", want: 60},
		{name: "maximum", content: "3600", want: 3600},
		{name: "surrounding whitespace", content: "  120\n", want: 120},
		{name: "below the minimum", content: "59", want: 900},
		{name: "above the maximum", content: "3601", want: 900},
		{name: "zero", content: "0", want: 900},
		{name: "negative", content: "-5", want: 900},
		{name: "not a number", content: "abc", want: 900},
		{name: "empty", content: "", want: 900},
		{name: "exponent", content: "1e3", want: 900},
		{name: "fraction", content: "12.5", want: 900},
		{name: "unreadable directory", dir: true, want: 900},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := DefaultPaths(t.TempDir(), "").AgentQuestionWindowSecondsFile()
			switch {
			case test.dir:
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case !test.missing:
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(test.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := LoadAgentQuestionWindowSecondsFile(path)
			if got != test.want {
				t.Fatalf("seconds = %d (err %v), want %d", got, err, test.want)
			}
			if test.dir != (err != nil) {
				t.Fatalf("err = %v, want an error only for an unreadable file", err)
			}
		})
	}

	if got, err := LoadAgentQuestionWindowSecondsFile(""); got != DefaultAgentQuestionWindowSeconds || err != nil {
		t.Fatalf("empty path = %d, %v; want the default and no error", got, err)
	}
}
