package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestSDKHostLostVerdictParity records a lost host through the real CLI and
// requires the read-only verdict to agree with PrepareAgentResume(AllowHostLost):
// a Claude Agent is prepared and a candidate with the prepared proof's
// conversation and binding; a Codex Agent is a candidate too while the prepare
// entry refuses it as not Claude. The verdict's Client cannot start an owner.
func TestSDKHostLostVerdictParity(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			ref, _, old := recordedLostHostCLI(t, ctx, f, provider)
			options := projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary}
			client, err := projmux.NewClient(options)
			if err != nil {
				t.Fatal(err)
			}
			prepared, prepareErr := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, AllowHostLost: true})
			handle, _, err := client.PrepareRegistrySnapshot(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			options.ProjmuxExecutable = filepath.Join(f.root, "missing-owner")
			noOwner, err := projmux.NewClient(options)
			if err != nil {
				t.Fatal(err)
			}
			verdict, err := noOwner.ReadAgentHostLostFromPrepared(ctx, handle, core.ReadAgentHostLostRequest{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if !verdict.Candidate || !verdict.HostIdentityAbsent || !verdict.ChildIdentityAbsent || !verdict.ActivationAbsent || verdict.AgentUID != old.Binding.AgentUID || verdict.PaneUID != old.Binding.PaneUID || verdict.Binding.Generation != old.Binding.Generation || verdict.Binding.OperationID != old.Binding.OperationID {
				t.Fatalf("verdict %+v for recorded %+v", verdict, old.Binding)
			}
			if provider == aiModeCodex {
				var refusal *projmux.Refusal
				if !errors.As(prepareErr, &refusal) || refusal.Code != projmux.CodeResumeRequestInvalid || verdict.Conversation != old.ThreadID {
					t.Fatalf("codex: verdict %+v prepare %v", verdict, prepareErr)
				}
				return
			}
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			if got := prepared.Preparation; got.AgentUID != verdict.AgentUID || got.PaneUID != verdict.PaneUID || got.Conversation != verdict.Conversation || got.Binding != verdict.Binding || !got.HostLost {
				t.Fatalf("prepared %+v differs from verdict %+v", got, verdict)
			}
		})
	}
}
