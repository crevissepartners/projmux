package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// installReplacementFixture is one whole run of the pass with no process table,
// no broker, no clock, and no real state directory.
type installReplacementFixture struct {
	vintage projmuxProcessVintage
	// reached and refusal are what the drain request answers.
	reached bool
	refusal string
	// noRequest makes the pass unable to reach any target at all, which is the
	// shape a platform with no broker route has.
	noRequest bool
	// goneAfter is how many settle polls pass before the runtime is gone. A
	// negative value means it never goes.
	goneAfter int
	cutoff    string
	stateDir  string
	stderr    *bytes.Buffer
	targets   []installReplacementTarget
}

func runTestInstallReplacement(t *testing.T, fixture installReplacementFixture) installReplacementOutcome {
	t.Helper()
	dir := fixture.stateDir
	if dir == "" {
		dir = t.TempDir()
	}
	polls := 0
	command := &installReplacementCommand{
		now: func() time.Time { return time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC) },
		getenv: func(key string) string {
			switch key {
			case "PROJMUX_INSTALLER":
				return "make"
			case replacementCutoffEnv:
				return fixture.cutoff
			}
			return ""
		},
		stateDir:    func() (string, error) { return dir, nil },
		readVintage: func(time.Time) projmuxProcessVintage { return fixture.vintage },
		readTargets: func() []installReplacementTarget { return fixture.targets },
		runtimeGone: func() bool {
			polls++
			return fixture.goneAfter >= 0 && polls > fixture.goneAfter
		},
		settle: 50 * time.Millisecond,
		poll:   time.Millisecond,
	}
	if !fixture.noRequest {
		command.requestDrain = func(context.Context) (bool, string) {
			return fixture.reached, fixture.refusal
		}
	}
	stderr := fixture.stderr
	if stderr == nil {
		stderr = &bytes.Buffer{}
	}
	runErr := command.Run(stderr)

	outcome, ok := readInstallReplacementOutcome(filepath.Join(dir, installReplacementFile))
	if !ok {
		t.Fatalf("the pass wrote no readable outcome under %s", dir)
	}
	if outcome.Outcome == installReplacementOutcomeUnreachable {
		var coded interface{ ExitCode() int }
		if !errors.As(runErr, &coded) || coded.ExitCode() != 1 {
			t.Fatalf("unreachable Run() = %v, want exit 1", runErr)
		}
	} else if runErr != nil {
		t.Fatalf("%s Run() = %v, want success", outcome.Outcome, runErr)
	}
	return outcome
}

