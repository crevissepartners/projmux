package app

import "testing"

func TestCanonicalNpmBinaryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "npm retire dir directly under node_modules",
			input: "/home/u/.nvm/versions/node/v24.15.0/lib/node_modules/.projmux-lvpOxyM9/node_modules/@projmux/linux-x64/bin/projmux",
			want:  "/home/u/.nvm/versions/node/v24.15.0/lib/node_modules/projmux/node_modules/@projmux/linux-x64/bin/projmux",
		},
		{
			name:  "npm retire dir under scoped package directory",
			input: "/p/node_modules/@projmux/.linux-x64-AbCdEfGh/bin/projmux",
			want:  "/p/node_modules/@projmux/linux-x64/bin/projmux",
		},
		{
			name:  "both segments rewritten",
			input: "/p/node_modules/.projmux-AbCdEfGh/node_modules/@projmux/.linux-x64-ZyXwVuTs/bin/projmux",
			want:  "/p/node_modules/projmux/node_modules/@projmux/linux-x64/bin/projmux",
		},
		{
			name:  "unaffected npm dot-entry: .bin has no suffix",
			input: "/p/node_modules/.bin/projmux",
			want:  "/p/node_modules/.bin/projmux",
		},
		{
			name:  "unaffected npm dot-entry: dotted filename, not a hash suffix",
			input: "/p/node_modules/.package-lock.json",
			want:  "/p/node_modules/.package-lock.json",
		},
		{
			name:  "matching dir name outside node_modules is left alone",
			input: "/home/u/.projmux-lvpOxyM9/bin/projmux",
			want:  "/home/u/.projmux-lvpOxyM9/bin/projmux",
		},
		{
			name:  "plain system path",
			input: "/usr/local/bin/projmux",
			want:  "/usr/local/bin/projmux",
		},
		{
			name:  "plain go install path",
			input: "/home/u/go/bin/projmux",
			want:  "/home/u/go/bin/projmux",
		},
		{
			name:  "empty path",
			input: "",
			want:  "",
		},
		{
			name:  "hash suffix too short (7 chars)",
			input: "/p/node_modules/.projmux-abcdefg",
			want:  "/p/node_modules/.projmux-abcdefg",
		},
		{
			name:  "hash suffix too long (9 chars)",
			input: "/p/node_modules/.projmux-abcdefghi",
			want:  "/p/node_modules/.projmux-abcdefghi",
		},
		{
			name:  "hash suffix contains non-alphanumeric character",
			input: "/p/node_modules/.projmux-abc_defg",
			want:  "/p/node_modules/.projmux-abc_defg",
		},
		{
			name:  "greedy capture anchors the 8-char group at the end",
			input: "/p/node_modules/.foo-12345678-abcdefgh/bin/x",
			want:  "/p/node_modules/foo-12345678/bin/x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := canonicalNpmBinaryPath(tt.input); got != tt.want {
				t.Fatalf("canonicalNpmBinaryPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
