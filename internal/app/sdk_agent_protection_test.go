package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func protectionFixture(t *testing.T, process bool) metadata.Registry {
	t.Helper()
	f := newFakeResourceStore(t)
	addControlReadRoot(t, f)
	if process {
		for i := range f.registry.Panes {
			if f.registry.Panes[i].Spec.Role == metadata.PaneRoleAgent {
				f.registry.Panes[i].Spec.Runtime.Kind = metadata.RuntimeProcess
			}
		}
	}
	if err := f.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return f.registry
}

func TestAgentProtectionSDKCLIParity(t *testing.T) {
	for _, process := range []bool{false, true} {
		for _, uid := range []string{"agt-alpha-codex", "agt-home"} {
			for _, legacy := range []string{"absent", "true", "false", "", "legacy"} {
				for _, protect := range []bool{true, false} {
					t.Run(strings.Join([]string{map[bool]string{true: "process", false: "tmux"}[process], uid, legacy, map[bool]string{true: "set", false: "remove"}[protect]}, "/"), func(t *testing.T) {
						reg := protectionFixture(t, process)
						a, _ := reg.Agent(uid)
						if a.Metadata.Labels == nil {
							a.Metadata.Labels = map[string]string{}
						}
						a.Metadata.Labels["unrelated"] = "preserved"
						if legacy != "absent" {
							a.Metadata.Labels[metadata.LabelProtected] = legacy
						}
						sdkState, _, client := renameState(t, reg)
						cliState, s, _ := renameState(t, reg)
						req := core.SetAgentProtectionRequest{Ref: "uid:" + uid, Protected: protect}
						// A runtime call would fail the test. Selection must ignore inherited state.
						t.Setenv("TMUX", "/bad/socket,999,1")
						t.Setenv("TMUX_PANE", "%999")
						t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "foreign")
						calls := 0
						command := &labelCommand{store: s, runtime: func() metadata.RuntimeObservation { calls++; return metadata.RuntimeObservation{} }}
						operand := metadata.LabelProtected + "-"
						if protect {
							operand = metadata.LabelProtected + "=true"
						}
						for attempt := range 2 {
							before := projectRegistry(t, sdkState)
							got, err := client.SetAgentProtection(context.Background(), req)
							if err != nil {
								t.Fatal(err)
							}
							var stdout, stderr bytes.Buffer
							err = command.Run([]string{"agent", req.Ref, operand}, &stdout, &stderr)
							if err != nil {
								t.Fatal(err)
							}
							wantChanged := attempt == 0 && ((protect && legacy != "true") || (!protect && legacy != "absent"))
							if got.Changed != wantChanged {
								t.Fatalf("Changed=%v want %v", got.Changed, wantChanged)
							}
							sdkReg, cliReg := projectRegistry(t, sdkState), projectRegistry(t, cliState)
							// Wall-clock commit timestamps are the sole nondeterministic field.
							cliReg.UpdatedAt = sdkReg.UpdatedAt
							if !reflect.DeepEqual(sdkReg, cliReg) {
								t.Fatal("CLI/SDK Registry differs")
							}
							committed, _ := sdkReg.Agent(uid)
							expected := before.Clone()
							target, _ := expected.Agent(uid)
							if protect {
								target.Metadata.Labels[metadata.LabelProtected] = "true"
							} else {
								delete(target.Metadata.Labels, metadata.LabelProtected)
							}
							expected.UpdatedAt = sdkReg.UpdatedAt
							if !reflect.DeepEqual(expected, sdkReg) {
								t.Fatal("unrelated field or resource changed")
							}
							var projected core.Agent
							projection, _, _ := resources.ResourceFor(sdkReg, metadata.KindAgent, uid)
							raw, _ := json.Marshal(projection)
							if err := json.Unmarshal(raw, &projected); err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(got.Agent, projected) {
								t.Fatal("result is not committed Agent")
							}
							if stdout.String() != labelResultLine("label agent", metadata.KindAgent, committed.Metadata, wantChanged)+"\n" || stderr.Len() != 0 {
								t.Fatalf("CLI presentation: %q %q", stdout.String(), stderr.String())
							}
						}
						if calls != 0 {
							t.Fatalf("runtime calls=%d", calls)
						}
					})
				}
			}
		}
	}
}