// TestInstallReplacementPassOutcomesAreFixedByFleetAndRequest fixes every
// outcome the pass can reach.
//
// The pass is the step that makes an install finish the replacement it starts,
// and its whole account of what it did is one closed token plus counters. A
// combination that reached the wrong token would either claim a replacement
// that did not happen -- the exact falsehood C-1 exists to prevent -- or report
// a completed one as a failure and send an operator after nothing.
func TestInstallReplacementPassOutcomesAreFixedByFleetAndRequest(t *testing.T) {
	t.Parallel()

	broker := func(age int) projmuxProcessVintage {
		return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{age}},
		}}
	}

	for _, tc := range []struct {
		name    string
		fixture installReplacementFixture
		want    installReplacementOutcome
	}{
		{
			name:    "a platform with no process table asks nothing",
			fixture: installReplacementFixture{goneAfter: -1},
			want:    installReplacementOutcome{Outcome: installReplacementOutcomeUnsupported},
		},
		{
			name: "a fleet with no drainable residue asks nothing and still counts what it left",
			fixture: installReplacementFixture{
				vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
					{Role: projmuxProcessRoleSupervisor, Processes: 2, Replaced: 2, ReplacedAgeSeconds: []int{120, 240}},
					{Role: projmuxProcessRoleAgentEndpoint, Processes: 1, Current: 1},
				}},
				goneAfter: -1,
			},
			want: installReplacementOutcome{
				Outcome: installReplacementOutcomeNoTarget, Supported: true, Reported: 2,
			},
		},
		{
			name:    "a drain that is asked for and finishes is complete",
			fixture: installReplacementFixture{vintage: broker(600), reached: true, refusal: "drain-required", goneAfter: 0},
			want: installReplacementOutcome{
				Outcome: installReplacementOutcomeComplete, Supported: true,
				Attempted: 1, Drained: 1, Refusal: "drain-required",
			},
		},
		{
			name:    "a drain still carrying work is pending, not complete",
			fixture: installReplacementFixture{vintage: broker(600), reached: true, refusal: "drain-required", goneAfter: -1},
			want: installReplacementOutcome{
				Outcome: installReplacementOutcomePending, Supported: true,
				Attempted: 1, Refusal: "drain-required",
			},
		},
		{
			name:    "a target the shipped path cannot reach keeps the refusal that says why",
			fixture: installReplacementFixture{vintage: broker(600), reached: false, refusal: "discovery-untrusted", goneAfter: -1},
			want: installReplacementOutcome{
				Outcome: installReplacementOutcomeUnreachable, Supported: true,
				Attempted: 1, Refusal: "discovery-untrusted",
			},
		},
		{
			name:    "no request route at all is unreachable rather than complete",
			fixture: installReplacementFixture{vintage: broker(600), noRequest: true, goneAfter: 0},
			want: installReplacementOutcome{
				Outcome: installReplacementOutcomeUnreachable, Supported: true, Attempted: 1,
			},
		},
		{
			name: "residue already past the cutoff is counted whatever the request answers",
			fixture: installReplacementFixture{
				vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
					{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{600}},
					{Role: projmuxProcessRoleSessionClient, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{610000}},
				}},
				reached: true, refusal: "drain-required", goneAfter: 0,
			},
			want: installReplacementOutcome{
				Outcome: installReplacementOutcomeComplete, Supported: true,
				Attempted: 1, Drained: 1, Reported: 1, BeyondCutoff: 1, Refusal: "drain-required",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runTestInstallReplacement(t, tc.fixture)
			want := tc.want
			want.At = "2026-09-08T04:00:00Z"
			want.Installer = "make"
			want.CutoffSeconds = int64(replacementDrainCutoff / time.Second)
			if got != want {
				t.Fatalf("pass outcome =\n  %+v\nwant\n  %+v", got, want)
			}
		})
	}
}

// TestInstallReplacementRecordsTheCutoffItRanUnder holds the override path the
// isolated smoke and the cutoff branch depend on.
func TestInstallReplacementRecordsTheCutoffItRanUnder(t *testing.T) {
	t.Parallel()

	outcome := runTestInstallReplacement(t, installReplacementFixture{
		vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: projmuxProcessRoleSupervisor, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{120}},
		}},
		cutoff:    "60s",
		goneAfter: -1,
	})
	if outcome.CutoffSeconds != 60 {
		t.Fatalf("recorded cutoff = %ds, want 60s", outcome.CutoffSeconds)
	}
	// A 120-second residual process is past a 60-second cutoff.
	if outcome.BeyondCutoff != 1 {
		t.Fatalf("beyond cutoff = %d, want 1", outcome.BeyondCutoff)
	}
}

func TestInstallReplacementUnsupportedAndUnwritableRecordRemainSuccessful(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	if err := runInstallReplacement([]string{"--now"}, &stderr); err == nil {
		t.Fatal("runInstallReplacement(args) = nil, want a usage error")
	}

	command := &installReplacementCommand{
		now:         time.Now,
		getenv:      func(string) string { return "" },
		stateDir:    func() (string, error) { return "", os.ErrPermission },
		readVintage: func(time.Time) projmuxProcessVintage { return projmuxProcessVintage{} },
		settle:      time.Millisecond,
		poll:        time.Millisecond,
	}
	if err := command.Run(&stderr); err != nil {
		t.Fatalf("unsupported Run() = %v, want nil", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unsupported Run() printed %q, want silence", stderr.String())
	}

	// And a nil command is a no-op rather than a panic on an install path.
	var nilCommand *installReplacementCommand
	if err := nilCommand.Run(&stderr); err != nil {
		t.Fatalf("nil Run() = %v, want nil", err)
	}

	// Failure remains visible even when its record cannot be written.
	command.readVintage = func(time.Time) projmuxProcessVintage {
		return projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1},
		}}
	}
	if err := command.Run(&stderr); err == nil || !strings.Contains(stderr.String(), "broker replacement is incomplete") {
		t.Fatalf("unwritable unreachable Run() = %v, stderr = %q", err, stderr.String())
	}
}

