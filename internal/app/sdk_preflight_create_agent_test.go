package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// preflightRefusalCase is one create agent request CreateAgent refuses for
// its settings, as a CreateAgentRequest and as the same `create agent` argv.
type preflightRefusalCase struct {
	name string
	req  core.CreateAgentRequest
	args []string
	code projmux.Code
}

// preflightRefusalCases cover a missing profile, a profile whose provider
// differs, instructions for a provider that takes none, missing instructions,
// a refused effort and a profile on the dialogue lane, in Window extra.
func preflightRefusalCases() []preflightRefusalCase {
	base := func(provider string, extra ...string) []string {
		return append([]string{"agent", "--project", "sample", "--window", "extra", "--provider", provider, "--name", "worker"}, extra...)
	}
	req := func(r core.CreateAgentRequest) core.CreateAgentRequest {
		r.Project, r.Window, r.Name = "sample", "extra", "worker"
		return r
	}
	return []preflightRefusalCase{
		{"missing-profile", req(core.CreateAgentRequest{Provider: "claude", Profile: "absent"}), base("claude", "--profile", "absent"), projmux.CodeProfileNotFound},
		{"profile-provider-mismatch", req(core.CreateAgentRequest{Provider: "antigravity", Profile: "claude"}), base("antigravity", "--profile", "claude"), projmux.CodeProfileProviderMismatch},
		{"instructions-provider-unsupported", req(core.CreateAgentRequest{Provider: "antigravity", Instructions: "reviewer"}), base("antigravity", "--instructions", "reviewer"), projmux.CodeInstructionsProviderUnsupported},
		{"missing-instructions", req(core.CreateAgentRequest{Provider: "claude", Instructions: "absent"}), base("claude", "--instructions", "absent"), projmux.CodePersonaNotFound},
		{"effort", req(core.CreateAgentRequest{Provider: "claude", Effort: "bad"}), base("claude", "--effort", "bad"), projmux.CodeCreateRequestInvalid},
		{"profile-lane", req(core.CreateAgentRequest{Provider: "claude", Profile: "claude", DialogueReplyOnly: true}), base("claude", "--profile", "claude", "--dialogue-reply-only"), projmux.CodeProfileLaneUnsupported},
	}
}

// newPreflightFixture is the operator creator fixture with the profiles and
// instructions the refusal cases name.
func newPreflightFixture(t *testing.T) sdkOperatorCreatorFixture {
	t.Helper()
	f := newSDKOperatorCreatorFixture(t, sdkCreateBinary(t), sdkAgentHome(t), t.TempDir())
	if _, err := f.client.WriteProfile(context.Background(), "claude", []byte("provider = \"claude\"\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.WriteInstructions(context.Background(), "reviewer", []byte("Review carefully.\n")); err != nil {
		t.Fatal(err)
	}
	return f
}

// preflightCLICode is the token a CLI create refusal carries, as the SDK
// and the detached process creator classify it.
func preflightCLICode(err error) projmux.Code {
	return projmux.Code(mutationruntime.ProcessCreateErrorCode(err))
}

// TestPreflightCreateAgentRefusalMatchesCLI requires PreflightCreateAgent to
// refuse each settings defect with the message and token `create agent`
// gives for the same request.
func TestPreflightCreateAgentRefusalMatchesCLI(t *testing.T) {
	f := newPreflightFixture(t)
	for _, tc := range preflightRefusalCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, sdkErr := f.client.PreflightCreateAgent(context.Background(), tc.req)
			var refusal *projmux.Refusal
			if !errors.As(sdkErr, &refusal) || refusal.Code != tc.code {
				t.Fatalf("preflight %T %v, want %s", sdkErr, sdkErr, tc.code)
			}
			c := sdkAgentCLI(t, f.state, f.config, f.socket, f.binary, f.home)
			var stdout, stderr bytes.Buffer
			cliErr := c.Run(tc.args, &stdout, &stderr)
			if cliErr == nil {
				t.Fatalf("CLI created: %s", &stdout)
			}
			if cliErr.Error() != sdkErr.Error() {
				t.Fatalf("message differs\nCLI %q\nSDK %q", cliErr.Error(), sdkErr.Error())
			}
			if got := preflightCLICode(cliErr); got != refusal.Code {
				t.Fatalf("token differs: CLI %s SDK %s", got, refusal.Code)
			}
		})
	}
}

// TestPreflightCreateAgentMatchesCreateAgent requires the preflight refusal
// of a request to be the refusal CreateAgent gives it, Code and message.
func TestPreflightCreateAgentMatchesCreateAgent(t *testing.T) {
	f := newPreflightFixture(t)
	cases := preflightRefusalCases()
	cases = append(cases,
		preflightRefusalCase{name: "creator", req: core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Creator: "uid:missing"}},
		preflightRefusalCase{name: "tmux-detach", req: core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Host: "tmux", Detach: true}},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := projectRegistryBytes(t, f.state)
			_, preErr := f.client.PreflightCreateAgent(context.Background(), tc.req)
			_, createErr := f.client.CreateAgent(context.Background(), tc.req)
			var pre, created *projmux.Refusal
			if !errors.As(preErr, &pre) || !errors.As(createErr, &created) {
				t.Fatalf("untyped: preflight %T %v create %T %v", preErr, preErr, createErr, createErr)
			}
			if pre.Code != created.Code || preErr.Error() != createErr.Error() {
				t.Fatalf("preflight %s %q, CreateAgent %s %q", pre.Code, preErr, created.Code, createErr)
			}
			if projectRegistryBytes(t, f.state) != before {
				t.Fatal("refused create changed the Registry")
			}
		})
	}
}
