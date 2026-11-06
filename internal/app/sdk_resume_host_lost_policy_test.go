package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func hostLostPolicyClient(t *testing.T, f processCreateCLI) *projmux.Client {
	t.Helper()
	client, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func hostLostPolicyRequest(provider, ref string, policy core.ResumeHostLostPolicy) core.ResumeAgentRequest {
	req := core.ResumeAgentRequest{Ref: ref, Detach: true, HostLost: policy}
	if provider == aiModeClaude {
		req.Prompt = "policy first input"
	}
	return req
}

// requireHostLostPolicyRefused expects the existing refusal: reply code
// process-resume-refused with the token in the message, and no Registry change.
func requireHostLostPolicyRefused(t *testing.T, err error, token string, f processCreateCLI, before m.Registry, step string) {
	t.Helper()
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || string(refusal.Code) != "process-resume-refused" || !strings.Contains(err.Error(), token) {
		t.Fatalf("%s: want process-resume-refused with %s, got %#v (%v)", step, token, refusal, err)
	}
	if !reflect.DeepEqual(before, mustRegistry(t, f)) {
		t.Fatalf("%s: a refused resume changed the Registry", step)
	}
}

// ResumeAgentRequest.HostLost requires or refuses a host-lost resume in the
// owner process that also decides the candidate.
func TestPublicResumeAgentHostLostPolicyCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider+"/host-lost", func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
			cleanupDetachedResume(t, f, ref)
			client := hostLostPolicyClient(t, f)
			_, err := client.ResumeAgent(ctx, hostLostPolicyRequest(provider, ref, core.ResumeHostLostRefuse))
			requireHostLostPolicyRefused(t, err, "process-resume-not-resumable", f, before, "refuse a host-lost Agent")
			resumed, err := client.ResumeAgent(ctx, hostLostPolicyRequest(provider, ref, core.ResumeHostLostRequire))
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Binding.AgentUID != old.Binding.AgentUID || resumed.Binding.Generation == old.Binding.Generation {
				t.Fatalf("require did not start a new generation of the same Agent: %+v", resumed.Binding)
			}
			next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *m.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("the conversation was replaced")
			}
		})
		t.Run(provider+"/offline", func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first dialogue"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool { return r.ResumeState == m.ProcessResumable })
			cleanupDetachedResume(t, f, first.ref)
			client := hostLostPolicyClient(t, f)
			before := mustRegistry(t, f)
			_, err := client.ResumeAgent(ctx, hostLostPolicyRequest(provider, first.ref, core.ResumeHostLostRequire))
			requireHostLostPolicyRefused(t, err, "process-resume-not-resumable", f, before, "require on a resumable Agent")
			if _, err = client.ResumeAgent(ctx, hostLostPolicyRequest(provider, first.ref, core.ResumeHostLostRefuse)); err != nil {
				t.Fatal(err)
			}
			next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *m.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("the conversation was replaced")
			}
			before = mustRegistry(t, f)
			_, err = client.ResumeAgent(ctx, hostLostPolicyRequest(provider, first.ref, core.ResumeHostLostRequire))
			requireHostLostPolicyRefused(t, err, "process-resume-owned", f, before, "require while an owner is alive")
		})
	}
}