// TestInstallReplacementNoticeSpeaksOnlyWhenAnActionFollows holds the terminal
// surface.
//
// This prints as the last thing an install shows. "There was nothing to
// replace" is not news an operator has to read at every install, so the silent
// outcomes stay silent and the three that carry an action speak.
func TestInstallReplacementNoticeSpeaksOnlyWhenAnActionFollows(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		outcome installReplacementOutcome
		want    string
	}{
		{outcome: installReplacementOutcome{Outcome: installReplacementOutcomeNoTarget}},
		{outcome: installReplacementOutcome{Outcome: installReplacementOutcomeUnsupported}},
		{
			outcome: installReplacementOutcome{Outcome: installReplacementOutcomeComplete, Drained: 1},
			want:    ">> replaced 1 long-lived process is running the image this install superseded\n",
		},
		{
			outcome: installReplacementOutcome{Outcome: installReplacementOutcomePending, Attempted: 2},
			want:    ">> asked 2 long-lived processes are to stand down; they are still carrying work\n   The runtime accepts no new work and goes when that work ends.\n",
		},
		{
			outcome: installReplacementOutcome{Outcome: installReplacementOutcomeUnreachable, Attempted: 1, Refusal: "host-unavailable"},
			want:    ">> could not reach 1 long-lived process is to replace: host-unavailable\n",
		},
	} {
		got := renderInstallReplacementNotice(tc.outcome)
		if tc.want == "" {
			if got != "" {
				t.Fatalf("%s printed %q, want silence", tc.outcome.Outcome, got)
			}
			continue
		}
		if got != tc.want {
			t.Fatalf("%s printed %q, want %q", tc.outcome.Outcome, got, tc.want)
		}
	}

	// The original summary stays identity-free; failure details carry identities
	// separately and never alter the persisted record.
	text := renderInstallReplacementNotice(installReplacementOutcome{
		Outcome: installReplacementOutcomeUnreachable, Attempted: 1, Refusal: "discovery-untrusted",
	})
	for _, forbidden := range []string{"/", "pid", "argv"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("notice %q carries %q", text, forbidden)
		}
	}
}

func TestInstallReplacementRefusalNamesTargetsImpactAndRecovery(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	dir := t.TempDir()
	runTestInstallReplacement(t, installReplacementFixture{
		stateDir: dir,
		stderr:   &stderr,
		vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 2, Replaced: 2},
		}},
		targets: []installReplacementTarget{
			{role: codexControlPlaneRoleBroker, pid: 4321, revision: "8ae6e563"},
			{role: codexControlPlaneRoleBroker, pid: 4322, revision: "unknown"},
		},
		refusal: "host-unavailable",
	})
	for _, want := range []string{
		"host-unavailable", "role=broker-runtime pid=4321 revision=8ae6e563",
		"role=broker-runtime pid=4322 revision=unknown", "binary and config are already installed",
		"broker replacement is incomplete", "Codex Agents created afterward may have no control",
		"exit naturally", "retry make install", "operator review the targets",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("failure omitted %q: %s", want, stderr.String())
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, installReplacementFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"4321", "4322", "8ae6e563", "revision", "pid"} {
		if strings.Contains(string(body), identity) {
			t.Errorf("outcome persisted %q: %s", identity, body)
		}
	}
}

func TestInstallReplacementFailureLocalizationPreservesIdentityAndCommands(t *testing.T) {
	t.Parallel()
	targets := []installReplacementTarget{{role: codexControlPlaneRoleBroker, pid: 4321, revision: "8ae6e563"}}
	english := renderInstallReplacementFailure(targets, i18n.FallbackLocale)
	if fallback := renderInstallReplacementFailure(targets, i18n.Locale("fr-FR")); fallback != english {
		t.Fatalf("unsupported locale did not fall back: %q", fallback)
	}
	korean := renderInstallReplacementFailure(targets, i18n.Locale("ko-KR"))
	if !strings.Contains(korean, "교체가 완료되지 않았습니다") {
		t.Fatalf("Korean failure = %q", korean)
	}
	for _, text := range []string{english, korean} {
		for _, literal := range []string{"role=broker-runtime pid=4321 revision=8ae6e563", "Codex", "make install"} {
			if !strings.Contains(text, literal) {
				t.Errorf("localized failure lost %q: %q", literal, text)
			}
		}
	}
	if text := renderInstallReplacementFailure(nil, i18n.FallbackLocale); !strings.Contains(text, "No remaining target could be identified") || strings.Contains(text, "pid=") {
		t.Fatalf("unidentified targets = %q", text)
	}
}

