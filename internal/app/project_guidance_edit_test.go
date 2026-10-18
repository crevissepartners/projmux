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

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestProjectGuidanceEditingCLISDKParity(t *testing.T) {
	root := t.TempDir()
	cmd := &agentCommand{lookupEnv: func(key string) string {
		switch key {
		case "XDG_CONFIG_HOME":
			return filepath.Join(root, "config")
		case "XDG_STATE_HOME":
			return filepath.Join(root, "state")
		}
		return ""
	}}
	paths, err := configPaths(nil, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = storemetadata.NewDefaultStore(paths).Update(func(reg *metadata.Registry) error {
		_, err := (metadata.Mutator{DirExists: func(string) (bool, error) { return true, nil }}).RegisterProject(reg, metadata.RegisterProjectOptions{Root: root, Name: "sample"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := func(input string, args ...string) (string, error) {
		t.Helper()
		cmd.guidanceInput = strings.NewReader(input)
		var out, diagnostic bytes.Buffer
		err := cmd.Run(append([]string{"guidance"}, args...), &out, &diagnostic)
		return out.String(), err
	}
	raw, err := run("", "read", "-p", "sample", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var cli core.ProjectGuidanceFile
	if err = json.Unmarshal([]byte(raw), &cli); err != nil {
		t.Fatal(err)
	}
	read, err := sdk.ReadProjectGuidance(ctx, "sample")
	if err != nil || !reflect.DeepEqual(cli, read) || read.Present {
		t.Fatalf("read %+v %+v %v", cli, read, err)
	}
	for _, text := range []string{"", " \t\r\n", "한글 exact\r\n"} {
		raw, err = run(text, "validate", "--project", "sample", "--file", "-", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		var cliValidation core.ProjectGuidanceValidation
		if err = json.Unmarshal([]byte(raw), &cliValidation); err != nil {
			t.Fatal(err)
		}
		validation, err := sdk.ValidateProjectGuidance(ctx, "uid:"+read.ProjectUID, []byte(text))
		if err != nil || !reflect.DeepEqual(validation, cliValidation) {
			t.Fatal("validate parity", err)
		}
		raw, err = run(text, "set", "--project", "sample", "--file", "-", "--if-match", validation.Revision, "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(raw), &cli); err != nil {
			t.Fatal(err)
		}
		read, err = sdk.ReadProjectGuidance(ctx, "sample")
		if err != nil || read.Text != text || !read.Present || !reflect.DeepEqual(cli, read) {
			t.Fatal("write parity", err)
		}
	}
	_, cliErr := run("new", "set", "--project", "sample", "--file", "-", "--if-match", "stale")
	_, sdkErr := sdk.WriteProjectGuidance(ctx, "sample", []byte("new"), "stale")
	var refusal *projmux.Refusal
	if !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodeAgentGuidanceStale || cliErr == nil || !strings.Contains(cliErr.Error(), string(refusal.Code)) {
		t.Fatalf("stale %v %v", cliErr, sdkErr)
	}
	raw, err = run("", "delete", "--project", "sample", "--if-match", read.Revision, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &cli); err != nil {
		t.Fatal(err)
	}
	read, err = sdk.ReadProjectGuidance(ctx, "sample")
	if err != nil || read.Present || !reflect.DeepEqual(cli, read) {
		t.Fatal("delete parity", err)
	}

	// SDK and JSON expose bounded metadata; plain reads keep the too-large refusal.
	store := agentguidance.NewDefaultProjectStore(paths)
	path, err := store.Path(read.ProjectUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{agentguidance.MaxSize + 1, agentguidance.MaxSize * 16} {
		for _, action := range []string{"set", "delete"} {
			// Binary prefix demonstrates that oversize content is never decoded as text.
			content := bytes.Repeat([]byte{0xff}, size)
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			current, err := sdk.ReadProjectGuidance(ctx, "sample")
			if err != nil || !current.Oversize || current.Bytes != size || current.PrefixBytes != agentguidance.MaxSize+1 || current.PrefixDigest != agentguidance.Digest(content[:agentguidance.MaxSize+1]) || current.Text != "" || current.Digest != "" || current.Source != "" || current.EffectiveText != "" || current.EffectiveDigest != "" {
				t.Fatalf("oversize SDK %+v %v", current, err)
			}
			raw, err = run("", "read", "-p", "sample", "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(raw), &cli); err != nil || cli != current {
				t.Fatalf("oversize JSON %+v %v", cli, err)
			}
			raw, err = run("", "read", "-p", "sample")
			if raw != "" || err == nil || !strings.Contains(err.Error(), "agent-guidance-too-large") {
				t.Fatalf("plain read %q %v", raw, err)
			}
			validation, err := sdk.ValidateProjectGuidance(ctx, "sample", []byte("recovered"))
			if err != nil || validation.Revision != current.Revision {
				t.Fatalf("oversize validate %+v %v", validation, err)
			}
			args := []string{action, "--project", "sample", "--if-match", current.Revision, "-o", "json"}
			if action == "set" {
				args = append(args, "--file", "-")
			}
			raw, err = run("recovered", args...)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(raw), &cli); err != nil {
				t.Fatal(err)
			}
			after, err := sdk.ReadProjectGuidance(ctx, "sample")
			if err != nil || after != cli || after.Oversize || after.PrefixDigest != "" || after.PrefixBytes != 0 || after.Present != (action == "set") {
				t.Fatalf("recovered %+v %v", after, err)
			}
			if action == "set" && after.Text != "recovered" {
				t.Fatal("missing replacement")
			}
		}
	}

}
func TestProjectGuidanceCLIExplicitTargetNoFallback(t *testing.T) {
	for _, args := range [][]string{
		{"read", "--project", ""}, {"read", "-p", ""}, {"reset", "-p", "sample", "--if-match", "revision"}, {"validate", "--project=", "--file", "-"}, {"set", "--project=", "--file", "-", "--if-match", "revision"}, {"delete", "--project=", "--if-match", "revision"}, {"delete", "--if-match", "revision"}, {"reset", "--project", "sample", "--if-match", "revision"}, {"read", "--project", " "},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			cmd := &agentCommand{lookupEnv: func(key string) string {
				if key == "XDG_CONFIG_HOME" || key == "XDG_STATE_HOME" {
					return root
				}
				return ""
			}, guidanceInput: strings.NewReader("candidate")}
			var out, diagnostic bytes.Buffer
			err := cmd.Run(append([]string{"guidance"}, args...), &out, &diagnostic)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("expected usage error %v", err)
			}
			entries, e := os.ReadDir(root)
			if e != nil || len(entries) != 0 {
				t.Fatalf("invalid target wrote files %v %v", entries, e)
			}
		})
	}
}
