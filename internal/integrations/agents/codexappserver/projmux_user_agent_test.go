package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// observedProjmuxUserAgent is the verbatim initialize userAgent a Codex 0.154.0
// daemon returned to this client (isolated CODEX_HOME, 2026-09-17). The app
// server names its own version under the client's clientInfo.name, not under a
// Codex product prefix.
const observedProjmuxUserAgent = "projmux/0.154.0 (Ubuntu 26.4.0; x86_64) unknown (projmux; 0.15.3)"

// negotiateUserAgent runs the real initialize handshake against a synthetic
// peer that answers with userAgent, and returns the version the client keeps.
func negotiateUserAgent(t *testing.T, userAgent string) string {
	t.Helper()
	local, peer := net.Pipe()
	client := NewClient(local)
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		defer peer.Close()
		reader := bufio.NewReader(peer)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			done <- err
			return
		}
		var request struct {
			Method string           `json:"method"`
			ID     int64            `json:"id"`
			Params initializeParams `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil || request.Method != methodInitialize || request.Params.ClientInfo.Name != clientName {
			done <- fmt.Errorf("first request = %q", line)
			return
		}
		result, _ := json.Marshal(initializeResult{UserAgent: userAgent, PlatformFamily: "unix", PlatformOS: "linux"})
		if _, err := fmt.Fprintf(peer, "{\"id\":%d,\"result\":%s}\n", request.ID, result); err != nil {
			done <- err
			return
		}
		_, err = reader.ReadBytes('\n') // initialized notification
		done <- err
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	version, err := client.initialize(ctx, "0.15.3", true)
	if err != nil {
		t.Fatalf("initialize(%q): %v", userAgent, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("synthetic peer: %v", err)
	}
	return version
}

// TestProjmuxUserAgentVersionAgreesWithDaemonEvidence holds C-3: the attach
// version behind this client's own userAgent product token is read, so a
// daemon whose evidence names the same version is consistent and a different
// version is contradictory. Paths, prose, and arbitrary tokens stay unread.
func TestProjmuxUserAgentVersionAgreesWithDaemonEvidence(t *testing.T) {
	daemon := `{"status":"running","backend":"pid","managedCodexVersion":"0.154.0","cliVersion":"0.154.0","appServerVersion":"0.154.0"}`
	for _, tt := range []struct {
		name, userAgent, negotiated, running, agreement string
		owner                                           ManagerOwnership
		relation                                        VersionRelation
		action                                          NativeActionReadiness
		refusal                                         NativeActionRefusal
		attach                                          EndpointAttach
	}{
		{"observed projmux user agent", observedProjmuxUserAgent, "projmux/0.154.0", "0.154.0", "consistent",
			ManagerManaged, VersionCurrent, NativeActionReady, NativeActionRefusalNone, EndpointAttachAllowed},
		{"projmux user agent without platform", "projmux/0.154.0", "projmux/0.154.0", "0.154.0", "consistent",
			ManagerManaged, VersionCurrent, NativeActionReady, NativeActionRefusalNone, EndpointAttachAllowed},
		{"projmux user agent prerelease", "projmux/0.154.0-rc.1 (Ubuntu 26.4.0; x86_64)", "projmux/0.154.0-rc.1", "0.154.0-rc.1", "contradictory",
			ManagerUnknown, VersionUnknown, NativeActionRefused, NativeActionRefusalEvidenceContradictory, EndpointAttachRefused},
		{"projmux user agent other version", "projmux/0.153.4 (Ubuntu 26.4.0; x86_64) unknown (projmux; 0.15.3)", "projmux/0.153.4", "0.153.4", "contradictory",
			ManagerUnknown, VersionUnknown, NativeActionRefused, NativeActionRefusalEvidenceContradictory, EndpointAttachRefused},
		{"codex product prefix still read", "codex-cli/0.154.0", "codex-cli/0.154.0", "0.154.0", "consistent",
			ManagerManaged, VersionCurrent, NativeActionReady, NativeActionRefusalNone, EndpointAttachAllowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			health := agreementFor(t, daemon, negotiateUserAgent(t, tt.userAgent), tt.negotiated)
			if health.RunningVersion != tt.running || health.ManagerEvidence.Agreement != tt.agreement ||
				health.ManagerOwnership != tt.owner || health.VersionRelation != tt.relation ||
				health.NativeAction != tt.action || health.NativeRefusal != tt.refusal {
				t.Fatalf("decision = %+v, evidence %+v", health, health.ManagerEvidence)
			}
			if got := AuthorityFor(health).Attach; got != tt.attach {
				t.Fatalf("attach = %s, want %s", got, tt.attach)
			}
		})
	}

	for _, userAgent := range []string{
		"/home/user/.local/bin/projmux/0.154.0",
		"./projmux/0.154.0",
		"projmux/../0.154.0",
		"projmux//0.154.0",
		"projmux/projmux/0.154.0",
		"projmux/0.154.0/secret",
		"projmux/0.154.0-secret",
		"projmux/sk-0.154.0",
		"projmux/v0.154.0",
		"projmux 0.154.0 is running",
		"Projmux/0.154.0",
		"projmuxx/0.154.0",
		"other-client/0.154.0",
		"projmux/" + strings.Repeat("1", 40) + ".0.0",
		"projmux/",
		"",
	} {
		t.Run("unread "+userAgent, func(t *testing.T) {
			if got := safeVersion(userAgent); got != "" {
				t.Fatalf("safeVersion(%q) = %q", userAgent, got)
			}
			health := agreementFor(t, daemon, negotiateUserAgent(t, userAgent), "")
			if health.RunningVersion != "" || health.ManagerEvidence.Agreement != "insufficient" ||
				health.ManagerOwnership != ManagerUnknown || health.NativeRefusal != NativeActionRefusalOwnershipUnknown {
				t.Fatalf("unsafe user agent became evidence: %+v, evidence %+v", health, health.ManagerEvidence)
			}
			data, _ := json.Marshal(health)
			if strings.Contains(string(data), "secret") || strings.Contains(string(data), "/home/") {
				t.Fatalf("health leaked user agent text: %s", data)
			}
		})
	}
}

// agreementFor runs the synthetic official daemon version fixture through the
// same observation-and-agreement path ProbeDefaultProxy uses.
func agreementFor(t *testing.T, daemon, negotiated, want string) Health {
	t.Helper()
	if negotiated != want {
		t.Fatalf("negotiated version = %q, want %q", negotiated, want)
	}
	fixture, err := startDaemonVersionFixture(t.TempDir(), daemon, false, daemonVersionFixtureReadinessTimeout)
	if err != nil {
		t.Fatal(err)
	}
	observation := observeManager(t.Context(), time.Second, func(string) (string, error) { return fixture.executable, nil }, exec.CommandContext)
	if err := fixture.terminate(daemonVersionFixtureTerminationTimeout); err != nil {
		t.Fatal(err)
	}
	health := withManagerObservation(Decide(AvailabilityAvailable, ReasonNone, negotiated, EndpointStdioProxy, ConnectionReady, true), observation)
	if health.ManagerEvidence == nil {
		t.Fatalf("manager evidence missing: %+v", health)
	}
	return health
}
