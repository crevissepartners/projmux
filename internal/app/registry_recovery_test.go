package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// recoveryHarness is one isolated state directory plus the command wired to it.
// Every test builds its own, so no test can read or write the operator's own
// Registry -- which for this route would mean planning or restoring over it.
type recoveryHarness struct {
	t       *testing.T
	command *registryRecoveryCommand
	store   *intmetadata.Store
	dir     string
	tmux    *recordingRecoveryRunner
}

// recordingRecoveryRunner answers the three mirror queries from canned output
// and records every argv it was handed. Recording is the point: a plan that
// does not need the mirror must make no tmux call at all.
type recordingRecoveryRunner struct {
	calls    [][]string
	sessions string
	windows  string
	panes    string
	err      error
}

func (r *recordingRecoveryRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.err != nil {
		return nil, r.err
	}
	for _, arg := range args {
		switch arg {
		case "list-sessions":
			return []byte(r.sessions), nil
		case "list-windows":
			return []byte(r.windows), nil
		case "list-panes":
			return []byte(r.panes), nil
		}
	}
	return nil, fmt.Errorf("unexpected tmux call %v", args)
}

// mirrorRow renders one row in the separator convention the mirror formats use.
func mirrorRow(fields ...string) string {
	return strings.Join(fields, "\\037") + "\n"
}

func newRecoveryHarness(t *testing.T) *recoveryHarness {
	t.Helper()
	dir := t.TempDir()
	store := intmetadata.NewStore(filepath.Join(dir, "metadata", "registry.json"))
	runner := &recordingRecoveryRunner{}
	command := newRegistryRecoveryCommand(nil)
	command.newStore = func() (*intmetadata.Store, error) { return store, nil }
	command.lookupEnv = func(string) string { return "" }
	command.runner = runner
	command.observeFragments = func(ctx context.Context, target explicitTmuxTarget) ([]intmetadata.IdentityFragment, error) {
		return intmetadata.NewMirror(explicitTmuxRunner{runner: runner, target: target}).ObserveIdentityFragments(ctx)
	}
	return &recoveryHarness{t: t, command: command, store: store, dir: dir, tmux: runner}
}

