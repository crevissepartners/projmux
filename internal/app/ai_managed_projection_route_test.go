package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// These fixtures enter hook ingest and the internal compatibility handler through Run, with a real
// managed Registry binding. Attribution reads are separate from the write
// transport, so a permissive/already-routed fake cannot conceal a raw write.
type managedProjectionCase struct {
	name  string
	args  []string
	want  map[string]string
	kind  coremetadata.AgentInteractionKind
	topic string
}

func managedProjectionCases(pane string) []managedProjectionCase {
	return []managedProjectionCase{
		{"interaction_hook_set", []string{"ingest", "claude-hook"}, map[string]string{aiPaneStateOption: "thinking", aiPaneBadgeKindOption: "in_progress", attentionStateOption: "busy"}, coremetadata.InteractionInProgress, ""},
		{"interaction_internal_idle", []string{"status", "set", "idle", pane}, map[string]string{aiPaneStateOption: "idle", aiPaneBadgeKindOption: "", attentionStateOption: ""}, coremetadata.InteractionIdle, ""},
		{"topic_set", []string{"topic", "set", "routed topic", "--pane", pane}, map[string]string{aiPaneTopicOption: "routed topic", aiPaneTopicManualOption: "on"}, "", "routed topic"},
		{"topic_clear", []string{"topic", "clear", "--pane", pane}, map[string]string{aiPaneTopicOption: "", aiPaneTopicManualOption: ""}, "", ""},
	}
}

func assertManagedProjectionRegistry(t *testing.T, h *sessionRefHarness, tc managedProjectionCase) {
	t.Helper()
	a := h.agent(t)
	if strings.HasPrefix(tc.name, "interaction") {
		source := "compatibility-ai"
		if tc.args[0] == "ingest" {
			source = "provider-hook"
		}
		if a.Status.Interaction.Kind != tc.kind || a.Status.Interaction.Source != source {
			t.Errorf("committed interaction = %+v, want %s %s", a.Status.Interaction, tc.kind, source)
		}
	} else if got := a.Metadata.Annotations[coremetadata.AnnotationAgentTopic]; got != tc.topic {
		t.Errorf("committed topic = %q, want %q", got, tc.topic)
	}
}

func prepareManagedProjection(t *testing.T) *sessionRefHarness {
	t.Helper()
	h := newSessionRefHarness(t, aiModeClaude)
	a, _ := h.registry.Agent(h.agentUID)
	a.Status.Interaction = coremetadata.AgentInteraction{Kind: coremetadata.InteractionInProgress, Source: "manual", ObservedAt: sessionRefObservedAt.Add(-time.Minute)}
	if a.Metadata.Annotations == nil {
		a.Metadata.Annotations = map[string]string{}
	}
	a.Metadata.Annotations[coremetadata.AnnotationAgentTopic] = "old topic"
	h.cmd.notifyStore = &stubNotifyStore{}
	h.cmd.producer = &storeAttentionNotifyProducer{store: &stubNotifyStore{}, ttl: time.Minute}
	return h
}

func TestManagedProjectionRoutingCallers(t *testing.T) {
	for _, tc := range managedProjectionCases("%7") {
		t.Run(tc.name, func(t *testing.T) {
			for _, inherited := range []bool{false, true} {
				t.Run(map[bool]string{false: "detached", true: "inherited"}[inherited], func(t *testing.T) {
					h := prepareManagedProjection(t)
					prefix := []string{"-L", defaultAppSocket}
					if inherited {
						prefix = []string{"-S", "/tmp/managed-fixture/socket"}
						base := h.cmd.lookupEnv
						h.cmd.lookupEnv = func(key string) string {
							if key == "TMUX" {
								return "/tmp/managed-fixture/socket,42,0"
							}
							return base(key)
						}
					}
					values := map[string]string{}
					for key := range tc.want {
						values[key] = "sentinel"
					}
					writes := 0
					h.cmd.runCommand = func(_ context.Context, name string, args ...string) error {

						if name != "tmux" || len(args) < 3 || !reflect.DeepEqual(args[:2], prefix) {
							return fmt.Errorf("unrouted managed projection: %q", args)
						}
						plain := args[2:]
						if len(plain) != 6 || plain[0] != "set-option" {
							return fmt.Errorf("unexpected write: %q", args)
						}
						key := plain[4]
						if plain[2] == "-u" {
							key = plain[5]
						}
						if _, projected := tc.want[key]; projected && h.updates != 1 {
							t.Error("projection attempted before Registry commit")
						}
						writes++
						if plain[2] == "-u" {
							delete(values, plain[5])
						} else {
							values[plain[4]] = plain[5]
						}
						return nil
					}
					var out, errOut bytes.Buffer
					err := runManagedProjectionCase(h, tc, &out, &errOut)
					if h.updates != 1 {
						t.Errorf("managed caller committed %d Registry updates, want 1", h.updates)
					}
					assertManagedProjectionRegistry(t, h, tc)
					if err != nil || out.Len() != 0 || errOut.Len() != 0 {
						t.Errorf("managed caller result=%v stdout=%q stderr=%q", err, out.String(), errOut.String())
					}
					for key, want := range tc.want {
						if got := values[key]; got != want {
							t.Errorf("managed target %s=%q, want %q (successful writes=%d)", key, got, want, writes)
						}
						if want == "" {
							if _, exists := values[key]; exists {
								t.Errorf("managed target %s must be unset", key)
							}
						}
					}
				})
			}
		})
	}
}

