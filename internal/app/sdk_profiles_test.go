package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/profile"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func profileSDKAt(t *testing.T, cmd *profileCommand) *projmux.Client {
	t.Helper()
	paths, err := configPaths(cmd.homeDir, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func profileCLI(cmd *profileCommand, input []byte, args ...string) (string, error) {
	cmd.stdin = bytes.NewReader(input)
	var out, stderr bytes.Buffer
	err := cmd.Run(args, &out, &stderr)
	return out.String(), err
}
func profileCode(t *testing.T, err error) projmux.Code {
	t.Helper()
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("expected Refusal: %T %v", err, err)
	}
	return refusal.Code
}
func profileCells(p core.ProfileInfo) []string {
	return []string{p.Name, p.Source, p.Provider, p.Instructions, p.Model, p.Effort, p.Roles, p.Digest, p.Valid, p.Scope}
}
func TestProfileSDKCLIParity(t *testing.T) {
	cmd, _ := newProfileTestCommand(t, "")
	c := profileSDKAt(t, cmd)
	ctx := context.Background()
	empty, err := c.ListProfiles(ctx, "")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list %+v %v", empty, err)
	}
	contents := map[string]string{"global": "", "complete": "provider = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\nroles = [\"reader\",\"writer\"]\n", "p": "project = \"" + profileProjectP + "\"", "q": "project = \"" + profileProjectQ + "\""}
	for name, text := range contents {
		result, err := c.WriteProfile(ctx, name, []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(result.Path)
		if err != nil || string(got) != text || result.Size != int64(len(text)) {
			t.Fatalf("write %+v %v", result, err)
		}
		for path, mode := range map[string]os.FileMode{result.Path: 0600, filepath.Dir(result.Path): 0700} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("permissions %s %v", path, err)
			}
		}
		read, err := c.ReadProfile(ctx, name)
		shown, cliErr := profileCLI(cmd, nil, "show", name)
		if err != nil || cliErr != nil || shown != string(read.Content) {
			t.Fatalf("read parity %v %v", err, cliErr)
		}
		read.Content = append(read.Content, 'x')
		again, _ := c.ReadProfile(ctx, name)
		if string(again.Content) != text {
			t.Fatal("read aliases store")
		}
		// Replacing through CLI has the same raw bytes and permissions.
		out, err := profileCLI(cmd, []byte(text), "set", name)
		if err != nil || out != fmt.Sprintf("set profile %s %s (%d bytes) at %s\n", result.Name, result.Digest, result.Size, result.Path) {
			t.Fatalf("set %q %v", out, err)
		}
		info, _ := os.Stat(result.Path)
		got, _ = os.ReadFile(result.Path)
		if info.Mode().Perm() != 0600 || string(got) != text {
			t.Fatal("CLI storage differs")
		}
	}
	paths, _ := cmd.service().Paths()
	for name, text := range map[string]string{"broken": "model =", "invalid": "instructions = \"missing\""} {
		if err := os.WriteFile(filepath.Join(paths.ConfigDir, "profiles", name+".toml"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		read, err := c.ReadProfile(ctx, name)
		shown, cliErr := profileCLI(cmd, nil, "show", name)
		if err != nil || cliErr != nil || string(read.Content) != text || shown != text {
			t.Fatalf("invalid raw read %v %v", err, cliErr)
		}
	}
	// Use the existing real Registry fixture and exact Project refs.
	registry := profileProjectRegistry(t, resourceFixtureRegistry(t))
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	path := storemetadata.PathFor(paths.StateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "alpha", "uid:" + profileProjectP} {
		list, err := c.ListProfiles(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"list"}
		if ref != "" {
			args = append(args, "--project", ref)
		}
		out, err := profileCLI(cmd, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != len(list)+1 {
			t.Fatalf("rows %q vs %+v", out, list)
		}
		for i, p := range list {
			// Invalid VALID cells contain a space; normalize that cell to compare all ten.
			row := strings.ReplaceAll(lines[i+1], "no (", "no(")
			fields := strings.Fields(row)
			want := profileCells(p)
			want[8] = strings.ReplaceAll(want[8], "no (", "no(")
			if !reflect.DeepEqual(fields, want) {
				t.Fatalf("cells %v vs %v", fields, want)
			}
		}
	}
	if err := c.DeleteProfile(ctx, "complete", true); err != nil {
		t.Fatal(err)
	}
	if _, err := profileCLI(cmd, nil, "delete", "global", "--yes"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"complete", "global"} {
		if _, err := os.Stat(filepath.Join(paths.ConfigDir, "profiles", name+".toml")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("delete parity", err)
		}
	}
}

func TestProfileSDKRefusalParity(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name, text string
		code       projmux.Code
	}{
		{"bad/name", "", projmux.CodeProfileNameInvalid}, {"none", "", projmux.CodeProfileNameReserved},
		{"big", strings.Repeat("x", profile.MaxSize+1), projmux.CodeProfileTooLarge},
		{"syntax", "model =", projmux.CodeProfileSyntaxInvalid},
		{"key", "unknown = \"x\"", projmux.CodeProfileKeyUnknown},
		{"table", "[unknown]", projmux.CodeProfileTableUnknown},
		{"value", "effort = \"bad\"", projmux.CodeProfileValueInvalid},
		{"instructions", "instructions = \"absent\"", projmux.CodeProfileInstructionsNotFound},
		{"roles", "roles = [\"a\",\"a\"]", projmux.CodeProfileRoleDuplicate},
		{"provider", "provider = \"bad\"", projmux.CodeProfileProviderUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _ := newProfileTestCommand(t, "")
			c := profileSDKAt(t, cmd)
			_, sdkErr := c.WriteProfile(ctx, test.name, []byte(test.text))
			_, cliErr := profileCLI(cmd, []byte(test.text), "set", test.name)
			if profileCode(t, sdkErr) != test.code || cliErr == nil || cliErr.Error() != "profile set: "+sdkErr.Error() || !IsUsageError(cliErr) {
				t.Fatalf("SDK %v CLI %v", sdkErr, cliErr)
			}
		})
	}
	cmd, _ := newProfileTestCommand(t, "")
	c := profileSDKAt(t, cmd)
	for _, name := range []string{"absent", "valid"} {
		if name == "valid" {
			if _, err := c.WriteProfile(ctx, name, nil); err != nil {
				t.Fatal(err)
			}
		}
		sdkErr := c.DeleteProfile(ctx, name, false)
		_, cliErr := profileCLI(cmd, nil, "delete", name)
		if profileCode(t, sdkErr) != projmux.CodeProfileConfirmationRequired || cliErr.Error() != sdkErr.Error() || !IsUsageError(cliErr) {
			t.Fatalf("confirmation %v %v", sdkErr, cliErr)
		}
	}
	_, sdkErr := c.ReadProfile(ctx, "absent")
	_, cliErr := profileCLI(cmd, nil, "show", "absent")
	if profileCode(t, sdkErr) != projmux.CodeProfileNotFound || cliErr.Error() != "profile show: "+sdkErr.Error() || IsUsageError(cliErr) {
		t.Fatalf("missing %v %v", sdkErr, cliErr)
	}
	sdkErr = c.DeleteProfile(ctx, "absent", true)
	_, cliErr = profileCLI(cmd, nil, "delete", "absent", "--yes")
	if profileCode(t, sdkErr) != projmux.CodeProfileNotFound || cliErr.Error() != "profile delete: "+sdkErr.Error() || IsUsageError(cliErr) {
		t.Fatal("missing delete")
	}
	for _, ref := range []string{"uid:", "absent"} {
		_, sdkErr := c.ListProfiles(ctx, ref)
		_, cliErr := profileCLI(cmd, nil, "list", "--project", ref)
		want := projmux.CodeResourceNotFound
		if ref == "uid:" {
			want = projmux.CodeSelectorInvalid
		}
		if profileCode(t, sdkErr) != want || cliErr.Error() != sdkErr.Error() {
			t.Fatalf("project %v %v", sdkErr, cliErr)
		}
	}
}

