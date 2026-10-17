package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestProfileInspectCLIAndSDKParity(t *testing.T) {
	for _, verb := range []string{"parse", "preview"} {
		for _, content := range []string{"", profileTestContent, "model=", string([]byte{0xff}), "unknown=\"value\"", "[other]", "roles=[\"same\",\"same\"]", "provider=\"unknown\"", strings.Repeat(" ", profile.MaxSize+1)} {
			cmd, dir := newProfileTestCommand(t, content)
			client, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Dir(dir)})
			if err != nil {
				t.Fatal(err)
			}
			args := []string{verb}
			if verb == "preview" {
				args = append(args, "candidate")
			}
			output, _, cliErr := runProfile(cmd, args...)
			var result any
			var sdkErr error
			if verb == "parse" {
				result, sdkErr = client.ParseProfile(context.Background(), []byte(content))
			} else {
				result, sdkErr = client.PreviewProfile(context.Background(), "candidate", []byte(content))
			}
			if sdkErr != nil {
				var refusal *projmux.Refusal
				if !errors.As(sdkErr, &refusal) || cliErr == nil || !strings.Contains(cliErr.Error(), string(refusal.Code)) || exitCodeOf(cliErr) != 2 || output != "" {
					t.Fatalf("%s CLI %q %v vs SDK %v", verb, output, cliErr, sdkErr)
				}
			} else {
				expected, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if cliErr != nil || strings.TrimSpace(output) != string(expected) || strings.Contains(output, "null") {
					t.Fatalf("%s CLI %q %v vs SDK %s", verb, output, cliErr, expected)
				}
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inspect created store: %v", err)
			}
		}
	}
}

func TestProfileInspectInputRoutesAndErrors(t *testing.T) {
	for _, verb := range []string{"parse", "preview"} {
		for _, mode := range []string{"implicit", "stdin", "file", "missing", "syntax"} {
			cmd, _ := newProfileTestCommand(t, "model=\"opus\"")
			args := []string{verb}
			if verb == "preview" {
				args = append(args, "candidate")
			}
			switch mode {
			case "stdin":
				args = append(args, "--file", "-")
			case "file":
				path := filepath.Join(t.TempDir(), "input.toml")
				if err := os.WriteFile(path, []byte("model=\"opus\""), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--file", path)
			case "missing":
				args = append(args, "--file", filepath.Join(t.TempDir(), "missing"))
			case "syntax":
				args = append(args, "extra")
			}
			out, _, err := runProfile(cmd, args...)
			wantCode := 0
			if mode == "missing" {
				wantCode = 1
			}
			if mode == "syntax" {
				wantCode = 2
			}
			if exitCodeOf(err) != wantCode {
				t.Fatalf("%s %s: %q %v", verb, mode, out, err)
			}
		}
	}
	// Parse must work even if HOME resolution would fail, and preview validates
	// names before a reader is touched.
	cmd := &profileCommand{homeDir: func() (string, error) { t.Fatal("parse consulted HOME"); return "", nil }, lookupEnv: func(string) string { return "" }, stdin: bytes.NewReader(nil)}
	if out, _, err := runProfile(cmd, "parse"); err != nil || !strings.Contains(out, `"roles":[]`) {
		t.Fatalf("%s %v", out, err)
	}
	cmd.stdin = profileUnreadableInput{}
	if _, _, err := runProfile(cmd, "preview", "../invalid"); exitCodeOf(err) != 2 {
		t.Fatal(err)
	}
}

type profileUnreadableInput struct{}

func (profileUnreadableInput) Read([]byte) (int, error) { panic("invalid name read stdin") }

func TestProfileSetOutputUnchangedAfterPreview(t *testing.T) {
	cmd, dir := newProfileTestCommand(t, "model=\"opus\"")
	if _, _, err := runProfile(cmd, "preview", "candidate"); err != nil {
		t.Fatal(err)
	}
	cmd.stdin = strings.NewReader("model=\"opus\"")
	out, _, err := runProfile(cmd, "set", "candidate")
	content := []byte("model=\"opus\"")
	want := "set profile candidate " + profile.Digest(content) + " (12 bytes) at " + filepath.Join(dir, "candidate.toml") + "\n"
	if err != nil || !reflect.DeepEqual(out, want) {
		t.Fatalf("got %q %v want %q", out, err, want)
	}
}