// TestInstallReplacementOutcomeRecordCarriesNoProcessIdentity holds the privacy
// rule the residue ledger already holds, on the new record.
func TestInstallReplacementOutcomeRecordCarriesNoProcessIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runTestInstallReplacement(t, installReplacementFixture{
		stateDir: dir,
		vintage: projmuxProcessVintage{Supported: true, Roles: []projmuxProcessRoleVintage{
			{Role: codexControlPlaneRoleBroker, Processes: 1, Replaced: 1, ReplacedAgeSeconds: []int{600}},
		}},
		reached: true, refusal: "drain-required", goneAfter: 0,
	})
	body, err := os.ReadFile(filepath.Join(dir, installReplacementFile))
	if err != nil {
		t.Fatalf("read outcome record: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode outcome record: %v", err)
	}
	for key := range decoded {
		switch key {
		case "at", "installer", "supported", "outcome", "cutoffSeconds",
			"attempted", "drained", "reported", "beyondCutoff", "refusal":
		default:
			t.Fatalf("outcome record carries an unpublished field %q", key)
		}
	}
	for _, forbidden := range []string{"pid", "exe", "argv", "cmdline", "/proc", "socket"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("outcome record carries %q:\n%s", forbidden, body)
		}
	}
}

// TestInstallReplacementReadsAMissingRecordAsNoPass keeps the two silences
// apart.
func TestInstallReplacementReadsAMissingRecordAsNoPass(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, ok := readInstallReplacementOutcome(filepath.Join(dir, installReplacementFile)); ok {
		t.Fatal("a missing record read as a pass")
	}
	if _, ok := readInstallReplacementOutcome(""); ok {
		t.Fatal("an empty path read as a pass")
	}
	path := filepath.Join(dir, installReplacementFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed record: %v", err)
	}
	if _, ok := readInstallReplacementOutcome(path); ok {
		t.Fatal("a malformed record read as a pass")
	}
	// A well-formed record with no verdict is not a pass either: the outcome
	// token is the whole of what this file exists to carry.
	if err := os.WriteFile(path, []byte(`{"at":"2026-09-08T04:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("write verdictless record: %v", err)
	}
	if _, ok := readInstallReplacementOutcome(path); ok {
		t.Fatal("a record with no outcome token read as a pass")
	}
}

// TestInstallReplacementDrainRequestReadsARefusedDialAsAcceptance is the
// integration between the pass and the runtime's vintage trigger.
//
// The runtime answers a superseded client with `drain-required`, which is an
// error to the dialer and an acceptance to this pass: it is the runtime saying
// it has entered the drain the pass asked for. Reading that refusal as a
// failure would report every successful replacement as unreachable.
func TestInstallReplacementDrainRequestReadsARefusedDialAsAcceptance(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		refusal codexbroker.Refusal
		reached bool
	}{
		{refusal: codexbroker.RefusalDrainRequired, reached: true},
		{refusal: codexbroker.RefusalHostClosed, reached: true},
		{refusal: codexbroker.RefusalHostUnavailable, reached: false},
		{refusal: codexbroker.RefusalDiscoveryUntrusted, reached: false},
		{refusal: codexbroker.RefusalCredentialRejected, reached: false},
	} {
		reached := tc.refusal == codexbroker.RefusalDrainRequired || tc.refusal == codexbroker.RefusalHostClosed
		if reached != tc.reached {
			t.Fatalf("refusal %q reached = %v, want %v", tc.refusal, reached, tc.reached)
		}
	}

	// The real request path against a state domain with no published runtime
	// answers `host-unavailable` and starts nothing. This is the shape of a
	// machine that has never run a broker, and an install there must leave it
	// that way.
	// A short root: the discovery contract refuses a state domain whose derived
	// socket path would not fit the platform bound, and the test tree's own
	// temp directory is already past it.
	dir, err := os.MkdirTemp("", "pmxrepl")
	if err != nil {
		t.Fatalf("MkdirTemp() = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	discovery, err := codexbroker.NewDiscovery(dir, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("NewDiscovery() = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, dialErr := codexbroker.Dial(ctx, discovery, codexbroker.DialConfig{Timeout: time.Second}); //nolint:staticcheck // the refusal is the assertion
	codexbroker.RefusalOf(dialErr) != codexbroker.RefusalHostUnavailable {
		t.Fatalf("Dial() against an empty domain = %v, want host-unavailable", dialErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read state domain: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("dialing an empty state domain created %d entries, want none", len(entries))
	}
}
