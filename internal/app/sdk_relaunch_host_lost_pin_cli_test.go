package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func sdkHostLostClient(t *testing.T, f processCreateCLI) *projmux.Client {
	t.Helper()
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Without the opt-in the SDK refuses a lost host as it did before the field
// existed; the Code literal below was observed on main before the change.
func TestSDKRelaunchLostHostWithoutOptInRefusesAsToday(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := hostLostCLIProviderFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
			_, err := sdkHostLostClient(t, f).RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Detach: true, Confirm: true, Prompt: "continue"})
			var refused *projmux.Refusal
			if !errors.As(err, &refused) || refused.Code != "OBSERVE" || refused.AgentUID != old.Binding.AgentUID {
				t.Fatalf("refusal: %v", err)
			}
			if !reflect.DeepEqual(before, mustRegistry(t, f)) {
				t.Fatal("refusal wrote the Registry")
			}
		})
	}
}
