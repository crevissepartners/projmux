package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestSupervisedProviderExportsExactActivationEvidenceToHooks(t *testing.T) {
	t.Parallel()
	spec := superviseSpec{PaneUID: "pane-exact", Generation: "gen-exact", RuntimeID: "%41"}
	outcome, err := runSupervisedChildWithEnvironment([]string{
		"sh", "-c",
		`test "$` + internalActivationPaneUIDEnv + `" = pane-exact && test "$` + internalActivationGenerationEnv + `" = gen-exact && test "$` + runtimeMutationAnchorPaneEnv + `" = %41`,
	}, "", activationEnvironment(spec), nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ExitCode != 0 || outcome.Signal != "" {
		t.Fatalf("activation evidence child outcome = %+v", outcome)
	}
}

func TestNativeActivationEnvironmentAddsOnlyThePrivateExactRouteAnchor(t *testing.T) {
	t.Parallel()

	base := activationEnvironment(superviseSpec{PaneUID: "pane-exact", Generation: "gen-exact"})
	anchored := activationEnvironment(superviseSpec{PaneUID: "pane-exact", Generation: "gen-exact", RuntimeID: "%41"})
	if len(anchored) != len(base)+1 || anchored[len(anchored)-1] != runtimeMutationAnchorPaneEnv+"=%41" {
		t.Fatalf("anchored activation environment = %#v, base=%#v", anchored, base)
	}
	for _, entry := range anchored {
		if strings.HasPrefix(entry, "PROJMUX_") {
			t.Fatalf("native route authority added public hook environment %q", entry)
		}
	}
	for _, runtimeID := range []string{"", "41", "%41 ", "%bad"} {
		environment := activationEnvironment(superviseSpec{PaneUID: "pane-exact", Generation: "gen-exact", RuntimeID: runtimeID})
		if runtimeID == "%41 " {
			if len(environment) != len(base)+1 || environment[len(environment)-1] != runtimeMutationAnchorPaneEnv+"=%41" {
				t.Fatalf("trimmed exact runtime environment = %#v", environment)
			}
			continue
		}
		if len(environment) != len(base) {
			t.Fatalf("non-exact runtime %q exported an anchor: %#v", runtimeID, environment)
		}
	}
}

func TestSupervisedActivationRefusesPartialAgentIdentityBeforeProviderStart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sideEffect := filepath.Join(root, "provider-started")
	cmd := &superviseCommand{
		journal:       terminationJournal{path: filepath.Join(root, terminationJournalFile)},
		runActivation: runSupervisedChildWithActivation,
	}
	err := cmd.Run([]string{
		"--pane-uid", "pane-1", "--agent-uid", "agent-1", "--generation", "gen-1",
		"--", "sh", "-c", `: > "$1"`, "partial-agent", sideEffect,
	}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "agent activation requires an operation id") {
		t.Fatalf("partial Agent activation = %v", err)
	}
	if _, statErr := os.Stat(sideEffect); !os.IsNotExist(statErr) {
		t.Fatalf("partial Agent activation started provider: %v", statErr)
	}
	if receipts := readTestTerminationJournal(t, cmd); len(receipts) != 0 {
		t.Fatalf("partial Agent activation fabricated receipts: %#v", receipts)
	}
}

func TestSupervisedActivationRefusesUnexpectedRegistryShapeBeforeProviderStart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sideEffect := filepath.Join(root, "provider-started")
	cmd := &superviseCommand{
		journal:       terminationJournal{path: filepath.Join(root, terminationJournalFile)},
		runActivation: runSupervisedChildWithActivation,
	}
	err := cmd.Run([]string{
		"--pane-uid", "pane-1", "--agent-uid", "agent-1", "--generation", "gen-1",
		"--operation-id", "op-1", "--registry-path", filepath.Join(root, "other.json"),
		"--", "sh", "-c", `: > "$1"`, "unexpected-registry", sideEffect,
	}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected shape") {
		t.Fatalf("unexpected Registry shape = %v", err)
	}
	if _, statErr := os.Stat(sideEffect); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected Registry shape started provider: %v", statErr)
	}
	if receipts := readTestTerminationJournal(t, cmd); len(receipts) != 0 {
		t.Fatalf("unexpected Registry shape fabricated receipts: %#v", receipts)
	}
}

