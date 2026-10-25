package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// settingsFixtureClient is the process Registry golden (agent-01, a Running
// Codex process Agent on pane-02) made busy, plus three Pending tmux Agents in
// the same Project: two Claude ones, one recording instructions whose content
// changed, and a Codex one.
func settingsFixtureClient(t *testing.T) (*Client, config.Paths, map[string]string) {
	t.Helper()
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	if err := os.MkdirAll(persona.NewDefaultStore(paths).Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(persona.NewDefaultStore(paths).Dir(), "reviewer"+persona.FileExt), []byte("REVIEWER_V2"), 0600); err != nil {
		t.Fatal(err)
	}
	uids := map[string]string{"codex-process": "agent-01"}
	_, err := store.NewStore(store.PathFor(paths.StateDir)).Update(func(reg *metadata.Registry) error {
		m := store.DefaultMutator()
		for _, a := range []struct{ name, provider string }{{"claude-a", "claude"}, {"claude-b", "claude"}, {"codex-tmux", "codex"}} {
			agent, err := m.CreateAgent(reg, "window-01", metadata.CreateAgentOptions{Name: a.name, Provider: a.provider})
			if err != nil {
				return err
			}
			uids[a.name] = agent.Metadata.UID
		}
		claude, _ := reg.Agent(uids["claude-b"])
		claude.Metadata.Annotations = map[string]string{metadata.AnnotationAgentPersona: "reviewer", metadata.AnnotationAgentPersonaDigest: persona.Digest([]byte("REVIEWER_V1"))}
		busy, _ := reg.Agent("agent-01")
		busy.Status.Interaction = metadata.AgentInteraction{Kind: metadata.InteractionInProgress, ObservedAt: time.Now(), Source: metadata.InteractionSourceProviderHook}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, paths, uids
}

// countingPromptPlanner counts the parts the read plans, by part and key.
type countingPromptPlanner struct {
	next  runtime.RelaunchPromptPlanner
	calls map[string]int
}

func (p *countingPromptPlanner) PlanAgentGuidance(provider string, recorded map[string]string) runtime.AgentGuidanceLaunch {
	p.calls["guidance "+provider+" tmux"]++
	return p.next.PlanAgentGuidance(provider, recorded)
}
func (p *countingPromptPlanner) PlanCodexAgentGuidance() runtime.AgentGuidanceLaunch {
	p.calls["guidance codex tmux"]++
	return p.next.PlanCodexAgentGuidance()
}
func (p *countingPromptPlanner) PlanProcessAgentGuidance() runtime.AgentGuidanceLaunch {
	p.calls["guidance process"]++
	return p.next.PlanProcessAgentGuidance()
}
func (p *countingPromptPlanner) PlanProjectLinks(provider string, project metadata.Project, recorded map[string]string) runtime.ProjectLinksLaunch {
	p.calls["links "+provider+" "+project.Metadata.UID]++
	return p.next.PlanProjectLinks(provider, project, recorded)
}
func (p *countingPromptPlanner) PlanCodexProjectLinks(project metadata.Project) runtime.ProjectLinksLaunch {
	p.calls["links codex "+project.Metadata.UID]++
	return p.next.PlanCodexProjectLinks(project)
}

func TestReadAgentSettingsReadsRegistryOnce(t *testing.T) {
	c, _, uids := settingsFixtureClient(t)
	loads := 0
	previousLoad, previousPlanner := loadAgentSettingsRegistry, newAgentSettingsPlanner
	counting := &countingPromptPlanner{calls: map[string]int{}}
	loadAgentSettingsRegistry = func(paths config.Paths) (metadata.Registry, error) {
		loads++
		return previousLoad(paths)
	}
	newAgentSettingsPlanner = func(paths config.Paths) runtime.RelaunchPromptPlanner {
		counting.next = previousPlanner(paths)
		return counting
	}
	t.Cleanup(func() { loadAgentSettingsRegistry, newAgentSettingsPlanner = previousLoad, previousPlanner })
	got, err := c.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if loads != 1 {
		t.Fatalf("Registry loads %d, want 1", loads)
	}
	want := map[string]int{"guidance claude tmux": 1, "links claude project-01": 1, "guidance codex tmux": 1, "links codex project-01": 1, "guidance process": 1}
	if !reflect.DeepEqual(counting.calls, want) {
		t.Fatalf("planned %v, want each part once per provider and host or Project: %v", counting.calls, want)
	}
	var order []string
	for _, a := range got.Agents {
		order = append(order, a.AgentUID)
	}
	if len(order) != len(uids) || !slices.IsSorted(order) {
		t.Fatalf("agents %v, want every Agent in UID order", order)
	}
}

func TestReadAgentSettingsAdmitsNothing(t *testing.T) {
	c, paths, uids := settingsFixtureClient(t)
	bin := t.TempDir()
	marker := filepath.Join(bin, "tmux-called")
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	registryPath := store.PathFor(paths.StateDir)
	socket := runtimeservice.ProcessHostSocket("codex", registryPath, "pane-02", "gen-one")
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(socket)) })
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	beforeBytes, _ := os.ReadFile(registryPath)
	beforeInfo, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	beforePersonas := listTree(t, persona.NewDefaultStore(paths).Dir())

	got, err := c.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{AgentUIDs: []string{uids["codex-process"], uids["claude-a"], uids["claude-b"], uids["codex-tmux"], "agent-missing", uids["claude-a"]}})
	if err != nil {
		t.Fatal(err)
	}

	afterBytes, _ := os.ReadFile(registryPath)
	afterInfo, _ := os.Stat(registryPath)
	if !bytes.Equal(beforeBytes, afterBytes) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("ReadAgentSettings changed the Registry file")
	}
	if !reflect.DeepEqual(beforePersonas, listTree(t, persona.NewDefaultStore(paths).Dir())) {
		t.Fatal("ReadAgentSettings changed the personas directory")
	}
	if _, err := os.Stat(filepath.Join(paths.StateDir, persona.DirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadAgentSettings wrote an instructions snapshot: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ReadAgentSettings invoked tmux")
	}
	_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(50 * time.Millisecond))
	if conn, err := listener.Accept(); err == nil {
		conn.Close()
		t.Fatal("ReadAgentSettings dialed the process host")
	}

	byUID := map[string]core.AgentSettingsResolution{}
	for _, a := range got.Agents {
		byUID[a.AgentUID] = a
	}
	if len(got.Agents) != 5 {
		t.Fatalf("duplicates not read once: %+v", got.Agents)
	}
	if a := byUID["agent-missing"]; a.Unavailable != string(CodeResourceNotFound) || a.Layered {
		t.Fatalf("missing UID %+v", a)
	}
	for name, host := range map[string]string{"codex-process": "process", "claude-a": "tmux", "claude-b": "tmux", "codex-tmux": "tmux"} {
		a := byUID[uids[name]]
		if !a.Layered || a.Unavailable != "" || a.Host != host || a.Reasons == nil {
			t.Fatalf("%s (busy or Pending) not resolved: %+v", name, a)
		}
	}
	if b := byUID[uids["claude-b"]]; b.New.Instructions.Value != "reviewer" || !slices.Contains(b.Reasons, agentsettings.ReasonInstructionsContentChanged) {
		t.Fatalf("changed instructions content not resolved: %+v", b)
	}
}

