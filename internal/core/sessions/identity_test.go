package sessions

import (
	"errors"
	"testing"
)

func TestSessionNameParity(t *testing.T) {
	t.Parallel()

	namer := NewNamer("/home/es5h")

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{
			name: "home directory maps to home session",
			dir:  "/home/es5h",
			want: "home",
		},
		{
			name: "home trailing slash keeps shell parity",
			dir:  "/home/es5h/",
			want: "home-es5h",
		},
		{
			name: "home child directory uses parent and base names",
			dir:  "/home/es5h/workspace",
			want: "es5h-workspace",
		},
		{
			name: "project sessions include parent and base directory",
			dir:  "/home/es5h/source/repos/projmux",
			want: "repos-projmux",
		},
		{
			name: "project sessions sanitize parent and base names",
			dir:  "/var/tmp/repo.name:feature/path",
			want: "repo_name_feature-path",
		},
		{
			name: "top level directories fall back to basename only",
			dir:  "/tmp",
			want: "tmp",
		},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := namer.SessionName(tt.dir)
			if got != tt.want {
				t.Fatalf("SessionName(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

func TestSanitizeParity(t *testing.T) {
	t.Parallel()

	got := Sanitize("repo.name: feature/path")
	want := "repo_name_-feature-path"

	if got != want {
		t.Fatalf("Sanitize() = %q, want %q", got, want)
	}
}

func TestValidateSessionName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"repos-projmux", "home", "a.b", "repos-projmux--prj-01"} {
		if err := ValidateSessionName(name); err != nil {
			t.Fatalf("ValidateSessionName(%q) error = %v", name, err)
		}
	}
	for _, name := range []string{"", "   ", ".", "..", "/abs", "a/b", `a\b`} {
		if err := ValidateSessionName(name); !errors.Is(err, ErrInvalidSessionName) {
			t.Fatalf("ValidateSessionName(%q) error = %v, want ErrInvalidSessionName", name, err)
		}
	}
}
