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
		{name: "relative is kept", home: "/h", xdg: "rel", want: "rel"},
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