func TestAgentProtectionSDKSelectorsAndRefusals(t *testing.T) {
	reg := protectionFixture(t, false)
	for _, tc := range []struct {
		req  core.SetAgentProtectionRequest
		uid  string
		code projmux.Code
	}{
		{req: core.SetAgentProtectionRequest{Ref: "uid:agt-alpha-codex"}, uid: "agt-alpha-codex"},
		{req: core.SetAgentProtectionRequest{Ref: "codex", Project: "alpha", Window: "main"}, uid: "agt-alpha-codex"},
		{req: core.SetAgentProtectionRequest{Ref: "codex", Window: "uid:win-home"}, uid: "agt-home"},
		{req: core.SetAgentProtectionRequest{}, code: projmux.CodeSelectorInvalid},
		{req: core.SetAgentProtectionRequest{Ref: "uid:"}, code: projmux.CodeSelectorInvalid},
		{req: core.SetAgentProtectionRequest{Ref: "bad name", Project: "alpha"}, code: projmux.CodeSelectorInvalid},
		{req: core.SetAgentProtectionRequest{Ref: "codex"}, code: projmux.CodeSelectorInvalid},
		{req: core.SetAgentProtectionRequest{Ref: "codex", Window: "main"}, code: projmux.CodeSelectorAmbiguous},
		{req: core.SetAgentProtectionRequest{Ref: "uid:absent"}, code: projmux.CodeResourceNotFound},
		{req: core.SetAgentProtectionRequest{Ref: "uid:agt-alpha-codex", Project: "beta"}, code: projmux.CodeResourceNotFound},
		{req: core.SetAgentProtectionRequest{Ref: "uid:agt-alpha-codex", Window: "uid:win-home"}, code: projmux.CodeResourceNotFound},
	} {
		t.Run(tc.req.Ref+"/"+tc.req.Project+"/"+tc.req.Window, func(t *testing.T) {
			state, _, client := renameState(t, reg)
			before, err := os.ReadFile(store.PathFor(state))
			if err != nil {
				t.Fatal(err)
			}
			tc.req.Protected = true
			got, err := client.SetAgentProtection(context.Background(), tc.req)
			if tc.code != "" {
				var refusal *projmux.Refusal
				if !errors.As(err, &refusal) || refusal.Code != tc.code || got.Changed {
					t.Fatalf("%+v %v want %s", got, err, tc.code)
				}
				after, _ := os.ReadFile(store.PathFor(state))
				if !bytes.Equal(before, after) {
					t.Fatal("refusal mutated Registry")
				}
			} else if err != nil || got.Agent.Metadata.UID != tc.uid {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	for _, deadline := range []bool{false, true} {
		state, _, client := renameState(t, reg)
		var ctx context.Context
		var cancel context.CancelFunc
		want := context.Canceled
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
			want = context.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		defer cancel()
		before, _ := os.ReadFile(store.PathFor(state))
		_, err := client.SetAgentProtection(ctx, core.SetAgentProtectionRequest{Ref: "uid:agt-alpha-codex", Protected: true})
		after, _ := os.ReadFile(store.PathFor(state))
		if !errors.Is(err, want) || !bytes.Equal(before, after) {
			t.Fatal(err)
		}
	}
	state, _, client := renameState(t, reg)
	if err := os.WriteFile(store.PathFor(state), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := client.SetAgentProtection(context.Background(), core.SetAgentProtectionRequest{Ref: "uid:agt-alpha-codex"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRegistryUnavailable {
		t.Fatal(err)
	}
}
