package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type validateOutcome struct {
	proof    core.AgentResumePreparation
	code     projmux.Code
	agent    string
	detail   *projmux.ResumeProcessError
	canceled bool
	ok       bool
}

func validateOutcomeOf(proof core.AgentResumePreparation, err error) validateOutcome {
	out := validateOutcome{proof: proof, ok: err == nil}
	var refusal *projmux.Refusal
	if errors.As(err, &refusal) {
		out.code, out.agent = refusal.Code, refusal.AgentUID
	}
	errors.As(err, &out.detail)
	out.canceled = errors.Is(err, context.Canceled)
	return out
}

// TestSDKValidateFromPreparedParity runs both validation entries on one seed
// and requires the same verdict. The new entry starts no owner, so its
// ResumeProcessError carries the same Code and Binding but no Owner.
func TestSDKValidateFromPreparedParity(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	f := hostLostCLIProviderFixture(t, "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ref, _, _ := recordedLostHostCLI(t, ctx, f, "claude")
	options := projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary}
	client, err := projmux.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, AllowHostLost: true})
	if err != nil {
		t.Fatal(err)
	}
	handle, _, err := client.PrepareRegistrySnapshot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The new entry must not need the owner binary: a Client that cannot start
	// one still judges the handle.
	options.ProjmuxExecutable = filepath.Join(f.root, "missing-owner")
	noOwner, err := projmux.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := New().agent.deferredStatePath("deferred-launches", prepared.Preparation.AgentUID)
	rewrite := func(change func(map[string]any)) func() {
		raw, e := os.ReadFile(sidecar)
		if e != nil {
			t.Fatal(e)
		}
		var record map[string]any
		if e = json.Unmarshal(raw, &record); e != nil {
			t.Fatal(e)
		}
		change(record)
		changed, e := json.Marshal(record)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(sidecar, changed, 0600); e != nil {
			t.Fatal(e)
		}
		return func() {
			if e := os.WriteFile(sidecar, raw, 0600); e != nil {
				t.Error(e)
			}
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*core.AgentResumePreparation, *core.ValidateAgentResumePreparationRequest) func()
		valid  bool
	}{
		{"host-lost proof", func(*core.AgentResumePreparation, *core.ValidateAgentResumePreparationRequest) func() { return nil }, true},
		{"digest", func(p *core.AgentResumePreparation, _ *core.ValidateAgentResumePreparationRequest) func() {
			p.LaunchDigest = "changed"
			return nil
		}, false},
		{"pane", func(p *core.AgentResumePreparation, _ *core.ValidateAgentResumePreparationRequest) func() {
			p.PaneUID = "wrong-pane"
			return nil
		}, false},
		{"conversation", func(p *core.AgentResumePreparation, _ *core.ValidateAgentResumePreparationRequest) func() {
			p.Conversation = "other"
			return nil
		}, false},
		{"stale binding", func(p *core.AgentResumePreparation, _ *core.ValidateAgentResumePreparationRequest) func() {
			p.Binding.Generation = "other-generation"
			return nil
		}, false},
		{"other agent", func(p *core.AgentResumePreparation, _ *core.ValidateAgentResumePreparationRequest) func() {
			p.AgentUID = "agent-other"
			return nil
		}, false},
		{"host-lost bit", func(p *core.AgentResumePreparation, _ *core.ValidateAgentResumePreparationRequest) func() {
			p.HostLost = false
			return nil
		}, false},
		{"empty ref", func(_ *core.AgentResumePreparation, r *core.ValidateAgentResumePreparationRequest) func() {
			r.Ref = ""
			return nil
		}, false},
		{"unknown ref", func(_ *core.AgentResumePreparation, r *core.ValidateAgentResumePreparationRequest) func() {
			r.Ref = "uid:agent-missing"
			return nil
		}, false},
		{"sidecar missing", func(*core.AgentResumePreparation, *core.ValidateAgentResumePreparationRequest) func() {
			raw, e := os.ReadFile(sidecar)
			if e != nil || os.Remove(sidecar) != nil {
				t.Fatal(e)
			}
			return func() {
				if e := os.WriteFile(sidecar, raw, 0600); e != nil {
					t.Error(e)
				}
			}
		}, false},
		{"permission mode", func(*core.AgentResumePreparation, *core.ValidateAgentResumePreparationRequest) func() {
			return rewrite(func(record map[string]any) {
				command, _ := record["Command"].(map[string]any)
				args, _ := command["Args"].([]any)
				command["Args"] = append([]any{"--permission-mode", "bypassPermissions", "--permission-mode", "plan"}, args...)
			})
		}, false},
		{"snapshot files", func(*core.AgentResumePreparation, *core.ValidateAgentResumePreparationRequest) func() {
			return rewrite(func(record map[string]any) {
				files, _ := record["Files"].(map[string]any)
				files[filepath.Join(f.root, "no-such-snapshot-file")] = "digest"
			})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof, req := prepared.Preparation, core.ValidateAgentResumePreparationRequest{Ref: ref}
			restore := tc.change(&proof, &req)
			if restore != nil {
				defer restore()
			}
			req.Preparation = proof
			oldProof, oldErr := client.ValidateAgentResumePreparation(ctx, req)
			newProof, newErr := noOwner.ValidateAgentResumePreparationFromPrepared(ctx, handle, req)
			want, got := validateOutcomeOf(oldProof, oldErr), validateOutcomeOf(newProof, newErr)
			if want.ok != tc.valid || got.ok != want.ok || got.proof != want.proof || got.code != want.code || got.agent != want.agent {
				t.Fatalf("verdict differs: old %+v (%v) new %+v (%v)", want, oldErr, got, newErr)
			}
			if (want.detail == nil) != (got.detail == nil) {
				t.Fatalf("detail presence differs: old %v new %v", oldErr, newErr)
			}
			if want.detail != nil {
				if want.detail.Code != got.detail.Code || want.detail.Binding != got.detail.Binding || got.detail.Owner != (core.ProcessIdentity{}) {
					t.Fatalf("detail differs: old %+v new %+v", want.detail, got.detail)
				}
			}
		})
	}
	t.Run("entry cancellation", func(t *testing.T) {
		canceled, stop := context.WithCancel(ctx)
		stop()
		req := core.ValidateAgentResumePreparationRequest{Ref: ref, Preparation: prepared.Preparation}
		want := validateOutcomeOf(client.ValidateAgentResumePreparation(canceled, req))
		got := validateOutcomeOf(client.ValidateAgentResumePreparationFromPrepared(canceled, handle, req))
		if !want.canceled || !got.canceled {
			t.Fatalf("cancellation: old %+v new %+v", want, got)
		}
	})
}