func TestProfileSDKIsolationAndCancellation(t *testing.T) {
	before := os.Environ()
	a, _ := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "a")})
	b, _ := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "b")})
	ctx := context.Background()
	if _, err := a.WriteProfile(ctx, "same", []byte("model = \"opus\"")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteProfile(ctx, "same", []byte("model = \"sonnet\"")); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.WriteProfile(cancelCtx, "same", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := a.DeleteProfile(cancelCtx, "same", true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.ListProfiles(cancelCtx, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.ReadProfile(cancelCtx, "same"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	x, _ := a.ReadProfile(ctx, "same")
	y, _ := b.ReadProfile(ctx, "same")
	if string(x.Content) != "model = \"opus\"" || string(y.Content) != "model = \"sonnet\"" || !reflect.DeepEqual(before, os.Environ()) {
		t.Fatal("isolation or canceled mutation")
	}
}

func TestProfileSDKStoreAndProjectFailures(t *testing.T) {
	ctx := context.Background()
	cmd, _ := newProfileTestCommand(t, "")
	c := profileSDKAt(t, cmd)
	if _, err := c.WriteProfile(ctx, "claimed", []byte("roles = [\"shared\"]")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, text string
		code       projmux.Code
	}{
		{"other", "roles = [\"shared\"]", projmux.CodeProfileRoleClaimed},
		{"scoped", "project = \"" + profileProjectP + "\"", projmux.CodeProfileScopeChanged},
	} {
		if test.name == "scoped" {
			if _, err := c.WriteProfile(ctx, test.name, nil); err != nil {
				t.Fatal(err)
			}
		}
		_, sdkErr := c.WriteProfile(ctx, test.name, []byte(test.text))
		_, cliErr := profileCLI(cmd, []byte(test.text), "set", test.name)
		if profileCode(t, sdkErr) != test.code || cliErr.Error() != "profile set: "+sdkErr.Error() || !IsUsageError(cliErr) {
			t.Fatalf("%s: %v %v", test.name, sdkErr, cliErr)
		}
	}
	paths, _ := cmd.service().Paths()
	bigPath := filepath.Join(paths.ConfigDir, "profiles", "manual.toml")
	if err := os.WriteFile(bigPath, bytes.Repeat([]byte("x"), profile.MaxSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	_, sdkErr := c.ReadProfile(ctx, "manual")
	_, cliErr := profileCLI(cmd, nil, "show", "manual")
	if profileCode(t, sdkErr) != projmux.CodeProfileTooLarge || cliErr.Error() != "profile show: "+sdkErr.Error() || !IsUsageError(cliErr) {
		t.Fatalf("manual oversized %v %v", sdkErr, cliErr)
	}
	registryPath := storemetadata.PathFor(paths.StateDir)
	if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, sdkErr = c.ListProfiles(ctx, "alpha")
	_, cliErr = profileCLI(cmd, nil, "list", "--project", "alpha")
	if profileCode(t, sdkErr) != projmux.CodeRegistryUnavailable || cliErr.Error() != sdkErr.Error() || IsUsageError(cliErr) {
		t.Fatalf("Registry %v %v", sdkErr, cliErr)
	}
	// A write over a nonempty directory is an ordinary filesystem failure.
	// Deletion refuses a nonregular path as not-found. Both leave it untouched.
	bad := filepath.Join(paths.ConfigDir, "profiles", "blocked.toml")
	if err := os.Mkdir(bad, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "child"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, sdkErr = c.WriteProfile(ctx, "blocked", nil)
	_, cliErr = profileCLI(cmd, nil, "set", "blocked")
	var refusal *projmux.Refusal
	var sdkLink, cliLink *os.LinkError
	if sdkErr == nil || errors.As(sdkErr, &refusal) || !errors.As(sdkErr, &sdkLink) || !errors.As(cliErr, &cliLink) || sdkLink.Op != cliLink.Op || sdkLink.New != cliLink.New || !errors.Is(cliErr, sdkLink.Err) || IsUsageError(cliErr) {
		t.Fatalf("filesystem write %v %v", sdkErr, cliErr)
	}
	sdkErr = c.DeleteProfile(ctx, "blocked", true)
	_, cliErr = profileCLI(cmd, nil, "delete", "blocked", "--yes")
	if profileCode(t, sdkErr) != projmux.CodeProfileNotFound || cliErr.Error() != "profile delete: "+sdkErr.Error() || IsUsageError(cliErr) {
		t.Fatalf("nonregular delete %v %v", sdkErr, cliErr)
	}
	if _, err := os.Stat(filepath.Join(bad, "child")); err != nil {
		t.Fatal("failed mutation removed original directory", err)
	}
}
