package sessions

import "testing"

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
			name: "dotfiles directory keeps dedicated session name",
			dir:  "/home/es5h/dotfiles",
			want: "dotfiles",
		},
		{
			name: "home trailing slash keeps shell parity",
			dir:  "/home/es5h/",
			want: "home-es5h",
		},
		{
			name: "dotfiles trailing slash keeps shell parity",
			dir:  "/home/es5h/dotfiles/",
			want: "es5h-dotfiles",
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
