package codexinstalled

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// This isolates the fixture's environment producer and the production PATH
// manager-observation consumer. It does not simulate an official manager or a
// successful wire handshake, and is not installed conformance evidence.
func TestManagedSelectionShimAndProbeFollowCurrentLinkWithoutVersionCache(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	current := filepath.Join(home, "packages", "standalone", "current")
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		t.Fatal(err)
	}
	shim, err := writeLedgerShim(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &Fixture{Root: root, CodexHome: home, realCodex: filepath.Join(current, "bin", "codex"), shimPath: shim, ledger: newLedger(filepath.Join(root, "ledger")), startResultPath: filepath.Join(root, "start-result")}
	t.Setenv("PATH", filepath.Dir(shim))
	for index, version := range []string{"0.151.0", "0.154.0"} {
		release := filepath.Join(root, version)
		if err := os.MkdirAll(filepath.Join(release, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		// Only read-only commands exist. The intentionally absent wire endpoint
		// must remain refused even when all manager-reported versions agree.
		script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n'--version') printf 'codex-cli %s\\n';;\n'app-server daemon version') printf '%%s\\n' '{\"status\":\"running\",\"backend\":\"pid\",\"cliVersion\":\"%s\",\"managedCodexVersion\":\"%s\",\"appServerVersion\":\"%s\"}';;\n'app-server proxy') exit 0;;\n*) exit 91;;\nesac\n", version, version, version, version)
		if err := os.WriteFile(filepath.Join(release, "bin", "codex"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		if index > 0 {
			if err := os.Remove(current); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(release, current); err != nil {
			t.Fatal(err)
		}
		fixture.ApplyEnv(t.Setenv)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := exec.CommandContext(ctx, "codex", "--version").Output()
		if err != nil || strings.TrimSpace(string(output)) != "codex-cli "+version {
			cancel()
			t.Fatalf("selected CLI did not follow current link: %q %v", output, err)
		}
		health := codexappserver.ProbeDefaultProxy(ctx, time.Second, "fixture", true)
		cancel()
		if health.CLIVersion != version || health.ManagedVersion != version || health.RunningVersion != version || health.Version != "" || codexappserver.AuthorityFor(health).Attach != codexappserver.EndpointAttachRefused {
			t.Fatalf("manager observation cached or metadata granted wire authority: %+v", health)
		}
	}
	if err := fixture.ledger.AssertNoLifecycleMutation(); err != nil {
		t.Fatal(err)
	}
}
