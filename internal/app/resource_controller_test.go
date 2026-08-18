package app

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// controllerReport is the parsed public projection. The kernel and the human
// renderer read one value, so parsing the JSON is parsing what the operator saw.
type controllerReport struct {
	HostMode        string                   `json:"hostMode"`
	Outcome         string                   `json:"outcome"`
	Items           []map[string]any         `json:"items"`
	CompletedStages []string                 `json:"completedStages"`
	Policy          []controller.Verdict     `json:"policy"`
	Reobserved      *controllerReobservation `json:"reobserved"`
	Retry           string                   `json:"retry"`
}

func parseControllerReport(t *testing.T, raw string) controllerReport {
	t.Helper()
	var report controllerReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("parse reconcile report: %v\n%s", err, raw)
	}
	return report
}

func reportItemKeys(report controllerReport) []string {
	out := make([]string, 0, len(report.Items))
	for _, item := range report.Items {
		key, _ := item["key"].(string)
		out = append(out, key)
	}
	return out
}

// lifecycleVerbCount counts every tmux verb that would create or destroy a
// runtime object. The kernel's whole no-start/no-import/no-delete claim is that
// this number never leaves zero.
func lifecycleVerbCount(server *fakeTmux) int {
	forbidden := []string{"new-session", "new-window", "split-window", "kill-session", "kill-window", "kill-pane"}
	count := 0
	for _, call := range server.calls {
		if len(call) > 0 && slices.Contains(forbidden, call[0]) {
			count++
		}
	}
	return count
}

func TestControllerDryRunAndExecuteShareOneSortedPlanAndRepeatIsWriteFree(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, preview)
	}
	previewReport := parseControllerReport(t, preview)
	if previewReport.HostMode != "app-owned" {
		t.Fatalf("dry-run host mode = %q, want app-owned", previewReport.HostMode)
	}
	if previewReport.Reobserved != nil {
		t.Fatalf("a dry-run reported a reobservation it never took: %+v", previewReport.Reobserved)
	}

	executed, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, executed)
	}
	executedReport := parseControllerReport(t, executed)
	// Acceptance 1, first half: the same plan, in the same order, projected
	// twice. Comparing the key sequence rather than a count is what catches a
	// reordering that would still leave the counts equal.
	if !slices.Equal(reportItemKeys(previewReport), reportItemKeys(executedReport)) {
		t.Fatalf("dry-run and execute used different plans:\n%v\n%v",
			reportItemKeys(previewReport), reportItemKeys(executedReport))
	}
	if executedReport.Reobserved == nil || !executedReport.Reobserved.Converged {
		t.Fatalf("execute did not reobserve a converged machine: %+v", executedReport.Reobserved)
	}
	if !slices.Contains(executedReport.CompletedStages, "reobserved: converged") {
		t.Fatalf("stage list omitted the reobservation: %v", executedReport.CompletedStages)
	}
	if !slices.ContainsFunc(executedReport.CompletedStages, func(stage string) bool {
		return strings.HasPrefix(stage, "exact socket observed:")
	}) {
		t.Fatalf("stage list omitted the observation: %v", executedReport.CompletedStages)
	}

	// Acceptance 1, second half: the repeat is not merely a smaller plan, it is
	// zero writes on both stores.
	writesBefore, mutationsBefore := store.writes, tmuxMutationCallCount(server)
	registryBefore, tmuxBefore := store.snapshot(), server.state()
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("repeat: %v\n%s", err, repeat)
	}
	repeatReport := parseControllerReport(t, repeat)
	if repeatReport.Outcome != "no-op" {
		t.Fatalf("repeat outcome = %q, want no-op\n%s", repeatReport.Outcome, repeat)
	}
	if store.writes != writesBefore || tmuxMutationCallCount(server) != mutationsBefore {
		t.Fatalf("repeat wrote: registry %d->%d, tmux %d->%d",
			writesBefore, store.writes, mutationsBefore, tmuxMutationCallCount(server))
	}
	if store.snapshot() != registryBefore || server.state() != tmuxBefore {
		t.Fatalf("repeat changed state:\n--- registry ---\n%s\n--- tmux ---\n%s", store.snapshot(), server.state())
	}
	if repeatReport.Reobserved != nil {
		t.Fatalf("a no-op reported a reobservation it had no reason to take: %+v", repeatReport.Reobserved)
	}
}