// seed performs writes semantic enough to leave bounded copies behind.
func (h *recoveryHarness) seed(writes int) {
	h.t.Helper()
	counts := map[coremetadata.Kind]int{}
	fixed := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	mutator := coremetadata.Mutator{
		Now: func() time.Time { return fixed },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counts[kind]++
			return fmt.Sprintf("%s-%02d", strings.ToLower(string(kind)), counts[kind]), nil
		},
		DirExists: func(string) (bool, error) { return true, nil },
	}
	if _, err := h.store.Update(func(reg *coremetadata.Registry) error {
		_, err := mutator.RegisterProject(reg, coremetadata.RegisterProjectOptions{
			Root: "/src/projmux", DefaultShell: "/bin/zsh",
			Topology:    []coremetadata.BootstrapWindow{{Command: "nvim"}},
			OperationID: "op-seed",
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed registry: %v", err)
	}
	for i := range writes {
		name := fmt.Sprintf("renamed-%d", i)
		if _, err := h.store.Update(func(reg *coremetadata.Registry) error {
			_, err := mutator.RenameProject(reg, reg.Projects[0].Metadata.UID, name)
			return err
		}); err != nil {
			h.t.Fatalf("write %d: %v", i, err)
		}
	}
}

func (h *recoveryHarness) metadataDir() string { return filepath.Dir(h.store.Path()) }

func (h *recoveryHarness) run(args ...string) (registryRecoveryReport, string, error) {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	err := h.command.Run(append(args, "-o", "json"), &stdout, &stderr)
	var report registryRecoveryReport
	if stdout.Len() > 0 {
		if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
			h.t.Fatalf("decode report: %v\n%s", decodeErr, stdout.String())
		}
	}
	return report, stdout.String(), err
}

func (h *recoveryHarness) fingerprint() []string {
	h.t.Helper()
	var out []string
	err := filepath.WalkDir(h.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		out = append(out, fmt.Sprintf("%s %v %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		h.t.Fatalf("fingerprint: %v", err)
	}
	return out
}

func (h *recoveryHarness) registryBytes() string {
	h.t.Helper()
	data, err := os.ReadFile(h.store.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "<missing>"
		}
		h.t.Fatalf("read registry: %v", err)
	}
	return string(data)
}

func (h *recoveryHarness) copyNames() []string {
	h.t.Helper()
	entries, err := os.ReadDir(filepath.Join(h.metadataDir(), "recovery"))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestReconcileRegistryDryRunIsZeroWriteAndStablySorted is acceptance 1 at the
// command boundary.
func TestReconcileRegistryDryRunIsZeroWriteAndStablySorted(t *testing.T) {
	t.Parallel()

	h := newRecoveryHarness(t)
	h.seed(3)
	if err := os.Remove(h.store.Path()); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}
	before := h.fingerprint()

	report, first, err := h.run("--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if report.Mode != "dry-run" || report.Outcome != "planned" {
		t.Fatalf("mode/outcome = %q/%q", report.Mode, report.Outcome)
	}
	if report.Current.State != intmetadata.RegistryStateMissing {
		t.Fatalf("current state = %q", report.Current.State)
	}
	if len(report.Sources) != 3 {
		t.Fatalf("sources = %d, want 3", len(report.Sources))
	}
	// Newest first, and every row carries the checksum and the reason it is or
	// is not restorable. That pairing is what makes the plan actionable.
	for index, source := range report.Sources {
		if index > 0 && report.Sources[index-1].Name < source.Name {
			t.Fatalf("candidates are not newest first: %s before %s", report.Sources[index-1].Name, source.Name)
		}
		if source.Checksum == "" || source.Reason == "" {
			t.Fatalf("candidate %s is missing a checksum or a reason: %+v", source.Name, source)
		}
	}
	if !strings.Contains(report.Next, "--expect-source-checksum "+report.Sources[0].Checksum) {
		t.Fatalf("next command does not guard the source it suggests: %q", report.Next)
	}
	if strings.Contains(report.Next, "--expect-current-checksum") {
		t.Fatalf("next command guards a current registry that has no bytes: %q", report.Next)
	}

	if got := h.fingerprint(); !equalRecoveryFingerprints(before, got) {
		t.Fatalf("a dry-run wrote to the state dir:\nbefore %v\nafter  %v", before, got)
	}
	if len(h.tmux.calls) != 0 {
		t.Fatalf("a dry-run with verified copies still queried tmux: %v", h.tmux.calls)
	}

	// Deterministic: same state, byte-identical report.
	_, second, err := h.run("--dry-run")
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	if first != second {
		t.Fatalf("dry-run output is not deterministic:\nfirst  %s\nsecond %s", first, second)
	}

	// Naming a source in dry-run mode is still zero-write.
	beforeSelected := h.fingerprint()
	selected, _, err := h.run("--dry-run", "--source", report.Sources[0].Name)
	if err != nil {
		t.Fatalf("dry-run with a source: %v", err)
	}
	if selected.Selection != "explicit" || selected.Selected == nil || selected.Selected.Name != report.Sources[0].Name {
		t.Fatalf("selection = %q %+v", selected.Selection, selected.Selected)
	}
	if got := h.fingerprint(); !equalRecoveryFingerprints(beforeSelected, got) {
		t.Fatalf("a dry-run with a source wrote to the state dir")
	}
}

// TestReconcileRegistryRestoresOnlyAnExplicitSourceAndRepeatsAsANoOp is
// acceptance 2 at the command boundary, plus the rule that the command never
// picks a source for the operator.
func TestReconcileRegistryRestoresOnlyAnExplicitSourceAndRepeatsAsANoOp(t *testing.T) {
	t.Parallel()

	h := newRecoveryHarness(t)
	h.seed(2)
	if err := os.Remove(h.store.Path()); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}

	// Without --source, execute mode still only plans: there is no implicit
	// "restore the newest".
	plan, _, err := h.run()
	if err != nil {
		t.Fatalf("plan without a source: %v", err)
	}
	if plan.Mode != "dry-run" || plan.Restore != nil {
		t.Fatalf("a sourceless run restored something: mode=%q restore=%+v", plan.Mode, plan.Restore)
	}
	if h.registryBytes() != "<missing>" {
		t.Fatalf("a sourceless run published a registry")
	}
	source := plan.Sources[0]

	restored, _, err := h.run("--source", source.Name, "--expect-source-checksum", source.Checksum)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Mode != "restore" || restored.Outcome != "restored" {
		t.Fatalf("mode/outcome = %q/%q", restored.Mode, restored.Outcome)
	}
	if restored.Restore == nil || !restored.Restore.Changed {
		t.Fatalf("restore report = %+v", restored.Restore)
	}
	if restored.Current.State != intmetadata.RegistryStateValid {
		t.Fatalf("post-restore current state = %q", restored.Current.State)
	}
	if restored.Current.Checksum != source.Checksum {
		t.Fatalf("the published registry is not the verified source bytes")
	}
	// Identity round-trips, which is the point of preserving bytes rather than
	// re-encoding them.
	loaded, err := h.store.LoadReadOnly()
	if err != nil {
		t.Fatalf("read after restore: %v", err)
	}
	if len(loaded.Projects) != 1 || loaded.Projects[0].Metadata.UID != "project-01" {
		t.Fatalf("restored projects = %+v", loaded.Projects)
	}
	if len(loaded.NameReservations) == 0 {
		t.Fatalf("the restored registry holds no name reservations")
	}

	copiesBefore := h.copyNames()
	bytesBefore := h.registryBytes()
	repeat, _, err := h.run("--source", source.Name)
	if err != nil {
		t.Fatalf("repeat restore: %v", err)
	}
	if repeat.Outcome != "no-op" || repeat.Restore == nil || repeat.Restore.Changed {
		t.Fatalf("repeat outcome = %q restore = %+v", repeat.Outcome, repeat.Restore)
	}
	if h.registryBytes() != bytesBefore {
		t.Fatalf("a repeat restore replaced the registry")
	}
	if !equalRecoveryFingerprints(copiesBefore, h.copyNames()) {
		t.Fatalf("a repeat restore added a copy: %v -> %v", copiesBefore, h.copyNames())
	}
}

// TestReconcileRegistryRefusesEveryUnverifiableSource is acceptance 3: each row
// is an actionable failure, and none of them changes the current Registry.
func TestReconcileRegistryRefusesEveryUnverifiableSource(t *testing.T) {
	t.Parallel()

	h := newRecoveryHarness(t)
	h.seed(3)
	valid := h.registryBytes()
	recovery := filepath.Join(h.metadataDir(), "recovery")
	writeRecoveryFixture(t, recovery, "registry-20260101T000000Z-00.json", "{ not json")
	writeRecoveryFixture(t, recovery, "registry-20260102T000000Z-00.json", recoveryWithSchemaVersion(t, valid, coremetadata.SchemaVersion+5))
	writeRecoveryFixture(t, recovery, "registry-20260103T000000Z-00.json", recoveryWithDanglingOwner(t, valid))

	cases := []struct {
		name   string
		args   []string
		wantIn string
	}{
		{name: "malformed", args: []string{"--source", "registry-20260101T000000Z-00.json"}, wantIn: "not decodable JSON"},
		{name: "schema too new", args: []string{"--source", "registry-20260102T000000Z-00.json"}, wantIn: "newer than the supported version"},
		{name: "invalid graph", args: []string{"--source", "registry-20260103T000000Z-00.json"}, wantIn: "not a valid resource graph"},
		{name: "ambiguous", args: []string{"--source", "registry-2026"}, wantIn: "name one exactly"},
		{name: "not found", args: []string{"--source", "no-such-copy.json"}, wantIn: "does not exist"},
		{name: "raced source", args: []string{"--source", "registry-20260101T000000Z-00.json", "--expect-source-checksum", recoveryZeroChecksum}, wantIn: "not decodable JSON"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			before := h.registryBytes()
			fingerprint := h.fingerprint()
			report, _, err := h.run(tt.args...)
			if err == nil {
				t.Fatalf("%s was accepted", tt.name)
			}
			if report.Outcome != "refused" {
				t.Fatalf("outcome = %q, want refused", report.Outcome)
			}
			if !strings.Contains(report.Error, tt.wantIn) {
				t.Fatalf("report error %q does not explain the refusal (%q)", report.Error, tt.wantIn)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Fatalf("returned error %q does not explain the refusal (%q)", err, tt.wantIn)
			}
			if h.registryBytes() != before {
				t.Fatalf("%s changed the current Registry", tt.name)
			}
			if got := h.fingerprint(); !equalRecoveryFingerprints(fingerprint, got) {
				t.Fatalf("%s wrote to the state dir:\nbefore %v\nafter  %v", tt.name, fingerprint, got)
			}
		})
	}

	// A stale guard against an otherwise good source is the race row proper.
	t.Run("raced guard on a valid source", func(t *testing.T) {
		before := h.registryBytes()
		plan, _, err := h.run("--dry-run")
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		var good intmetadata.RecoverySource
		for _, source := range plan.Sources {
			if source.Eligible {
				good = source
				break
			}
		}
		if good.Name == "" {
			t.Fatalf("no eligible source to race")
		}
		_, report, err := h.run("--source", good.Name, "--expect-current-checksum", recoveryZeroChecksum)
		if err == nil {
			t.Fatalf("a stale current-registry guard was accepted")
		}
		if !strings.Contains(report, "re-run the preview") {
			t.Fatalf("the race report does not say what to do next: %s", report)
		}
		if h.registryBytes() != before {
			t.Fatalf("a raced restore changed the current Registry")
		}
	})
}