func TestReadAgentSettingsRefusals(t *testing.T) {
	c, _, _ := settingsFixtureClient(t)
	var refusal *Refusal
	for _, uids := range [][]string{{""}, {"uid:agent-01"}} {
		if _, err := c.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{AgentUIDs: uids}); !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid {
			t.Fatalf("%q: %v", uids, err)
		}
	}
	absent, _ := graphFixtureClient(t, nil)
	if got, err := absent.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{}); err != nil || len(got.Agents) != 0 {
		t.Fatalf("absent Registry: %+v %v", got, err)
	}
	corrupt, _ := graphFixtureClient(t, []byte("{"))
	if _, err := corrupt.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{}); !errors.As(err, &refusal) || refusal.Code != CodeRegistryUnavailable {
		t.Fatalf("unreadable Registry: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ReadAgentSettings(ctx, core.ReadAgentSettingsRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestAgentLaunchSettingsJSONMatchesCore(t *testing.T) {
	setting := func(p string) agentsettings.Setting {
		return agentsettings.Setting{Value: p + "-value", Source: p + "-source", ProfileValue: p + "-profile", Override: true}
	}
	full := agentsettings.Settings{Profile: agentsettings.ProfileLayer{Name: "lead", Digest: "sha256:lead", Source: "flag"}, Instructions: setting("instructions"), Model: setting("model"), Effort: setting("effort")}
	for _, s := range []agentsettings.Settings{full, {}} {
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(publicAgentLaunchSettings(s))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("public settings JSON differs:\n got %s\nwant %s %v", got, want, err)
		}
	}
}

func listTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