func TestControllerNeverStartsImportsOrDeletesOfflineControlOrUnattributedRuntime(t *testing.T) {
	t.Parallel()

	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")

	// Home: an app-owned control session. It is deliberately not a Project, and
	// the kernel must leave it exactly as it found it.
	home := server.addSession("home")
	home.opts[tmuxopts.SessionRole] = "control"
	// A scratch session an operator opened. No mirrored identity, no Registry
	// row, and no business being adopted.
	plain := server.addSession("scratch")
	// An offline Registry Project: a row with no runtime object anywhere.
	mutator := store.mutator()
	store.dirs[root+"/offline"] = true
	registered, err := mutator.RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Name: "offline-project", Root: root + "/offline", DefaultShell: "/bin/zsh", OperationID: "op-offline",
	})
	if err != nil {
		t.Fatal(err)
	}
	offline := registered.Project

	homeBefore, plainBefore := server.state(), len(plain.windows)
	sessionsBefore, panesBefore, windowsBefore := len(server.sessions), server.paneCount(), server.windowCount()
	_ = homeBefore

	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, stdout)
	}
	report := parseControllerReport(t, stdout)

	if got := lifecycleVerbCount(server); got != 0 {
		t.Fatalf("controller issued %d lifecycle verb(s); calls=%v", got, server.calls)
	}
	if len(server.sessions) != sessionsBefore || server.paneCount() != panesBefore || server.windowCount() != windowsBefore {
		t.Fatalf("controller changed the runtime shape: sessions %d->%d panes %d->%d windows %d->%d",
			sessionsBefore, len(server.sessions), panesBefore, server.paneCount(), windowsBefore, server.windowCount())
	}
	if len(home.opts) != 1 || home.opts[tmuxopts.SessionRole] != "control" {
		t.Fatalf("controller wrote onto the control session: %v", home.opts)
	}
	if len(plain.opts) != 0 || len(plain.windows) != plainBefore {
		t.Fatalf("controller wrote onto an unattributed session: opts=%v windows=%d", plain.opts, len(plain.windows))
	}
	// The offline Project is still offline, and still exactly one row: neither
	// started nor pruned because its runtime object is absent.
	if _, ok := store.registry.Project(offline.Metadata.UID); !ok {
		t.Fatal("controller deleted an offline Registry Project")
	}
	for _, project := range store.registry.Projects {
		if project.Spec.Root == root+"/offline" && project.Metadata.UID != offline.Metadata.UID {
			t.Fatalf("controller minted a second Project for the offline root: %s", project.Metadata.UID)
		}
	}

	// The refusals are reported, not merely performed. Without this the report
	// cannot distinguish "left alone on purpose" from "never looked at".
	wantIntents := map[controller.Intent]bool{
		controller.IntentStart: false, controller.IntentImport: false, controller.IntentDelete: false,
	}
	for _, verdict := range report.Policy {
		if verdict.Authority != controller.AuthorityRefuse && verdict.Intent != controller.IntentRepairBinding && verdict.Intent != controller.IntentRepairMirror {
			t.Fatalf("lifecycle verdict %+v is not a refusal", verdict)
		}
		if _, tracked := wantIntents[verdict.Intent]; tracked {
			wantIntents[verdict.Intent] = true
		}
	}
	for intent, present := range wantIntents {
		if !present {
			t.Fatalf("report did not state the %s refusal: %+v", intent, report.Policy)
		}
	}
	if !slices.ContainsFunc(report.Policy, func(v controller.Verdict) bool {
		return v.Intent == controller.IntentImport && v.Class == "control"
	}) {
		t.Fatalf("report did not state that the control session was not imported: %+v", report.Policy)
	}
}

func TestControllerAbortsBeforeTheFirstWriteWhenAHandleGuardGoesStale(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	// Converge once so the second run plans against bound handles whose uids a
	// race can then invalidate.
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	window := server.sessions[0].windows[0]
	window.opts[tmuxopts.WindowName] = "drifted"

	// Fire at the plan-to-execute boundary: the Registry has committed, the
	// guards have not run yet, and the handle the plan authorized is claimed by
	// something else.
	base := command.resources
	command.resources = &resourceStore{
		load: base.load, snapshot: base.snapshot, mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			registry, changed, err := base.updateConvergent(fn)
			window.opts[tmuxopts.WindowUID] = "win-somebody-else"
			return registry, changed, err
		},
	}
	mutationsBefore := tmuxMutationCallCount(server)
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "tmux prevalidation") {
		t.Fatalf("stale guard did not abort at prevalidation: err=%v\n%s", err, stdout)
	}
	if got := tmuxMutationCallCount(server); got != mutationsBefore {
		t.Fatalf("a stale plan wrote %d time(s) before aborting", got-mutationsBefore)
	}
	report := parseControllerReport(t, stdout)
	if report.Outcome != "failed" || report.Retry == "" {
		t.Fatalf("aborted run did not report a retry: %+v", report)
	}
	if !slices.Contains(report.CompletedStages, "Registry commit") && !slices.Contains(report.CompletedStages, "Registry commit (no-op)") {
		t.Fatalf("durable desired state was not reported as committed: %v", report.CompletedStages)
	}
	if !strings.Contains(stdout, "changed before repair") {
		t.Fatalf("report did not name the stale guard:\n%s", stdout)
	}
	_ = store
}

