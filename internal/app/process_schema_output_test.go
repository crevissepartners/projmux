package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Real public reads consume the persisted process fields without saving a
// migration, repairing state, or contacting tmux/provider transports.
func TestProcessSchemaGetDescribeGoldenAndReadOnly(t *testing.T) {
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	var pane coremetadata.Pane
	for _, p := range reg.Panes {
		if p.Spec.Runtime.Kind == coremetadata.RuntimeProcess {
			pane = p
		}
	}
	b := pane.Status.Activation.Process.Binding
	home := t.TempDir()
	state := filepath.Join(home, "state", "projmux")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path := intmetadata.PathFor(state)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	noRuntime := func() coremetadata.RuntimeObservation { return coremetadata.RuntimeObservation{} }
	get := &getCommand{loadRegistry: loadResourceRegistry, runtime: noRuntime}
	describe := &describeCommand{loadRegistry: loadResourceRegistry, runtime: noRuntime}
	var got bytes.Buffer
	args := []string{"pane", "--project", "uid:" + b.ProjectUID, "--window", "uid:" + b.WindowUID, "--pane", "uid:" + b.PaneUID}
	stdout, stderr, err := runGet(t, get, append(append([]string{}, args...), "-o", "json")...)
	if err != nil {
		t.Fatalf("get: %v %s", err, stderr)
	}
	got.WriteString("== get pane json ==\n" + stdout)
	var output, errors bytes.Buffer
	if err := describe.Run(args, &output, &errors); err != nil {
		t.Fatalf("describe: %v %s", err, &errors)
	}
	got.WriteString("== describe pane ==\n" + output.String())
	output.Reset()
	errors.Reset()
	if err := describe.Run([]string{"agent", "uid:" + b.AgentUID, "--project", "uid:" + b.ProjectUID, "--window", "uid:" + b.WindowUID}, &output, &errors); err != nil {
		t.Fatalf("describe agent: %v %s", err, &errors)
	}
	got.WriteString("== describe agent ==\n" + output.String())
	golden := "testdata/process-schema-reads.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("process read golden mismatch:\n%s", &got)
	}
	after, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("query created state")
	}
	read, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(read, raw) {
		t.Fatal("query saved registry")
	}
}