func TestSupervisedShellBypassesAgentAdmissionWithOrWithoutOperationID(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		operationID string
	}{
		{name: "without operation id"},
		{name: "with operation id", operationID: "op-shell"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outcome, err := runSupervisedChildWithActivation(
				[]string{"sh", "-c", "exit 0"}, "",
				superviseSpec{PaneUID: "pane-shell", Generation: "gen-shell", OperationID: test.operationID},
			)
			if err != nil || outcome.ExitCode != 0 || outcome.Signal != "" {
				t.Fatalf("shell compatibility = outcome=%+v err=%v", outcome, err)
			}
		})
	}
}

func TestSuperviseActivationHandshakeDistinguishesGateFailureFromProviderExit(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}

	t.Run("admission or exec failure produces no receipt", func(t *testing.T) {
		cmd := &superviseCommand{
			journal: terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)},
			runActivation: func([]string, string, superviseSpec) (processOutcome, error) {
				return runSupervisedActivationGateWithSignalSource(
					[]string{shell, "-c", `printf 'activation-failed\n' >&3; exit 42`}, make(chan os.Signal))
			},
		}
		err := cmd.Run([]string{"--pane-uid", "pane-1", "--agent-uid", "agent-1", "--generation", "gen-1", "--operation-id", "op-1", "--", "provider"}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "activation gate refused provider launch") {
			t.Fatalf("gate failure = %v", err)
		}
		if receipts := readTestTerminationJournal(t, cmd); len(receipts) != 0 {
			t.Fatalf("gate failure fabricated receipts: %#v", receipts)
		}
	})

	for _, code := range []int{1, 42} {
		t.Run(fmt.Sprintf("admitted provider exit %d", code), func(t *testing.T) {
			cmd := &superviseCommand{
				journal: terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)},
				runActivation: func([]string, string, superviseSpec) (processOutcome, error) {
					return runSupervisedActivationGateWithSignalSource(
						[]string{shell, "-c", fmt.Sprintf("exec 3>&-; exit %d", code)}, make(chan os.Signal))
				},
			}
			err := cmd.Run([]string{"--pane-uid", "pane-1", "--agent-uid", "agent-1", "--generation", "gen-1", "--operation-id", "op-1", "--", "provider"}, nil, nil)
			var coded superviseExitError
			if !errors.As(err, &coded) || coded.ExitCode() != code {
				t.Fatalf("provider exit = %v, want %d", err, code)
			}
			receipts := readTestTerminationJournal(t, cmd)
			if len(receipts) != 1 || receipts[0].ExitCode == nil || *receipts[0].ExitCode != code {
				t.Fatalf("provider receipts = %#v", receipts)
			}
		})
	}

	t.Run("HUP during admission is killed evidence and provider side effect zero", func(t *testing.T) {
		root := t.TempDir()
		ready := filepath.Join(root, "gate-ready")
		providerSideEffect := filepath.Join(root, "provider-started")
		signals := make(chan os.Signal, 1)
		cmd := &superviseCommand{
			journal: terminationJournal{path: filepath.Join(root, terminationJournalFile)},
			runActivation: func([]string, string, superviseSpec) (processOutcome, error) {
				return runSupervisedActivationGateWithSignalSource([]string{
					shell, "-c", `: > "$1"; while :; do sleep 1; done; : > "$2"`,
					"activation-gate", ready, providerSideEffect,
				}, signals)
			},
		}
		done := make(chan error, 1)
		go func() {
			done <- cmd.Run([]string{"--pane-uid", "pane-1", "--agent-uid", "agent-1", "--generation", "gen-1", "--operation-id", "op-1", "--", "provider"}, nil, nil)
		}()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, statErr := os.Stat(ready); statErr == nil {
				break
			} else if !os.IsNotExist(statErr) {
				t.Fatal(statErr)
			}
			if time.Now().After(deadline) {
				t.Fatal("activation gate did not reach its held admission boundary")
			}
			time.Sleep(10 * time.Millisecond)
		}
		signals <- syscall.SIGHUP
		err := <-done
		var coded superviseExitError
		if !errors.As(err, &coded) || coded.ExitCode() != 129 {
			t.Fatalf("HUP admission exit = %v", err)
		}
		if _, statErr := os.Stat(providerSideEffect); !os.IsNotExist(statErr) {
			t.Fatalf("provider side effect after admission HUP: %v", statErr)
		}
		receipts := readTestTerminationJournal(t, cmd)
		if len(receipts) != 1 || receipts[0].Classification != coremetadata.TerminationKilled || receipts[0].Signal != "HUP" {
			t.Fatalf("HUP admission receipts = %#v", receipts)
		}
	})
}

