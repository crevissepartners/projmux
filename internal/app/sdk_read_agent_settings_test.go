package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// readSettingsFixture seeds one Offline Agent with a recorded conversation:
// on tmux, or on a retired, resumable process Pane.
func readSettingsFixture(t *testing.T, state, window, name, provider, host, root string, annotations map[string]string) string {
	t.Helper()
	var uid string
	_, err := store.NewStore(store.PathFor(state)).Update(func(reg *metadata.Registry) error {
		m := store.DefaultMutator()
		agent, err := m.CreateAgent(reg, window, metadata.CreateAgentOptions{Name: name, Provider: provider, Workspace: metadata.AgentWorkspace{CWD: root}, Annotations: annotations})
		if err != nil {
			return err
		}
		uid = agent.Metadata.UID
		pane, err := m.AttachAgentPane(reg, uid, metadata.BootstrapPane{CWD: root, Name: name + "-pane"}, "read-settings-"+name)
		if err != nil {
			return err
		}
		conversation := "11111111-2222-4333-8444-" + map[string]string{"claude-tmux": "000000000001", "codex-tmux": "000000000002", "claude-process": "000000000003", "codex-process": "000000000004", "claude-reply-only": "000000000005", "claude-reasons": "000000000006"}[name]
		if host == "tmux" {
			if _, err := m.ReleaseAgentPane(reg, uid, metadata.AgentExitNormal, ""); err != nil {
				return err
			}
		}
		if _, _, err := m.RecordAgentSessionRef(reg, uid, metadata.AgentSessionObservation{Provider: provider, SessionID: conversation, ThreadID: conversation}); err != nil {
			return err
		}
		a, _ := reg.Agent(uid)
		if provider == "codex" {
			a.Status.SessionRef.Codex.Endpoint = &metadata.CodexEndpointRef{StateDomainID: "read-settings-domain", EndpointGenerationID: "read-settings-generation"}
			a.Status.SessionRef.Codex.Lifecycle = &metadata.CodexGenerationLifecycleRef{State: metadata.CodexGenerationCurrent}
		}
		if host == "tmux" {
			_, err = m.TransitionAgent(reg, uid, metadata.PhaseOffline, "")
			return err
		}
		p, _ := reg.Pane(pane.Metadata.UID)
		w, _ := reg.Window(window)
		binding := metadata.ProcessBinding{HostInstanceID: "host", ProjectUID: w.Metadata.OwnerUID(), WindowUID: window, AgentUID: uid, PaneUID: p.Metadata.UID, Generation: "retired", OperationID: "connection"}
		p.Spec.Runtime.Kind = metadata.RuntimeProcess
		p.Status.Activation = metadata.PaneActivation{}
		p.Status.ProcessSession = &metadata.ProcessSessionRecord{Provider: provider, Binding: binding, ConnectionID: "connection", ResumeState: metadata.ProcessResumable}
		if provider == "codex" {
			p.Status.ProcessSession.ThreadID = conversation
		} else {
			p.Status.ProcessSession.SessionID = conversation
		}
		a.Status.Phase = metadata.PhaseOffline
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

// TestReadAgentSettingsMatchesCLIRelaunchDryRun is the comparison the SDK
// read promises: for each Agent, Current, New and Reasons are the
// currentSettings, newSettings and relaunchReasons of the CLI's plain
// `agent relaunch --dry-run -o json`, byte for byte, on the same Registry, with
// agent guidance, Project label link rules and a changed instructions content
// in play.
func TestReadAgentSettingsMatchesCLIRelaunchDryRun(t *testing.T) {
	relaunchProviderFence(t)
	home, root := sdkAgentHome(t), t.TempDir()
	state, configDir := filepath.Join(t.TempDir(), "projmux"), filepath.Join(t.TempDir(), "projmux")
	t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(configDir))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: configDir, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	reg := projectRegistry(t, state)
	window, project := reg.Windows[0].Metadata.UID, reg.Projects[0].Metadata.UID
	writeAgentGuidance(t, paths, []byte("READ_SETTINGS_GUIDANCE"))
	writeLinkRules(t, paths, project, linkRulesAlpha)
	personas := persona.NewDefaultStore(paths).Dir()
	if err := os.MkdirAll(personas, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(personas, "reviewer"+persona.FileExt), []byte("REVIEWER_V2"), 0600); err != nil {
		t.Fatal(err)
	}
	uids := map[string]string{}
	for _, f := range []struct {
		name, provider, host string
		annotations          map[string]string
	}{
		{"claude-tmux", "claude", "tmux", nil},
		{"codex-tmux", "codex", "tmux", nil},
		{"claude-process", "claude", "process", nil},
		{"codex-process", "codex", "process", nil},
		{"claude-reply-only", "claude", "tmux", map[string]string{metadata.AnnotationAgentDialogueReplyOnly: metadata.DialogueReplyOnlyOn}},
		{"claude-reasons", "claude", "tmux", map[string]string{metadata.AnnotationAgentPersona: "reviewer", metadata.AnnotationAgentPersonaDigest: persona.Digest([]byte("REVIEWER_V1")), metadata.AnnotationAgentEffort: "low"}},
	} {
		uids[f.name] = readSettingsFixture(t, state, window, f.name, f.provider, f.host, root, f.annotations)
	}
	var all []string
	for _, uid := range uids {
		all = append(all, uid)
	}
	read, err := client.ReadAgentSettings(context.Background(), core.ReadAgentSettingsRequest{AgentUIDs: all})
	if err != nil {
		t.Fatal(err)
	}
	byUID := map[string]core.AgentSettingsResolution{}
	for _, a := range read.Agents {
		byUID[a.AgentUID] = a
	}
	cli := New().agent
	for name, uid := range uids {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := cli.runRelaunch([]string{"uid:" + uid, "--dry-run", "-o", "json"}, &stdout, &stderr); err != nil {
				t.Fatalf("CLI dry run: %v\n%s", err, stderr.String())
			}
			var want struct {
				CurrentSettings json.RawMessage `json:"currentSettings"`
				NewSettings     json.RawMessage `json:"newSettings"`
				CurrentHost     string          `json:"currentHost"`
				RelaunchReasons []string        `json:"relaunchReasons"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &want); err != nil {
				t.Fatal(err)
			}
			got := byUID[uid]
			if !got.Layered || got.Unavailable != "" || got.Host != want.CurrentHost {
				t.Fatalf("SDK %+v, CLI host %q", got, want.CurrentHost)
			}
			for side, pair := range map[string][2]any{"current": {got.Current, want.CurrentSettings}, "new": {got.New, want.NewSettings}} {
				gotJSON, _ := json.Marshal(pair[0])
				var compact bytes.Buffer
				if err := json.Compact(&compact, pair[1].(json.RawMessage)); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(gotJSON, compact.Bytes()) {
					t.Fatalf("%s settings differ\n SDK %s\n CLI %s", side, gotJSON, compact.Bytes())
				}
			}
			if !reflect.DeepEqual(got.Reasons, want.RelaunchReasons) {
				t.Fatalf("reasons SDK %q CLI %q", got.Reasons, want.RelaunchReasons)
			}
			if name == "claude-reasons" && (len(got.Reasons) == 0 || got.New.Instructions.Value != "reviewer") {
				t.Fatalf("fixture must give reasons: %+v", got)
			}
		})
	}
	// The fixtures must exercise the prompt parts: every Agent but the
	// reply-only one passes the guidance its host plans.
	if !slices.Contains(byUID[uids["claude-tmux"]].Reasons, agentsettings.ReasonGuidanceChanged) || slices.Contains(byUID[uids["claude-reply-only"]].Reasons, agentsettings.ReasonGuidanceChanged) {
		t.Fatalf("prompt parts not exercised: %+v", read.Agents)
	}
}
