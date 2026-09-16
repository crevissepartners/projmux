package termview

import (
	"reflect"
	"testing"
)

func TestParseSGR(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []Run
	}{
		{"plain", "hello", []Run{{Text: "hello"}}},
		{"empty", "", []Run{}},
		{
			"basic colours and reset",
			"\x1b[31mred\x1b[0m plain \x1b[42;97mbg\x1b[39;49mdefault",
			[]Run{
				{Text: "red", FG: ansi16[1]},
				{Text: " plain "},
				{Text: "bg", FG: ansi16[15], BG: ansi16[2]},
				{Text: "default"},
			},
		},
		{
			"bare SGR resets",
			"\x1b[1;4mx\x1b[my",
			[]Run{{Text: "x", Bold: true, Underline: true}, {Text: "y"}},
		},
		{
			"256 colours",
			"\x1b[38;5;196ma\x1b[48;5;3mb\x1b[38;5;244mc",
			[]Run{
				{Text: "a", FG: "rgb(255,0,0)"},
				{Text: "b", FG: "rgb(255,0,0)", BG: ansi16[3]},
				{Text: "c", FG: "rgb(128,128,128)", BG: ansi16[3]},
			},
		},
		{
			"truecolour with colon form and clamping",
			"\x1b[38:2:10:20:30mt\x1b[48;2;300;0;5mu",
			[]Run{
				{Text: "t", FG: "rgb(10,20,30)"},
				{Text: "u", FG: "rgb(10,20,30)", BG: "rgb(255,0,5)"},
			},
		},
		{
			"bold, dim, reverse and their resets",
			"\x1b[1;2;3;7mA\x1b[22mB\x1b[27;23mC",
			[]Run{
				{Text: "A", Bold: true, Dim: true, Italic: true, Reverse: true},
				{Text: "B", Italic: true, Reverse: true},
				{Text: "C"},
			},
		},
		{
			"malformed extended colour stops",
			"\x1b[1;38;5mx",
			[]Run{{Text: "x", Bold: true}},
		},
		{
			"control characters and non-SGR escapes are dropped",
			"a\x07b\x00c\td\x1b[2Ke\x1b[?25hf\x1b(Bg\x1b=h\x1b",
			[]Run{{Text: "abc\tdefgh"}},
		},
		{
			"hyperlinks drop their URL but keep their text",
			"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ \x1b]0;title\x07x \x1b]8;;open",
			[]Run{{Text: "link x "}},
		},
		{
			"private SGR-shaped sequence is not an attribute",
			"\x1b[>4;2mx",
			[]Run{{Text: "x"}},
		},
		{
			"multibyte text is kept whole",
			"\x1b[32mé世\U0001F600",
			[]Run{{Text: "é世\U0001F600", FG: ansi16[2]}},
		},
		{
			"unterminated CSI leaves the rest as text",
			"x\x1b[31",
			[]Run{{Text: "x[31"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSGR(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseSGR(%q)\n got %#v\nwant %#v", tc.line, got, tc.want)
			}
		})
	}
}

func TestXterm256(t *testing.T) {
	cases := map[int]string{
		-1:  "",
		256: "",
		0:   ansi16[0],
		15:  ansi16[15],
		16:  "rgb(0,0,0)",
		21:  "rgb(0,0,255)",
		231: "rgb(255,255,255)",
		232: "rgb(8,8,8)",
		255: "rgb(238,238,238)",
	}
	for index, want := range cases {
		if got := xterm256(index); got != want {
			t.Errorf("xterm256(%d) = %q, want %q", index, got, want)
		}
	}
}