func TestControllerAbortsWhenTheExactSocketChangesUnderIt(t *testing.T) {
	t.Parallel()

	command, _, server, _, _ := newReconcileFixture(t, "-L", "primary")
	base := command.resources
	command.resources = &resourceStore{
		load: base.load, snapshot: base.snapshot, mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			registry, changed, err := base.updateConvergent(fn)
			// A different server is answering on the same routing. Every guard
			// below this point would be proving facts about the wrong machine.
			server.socketPath = "/tmp/fake-tmux/somebody-else"
			return registry, changed, err
		},
	}
	mutationsBefore := tmuxMutationCallCount(server)
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(stdout, "socket changed before repair") {
		t.Fatalf("socket guard did not abort: err=%v\n%s", err, stdout)
	}
	if got := tmuxMutationCallCount(server); got != mutationsBefore {
		t.Fatalf("a redirected socket was written to %d time(s)", got-mutationsBefore)
	}
}

func TestControllerRegistryCommitFailureLeavesZeroTmuxWrites(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	registryBefore, tmuxBefore := store.snapshot(), server.state()
	base := command.resources
	command.resources = &resourceStore{
		load: base.load, snapshot: base.snapshot, mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			working := store.registry.Clone()
			if err := fn(&working); err != nil {
				return coremetadata.Registry{}, false, err
			}
			return coremetadata.Registry{}, false, errIntentionalCommitFailure
		},
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil {
		t.Fatalf("injected commit failure succeeded:\n%s", stdout)
	}
	if tmuxMutationCallCount(server) != 0 || server.state() != tmuxBefore {
		t.Fatalf("a failed Registry commit reached tmux:\n%s", server.state())
	}
	if store.snapshot() != registryBefore {
		t.Fatalf("a failed Registry commit changed the Registry:\n%s", store.snapshot())
	}
	report := parseControllerReport(t, stdout)
	if report.Outcome != "failed" || !slices.ContainsFunc(report.CompletedStages, func(stage string) bool {
		return strings.HasPrefix(stage, "exact socket observed:")
	}) {
		t.Fatalf("commit failure lost the stage list: %+v", report)
	}
	if report.Reobserved != nil {
		t.Fatalf("a failed run claimed a reobservation: %+v", report.Reobserved)
	}
}

// errIntentionalCommitFailure is the injected durable-store failure. It is a
// package-level value so the assertion above compares against the same error the
// store returned rather than a message.
var errIntentionalCommitFailure = errors.New("injected registry commit failure")

func TestControllerConvergesOnAStandaloneHostTheOperatorNamed(t *testing.T) {
	t.Parallel()

	// The operator's own tmux carries no @projmux_app marker, so every unmarked
	// object on it resolves foreign. Refusing there by default is right; refusing
	// a repair the operator aimed at that exact socket is not, and this is the
	// case a real `-L` smoke against a server projmux did not start exercises.
	command, store, server, _, _ := newReconcileFixture(t, "-L", "guest")
	server.appMarker = ""

	stdout, _, err := runReconcile(t, command, "resources", "--socket", "guest", "-o", "json")
	if err != nil {
		t.Fatalf("standalone execute: %v\n%s", err, stdout)
	}
	report := parseControllerReport(t, stdout)
	if report.HostMode != "standalone" {
		t.Fatalf("host mode = %q, want standalone", report.HostMode)
	}
	if report.Outcome != "changed" {
		t.Fatalf("standalone outcome = %q, want changed\n%s", report.Outcome, stdout)
	}
	if len(store.registry.Projects) != 1 {
		t.Fatalf("standalone repair did not commit one Project: %s", store.snapshot())
	}
	if got := lifecycleVerbCount(server); got != 0 {
		t.Fatalf("standalone repair issued %d lifecycle verb(s)", got)
	}
	if report.Reobserved == nil || !report.Reobserved.Converged {
		t.Fatalf("standalone repair did not reobserve convergence: %+v", report.Reobserved)
	}

	repeat, _, err := runReconcile(t, command, "resources", "--socket", "guest", "-o", "json")
	if err != nil || parseControllerReport(t, repeat).Outcome != "no-op" {
		t.Fatalf("standalone repeat is not a no-op: err=%v\n%s", err, repeat)
	}
}
