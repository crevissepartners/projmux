package projmux_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/personacmd"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func clientAt(t *testing.T, dir string) *projmux.Client {
	t.Helper()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func cliAt(dir string, input []byte) *personacmd.Command {
	return personacmd.New("instructions", func() (config.Paths, error) { return config.Paths{ConfigDir: dir}, nil }, func() string { return "" }, nil, bytes.NewReader(input))
}
func runCLI(dir string, input []byte, args ...string) (string, error) {
	var out, stderr bytes.Buffer
	err := cliAt(dir, input).Run(args, &out, &stderr)
	return out.String(), err
}
func codeOf(t *testing.T, err error) projmux.Code {
	t.Helper()
	var r *projmux.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("expected typed Refusal, got %T: %v", err, err)
	}
	return r.Code
}

func TestInstructionCLIParity(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"Reviewer", "reviewer", "review-1", "..", ".hidden", "-flag", "a/b", ""} {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			sdkDir := filepath.Join(t.TempDir(), "sdk")
			cliDir := filepath.Join(t.TempDir(), "cli")
			content := []byte("raw\x00bytes\r\nwithout final newline")
			result, sdkErr := clientAt(t, sdkDir).WriteInstructions(ctx, name, content)
			out, cliErr := runCLI(cliDir, content, "set", name)
			if sdkErr != nil {
				code := codeOf(t, sdkErr)
				var input *coremetadata.InputError
				if !errors.As(cliErr, &input) || !strings.Contains(cliErr.Error(), string(code)) {
					t.Fatalf("CLI input refusal %v vs SDK %v", cliErr, sdkErr)
				}
				if _, err := os.Stat(filepath.Join(sdkDir, "personas")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid write created directory: %v", err)
				}
				return
			}
			if cliErr != nil {
				t.Fatal(cliErr)
			}
			if !strings.Contains(out, result.Digest) || !strings.Contains(out, fmt.Sprintf("(%d bytes)", result.Size)) {
				t.Fatalf("CLI output %q vs %+v", out, result)
			}
			sdkBytes, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			cliBytes, err := os.ReadFile(filepath.Join(cliDir, "personas", name+".md"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sdkBytes, content) || !bytes.Equal(sdkBytes, cliBytes) {
				t.Fatal("stored bytes differ")
			}
			read, err := clientAt(t, sdkDir).ReadInstructions(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			shown, err := runCLI(cliDir, nil, "show", name)
			if err != nil || shown != string(read.Content) {
				t.Fatalf("show %q, %v", shown, err)
			}
			for _, path := range []string{result.Path, filepath.Join(cliDir, "personas", name+".md")} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("file permission %s: %v", path, err)
				}
				info, err = os.Stat(filepath.Dir(path))
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("directory permission: %v", err)
				}
			}
			entries, err := clientAt(t, sdkDir).ListInstructions(ctx)
			if err != nil || len(entries) != 1 || entries[0].Digest != result.Digest {
				t.Fatalf("list %+v %v", entries, err)
			}
			listed, err := runCLI(cliDir, nil, "list")
			if err != nil || !strings.Contains(listed, result.Digest) {
				t.Fatalf("CLI list %q %v", listed, err)
			}
			if err := clientAt(t, sdkDir).DeleteInstructions(ctx, name); err != nil {
				t.Fatal(err)
			}
			deleted, err := runCLI(cliDir, nil, "delete", name, "--yes")
			if err != nil || deleted != "deleted instructions "+name+"\n" {
				t.Fatalf("delete %q %v", deleted, err)
			}
		})
	}
}