// TestReconcileRegistryDiagnosesPartialMirrorEvidenceWithoutRebuilding is the
// partial-evidence half: fragments are reported, the gaps are stated, and no
// Registry is produced from either.
func TestReconcileRegistryDiagnosesPartialMirrorEvidenceWithoutRebuilding(t *testing.T) {
	t.Parallel()

	h := newRecoveryHarness(t)
	h.seed(1)
	// Lose both the registry and every copy: the only evidence left is live tmux.
	if err := os.Remove(h.store.Path()); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(h.metadataDir(), "recovery")); err != nil {
		t.Fatalf("drop copies: %v", err)
	}
	h.tmux.sessions = mirrorRow("proj-alpha", "alpha", "/src/alpha", "alpha")
	h.tmux.windows = mirrorRow("win-editor", "editor", "@7", "alpha", "1")
	h.tmux.panes = mirrorRow("pane-shell", "zsh", "%3", "@7", "") +
		mirrorRow("pane-agent", "codex", "%4", "@7", "codex") +
		mirrorRow("", "", "%5", "@7", "")
	h.command.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/pmx-test/socket,1234,0"
		}
		return ""
	}
	before := h.fingerprint()

	report, _, err := h.run()
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Outcome != "unrecoverable" {
		t.Fatalf("outcome = %q, want unrecoverable", report.Outcome)
	}
	if report.Mirror == nil || !report.Mirror.Available {
		t.Fatalf("mirror = %+v", report.Mirror)
	}
	if report.Mirror.Target != "tmux -S /tmp/pmx-test/socket" {
		t.Fatalf("mirror target = %q; the inherited absolute socket must be the exact target", report.Mirror.Target)
	}
	// The uid-less pane is unattributed runtime, not a recoverable fragment.
	if report.Mirror.Counts.Projects != 1 || report.Mirror.Counts.Windows != 1 || report.Mirror.Counts.Panes != 2 {
		t.Fatalf("counts = %+v", report.Mirror.Counts)
	}
	if report.Mirror.Counts.AgentPanes != 1 {
		t.Fatalf("agent panes = %d; a pane carrying a provider option proves an Agent whose uid is not mirrored", report.Mirror.Counts.AgentPanes)
	}
	// Containment is resolved, and it is labelled containment rather than
	// ownership.
	byUID := map[string]intmetadata.IdentityFragment{}
	for _, fragment := range report.Mirror.Recoverable {
		byUID[fragment.UID] = fragment
	}
	if got := byUID["win-editor"].ContainerUID; got != "proj-alpha" {
		t.Fatalf("window container = %q, want the session's Project uid", got)
	}
	if got := byUID["pane-agent"].ContainerUID; got != "win-editor" {
		t.Fatalf("pane container = %q, want the containing Window uid", got)
	}
	if got := byUID["proj-alpha"].Root; got != "/src/alpha" {
		t.Fatalf("project root = %q", got)
	}
	scopes := map[string]bool{}
	for _, gap := range report.Mirror.Unrecoverable {
		scopes[gap.Scope] = true
		if gap.Reason == "" {
			t.Fatalf("gap %q has no reason", gap.Scope)
		}
	}
	for _, want := range []string{"offline-resources", "agent-resources", "pane-owner-relation", "name-reservations", "window-anchor-refs", "labels-annotations-status"} {
		if !scopes[want] {
			t.Fatalf("the diagnostic does not state the %s gap: %v", want, scopes)
		}
	}
	// Diagnosing must not have rebuilt, restored, or created anything.
	if h.registryBytes() != "<missing>" {
		t.Fatalf("the mirror diagnostic created a Registry")
	}
	if got := h.fingerprint(); !equalRecoveryFingerprints(before, got) {
		t.Fatalf("the mirror diagnostic wrote to the state dir:\nbefore %v\nafter  %v", before, got)
	}
	// Read-only tmux calls only: no set-option, no new-session, no kill.
	if len(h.tmux.calls) == 0 {
		t.Fatalf("the mirror diagnostic made no tmux call")
	}
	for _, call := range h.tmux.calls {
		if !recoveryCallIsReadOnly(call) {
			t.Fatalf("the mirror diagnostic issued a mutating tmux call: %v", call)
		}
		if call[1] != "-S" || call[2] != "/tmp/pmx-test/socket" {
			t.Fatalf("a mirror call left the exact socket: %v", call)
		}
	}
}

