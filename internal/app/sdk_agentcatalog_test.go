package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestAgentCatalogSDKCLIParity(t *testing.T) {
	reg, client := sdkResourceFixture(t)
	for _, provider := range []string{"claude", "codex", "antigravity"} {
		sdk, err := client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Provider: provider})
		if err != nil {
			t.Fatal(err)
		}
		cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) {
			t.Fatal("static query read registry")
			return metadata.Registry{}, nil
		}}
		var out, stderr bytes.Buffer
		if err = cmd.Run([]string{"capabilities", "--provider", provider, "-o", "json"}, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if sdkJSON(t, sdk) != out.String() {
			t.Fatalf("%s bytes differ", provider)
		}
	}
	for _, ref := range []string{"reader", "uid:agt-alpha"} {
		sdk, err := client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Agent: ref})
		if err != nil {
			t.Fatal(err)
		}
		cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }}
		var out, stderr bytes.Buffer
		if err = cmd.Run([]string{"capabilities", ref, "-o", "json"}, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if sdkJSON(t, sdk) != out.String() {
			t.Fatalf("Agent %s bytes differ:\n%s\n%s", ref, sdkJSON(t, sdk), out.String())
		}
	}
	for _, provider := range []string{"claude", "codex"} {
		stub := func(ctx context.Context) ([]string, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 15*time.Second {
				t.Fatal("missing CLI model bound")
			}
			return []string{"z-model", "a-model", "z-model"}, nil
		}
		c, err := projmux.NewClient(projmux.Options{CodexModelLister: stub})
		if err != nil {
			t.Fatal(err)
		}
		sdk, err := c.Models(context.Background(), provider)
		if err != nil {
			t.Fatal(err)
		}
		cmd := &agentCommand{listCodexModels: stub}
		var out, stderr bytes.Buffer
		if err = cmd.Run([]string{"models", "--provider", provider, "-o", "json"}, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if sdkJSON(t, sdk) != out.String() {
			t.Fatalf("%s model bytes differ", provider)
		}
	}
}
func TestAgentCatalogSDKRefusalParity(t *testing.T) {
	reg, client := sdkResourceFixture(t)
	for _, test := range []struct {
		request projmux.CapabilitiesRequest
		args    []string
		code    projmux.Code
	}{
		{projmux.CapabilitiesRequest{Provider: "bad"}, []string{"capabilities", "--provider", "bad"}, projmux.CodeProviderInvalid},
		{projmux.CapabilitiesRequest{Agent: "uid:"}, []string{"capabilities", "uid:"}, projmux.CodeSelectorInvalid},
		{projmux.CapabilitiesRequest{Agent: "absent"}, []string{"capabilities", "absent"}, projmux.CodeResourceNotFound},
		{projmux.CapabilitiesRequest{Provider: "claude", Agent: "reader"}, []string{"capabilities", "reader", "--provider", "claude"}, projmux.CodeCapabilitiesRequestInvalid},
	} {
		cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }}
		var out, stderr bytes.Buffer
		cliErr := cmd.Run(test.args, &out, &stderr)
		_, sdkErr := client.Capabilities(context.Background(), test.request)
		var refusal *projmux.Refusal
		if !errors.As(sdkErr, &refusal) || refusal.Code != test.code || cliErr == nil || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != 2 || out.Len() != 0 {
			t.Fatalf("%v CLI=%v SDK=%v code=%v", test.args, cliErr, sdkErr, refusal)
		}
	}
	for _, provider := range []string{"bad", "antigravity"} {
		cmd := &agentCommand{}
		var out, stderr bytes.Buffer
		cliErr := cmd.Run([]string{"models", "--provider", provider}, &out, &stderr)
		_, sdkErr := client.Models(context.Background(), provider)
		var refusal *projmux.Refusal
		if !errors.As(sdkErr, &refusal) || cliErr == nil || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != 2 {
			t.Fatalf("models CLI=%v SDK=%v", cliErr, sdkErr)
		}
		want := projmux.CodeProviderInvalid
		if provider == "antigravity" {
			want = projmux.CodeModelsUnsupported
		}
		if refusal.Code != want {
			t.Fatal(refusal)
		}
	}
	for _, test := range []struct {
		cause error
		code  projmux.Code
	}{{errors.New("stub unavailable"), projmux.CodeModelsUnavailable}, {context.DeadlineExceeded, projmux.CodeModelsTimeout}} {
		stub := func(context.Context) ([]string, error) { return nil, test.cause }
		client, err := projmux.NewClient(projmux.Options{CodexModelLister: stub})
		if err != nil {
			t.Fatal(err)
		}
		_, sdkErr := client.Models(context.Background(), "codex")
		var refusal *projmux.Refusal
		cmd := &agentCommand{listCodexModels: stub}
		var out, stderr bytes.Buffer
		cliErr := cmd.Run([]string{"models", "--provider", "codex"}, &out, &stderr)
		if !errors.As(sdkErr, &refusal) || refusal.Code != test.code || cliErr == nil || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != 1 {
			t.Fatalf("CLI=%v SDK=%v", cliErr, sdkErr)
		}
	}
}

