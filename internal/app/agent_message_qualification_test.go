package app

import (
	"os"
	"path/filepath"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestClaudeQualificationResponseRequiresExactStableClosedShape(t *testing.T) {
	ref := "qualification-exact"
	pending := claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-pending",
		QualificationRef: ref, ProviderVersion: claudeFrozenFrameProviderVersion, AutoResend: false}
	if _, ok := validateClaudeQualificationResponse(pending, "", ""); !ok {
		t.Fatal("exact initial pending response refused")
	}
	qualified := claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-qualified",
		QualificationRef: ref, ProviderVersion: claudeFrozenFrameProviderVersion,
		Reason: "exact-public-init-and-stop-marker", AutoResend: false}
	if _, ok := validateClaudeQualificationResponse(qualified, ref, "qualification-pending"); !ok {
		t.Fatal("exact pending-to-qualified response refused")
	}
	for _, test := range []struct {
		name       string
		response   claudeCoordinationResponse
		expected   string
		priorState string
	}{
		{name: "missing version", response: func() claudeCoordinationResponse { value := pending; value.ProviderVersion = ""; return value }()},
		{name: "old version", response: func() claudeCoordinationResponse { value := pending; value.ProviderVersion = "2.1.261"; return value }()},
		{name: "missing ref", response: func() claudeCoordinationResponse { value := pending; value.QualificationRef = ""; return value }()},
		{name: "poll ref mismatch", response: qualified, expected: "qualification-other", priorState: "qualification-pending"},
		{name: "poll state regression", response: func() claudeCoordinationResponse {
			value := pending
			value.Kind = "qualification-writing"
			return value
		}(), expected: ref, priorState: "qualification-pending"},
		{name: "qualified ambiguous", response: func() claudeCoordinationResponse { value := qualified; value.Ambiguous = true; return value }(), expected: ref, priorState: "qualification-pending"},
		{name: "qualified reason mismatch", response: func() claudeCoordinationResponse { value := qualified; value.Reason = "echo-matched"; return value }(), expected: ref, priorState: "qualification-pending"},
		{name: "auto resend", response: func() claudeCoordinationResponse { value := pending; value.AutoResend = true; return value }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := validateClaudeQualificationResponse(test.response, test.expected, test.priorState); ok {
				t.Fatalf("uncertain qualification response accepted: %+v", test.response)
			}
		})
	}
}

func TestClaudeSourceNeedsCurrentLeaseButOnlyTargetNeedsIngressQualification(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	registry, err := intmetadata.NewStore(fixture.registryPath).LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := registry.Agent(fixture.route.AgentUID)
	if !ok {
		t.Fatal("fixture Agent missing")
	}
	resolver := liveAgentMessageRouteResolver{
		leaseProbe: func(string, coremetadata.AgentRouteRef) bool { return true },
		eligibilityProbe: func(string, coremetadata.AgentRouteRef) bool {
			return false
		},
	}
	source, err := resolver.Resolve(registry, *agent)
	if err != nil || !source.Same(fixture.route) || source.AgentUID != agent.Metadata.UID {
		t.Fatalf("same-UID recovered Claude source route=%+v err=%v", source, err)
	}
	if target, err := resolver.ResolveTarget(registry, *agent); err == nil || target.AgentUID != "" {
		t.Fatalf("unqualified Claude target route=%+v err=%v", target, err)
	}
}

func TestClaudeQualificationEvidenceFileMustStayExactOwnedPrivateInode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "qualification.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeQualificationEvidence(path); err != nil {
		t.Fatalf("exact private file refused before semantic validation: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeQualificationEvidence(path); err == nil {
		t.Fatal("world-readable evidence file accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeQualificationEvidence(path); err == nil {
		t.Fatal("unknown evidence field accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1} {"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeQualificationEvidence(path); err == nil {
		t.Fatal("multiple JSON values accepted")
	}
	link := filepath.Join(root, "qualification-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeQualificationEvidence(link); err == nil {
		t.Fatal("symlinked evidence file accepted")
	}
}