// TestReconcileRegistryReportsMirrorUnavailabilityAsAReason keeps recovery
// possible on the machine state where tmux is least likely to be running.
func TestReconcileRegistryReportsMirrorUnavailabilityAsAReason(t *testing.T) {
	t.Parallel()

	t.Run("no transport", func(t *testing.T) {
		t.Parallel()
		h := newRecoveryHarness(t)
		h.seed(1)
		if err := os.Remove(h.store.Path()); err != nil {
			t.Fatalf("simulate loss: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(h.metadataDir(), "recovery")); err != nil {
			t.Fatalf("drop copies: %v", err)
		}
		report, _, err := h.run()
		if err != nil {
			t.Fatalf("plan outside tmux: %v", err)
		}
		if report.Mirror == nil || report.Mirror.Available {
			t.Fatalf("mirror = %+v", report.Mirror)
		}
		if !strings.Contains(report.Mirror.Reason, "--socket") {
			t.Fatalf("reason %q does not say how to supply a target", report.Mirror.Reason)
		}
		if len(report.Mirror.Unrecoverable) == 0 {
			t.Fatalf("the gap statement disappeared with the transport")
		}
		if len(h.tmux.calls) != 0 {
			t.Fatalf("a missing transport still probed a tmux server: %v", h.tmux.calls)
		}
	})

	t.Run("query failure", func(t *testing.T) {
		t.Parallel()
		h := newRecoveryHarness(t)
		h.seed(1)
		if err := os.Remove(h.store.Path()); err != nil {
			t.Fatalf("simulate loss: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(h.metadataDir(), "recovery")); err != nil {
			t.Fatalf("drop copies: %v", err)
		}
		h.tmux.err = errors.New("no server running")
		report, _, err := h.run("--socket", "pmx-test")
		if err != nil {
			t.Fatalf("plan with a dead server: %v", err)
		}
		if report.Mirror == nil || report.Mirror.Available {
			t.Fatalf("mirror = %+v", report.Mirror)
		}
		if !strings.Contains(report.Mirror.Reason, "no server running") {
			t.Fatalf("reason %q does not carry the observation failure", report.Mirror.Reason)
		}
		if report.Mirror.Target != "tmux -L pmx-test" {
			t.Fatalf("target = %q", report.Mirror.Target)
		}
	})
}

// TestReconcileRegistryLeavesTheMirrorAloneWhenItIsNotEvidence is the
// forbidden-call audit. A healthy Registry and a Registry with verified copies
// are both answerable without touching tmux.
func TestReconcileRegistryLeavesTheMirrorAloneWhenItIsNotEvidence(t *testing.T) {
	t.Parallel()

	t.Run("healthy registry", func(t *testing.T) {
		t.Parallel()
		h := newRecoveryHarness(t)
		h.seed(1)
		h.command.lookupEnv = func(key string) string {
			if key == "TMUX" {
				return "/tmp/pmx-test/socket,1,0"
			}
			return ""
		}
		report, _, err := h.run()
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if report.Mirror != nil {
			t.Fatalf("a healthy Registry produced a mirror diagnostic: %+v", report.Mirror)
		}
		if len(h.tmux.calls) != 0 {
			t.Fatalf("a healthy Registry plan queried tmux: %v", h.tmux.calls)
		}
	})

	t.Run("first use", func(t *testing.T) {
		t.Parallel()
		h := newRecoveryHarness(t)
		report, _, err := h.run()
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if report.Outcome != "no-op" {
			t.Fatalf("a genuine first use reported %q", report.Outcome)
		}
		if report.Current.State != intmetadata.RegistryStateFirstUse {
			t.Fatalf("current state = %q", report.Current.State)
		}
		if report.Mirror != nil {
			t.Fatalf("a first use produced a mirror diagnostic")
		}
		if _, err := os.Stat(filepath.Join(h.dir, "metadata")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("planning against a first-use state dir materialized it: %v", err)
		}
	})

	t.Run("lost registry with a verified copy", func(t *testing.T) {
		t.Parallel()
		h := newRecoveryHarness(t)
		h.seed(1)
		if err := os.Remove(h.store.Path()); err != nil {
			t.Fatalf("simulate loss: %v", err)
		}
		report, _, err := h.run()
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if report.Mirror != nil {
			t.Fatalf("a recoverable loss produced a mirror diagnostic: %+v", report.Mirror)
		}
		if len(h.tmux.calls) != 0 {
			t.Fatalf("a recoverable loss queried tmux: %v", h.tmux.calls)
		}
	})
}

// TestReconcileRegistryUsageRefusalsHappenBeforeAnyRead keeps a malformed
// invocation from being reported as a state problem.
func TestReconcileRegistryUsageRefusalsHappenBeforeAnyRead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		args   []string
		wantIn string
	}{
		{name: "two sources", args: []string{"--source", "a.json", "--source", "b.json"}, wantIn: "exactly one --source"},
		{name: "blank source", args: []string{"--source", "  "}, wantIn: "non-empty recovery copy name"},
		{name: "positional", args: []string{"newest"}, wantIn: "does not accept positional arguments"},
		{name: "both sockets", args: []string{"--socket", "a", "--socket-path", "/tmp/s"}, wantIn: "only one of --socket and --socket-path"},
		{name: "bad output", args: []string{"-o", "yaml"}, wantIn: "unsupported reconcile registry output"},
		{name: "short checksum", args: []string{"--source", "a.json", "--expect-source-checksum", "sha256:abc"}, wantIn: "64 hex characters"},
		{name: "unprefixed checksum", args: []string{"--source", "a.json", "--expect-current-checksum", strings.Repeat("a", 64)}, wantIn: "64 hex characters"},
		{name: "uppercase checksum", args: []string{"--source", "a.json", "--expect-source-checksum", "sha256:" + strings.Repeat("A", 64)}, wantIn: "64 hex characters"},
		{name: "guard without a source", args: []string{"--expect-source-checksum", recoveryZeroChecksum}, wantIn: "checksum guards require --source"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newRecoveryHarness(t)
			h.command.newStore = func() (*intmetadata.Store, error) {
				t.Fatalf("%s opened the store before failing on usage", tt.name)
				return nil, nil
			}
			var stdout, stderr bytes.Buffer
			err := h.command.Run(tt.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("%s was accepted", tt.name)
			}
			if !IsUsageError(err) {
				t.Fatalf("%s error = %v, want a usage error", tt.name, err)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Fatalf("%s error = %q, want %q", tt.name, err, tt.wantIn)
			}
			if stdout.Len() != 0 {
				t.Fatalf("%s wrote a report to stdout: %s", tt.name, stdout.String())
			}
		})
	}
}

