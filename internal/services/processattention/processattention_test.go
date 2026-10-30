package processattention

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
)

func TestRecordBadgeAggregatesPending(t *testing.T) {
	r := Record{Badge: aibadge.ResponseComplete, Pending: map[string]Pending{
		"b": {Kind: aibadge.ApprovalRequired, Sequence: 2},
		"a": {Kind: aibadge.InputRequired, Sequence: 1},
	}}
	want := aibadge.Aggregate(aibadge.Aggregate(aibadge.ResponseComplete, aibadge.InputRequired), aibadge.ApprovalRequired)
	if got := r.Effective(); got != want {
		t.Fatalf("Effective = %q, want %q", got, want)
	}
	if got := (Record{}).Effective(); got != "" {
		t.Fatalf("empty record badge = %q", got)
	}
}

func TestReadStoreMissingEmptyAndDamaged(t *testing.T) {
	dir := t.TempDir()
	if got, err := ReadStore(dir); err != nil || len(got) != 0 {
		t.Fatalf("missing store: %v %v", got, err)
	}
	path := StorePath(dir)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadStore(dir); err != nil || len(got) != 0 {
		t.Fatalf("empty store: %v %v", got, err)
	}
	for _, damaged := range []string{"{", "null"} {
		if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadStore(dir); !errors.Is(err, ErrDamaged) {
			t.Fatalf("damaged %q: %v", damaged, err)
		}
	}
	if filepath.Base(path) != "process-attention.json" {
		t.Fatalf("store file name changed: %s", path)
	}
}

func TestDeclarationsMatchRegistryRefresh(t *testing.T) {
	raw, err := os.ReadFile("../../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err = json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pane, ok := reg.Pane("pane-02")
	if !ok {
		t.Fatal("fixture lacks pane-02")
	}
	pane.Status.Activation.Process.HostProcess.OwnerUID = identity.OwnerUID
	pane.Status.Activation.Process.Child.OwnerUID = identity.OwnerUID
	got := Declarations(reg)
	b := runtimeservice.ProcessSchemaBinding(pane.Status.Activation.Process.Binding)
	var found bool
	for _, binding := range got.Bindings {
		if binding.Pane == b.Pane {
			found = binding == b
		}
		if got.Providers[binding.Pane] == "" {
			t.Fatalf("binding %q has no provider", binding.Pane)
		}
	}
	if !found {
		t.Fatalf("pane-02 declaration missing or changed: %+v", got.Bindings)
	}
	for i := 1; i < len(got.Bindings); i++ {
		if got.Bindings[i-1].Pane >= got.Bindings[i].Pane {
			t.Fatalf("bindings not sorted by pane: %+v", got.Bindings)
		}
	}
}