func TestActivationExecCloseOnExecDoesNotWaitForProviderDescendants(t *testing.T) {
	if os.Getenv("PMX_TEST_ACTIVATION_CLOEXEC_HELPER") == "1" {
		markActivationFailureCloseOnExec(3)
		if err := execCommittedActivation([]string{"sh", "-c", "sleep 2 & exit 0"}, "", superviseSpec{}); err != nil {
			os.Exit(97)
		}
		os.Exit(98)
	}

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	outcome, err := runSupervisedActivationGate([]string{
		"sh", "-c", `PMX_TEST_ACTIVATION_CLOEXEC_HELPER=1 exec "$1" -test.run '^TestActivationExecCloseOnExecDoesNotWaitForProviderDescendants$'`,
		"activation-cloexec", testBinary,
	})
	if err != nil || outcome.ExitCode != 0 || outcome.Signal != "" {
		t.Fatalf("CLOEXEC helper = outcome=%+v err=%v", outcome, err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("supervisor waited %s for a provider descendant that inherited the private failure fd", elapsed)
	}
}

func TestRunSupervisedChildRetainsRelayedHUPEvidenceWhenChildExits129(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}

	ready := filepath.Join(t.TempDir(), "ready")
	signals := make(chan os.Signal, 1)
	type childResult struct {
		outcome processOutcome
		err     error
	}
	result := make(chan childResult, 1)
	go func() {
		outcome, runErr := runSupervisedChildWithSignalSource([]string{
			shell, "-c",
			`trap 'exit 129' HUP; : > "$1"; while :; do sleep 10; done`,
			"supervised-hup-fixture", ready,
		}, "", signals)
		result <- childResult{outcome: outcome, err: runErr}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, statErr := os.Stat(ready); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			t.Fatalf("stat child readiness: %v", statErr)
		}
		select {
		case got := <-result:
			t.Fatalf("child finished before HUP: outcome=%#v err=%v", got.outcome, got.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for child HUP trap")
		}
		time.Sleep(10 * time.Millisecond)
	}

	signals <- syscall.SIGHUP
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("runSupervisedChildWithSignalSource error = %v", got.err)
		}
		want := processOutcome{Signal: "HUP", SignalNumber: int(syscall.SIGHUP)}
		if got.outcome != want {
			t.Fatalf("outcome = %#v, want relayed HUP %#v", got.outcome, want)
		}
		if classification := coremetadata.ClassifyProcessExit(got.outcome.ExitCode, got.outcome.Signal); classification != coremetadata.TerminationKilled {
			t.Fatalf("classification = %q, want %q", classification, coremetadata.TerminationKilled)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for child to translate relayed HUP")
	}
}

func TestRunSupervisedChildDoesNotInferHUPFromExit129(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}

	outcome, err := runSupervisedChildWithSignalSource(
		[]string{shell, "-c", "exit 129"},
		"",
		make(chan os.Signal),
	)
	if err != nil {
		t.Fatalf("runSupervisedChildWithSignalSource error = %v", err)
	}
	want := processOutcome{ExitCode: 129}
	if outcome != want {
		t.Fatalf("outcome = %#v, want plain exit %#v", outcome, want)
	}
	if classification := coremetadata.ClassifyProcessExit(outcome.ExitCode, outcome.Signal); classification != coremetadata.TerminationAbnormal {
		t.Fatalf("classification = %q, want %q", classification, coremetadata.TerminationAbnormal)
	}
}
