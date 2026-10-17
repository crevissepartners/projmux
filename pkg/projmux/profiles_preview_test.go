package projmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Capture bytes, permissions and mtimes, including directories; read-only calls
// must not create migration/lock files or silently repair existing permissions.
func profileTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data := []byte{}
		if info.Mode().IsRegular() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		out[strings.TrimPrefix(path, root)] = fmt.Sprintf("%v:%v:%x", info.Mode(), info.ModTime().UnixNano(), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseProfileWebParity(t *testing.T) {
	// readProfileImport's TOML stage delegates to the existing profile parser.
	// These independently specified expected values also pin webPermissionsOf's
	// non-null lists and value-based HasPermissions behavior, without bundle code.
	c := clientAt(t, filepath.Join(t.TempDir(), "absent"))
	for _, tc := range []struct {
		content string
		want    core.ProfileSpec
		set     bool
	}{
		{"", core.ProfileSpec{Roles: []string{}, Permissions: core.ProfilePermissions{Allow: []string{}, Deny: []string{}}}, false},
		{"[permissions]\nallow=[]\ndeny=[]", core.ProfileSpec{Roles: []string{}, Permissions: core.ProfilePermissions{Allow: []string{}, Deny: []string{}}}, false},
		{`project="proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"
provider="codex"
instructions="missing"
model="gpt-6.1-sol"
effort="high"
roles=["reader", "writer",]
[permissions]
sandbox="workspace-write"
approval="on-request"
allow=["Read(./docs/**)", "Bash(git status *)"]
deny=["WebFetch(domain:example.com)"]`, core.ProfileSpec{Project: "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa", Provider: "codex", Instructions: "missing", Model: "gpt-6.1-sol", Effort: "high", Roles: []string{"reader", "writer"}, Permissions: core.ProfilePermissions{Sandbox: "workspace-write", Approval: "on-request", Allow: []string{"Read(./docs/**)", "Bash(git status *)"}, Deny: []string{"WebFetch(domain:example.com)"}}}, true},
	} {
		got, err := c.ParseProfile(context.Background(), []byte(tc.content))
		if err != nil || !reflect.DeepEqual(got, tc.want) || got.Permissions.HasPermissions() != tc.set {
			t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
		}
		encoded, err := json.Marshal(got)
		if err != nil || strings.Contains(string(encoded), "null") {
			t.Fatalf("JSON %s %v", encoded, err)
		}
	}
	for _, tc := range []struct {
		content string
		code    projmux.Code
	}{
		{"model=", projmux.CodeProfileSyntaxInvalid},
		{string([]byte{0xff}), projmux.CodeProfileSyntaxInvalid},
		{"unknown=\"value\"", projmux.CodeProfileKeyUnknown},
		{"[other]", projmux.CodeProfileTableUnknown},
		{"effort=\"ultra\"", projmux.CodeProfileValueInvalid},
		{"roles=[\"same\",\"same\"]", projmux.CodeProfileRoleDuplicate},
		{"provider=\"unknown\"", projmux.CodeProfileProviderUnknown},
		{strings.Repeat(" ", profile.MaxSize+1), projmux.CodeProfileTooLarge},
	} {
		got, err := c.ParseProfile(context.Background(), []byte(tc.content))
		if codeOf(t, err) != tc.code || !reflect.DeepEqual(got, core.ProfileSpec{}) {
			t.Fatalf("got %+v %v", got, err)
		}
	}
	// Exact limit succeeds.
	if _, err := c.ParseProfile(context.Background(), []byte(strings.Repeat(" ", profile.MaxSize))); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePreviewNoEffectsAndWriteParity(t *testing.T) {
	for _, scenario := range []string{"empty", "success", "name", "reserved", "size", "syntax", "instructions", "role", "scope", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "config")
			ctx := context.Background()
			c := clientAt(t, dir)
			name, content := "candidate", []byte("model=\"opus\"\n")
			switch scenario {
			case "success":
				if _, err := c.WriteInstructions(ctx, "guide", []byte("guide")); err != nil {
					t.Fatal(err)
				}
				content = []byte("instructions=\"guide\"\n[permissions]\nallow=[\"Read\"]")
			case "name":
				name = "../bad"
			case "reserved":
				name = "none"
			case "size":
				content = []byte(strings.Repeat(" ", profile.MaxSize+1))
			case "syntax":
				content = []byte("model=")
			case "instructions":
				content = []byte("instructions=\"gone\"")
			case "role":
				if _, err := c.WriteProfile(ctx, "other", []byte("roles=[\"taken\"]")); err != nil {
					t.Fatal(err)
				}
				content = []byte("roles=[\"taken\"]")
			case "scope":
				if _, err := c.WriteProfile(ctx, name, content); err != nil {
					t.Fatal(err)
				}
				content = []byte("project=\"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa\"")
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := profileTree(t, root)
			preview, err := c.PreviewProfile(ctx, name, content)
			if after := profileTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("preview changed tree: %v => %v", before, after)
			}
			_, parseErr := c.ParseProfile(ctx, content)
			if after := profileTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("parse changed tree")
			}
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) || !errors.Is(parseErr, context.Canceled) {
					t.Fatalf("cancel: %v %v", err, parseErr)
				}
				return
			}
			result, writeErr := c.WriteProfile(ctx, name, content)
			if err != nil {
				if !reflect.DeepEqual(before, profileTree(t, root)) {
					t.Fatal("rejected Write changed tree")
				}
				if writeErr == nil || codeOf(t, err) != codeOf(t, writeErr) || err.Error() != writeErr.Error() {
					t.Fatalf("preview %v vs Write %v", err, writeErr)
				}
				if !reflect.DeepEqual(preview, core.ProfilePreview{}) {
					t.Fatalf("partial preview: %+v", preview)
				}
			} else if writeErr != nil || !preview.DryRun || preview.Name != result.Name || preview.Digest != result.Digest || preview.Size != result.Size {
				t.Fatalf("preview %+v %v vs Write %+v %v", preview, err, result, writeErr)
			}
		})
	}
}

func TestProfileParseAndPreviewMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	root := t.TempDir()
	before := profileTree(t, root)
	c, err := projmux.NewClient(projmux.Options{StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ParseProfile(context.Background(), []byte("model=\"opus\"")); err != nil {
		t.Fatal(err)
	}
	if _, err = c.PreviewProfile(context.Background(), "candidate", nil); err == nil {
		t.Fatal("preview should retain Write's missing-home refusal")
	}
	if !reflect.DeepEqual(before, profileTree(t, root)) {
		t.Fatal("missing-home calls changed tree")
	}
}