func TestManagedProjectionRoutingFailureKeepsCommit(t *testing.T) {
	for _, tc := range managedProjectionCases("%7") {
		for _, refusal := range []string{"disappeared", "denied", "foreign", "write-refused", "inherited-disappeared"} {
			t.Run(tc.name+"/"+refusal, func(t *testing.T) {
				h := prepareManagedProjection(t)
				prefix := []string{"-L", defaultAppSocket}
				if refusal == "inherited-disappeared" {
					prefix = []string{"-S", "/tmp/managed-fixture/disappeared"}
					baseEnv := h.cmd.lookupEnv
					h.cmd.lookupEnv = func(key string) string {
						if key == "TMUX" {
							return "/tmp/managed-fixture/disappeared,42,0"
						}
						return baseEnv(key)
					}
				}
				baseRead := h.cmd.readCommand
				h.cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if h.updates > 0 && len(args) > 0 && args[len(args)-1] == aiPaneOptionRouteFormat {
						if h.updates != 1 {
							t.Fatal("route refusal must occur after managed commit")
						}
						switch refusal {
						case "disappeared":
							return nil, os.ErrNotExist
						case "denied":
							return nil, os.ErrPermission
						case "foreign":
							return []byte(strings.Join([]string{"1", "%8", h.paneUID}, tmuxRowSep)), nil
						}
					}
					return baseRead(ctx, name, args...)
				}
				attempts := 0
				h.cmd.runCommand = func(_ context.Context, name string, args ...string) error {
					if h.updates == 0 {
						return nil
					} // Hook markers precede the managed commit.
					attempts++
					if h.updates != 1 {
						t.Error("write before commit")
					}
					if !reflect.DeepEqual(args[:min(len(args), 2)], prefix) {
						t.Errorf("fallback write %s %q", name, args)
					}
					return errors.New("fixture write refused")
				}
				err := runManagedProjectionCase(h, tc, &bytes.Buffer{}, &bytes.Buffer{})
				if h.updates != 1 {
					t.Errorf("managed caller committed %d Registry updates, want 1", h.updates)
				}
				assertManagedProjectionRegistry(t, h, tc)
				verb := "ai " + tc.args[0]
				if tc.args[0] == "ingest" {
					verb = "ai status"
				}
				wantErr := committedMirrorError(verb, coremetadata.KindAgent, h.agentUID, errAIPaneWriteUnavailable).Error()
				if err == nil || err.Error() != wantErr {
					t.Errorf("committedMirrorError = %v, want %q", err, wantErr)
				}
				wantAttempts := 0
				if refusal == "write-refused" || refusal == "inherited-disappeared" {
					wantAttempts = 1
				}
				if attempts != wantAttempts {
					t.Errorf("write attempts=%d, want %d; no fallback or later option allowed", attempts, wantAttempts)
				}
			})
		}
	}
}

func runManagedProjectionCase(h *sessionRefHarness, tc managedProjectionCase, out, errOut *bytes.Buffer) error {
	args := append([]string{}, tc.args...)
	if tc.args[0] == "ingest" {
		h.cmd.stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","cwd":"/src/app"}`)
		args = append(args, "--pane", h.paneUID)
	}
	return h.cmd.Run(args, out, errOut)
}
