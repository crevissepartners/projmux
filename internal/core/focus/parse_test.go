package focus

import (
	"errors"
	"testing"
)

func TestParse_Forms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		spec string
		want Target
	}{
		{
			name: "session only",
			spec: "sess",
			want: Target{Raw: "sess", Session: "sess"},
		},
		{
			name: "session and window index",
			spec: "sess:1",
			want: Target{
				Raw:            "sess:1",
				Session:        "sess",
				WindowIndex:    1,
				HasWindowIndex: true,
			},
		},
		{
			name: "session window pane indices",
			spec: "sess:1.0",
			want: Target{
				Raw:            "sess:1.0",
				Session:        "sess",
				WindowIndex:    1,
				HasWindowIndex: true,
				PaneIndex:      0,
				HasPaneIndex:   true,
			},
		},
		{
			name: "session window id",
			spec: "sess:@7",
			want: Target{
				Raw:      "sess:@7",
				Session:  "sess",
				WindowID: "@7",
			},
		},
		{
			name: "session window index pane id",
			spec: "sess:1.%12",
			want: Target{
				Raw:            "sess:1.%12",
				Session:        "sess",
				WindowIndex:    1,
				HasWindowIndex: true,
				PaneID:         "%12",
			},
		},
		{
			name: "session window id and pane id",
			spec: "sess:@4.%9",
			want: Target{
				Raw:      "sess:@4.%9",
				Session:  "sess",
				WindowID: "@4",
				PaneID:   "%9",
			},
		},
		{
			name: "leading and trailing whitespace",
			spec: "  sess:2.1  ",
			want: Target{
				Raw:            "sess:2.1",
				Session:        "sess",
				WindowIndex:    2,
				HasWindowIndex: true,
				PaneIndex:      1,
				HasPaneIndex:   true,
			},
		},
		{
			name: "session with dashes",
			spec: "team-projmux:0",
			want: Target{
				Raw:            "team-projmux:0",
				Session:        "team-projmux",
				WindowIndex:    0,
				HasWindowIndex: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tc.spec)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tc.spec, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q) = %#v, want %#v", tc.spec, got, tc.want)
			}
		})
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		spec string
	}{
		{name: "empty", spec: ""},
		{name: "whitespace", spec: "   "},
		{name: "missing session", spec: ":1"},
		{name: "missing window after colon", spec: "sess:"},
		{name: "missing pane after dot", spec: "sess:1."},
		{name: "negative window index", spec: "sess:-1"},
		{name: "negative pane index", spec: "sess:1.-2"},
		{name: "non-numeric window", spec: "sess:abc"},
		{name: "non-numeric pane", spec: "sess:1.abc"},
		{name: "bare at", spec: "sess:@"},
		{name: "bare percent", spec: "sess:1.%"},
		{name: "non-numeric window id", spec: "sess:@a"},
		{name: "non-numeric pane id", spec: "sess:1.%a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(tc.spec); err == nil {
				t.Fatalf("Parse(%q) returned nil error, want error", tc.spec)
			}
		})
	}
}

func TestParse_EmptyReturnsSentinel(t *testing.T) {
	t.Parallel()
	if _, err := Parse(""); !errors.Is(err, ErrEmptyTarget) {
		t.Fatalf("Parse(\"\") error = %v, want ErrEmptyTarget", err)
	}
}

func TestTarget_Selectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		target   Target
		wantHasW bool
		wantHasP bool
		wantWSel string
		wantPSel string
	}{
		{
			name:     "session only",
			target:   Target{Session: "sess"},
			wantHasW: false,
			wantHasP: false,
		},
		{
			name: "window id wins over index",
			target: Target{
				Session:        "sess",
				WindowIndex:    1,
				HasWindowIndex: true,
				WindowID:       "@7",
			},
			wantHasW: true,
			wantWSel: "@7",
		},
		{
			name: "pane id wins over index",
			target: Target{
				Session:        "sess",
				WindowIndex:    1,
				HasWindowIndex: true,
				PaneIndex:      2,
				HasPaneIndex:   true,
				PaneID:         "%9",
			},
			wantHasW: true,
			wantHasP: true,
			wantWSel: "1",
			wantPSel: "%9",
		},
		{
			name: "indices only",
			target: Target{
				Session:        "sess",
				WindowIndex:    3,
				HasWindowIndex: true,
				PaneIndex:      0,
				HasPaneIndex:   true,
			},
			wantHasW: true,
			wantHasP: true,
			wantWSel: "3",
			wantPSel: "0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.target.HasWindow(); got != tc.wantHasW {
				t.Fatalf("HasWindow = %v, want %v", got, tc.wantHasW)
			}
			if got := tc.target.HasPane(); got != tc.wantHasP {
				t.Fatalf("HasPane = %v, want %v", got, tc.wantHasP)
			}
			if got := tc.target.WindowSelector(); got != tc.wantWSel {
				t.Fatalf("WindowSelector = %q, want %q", got, tc.wantWSel)
			}
			if got := tc.target.PaneSelector(); got != tc.wantPSel {
				t.Fatalf("PaneSelector = %q, want %q", got, tc.wantPSel)
			}
		})
	}
}