func TestInstructionRefusalsAndFilesystemParity(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"missing", "too-large", "manual-too-large", "in-use", "invalid-profile-in-use", "write-failure", "delete-failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "config")
			c := clientAt(t, dir)
			name := "reviewer"
			content := []byte("old bytes")
			if scenario != "missing" && scenario != "write-failure" {
				if _, err := c.WriteInstructions(ctx, name, content); err != nil {
					t.Fatal(err)
				}
			}
			var sdkErr, cliErr error
			var want projmux.Code
			switch scenario {
			case "missing":
				_, sdkErr = c.ReadInstructions(ctx, name)
				_, cliErr = runCLI(dir, nil, "show", name)
				want = projmux.CodeNotFound
				if codeOf(t, c.DeleteInstructions(ctx, name)) != want {
					t.Fatal("missing delete code")
				}
			case "too-large":
				big := bytes.Repeat([]byte("x"), 65537)
				_, sdkErr = c.WriteInstructions(ctx, name, big)
				_, cliErr = runCLI(dir, big, "set", name)
				want = projmux.CodeTooLarge
			case "manual-too-large":
				if err := os.WriteFile(filepath.Join(dir, "personas", name+".md"), bytes.Repeat([]byte("x"), 65537), 0o600); err != nil {
					t.Fatal(err)
				}
				_, sdkErr = c.ReadInstructions(ctx, name)
				_, cliErr = runCLI(dir, nil, "show", name)
				want = projmux.CodeTooLarge
			case "in-use", "invalid-profile-in-use":
				if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o700); err != nil {
					t.Fatal(err)
				}
				profileText := "instructions = \"reviewer\"\n"
				if scenario == "invalid-profile-in-use" {
					profileText += "roles = [\"shared\"]\n"
				}
				if err := os.WriteFile(filepath.Join(dir, "profiles", "guard.toml"), []byte(profileText), 0o600); err != nil {
					t.Fatal(err)
				}
				if scenario == "invalid-profile-in-use" {
					if err := os.WriteFile(filepath.Join(dir, "profiles", "other.toml"), []byte(profileText), 0o600); err != nil {
						t.Fatal(err)
					}
					entries, err := profile.NewDefaultStore(config.Paths{ConfigDir: dir}).List()
					if err != nil || len(entries) != 2 || entries[0].Valid || entries[1].Valid {
						t.Fatalf("invalid profile setup: %+v %v", entries, err)
					}
				}
				sdkErr = c.DeleteInstructions(ctx, name)
				_, cliErr = runCLI(dir, nil, "delete", name, "--yes")
				want = projmux.CodeInstructionsInUse
				var r *projmux.Refusal
				wantUsers := 1
				if scenario == "invalid-profile-in-use" {
					wantUsers = 2
				}
				if !errors.As(sdkErr, &r) || len(r.Users) != wantUsers || r.Users[0] != "guard" {
					t.Fatalf("users %v", sdkErr)
				}
			case "write-failure":
				if err := os.WriteFile(dir, []byte("blocked directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, sdkErr = c.WriteInstructions(ctx, name, content)
				_, cliErr = runCLI(dir, content, "set", name)
			case "delete-failure":
				if err := os.Remove(filepath.Join(dir, "personas", name+".md")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, "profiles")); err != nil {
					t.Fatal(err)
				}
				// A directory at the file path is refused as not found by both paths.
				if err := os.Mkdir(filepath.Join(dir, "personas", name+".md"), 0o700); err != nil {
					t.Fatal(err)
				}
				sdkErr = c.DeleteInstructions(ctx, name)
				_, cliErr = runCLI(dir, nil, "delete", name, "--yes")
				want = projmux.CodeNotFound
			}
			if sdkErr == nil || cliErr == nil {
				t.Fatalf("refusals SDK %v CLI %v", sdkErr, cliErr)
			}
			if want != "" {
				if codeOf(t, sdkErr) != want || !strings.Contains(cliErr.Error(), string(want)) {
					t.Fatalf("SDK %v CLI %v", sdkErr, cliErr)
				}
			} else {
				var refusal *projmux.Refusal
				if errors.As(sdkErr, &refusal) {
					t.Fatalf("filesystem error converted to refusal: %v", sdkErr)
				}
				if !strings.HasSuffix(cliErr.Error(), sdkErr.Error()) {
					t.Fatalf("filesystem errors differ: %v / %v", cliErr, sdkErr)
				}
			}
			if scenario == "too-large" || strings.Contains(scenario, "in-use") {
				actual, err := os.ReadFile(filepath.Join(dir, "personas", name+".md"))
				if err != nil || !bytes.Equal(actual, content) {
					t.Fatalf("refusal changed bytes: %q %v", actual, err)
				}
			}
		})
	}
}

func TestClientPathsAndCancellation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	defaults, err := projmux.NewClient(projmux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	explicit := clientAt(t, filepath.Join(root, "explicit"))
	other := clientAt(t, filepath.Join(root, "other"))
	// Captured defaults remain stable even when the embedding program changes HOME.
	t.Setenv("HOME", filepath.Join(root, "later"))
	ctx := context.Background()
	for i, c := range []*projmux.Client{defaults, explicit, other} {
		if _, err := c.WriteInstructions(ctx, "same", fmt.Append(nil, i)); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range []*projmux.Client{defaults, explicit, other} {
		r, err := c.ReadInstructions(ctx, "same")
		if err != nil || string(r.Content) != fmt.Sprint(i) {
			t.Fatalf("client %d: %+v %v", i, r, err)
		}
	}
	if os.Getenv("HOME") != filepath.Join(root, "later") || os.Getenv("XDG_CONFIG_HOME") != "" {
		t.Fatal("client changed global environment")
	}
	if _, err := os.Stat(filepath.Join(root, ".config", "projmux", "personas", "same.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := projmux.NewClient(projmux.Options{ConfigDir: "relative"}); err == nil {
		t.Fatal("relative path accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := explicit.WriteInstructions(canceled, "same", []byte("replacement")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := explicit.DeleteInstructions(canceled, "same"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := explicit.ReadInstructions(canceled, "same"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := explicit.ListInstructions(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r, err := explicit.ReadInstructions(ctx, "same")
	if err != nil || string(r.Content) != "1" {
		t.Fatalf("cancellation changed bytes: %+v %v", r, err)
	}
	// Returned content belongs to the caller.
	r.Content[0] = 'x'
	r, err = explicit.ReadInstructions(ctx, "same")
	if err != nil || string(r.Content) != "1" {
		t.Fatal("returned content aliases store")
	}
}

func TestCLIInvalidNameDoesNotReadInput(t *testing.T) {
	cmd := personacmd.New("instructions", func() (config.Paths, error) {
		t.Fatal("paths resolved before name validation")
		return config.Paths{}, nil
	}, func() string { return "" }, nil, panicReader{})
	if err := cmd.Run([]string{"set", "../bad"}, io.Discard, io.Discard); err == nil {
		t.Fatal("invalid name accepted")
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("invalid name read input") }