func persistCatalogFixture(t *testing.T, reg metadata.Registry) {
	t.Helper()
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	path := store.PathFor(paths.StateDir)
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestAgentCatalogSDKExactRuntimeAndIsolation(t *testing.T) {
	_, client := sdkResourceFixture(t)
	cmd, fake, _ := exactControlCLICommand(t)
	reg := fake.registry.Clone()
	persistCatalogFixture(t, reg)
	var out, stderr bytes.Buffer
	if err := cmd.Run([]string{"capabilities", "uid:agt-alpha-codex", "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	sdk, err := client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Agent: "uid:agt-alpha-codex"})
	if err != nil {
		t.Fatal(err)
	}
	if sdkJSON(t, sdk) != out.String() {
		t.Fatalf("exact runtime bytes differ:\n%s\n%s", sdkJSON(t, sdk), out.String())
	}
	if sdk.Runtime == nil || !sdk.Runtime.Ready || sdk.Runtime.ConnectionEpoch == 0 || sdk.Runtime.LiveVerified {
		t.Fatalf("missing exact epoch projection: %+v", sdk.Runtime)
	}
	sdk.Agent.Name = "changed"
	sdk.Runtime.ConnectionEpoch = 999
	*sdk.Capabilities[0].Available = !*sdk.Capabilities[0].Available
	again, err := client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Agent: "uid:agt-alpha-codex"})
	if err != nil || sdkJSON(t, again) != out.String() {
		t.Fatalf("exact graph aliases next read: %v", err)
	}
	// Offline Claude has no endpoint; neither SDK nor CLI may invent live authority.
	agent, _ := reg.Agent("agt-alpha-codex")
	agent.Spec.Provider = "claude"
	agent.Status.Phase = metadata.PhaseOffline
	agent.Status.PaneRef = ""
	persistCatalogFixture(t, reg)
	cmd = &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }}
	out.Reset()
	stderr.Reset()
	if err = cmd.Run([]string{"capabilities", "uid:agt-alpha-codex", "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	sdk, err = client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Agent: "uid:agt-alpha-codex"})
	if err != nil || sdkJSON(t, sdk) != out.String() || sdk.Runtime.Coordination == nil {
		t.Fatalf("Claude bytes: %v\n%s\n%s", err, sdkJSON(t, sdk), out.String())
	}
}
func TestAgentCatalogSDKSelectionAmbiguityAndRegistryFailure(t *testing.T) {
	reg, client := sdkResourceFixture(t)
	duplicate := reg.Agents[0].Clone()
	duplicate.Metadata.UID = "agt-beta"
	duplicate.Metadata.OwnerRef.UID = "win-beta-main"
	reg.Agents = append(reg.Agents, duplicate)
	reg.NameReservations = append(reg.NameReservations, metadata.NameReservation{Scope: "prj-beta", Kind: metadata.KindAgent, Name: "reader", UID: "agt-beta"})
	persistCatalogFixture(t, reg)
	cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }}
	var out, stderr bytes.Buffer
	cliErr := cmd.Run([]string{"capabilities", "reader"}, &out, &stderr)
	_, sdkErr := client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Agent: "reader"})
	var refusal *projmux.Refusal
	if !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodeSelectorAmbiguous || cliErr == nil || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != 2 {
		t.Fatalf("ambiguity CLI=%v SDK=%v", cliErr, sdkErr)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.PathFor(paths.StateDir), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = newAgentCommand()
	cliErr = cmd.Run([]string{"capabilities", "uid:agt-alpha"}, &out, &stderr)
	_, sdkErr = client.Capabilities(context.Background(), projmux.CapabilitiesRequest{Agent: "uid:agt-alpha"})
	if !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodeRegistryUnavailable || cliErr == nil || cliErr.Error() != sdkErr.Error() {
		t.Fatalf("registry CLI=%v SDK=%v", cliErr, sdkErr)
	}
}
