package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

func TestConnectionSelectionEvidenceDistinguishesShimReleaseAndDrift(t *testing.T) {
	root := t.TempDir()
	fixture := &codexinstalled.Fixture{Root: root, CodexHome: filepath.Join(root, "codex-home")}
	current := filepath.Join(fixture.CodexHome, "packages", "standalone", "current")
	shim := filepath.Join(root, "fixture-bin", "codex")
	for _, dir := range []string{filepath.Dir(current), filepath.Dir(shim)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 91\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	selectedPath := filepath.Join(current, "bin", "codex")
	t.Setenv("PATH", filepath.Dir(shim))
	t.Setenv("CODEX_HOME", fixture.CodexHome)
	t.Setenv("PROJMUX_CODEX_INSTALLED_HOME", fixture.CodexHome)
	t.Setenv("PROJMUX_CODEX_INSTALLED_REAL", selectedPath)
	var prior installedConnectionSelection
	var manager codexinstalled.ManagedDaemonProof
	for index, version := range []string{"0.151.0", "0.154.0"} {
		release := filepath.Join(root, version)
		executable := filepath.Join(release, "bin", "codex")
		if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(executable, []byte("fixture "+version), 0o700); err != nil {
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
		digest, err := codexinstalled.FileSHA256(executable)
		if err != nil {
			t.Fatal(err)
		}
		manager = codexinstalled.ManagedDaemonProof{Executable: executable, SHA256: digest, Version: version}
		observed := observeInstalledConnectionSelection(fixture, manager)
		if observed.Error != "" || !observed.EnvironmentMatches || !observed.MatchesManager || observed.SelectedPath != selectedPath || observed.SelectedResolved != executable || observed.SelectedSHA256 != digest || observed.StateDomainID == "" {
			t.Fatalf("exact selection not retained: %+v", observed)
		}
		if index > 0 && (observed.CLIPath != prior.CLIPath || observed.CLISHA256 != prior.CLISHA256 || observed.StateDomainID != prior.StateDomainID || observed.SelectedSHA256 == prior.SelectedSHA256) {
			t.Fatal("shim/domain stability or changed selected bytes were lost")
		}
		prior = observed
	}
	t.Setenv("PROJMUX_CODEX_INSTALLED_REAL", "/PRIVATE-ENVIRONMENT")
	observed := observeInstalledConnectionSelection(fixture, manager)
	raw, err := json.Marshal(observed)
	if err != nil || observed.Error != "fixture-environment-mismatch" || observed.CLISHA256 != "" || strings.Contains(string(raw), "PRIVATE-") {
		t.Fatalf("unexpected environment was read or exposed: %s %v", raw, err)
	}
	t.Setenv("PROJMUX_CODEX_INSTALLED_REAL", selectedPath)
	t.Setenv("PATH", t.TempDir())
	observed = observeInstalledConnectionSelection(fixture, manager)
	if observed.Error != "path-cli-not-fixture-shim" || observed.CLIPath != "" || observed.CLISHA256 != "" {
		t.Fatal("missing fixture PATH admitted an unverified CLI")
	}
	t.Setenv("PATH", filepath.Dir(shim))
	manager.Executable = "/PRIVATE-UNEXPECTED-RELEASE"
	observed = observeInstalledConnectionSelection(fixture, manager)
	if observed.Error != "selected-release-mismatch" || observed.SelectedResolved != "" || observed.SelectedSHA256 != "" {
		t.Fatal("unverified selected release was read or retained")
	}
}

func TestConnectionFailureProbeCannotReplaceCurrentRefusalWithLaterHealth(t *testing.T) {
	original := &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
	calls := 0
	var stages []installedConnectionStage
	probe := func(ctx context.Context) codexappserver.Health {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("failure diagnostic has no deadline")
		}
		return codexappserver.Health{Availability: codexappserver.AvailabilityAvailable, EndpointReadiness: codexappserver.EndpointReady, VersionRelation: codexappserver.VersionCurrent, ManagerOwnership: codexappserver.ManagerManaged, CLIVersion: "0.154.0", ManagedVersion: "0.154.0", RunningVersion: "0.154.0", Version: "0.154.0"}
	}
	record := func(stage installedConnectionStage) { stages = append(stages, stage) }
	if err := recordInstalledConnectionFailure(context.Background(), original, probe, record); err != original || calls != 1 || len(stages) != 1 || stages[0].Health.AttachRefusal != "none" || stages[0].Health.WireVersion != "0.154.0" {
		t.Fatal("later health changed the original failed attempt or was not retained")
	}
	if err := recordInstalledConnectionFailure(context.Background(), nil, probe, record); err != nil || calls != 1 || len(stages) != 1 {
		t.Fatal("successful connection gained a redundant diagnostic probe")
	}
}

func TestConnectionHealthEvidenceRejectsUnboundedProviderStrings(t *testing.T) {
	health := codexappserver.Health{Availability: "PRIVATE-PAYLOAD", Reason: "PRIVATE-PAYLOAD", ProbeReason: "PRIVATE-PAYLOAD", EndpointReadiness: "PRIVATE-PAYLOAD", Connection: "PRIVATE-PAYLOAD", ManagerOwnership: "PRIVATE-PAYLOAD", RunningExecutable: "PRIVATE-PAYLOAD", VersionRelation: "PRIVATE-PAYLOAD", CLIVersion: "/PRIVATE-PAYLOAD", ManagedVersion: "/PRIVATE-PAYLOAD", RunningVersion: "/PRIVATE-PAYLOAD", Version: "/PRIVATE-PAYLOAD"}
	raw, err := json.Marshal(projectInstalledConnectionHealth(health))
	if err != nil || strings.Contains(string(raw), "PRIVATE-") || !strings.Contains(string(raw), "unclassified") {
		t.Fatalf("unsafe health projection: %s %v", raw, err)
	}
}