// TestReconcileDispatchesRegistryAndResourcesSeparately keeps the two repair
// contracts from bleeding into one another.
func TestReconcileDispatchesRegistryAndResourcesSeparately(t *testing.T) {
	t.Parallel()

	command := newResourceReconcileCommand(nil)
	var stdout, stderr bytes.Buffer
	err := command.Run(nil, &stdout, &stderr)
	if err == nil || !IsUsageError(err) {
		t.Fatalf("bare reconcile error = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "resources or registry") {
		t.Fatalf("error %q does not name both subcommands", err)
	}
	usage := stderr.String()
	if !strings.Contains(usage, "projmux reconcile resources") || !strings.Contains(usage, "projmux reconcile registry") {
		t.Fatalf("usage does not show both subcommands: %s", usage)
	}
	if command.registry == nil {
		t.Fatalf("reconcile has no registry recovery route")
	}
}

// TestReconcileRegistryHumanOutputCarriesTheSameFacts keeps the default
// projection usable on its own: an operator who never passes -o json still sees
// the state, the candidates, the reasons, and the guarded next command.
func TestReconcileRegistryHumanOutputCarriesTheSameFacts(t *testing.T) {
	t.Parallel()

	h := newRecoveryHarness(t)
	h.seed(2)
	if err := os.Remove(h.store.Path()); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}
	writeRecoveryFixture(t, filepath.Join(h.metadataDir(), "recovery"), "registry-20260101T000000Z-00.json", "{ not json")

	var stdout, stderr bytes.Buffer
	if err := h.command.Run([]string{"--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"mode: dry-run", "outcome: planned", "selection: none",
		"current: missing", "initialized=true", "sources: 3 candidate(s)",
		"eligible valid", "rejected malformed", "not decodable JSON",
		"next: projmux reconcile registry --source", "--expect-source-checksum sha256:",
		"contents=projects=1/windows=1/panes=1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output is missing %q:\n%s", want, out)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("a successful dry-run wrote to stderr: %s", stderr.String())
	}
}

const recoveryZeroChecksum = "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"

func writeRecoveryFixture(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func recoveryWithSchemaVersion(t *testing.T, contents string, version int) string {
	t.Helper()
	return recoveryMutateJSON(t, contents, func(doc map[string]any) { doc["schemaVersion"] = version })
}

func recoveryWithDanglingOwner(t *testing.T, contents string) string {
	t.Helper()
	return recoveryMutateJSON(t, contents, func(doc map[string]any) {
		windows, _ := doc["windows"].([]any)
		first, _ := windows[0].(map[string]any)
		meta, _ := first["metadata"].(map[string]any)
		owner, _ := meta["ownerRef"].(map[string]any)
		owner["uid"] = "project-does-not-exist"
	})
}

func recoveryMutateJSON(t *testing.T, contents string, mutate func(map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(contents), &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	mutate(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return string(out) + "\n"
}

// recoveryCallIsReadOnly recognizes the three list queries the mirror is allowed
// to make. Anything else on a diagnostic path is a mutation.
func recoveryCallIsReadOnly(call []string) bool {
	for _, arg := range call {
		switch arg {
		case "list-sessions", "list-windows", "list-panes":
			return true
		}
	}
	return false
}

func equalRecoveryFingerprints(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
