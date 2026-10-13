package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessRetirementWitnessExactPrivateEvidence(t *testing.T) {
	reg, _, pane := stopFixture()
	activation := *pane.Status.Activation.Process
	writeStopWait(&reg)
	wait := reg.Panes[0].Status.LastTermination
	root := t.TempDir()
	path := filepath.Join(root, "metadata", "registry.json")
	if err := WriteProcessRetirement(path, activation, wait); err != nil {
		t.Fatal(err)
	}
	got, err := ReadProcessRetirement(path, activation.Binding, wait)
	if err != nil || !reflect.DeepEqual(got, activation) {
		t.Fatalf("witness %+v %v", got, err)
	}
	if err = WriteProcessRetirement(path, activation, wait); err != nil {
		t.Fatal(err)
	}
	wrong := activation
	wrong.HostProcess.Start = "other-owner"
	if err = WriteProcessRetirement(path, wrong, wait); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("replacement owner accepted", err)
	}
	other := *wait.Clone()
	other.OperationID = "other-operation"
	if _, err = ReadProcessRetirement(path, activation.Binding, &other); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("replacement Wait accepted", err)
	}
	witness, _ := retirementPath(path, activation.Binding)
	if err = os.Chmod(witness, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadProcessRetirement(path, activation.Binding, wait); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("nonprivate evidence accepted", err)
	}
	if err = os.Remove(witness); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, witness); err != nil {
		t.Fatal(err)
	}
	if err = WriteProcessRetirement(path, activation, wait); err == nil {
		t.Fatal("witness symlink accepted")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "unchanged" {
		t.Fatal("other resource changed")
	}
}
func TestProcessRetirementWitnessRejectsDirectoryLink(t *testing.T) {
	reg, _, pane := stopFixture()
	activation := *pane.Status.Activation.Process
	writeStopWait(&reg)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "process-retirements")); err != nil {
		t.Fatal(err)
	}
	if err := WriteProcessRetirement(filepath.Join(root, "metadata", "registry.json"), activation, reg.Panes[0].Status.LastTermination); err == nil {
		t.Fatal("linked evidence directory accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("outside explicit state directory changed")
	}
}
