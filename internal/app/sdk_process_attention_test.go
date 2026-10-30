package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// webProcessAttentionByAgent is the web's per-Agent filter over this package's
// consumer and store, the oracle of the public batch read.
func webProcessAttentionByAgent(t *testing.T, stateDir string, reg coremetadata.Registry) map[string]string {
	t.Helper()
	records, err := newProcessAttentionStore(stateDir).read()
	if err != nil || len(records) == 0 {
		return nil
	}
	consumer := processAttentionConsumer{readRegistry: func() (coremetadata.Registry, error) { return reg, nil }}
	if err = consumer.refresh(); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, binding := range consumer.bindings {
		r, ok := records[binding.Pane]
		if !ok || r.Binding != binding || binding.Generation == "" || r.Provider != consumer.providers[binding.Pane] || r.Terminal {
			continue
		}
		switch badge := r.badge(); badge {
		case aibadge.InputRequired, aibadge.ApprovalRequired, aibadge.InProgress, aibadge.ResponseComplete:
			out[binding.Agent] = badge
		}
	}
	return out
}

// The public batch read returns exactly the map the web builds from this
// package's consumer, for records the filter keeps and every kind it drops.
func TestPublicProcessAttentionMatchesConsumerFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	registryPath := intmetadata.PathFor(paths.StateDir)
	if err = os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const agents = 9
	_, err = intmetadata.NewStore(registryPath).Update(func(reg *coremetadata.Registry) error {
		mut := intmetadata.DefaultMutator()
		created, e := mut.RegisterProject(reg, coremetadata.RegisterProjectOptions{Root: t.TempDir(), Name: "attention-project", DefaultShell: "/bin/sh", OperationID: "register"})
		if e != nil {
			return e
		}
		for i := range agents {
			name := fmt.Sprintf("attention-%d", i)
			_, a, p, e := mut.CreateProcessWindow(reg, created.Project.Metadata.UID, coremetadata.BootstrapWindow{Name: name}, coremetadata.CreateAgentOptions{Name: name, Provider: "codex", OperationID: "create-" + name}, coremetadata.ProcessBinding{HostInstanceID: "host", Generation: "generation-" + name, OperationID: "op-" + name})
			if e != nil {
				return e
			}
			pane, _ := reg.Pane(p.Metadata.UID)
			agent, _ := reg.Agent(a.Metadata.UID)
			agent.Status.Phase = coremetadata.PhaseRunning
			identity := func(pid int, start string) coremetadata.ProcessIdentity {
				return coremetadata.ProcessIdentity{PID: pid, OwnerUID: uint32(os.Getuid()), Start: start}
			}
			if e = mut.RecordProcessActivation(reg, coremetadata.ProcessActivation{Binding: pane.Status.ProcessSession.Binding, HostProcess: identity(2000000000+i, "owner"), Child: identity(2100000000+i, "child")}, "conversation"); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := intmetadata.NewStore(registryPath).Load()
	if err != nil {
		t.Fatal(err)
	}
	declared := newRegistryProcessAttentionConsumer(func() (coremetadata.Registry, error) { return reg, nil })
	if err = declared.refresh(); err != nil || len(declared.bindings) != agents {
		t.Fatalf("declarations: %d %v", len(declared.bindings), err)
	}
	pending := func(kind string) map[string]processAttentionPending {
		return map[string]processAttentionPending{"request": {Kind: kind, Sequence: 1}}
	}
	records := map[string]processAttentionRecord{}
	for i, binding := range declared.bindings {
		r := processAttentionRecord{Binding: binding, Provider: "codex"}
		switch i {
		case 0:
			r.Pending = pending(aibadge.InputRequired)
		case 1:
			r.Pending = pending(aibadge.ApprovalRequired)
		case 2:
			r.Badge = aibadge.InProgress
		case 3:
			r.Badge = aibadge.ResponseComplete
		case 4: // a Terminal record shows nothing
			r.Terminal, r.Pending = true, pending(aibadge.InputRequired)
		case 5: // a record of an older generation
			r.Binding.Generation, r.Pending = "old", pending(aibadge.InputRequired)
		case 6: // another provider than the declaration's
			r.Provider, r.Pending = "claude", pending(aibadge.InputRequired)
		case 7: // a record whose badge is none of the four
			r.Badge = ""
		default: // declaration only, no record
			continue
		}
		records[binding.Pane] = r
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(newProcessAttentionStore(paths.StateDir).path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	want := webProcessAttentionByAgent(t, paths.StateDir, reg)
	if len(want) != 4 {
		t.Fatalf("oracle shows %d Agents, want the 4 kept ones: %v", len(want), want)
	}
	client, err := projmux.NewClient(projmux.Options{StateDir: paths.StateDir, ConfigDir: filepath.Join(home, "config"), HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadProcessAttention(context.Background(), core.ProcessAttentionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	public := map[string]string{}
	for agent, badge := range got.Agents {
		public[agent] = string(badge)
	}
	if !reflect.DeepEqual(public, want) {
		t.Fatalf("public %v, web filter %v", public, want)
	}
}
