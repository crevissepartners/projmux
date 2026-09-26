package config

import (
	"errors"
	"testing"
)

func TestResolveConfigHome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		home    string
		xdg     string
		want    string
		wantErr error
	}{
		{name: "unset", home: "/h", want: "/h/.config"},
		{name: "space is unset", home: "/h", xdg: " ", want: "/h/.config"},
		{name: "tab is unset", home: "/h", xdg: "\t", want: "/h/.config"},
		{name: "absolute", home: "/h", xdg: "/x", want: "/x"},
		{name: "trailing slash", home: "/h", xdg: "/x/", want: "/x"},
		{name: "surrounding space", home: "/h", xdg: " /x ", want: "/x"},
		{name: "relative is unset", home: "/h", xdg: "rel", want: "/h/.config"},
		{name: "blank without home", xdg: " ", wantErr: ErrHomeDirRequired},
		{name: "set without home", xdg: "/x", want: "/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveConfigHome(tc.home, tc.xdg)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolveConfigHome(%q, %q) error = %v, want %v", tc.home, tc.xdg, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ResolveConfigHome(%q, %q) = %q, want %q", tc.home, tc.xdg, got, tc.want)
			}
		})
	}
}

func TestHomesPathsTreatsBlankHomesAsUnset(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		homes     Homes
		wantCfg   string
		wantState string
		wantErr   error
	}{
		{name: "blank config home", homes: Homes{HomeDir: "/h", ConfigHome: " ", StateHome: "/s"}, wantCfg: "/h/.config/projmux", wantState: "/s/projmux"},
		{name: "blank state home", homes: Homes{HomeDir: "/h", ConfigHome: "/c", StateHome: " "}, wantCfg: "/c/projmux", wantState: "/h/.local/state/projmux"},
		{name: "trailing slash", homes: Homes{HomeDir: "/h", ConfigHome: "/x/"}, wantCfg: "/x/projmux", wantState: "/h/.local/state/projmux"},
		{name: "relative homes", homes: Homes{HomeDir: "/h", ConfigHome: "rel", StateHome: "./rel"}, wantCfg: "/h/.config/projmux", wantState: "/h/.local/state/projmux"},
		{name: "relative state home without home", homes: Homes{ConfigHome: "/c", StateHome: "~/s"}, wantErr: ErrHomeDirRequired},
		{name: "both blank without home", homes: Homes{ConfigHome: " ", StateHome: "\t"}, wantErr: ErrHomeDirRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := tc.homes.Paths()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Paths() error = %v, want %v", err, tc.wantErr)
			}
			if paths.ConfigDir != tc.wantCfg || paths.StateDir != tc.wantState {
				t.Fatalf("Paths() = %+v, want ConfigDir %q StateDir %q", paths, tc.wantCfg, tc.wantState)
			}
		})
	}
}

// TestResolveXDGHomes pins the one XDG base directory rule for all four
// homes: an absolute value is used, cleaned; an empty, blank, or relative one
// counts as unset and falls back under the home directory, or fails with
// ErrHomeDirRequired without one.
func TestResolveXDGHomes(t *testing.T) {
	t.Parallel()

	for _, resolver := range []struct {
		name     string
		resolve  func(homeDir, value string) (string, error)
		fallback string
	}{
		{name: "XDG_CONFIG_HOME", resolve: ResolveConfigHome, fallback: "/h/.config"},
		{name: "XDG_STATE_HOME", resolve: ResolveStateHome, fallback: "/h/.local/state"},
		{name: "XDG_DATA_HOME", resolve: ResolveDataHome, fallback: "/h/.local/share"},
		{name: "XDG_CACHE_HOME", resolve: ResolveCacheHome, fallback: "/h/.cache"},
	} {
		for _, tc := range []struct {
			name    string
			home    string
			value   string
			want    string
			wantErr error
		}{
			{name: "unset", home: "/h", want: resolver.fallback},
			{name: "empty", home: "/h", value: "", want: resolver.fallback},
			{name: "blank", home: "/h", value: " ", want: resolver.fallback},
			{name: "absolute", home: "/h", value: "/x", want: "/x"},
			{name: "absolute trailing slash", home: "/h", value: "/x/", want: "/x"},
			{name: "relative", home: "/h", value: "rel", want: resolver.fallback},
			{name: "dot relative", home: "/h", value: "./rel", want: resolver.fallback},
			{name: "unexpanded tilde", home: "/h", value: "~/x", want: resolver.fallback},
			{name: "unset without home", wantErr: ErrHomeDirRequired},
			{name: "relative without home", value: "rel", wantErr: ErrHomeDirRequired},
			{name: "absolute without home", value: "/x", want: "/x"},
		} {
			t.Run(resolver.name+"/"+tc.name, func(t *testing.T) {
				got, err := resolver.resolve(tc.home, tc.value)
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("resolve %s(%q, %q) error = %v, want %v", resolver.name, tc.home, tc.value, err, tc.wantErr)
				}
				if got != tc.want {
					t.Fatalf("resolve %s(%q, %q) = %q, want %q", resolver.name, tc.home, tc.value, got, tc.want)
				}
			})
		}
	}
}

// TestMissingHomeReasonNamesTheVariable pins the one missing-HOME reason:
// each resolver names its own XDG variable on one line, a blank home counts
// as none, and errors.Is still matches ErrHomeDirRequired.
func TestMissingHomeReasonNamesTheVariable(t *testing.T) {
	t.Parallel()

	for _, resolver := range []struct {
		name    string
		resolve func(homeDir, value string) (string, error)
	}{
		{name: XDGConfigHomeVar, resolve: ResolveConfigHome},
		{name: XDGStateHomeVar, resolve: ResolveStateHome},
		{name: XDGDataHomeVar, resolve: ResolveDataHome},
		{name: XDGCacheHomeVar, resolve: ResolveCacheHome},
	} {
		for _, home := range []string{"", " ", "\t"} {
			got, err := resolver.resolve(home, "rel")
			var missing *MissingHomeError
			if got != "" || !errors.As(err, &missing) || missing.Var != resolver.name || !errors.Is(err, ErrHomeDirRequired) {
				t.Fatalf("%s(home %q) = %q, %v; want a MissingHomeError for %s", resolver.name, home, got, err, resolver.name)
			}
			if want := "HOME or an absolute " + resolver.name + " is required"; err.Error() != want {
				t.Fatalf("%s error = %q, want %q", resolver.name, err, want)
			}
		}
	}

	if _, err := (Homes{ConfigHome: "/c"}).Paths(); err == nil || err.Error() != "HOME or an absolute XDG_STATE_HOME is required" {
		t.Fatalf("Paths() with only XDG_CONFIG_HOME error = %v, want the XDG_STATE_HOME reason", err)
	}
}
