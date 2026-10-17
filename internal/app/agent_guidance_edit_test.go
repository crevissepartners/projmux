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

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestAgentGuidanceEditingCLISDKParity(t *testing.T) {
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
	raw, err := run("", "read", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var cli core.AgentGuidanceFile
	if err := json.Unmarshal([]byte(raw), &cli); err != nil {
		t.Fatal(err)
	}
	read, err := sdk.ReadAgentGuidance(ctx)
	if err != nil || !reflect.DeepEqual(read, cli) || read.Present {
		t.Fatalf("read parity %+v %+v %v", read, cli, err)
	}
	content := "# Personal\r\nexact bytes\n"
	raw, err = run(content, "validate", "--file", "-", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var validation core.AgentGuidanceValidation
	if err := json.Unmarshal([]byte(raw), &validation); err != nil {
		t.Fatal(err)
	}
	validated, err := sdk.ValidateAgentGuidance(ctx, []byte(content))
	if err != nil || !reflect.DeepEqual(validated, validation) {
		t.Fatalf("validate parity %+v %+v %v", validated, validation, err)
	}
	file := filepath.Join(root, "candidate.md")
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = run("", "set", "--file", file, "--if-match", validation.Revision, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &cli); err != nil {
		t.Fatal(err)
	}
	read, err = sdk.ReadAgentGuidance(ctx)
	if err != nil || read.Text != content || !reflect.DeepEqual(read, cli) {
		t.Fatalf("write/read parity %+v %+v %v", read, cli, err)
	}
	_, cliErr := run(content, "set", "--file", "-", "--if-match", validation.Revision)
	_, sdkErr := sdk.WriteAgentGuidance(ctx, []byte(content), validation.Revision)
	var refusal *projmux.Refusal
	if !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodeAgentGuidanceStale || cliErr == nil || !strings.Contains(cliErr.Error(), string(refusal.Code)) {
		t.Fatalf("stale parity %v %v", cliErr, sdkErr)
	}
	_, cliErr = run("bad\x00", "validate", "--file", "-")
	_, sdkErr = sdk.ValidateAgentGuidance(ctx, []byte("bad\x00"))
	if !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodeAgentGuidanceInvalid || cliErr == nil || !strings.Contains(cliErr.Error(), string(refusal.Code)) {
		t.Fatalf("invalid parity %v %v", cliErr, sdkErr)
	}
	raw, err = run("", "reset", "--if-match", read.Revision, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &cli); err != nil {
		t.Fatal(err)
	}
	read, err = sdk.ReadAgentGuidance(ctx)
	if err != nil || read.Present || !reflect.DeepEqual(read, cli) {
		t.Fatal("reset parity", err)
	}
	// Existing show JSON and policy output still use the same legacy semantics.
	if err := sdk.SetAgentGuidanceEnabled(ctx, "", false); err != nil {
		t.Fatal(err)
	}
	raw, err = run("", "show", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	view, err := sdk.InspectAgentGuidance(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var show core.AgentGuidanceInspection
	if err := json.Unmarshal([]byte(raw), &show); err != nil {
		t.Fatal(err)
	}
	view.MigrationNotice = ""
	view.Source = ""
	if !reflect.DeepEqual(show, view) {
		t.Fatalf("show parity %+v %+v", show, view)
	}
}
func TestAgentGuidanceEditingUsageBeforeEffects(t *testing.T) {
	for _, args := range [][]string{{"set", "--file", "-"}, {"set", "--if-match", "rev"}, {"reset"}, {"read", "--project", "uid:missing"}, {"read", "extra"}, {"validate", "--file", "-", "-o", "yaml"}, {"reset", "--if-match", "rev", "extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			cmd := &agentCommand{lookupEnv: func(key string) string {
				if key == "XDG_CONFIG_HOME" {
					return root
				}
				return ""
			}}
			var out, diagnostic bytes.Buffer
			if err := cmd.Run(append([]string{"guidance"}, args...), &out, &diagnostic); err == nil {
				t.Fatal("accepted invalid usage")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("usage caused filesystem effects", err)
			}
		})
	}
}
