package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// seedRecoveryStore builds a store whose state directory has completed writes,
// so it holds a marker, a valid registry, and one bounded copy per semantic
// write. Every recovery test starts from a real envelope rather than a
// hand-written file, so what is exercised is what the write path actually
// produces.
func seedRecoveryStore(t *testing.T, writes int) (*Store, []string) {
	t.Helper()
	store := testStore(t)
	mutator := testMutator(map[string]bool{"/src/projmux": true})
	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		_, err := mutator.RegisterProject(reg, coremetadata.RegisterProjectOptions{
			Root:         "/src/projmux",
			DefaultShell: "/bin/zsh",
			Topology: []coremetadata.BootstrapWindow{
				{Command: "nvim"},
				{Name: "server", Panes: []coremetadata.BootstrapPane{{Command: "npm run dev"}}},
			},
			OperationID: "op-seed",
		})
		return err
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	var replaced []string
	for i := range writes {
		replaced = append(replaced, readFile(t, store.Path()))
		name := fmt.Sprintf("renamed-%d", i)
		// The rename goes through the mutator so the name reservation table
		// stays consistent. A hand-edited name would produce copies that fail
		// the graph guard, which would test the guard instead of the recovery.
		if _, err := store.Update(func(reg *coremetadata.Registry) error {
			if len(reg.Projects) == 0 {
				return errors.New("no project to rename")
			}
			_, err := mutator.RenameProject(reg, reg.Projects[0].Metadata.UID, name)
			return err
		}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	return store, replaced
}

func TestDegradedModeEntersOnlyForAnIllegalRegistry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state RegistryState
		want  bool
	}{
		{state: RegistryStateFirstUse, want: false},
		{state: RegistryStateValid, want: false},
		{state: RegistryStateMissing, want: true},
		{state: RegistryStateEmpty, want: true},
		{state: RegistryStateMalformed, want: true},
		{state: RegistryStateSchemaTooNew, want: true},
		{state: RegistryStateInvalid, want: true},
		{state: RegistryStateUnreadable, want: true},
	}
	for _, tt := range cases {
		t.Run(string(tt.state), func(t *testing.T) {
			inspection := RecoveryInspection{Current: RegistryFileInfo{
				State: tt.state, Detail: "classified fixture reason",
			}}
			mode := inspection.DegradedMode()
			if mode.Active != tt.want {
				t.Fatalf("state %q degraded = %t, want %t", tt.state, mode.Active, tt.want)
			}
			if !tt.want {
				if err := mode.Error(); err != nil {
					t.Fatalf("healthy state %q produced error %v", tt.state, err)
				}
				return
			}
			if mode.Next != RegistryRecoveryPlanCommand {
				t.Fatalf("state %q next = %q", tt.state, mode.Next)
			}
			if err := mode.Error(); !errors.Is(err, ErrRegistryDegraded) {
				t.Fatalf("state %q error = %v, want ErrRegistryDegraded", tt.state, err)
			}
		})
	}
}

// stateFingerprint captures everything a zero-write claim has to cover: which
// files exist, their inode, size, and modification time. It is deliberately
// stricter than comparing contents, because creating and removing a lock file
// leaves contents identical.
func stateFingerprint(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s mode=%v size=%d mtime=%d", path, info.Mode(), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fingerprint %s: %v", dir, err)
	}
	return out
}

// durableFingerprint captures the files that are state, skipping directories and
// the lock file.
//
// It is the right assertion for an operation that takes the cross-process lock.
// Taking the lock creates and removes a lock file, which moves the containing
// directory's mtime without any state changing -- the same property the Phase 0
// convergent-no-op contract has. What must not move is the registry, the marker,
// and the recovery copies, so those are what this compares.
func durableFingerprint(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range stateFingerprint(t, dir) {
		path, _, _ := strings.Cut(line, " mode=")
		if strings.HasSuffix(path, lockFileSuffix) {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			continue
		}
		out = append(out, line)
	}
	return out
}

func writeCopyFile(t *testing.T, store *Store, name, contents string) string {
	t.Helper()
	if err := os.MkdirAll(store.recoveryDir, 0o700); err != nil {
		t.Fatalf("mkdir recovery: %v", err)
	}
	path := filepath.Join(store.recoveryDir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write copy %s: %v", name, err)
	}
	return path
}

func sourceNamed(t *testing.T, inspection RecoveryInspection, name string) RecoverySource {
	t.Helper()
	for _, source := range inspection.Sources {
		if source.Name == name {
			return source
		}
	}
	t.Fatalf("no candidate named %q in %v", name, inspection.Sources)
	return RecoverySource{}
}

// TestInspectRecoveryClassifiesEveryStateWithoutWriting is the plan half of
// acceptance 1: a preview must be safe against a state directory nobody should
// be writing to, including one that does not exist yet.
func TestInspectRecoveryClassifiesEveryStateWithoutWriting(t *testing.T) {
	t.Parallel()

	t.Run("first use", func(t *testing.T) {
		t.Parallel()
		store := testStore(t)
		stateDir := filepath.Dir(filepath.Dir(store.Path()))
		before := stateFingerprint(t, stateDir)

		inspection, err := store.InspectRecovery()
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if inspection.Initialized {
			t.Fatalf("a fresh state dir reported an initialized marker")
		}
		if inspection.Current.State != RegistryStateFirstUse {
			t.Fatalf("current state = %q, want %q (%s)", inspection.Current.State, RegistryStateFirstUse, inspection.Current.Detail)
		}
		if len(inspection.Sources) != 0 {
			t.Fatalf("first use offered %d candidates", len(inspection.Sources))
		}
		if got := stateFingerprint(t, stateDir); !equalStrings(before, got) {
			t.Fatalf("inspecting a first-use state dir wrote to it:\nbefore %v\nafter  %v", before, got)
		}
		if _, err := os.Stat(filepath.Dir(store.Path())); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect materialized the metadata dir: %v", err)
		}
	})

	t.Run("state loss and copies", func(t *testing.T) {
		t.Parallel()
		store, replaced := seedRecoveryStore(t, 3)
		if err := os.Remove(store.Path()); err != nil {
			t.Fatalf("simulate loss: %v", err)
		}
		before := stateFingerprint(t, filepath.Dir(store.Path()))

		inspection, err := store.InspectRecovery()
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if !inspection.Initialized {
			t.Fatalf("a completed write left no marker")
		}
		if inspection.Current.State != RegistryStateMissing {
			t.Fatalf("current state = %q, want %q", inspection.Current.State, RegistryStateMissing)
		}
		if inspection.Current.Checksum != "" {
			t.Fatalf("a missing registry reported checksum %q", inspection.Current.Checksum)
		}
		if len(inspection.Sources) != len(replaced) {
			t.Fatalf("candidates = %d, want %d", len(inspection.Sources), len(replaced))
		}
		// Newest first, and the newest copy holds the bytes the last write
		// replaced. Both halves matter: order is what an operator reads, and the
		// pairing is what makes the order meaningful.
		for index, source := range inspection.Sources {
			if !source.Eligible {
				t.Fatalf("candidate %s is not eligible: %s", source.Name, source.Reason)
			}
			if source.Kind != RecoverySourceWriteCopy {
				t.Fatalf("candidate %s kind = %q", source.Name, source.Kind)
			}
			want := replaced[len(replaced)-1-index]
			if got := readFile(t, source.Path); got != want {
				t.Fatalf("candidate %d (%s) does not hold the bytes of write %d", index, source.Name, len(replaced)-1-index)
			}
			if source.Contents.Projects != 1 || source.Contents.Windows != 2 || source.Contents.Panes != 2 {
				t.Fatalf("candidate %s contents = %+v", source.Name, source.Contents)
			}
			if source.Contents.Reservations == 0 {
				t.Fatalf("candidate %s reported no name reservations; the reservation table is the part no mirror can rebuild", source.Name)
			}
		}
		if got := stateFingerprint(t, filepath.Dir(store.Path())); !equalStrings(before, got) {
			t.Fatalf("inspecting a lost state dir wrote to it:\nbefore %v\nafter  %v", before, got)
		}
	})

	t.Run("empty registry before any write is first use", func(t *testing.T) {
		t.Parallel()
		store := testStore(t)
		writeRegistryFile(t, store, "   \n")
		inspection, err := store.InspectRecovery()
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if inspection.Current.State != RegistryStateFirstUse {
			t.Fatalf("state = %q, want %q", inspection.Current.State, RegistryStateFirstUse)
		}
	})
}

// TestRecoveryCandidateVerificationIsFailClosed is the source half of the
// guard: every way a copy can be unusable classifies distinctly, and none of
// them is offered as restorable.
func TestRecoveryCandidateVerificationIsFailClosed(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 1)
	valid := readFile(t, store.Path())

	tooNew := mutateRegistryJSON(t, valid, func(doc map[string]any) {
		doc["schemaVersion"] = coremetadata.SchemaVersion + 7
	})
	danglingOwner := mutateRegistryJSON(t, valid, func(doc map[string]any) {
		windows, _ := doc["windows"].([]any)
		first, _ := windows[0].(map[string]any)
		meta, _ := first["metadata"].(map[string]any)
		owner, _ := meta["ownerRef"].(map[string]any)
		owner["uid"] = "project-does-not-exist"
	})
	duplicateUID := mutateRegistryJSON(t, valid, func(doc map[string]any) {
		windows, _ := doc["windows"].([]any)
		first, _ := windows[0].(map[string]any)
		second, _ := windows[1].(map[string]any)
		firstMeta, _ := first["metadata"].(map[string]any)
		secondMeta, _ := second["metadata"].(map[string]any)
		secondMeta["uid"] = firstMeta["uid"]
	})

	cases := []struct {
		name      string
		contents  string
		wantState RegistryState
		wantIn    string
	}{
		{name: "malformed", contents: "{ not json", wantState: RegistryStateMalformed, wantIn: "not decodable JSON"},
		{name: "empty", contents: "\n\t \n", wantState: RegistryStateEmpty, wantIn: "holds no content"},
		{name: "schema too new", contents: tooNew, wantState: RegistryStateSchemaTooNew},
		{name: "dangling owner", contents: danglingOwner, wantState: RegistryStateInvalid, wantIn: "not a valid resource graph"},
		{name: "duplicate uid", contents: duplicateUID, wantState: RegistryStateInvalid, wantIn: "not a valid resource graph"},
	}
	for index, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			name := fmt.Sprintf("registry-20260101T00000%dZ-00.json", index)
			writeCopyFile(t, store, name, tt.contents)
			inspection, err := store.InspectRecovery()
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			source := sourceNamed(t, inspection, name)
			if source.State != tt.wantState {
				t.Fatalf("state = %q, want %q (%s)", source.State, tt.wantState, source.Detail)
			}
			if source.Eligible {
				t.Fatalf("%s was offered as restorable", tt.name)
			}
			if tt.wantIn != "" && !strings.Contains(source.Reason, tt.wantIn) {
				t.Fatalf("reason %q does not explain the refusal (%q)", source.Reason, tt.wantIn)
			}

			// The same classification must refuse a restore, and refusing must
			// leave the live registry untouched.
			before := readFile(t, store.Path())
			fingerprint := stateFingerprint(t, filepath.Dir(store.Path()))
			_, err = store.RestoreFrom(RestoreRequest{SourcePath: source.Path})
			if !errors.Is(err, ErrRecoverySourceRejected) {
				t.Fatalf("restore error = %v, want ErrRecoverySourceRejected", err)
			}
			if got := readFile(t, store.Path()); got != before {
				t.Fatalf("a refused restore replaced the registry")
			}
			if got := stateFingerprint(t, filepath.Dir(store.Path())); !equalStrings(fingerprint, got) {
				t.Fatalf("a refused restore changed the state dir:\nbefore %v\nafter  %v", fingerprint, got)
			}
			if err := os.Remove(source.Path); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		})
	}
}

// TestNonStoreFilesAreNeverOfferedAsRecoverySources keeps the bounded set
// bounded: a file this store did not create is not evidence about the registry,
// and offering it would be a guess.
func TestNonStoreFilesAreNeverOfferedAsRecoverySources(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 1)
	valid := readFile(t, store.Path())
	writeCopyFile(t, store, "operator-note.txt", "remember to check the backup drive")
	writeCopyFile(t, store, "my-own-copy.json", valid)
	writeCopyFile(t, store, "registry-not-a-stamp.json", valid)

	inspection, err := store.InspectRecovery()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, source := range inspection.Sources {
		if !strings.HasPrefix(source.Name, recoveryFilePrefix) && !strings.HasPrefix(source.Name, preservedFilePrefix) {
			t.Fatalf("candidate %q was not written by this store", source.Name)
		}
		if source.Name == "registry-not-a-stamp.json" {
			t.Fatalf("a file with an unparsable stamp became a candidate")
		}
	}
}

// TestARestoreIsByteSemanticAndRepeatsAsANoOp is acceptance 2.
func TestARestoreIsByteSemanticAndRepeatsAsANoOp(t *testing.T) {
	t.Parallel()

	store, replaced := seedRecoveryStore(t, 3)
	if err := os.Remove(store.Path()); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}
	inspection, err := store.InspectRecovery()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	newest := inspection.EligibleSources()[0]

	result, err := store.RestoreFrom(RestoreRequest{
		SourcePath:           newest.Path,
		ExpectSourceChecksum: newest.Checksum,
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !result.Changed {
		t.Fatalf("restoring over a missing registry reported no change")
	}
	if result.PreservedPath != "" {
		t.Fatalf("there were no bytes to preserve, yet %s was written", result.PreservedPath)
	}
	// Byte-semantic: the published registry is the verified copy, not a
	// re-encoding of it, so uids, owner relations, and name reservations are
	// preserved exactly rather than by argument.
	if got, want := readFile(t, store.Path()), replaced[len(replaced)-1]; got != want {
		t.Fatalf("restored registry is not byte-identical to the verified source")
	}
	loaded, err := store.LoadReadOnly()
	if err != nil {
		t.Fatalf("read after restore: %v", err)
	}
	if len(loaded.Projects) != 1 || len(loaded.Windows) != 2 || len(loaded.Panes) != 2 {
		t.Fatalf("restored graph = %d/%d/%d projects/windows/panes", len(loaded.Projects), len(loaded.Windows), len(loaded.Panes))
	}
	if len(loaded.NameReservations) == 0 {
		t.Fatalf("the restored registry holds no name reservations")
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("restored registry does not validate: %v", err)
	}

	fingerprint := durableFingerprint(t, filepath.Dir(store.Path()))
	repeat, err := store.RestoreFrom(RestoreRequest{SourcePath: newest.Path, ExpectSourceChecksum: newest.Checksum})
	if err != nil {
		t.Fatalf("repeat restore: %v", err)
	}
	if repeat.Changed {
		t.Fatalf("a repeat restore reported a change")
	}
	if repeat.PreservedPath != "" {
		t.Fatalf("a repeat restore took a preserved copy at %s", repeat.PreservedPath)
	}
	// Byte-level no-op: the registry keeps its inode and mtime, the marker is
	// untouched, and no copy of either family appears.
	if got := durableFingerprint(t, filepath.Dir(store.Path())); !equalStrings(fingerprint, got) {
		t.Fatalf("a repeat restore wrote to the state dir:\nbefore %v\nafter  %v", fingerprint, got)
	}
	if _, err := os.Stat(store.lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a repeat restore left the lock behind: %v", err)
	}
}

// TestRepairDoesNotReuseOrdinaryWriteLockOrValidation is the negative contract
// between the two mutation paths. A deliberately held ordinary lock and a
// failing ordinary staged-validation hook cannot delay or disable recovery;
// RestoreFrom still performs its own source and staged graph verification.
func TestRepairDoesNotReuseOrdinaryWriteLockOrValidation(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 1)
	inspection, err := store.InspectRecovery()
	if err != nil {
		t.Fatalf("inspect recovery source: %v", err)
	}
	source := inspection.EligibleSources()[0]
	writeRegistryFile(t, store, `{"apiVersion":"projmux.io/v1alpha1","schemaVersion":1,"panes":[{"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-orphan","name":"zsh","ownerRef":{"kind":"Window","uid":"window-missing"}},"spec":{"role":"shell"}}]}`)

	if err := os.WriteFile(store.lockPath, []byte("ordinary writer intentionally held\n"), 0o600); err != nil {
		t.Fatalf("hold ordinary lock: %v", err)
	}
	defer os.Remove(store.lockPath)
	ordinaryValidationCalled := false
	store.hooks.validateStaged = func(string) error {
		ordinaryValidationCalled = true
		return errors.New("ordinary staged validation must not run")
	}

	type restoreOutcome struct {
		result RestoreResult
		err    error
	}
	done := make(chan restoreOutcome, 1)
	go func() {
		result, err := store.RestoreFrom(RestoreRequest{
			SourcePath:           source.Path,
			ExpectSourceChecksum: source.Checksum,
		})
		done <- restoreOutcome{result: result, err: err}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("repair while ordinary lock held: %v", outcome.err)
		}
		if !outcome.result.Changed {
			t.Fatal("repair reported no change over the invalid Registry")
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("repair waited for the ordinary registry write lock")
	}
	if ordinaryValidationCalled {
		t.Fatal("repair reused the ordinary staged-validation hook")
	}
	if _, err := os.Stat(store.lockPath); err != nil {
		t.Fatalf("repair removed or replaced the ordinary writer lock: %v", err)
	}
	if _, err := os.Stat(store.repairLockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repair left its recovery lock behind: %v", err)
	}
	registry, err := store.LoadSnapshot()
	if err != nil {
		t.Fatalf("read repaired Registry: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("repair published an invalid Registry: %v", err)
	}
}

// TestARestoreKeepsTheBytesItReplaced covers the way back. The corrupt case is
// the one that matters most: a verified-only rule would discard exactly the
// evidence an operator needs if the restore turns out to be the wrong call.
func TestARestoreKeepsTheBytesItReplaced(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		current   string
		wantState RegistryState
	}{
		{name: "valid current", wantState: RegistryStateValid},
		{name: "malformed current", current: "{ broken", wantState: RegistryStateMalformed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := seedRecoveryStore(t, 2)
			if tt.current != "" {
				writeRegistryFile(t, store, tt.current)
			}
			before := readFile(t, store.Path())

			inspection, err := store.InspectRecovery()
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			if inspection.Current.State != tt.wantState {
				t.Fatalf("current state = %q, want %q", inspection.Current.State, tt.wantState)
			}
			source := inspection.EligibleSources()[0]
			result, err := store.RestoreFrom(RestoreRequest{
				SourcePath:            source.Path,
				ExpectSourceChecksum:  source.Checksum,
				ExpectCurrentChecksum: inspection.Current.Checksum,
			})
			if err != nil {
				t.Fatalf("restore: %v", err)
			}
			if result.PreservedPath == "" {
				t.Fatalf("the replaced bytes were not preserved")
			}
			if got := readFile(t, result.PreservedPath); got != before {
				t.Fatalf("the preserved copy is not the bytes that were replaced")
			}
			if result.ReplacedState != tt.wantState {
				t.Fatalf("replaced state = %q, want %q", result.ReplacedState, tt.wantState)
			}
			// The preserved copy is a candidate in its own right, which is what
			// makes a restore reversible.
			after, err := store.InspectRecovery()
			if err != nil {
				t.Fatalf("re-inspect: %v", err)
			}
			found := false
			for _, candidate := range after.Sources {
				if candidate.Path != result.PreservedPath {
					continue
				}
				found = true
				if candidate.Kind != RecoverySourceReplacedCopy {
					t.Fatalf("preserved copy kind = %q", candidate.Kind)
				}
				if candidate.Eligible != (tt.wantState == RegistryStateValid) {
					t.Fatalf("preserved copy eligible = %t for a %s original", candidate.Eligible, tt.wantState)
				}
			}
			if !found {
				t.Fatalf("the preserved copy is not offered as a candidate")
			}
			if got := stat(t, result.PreservedPath).Mode().Perm(); got != 0o600 {
				t.Fatalf("preserved copy mode = %v", got)
			}
		})
	}
}

// TestPreservedCopiesStayBoundedAndDoNotDisturbWriteCopies keeps the two
// families independent: a restore must not consume the automatic history, and
// its own copies must not grow without bound.
func TestPreservedCopiesStayBoundedAndDoNotDisturbWriteCopies(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 6)
	writeCopies := len(store.recoveryCopyNames())
	if writeCopies != defaultRecoveryRetention {
		t.Fatalf("write copies = %d, want the bounded %d", writeCopies, defaultRecoveryRetention)
	}
	inspection, err := store.InspectRecovery()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	eligible := inspection.EligibleSources()

	// Alternate between two copies so every restore actually changes bytes.
	for i := range defaultRecoveryRetention + 3 {
		source := eligible[i%2]
		if _, err := store.RestoreFrom(RestoreRequest{SourcePath: source.Path}); err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
	}
	if got := len(store.preservedCopyNames()); got != defaultRecoveryRetention {
		t.Fatalf("preserved copies = %d, want the bounded %d", got, defaultRecoveryRetention)
	}
	if got := len(store.recoveryCopyNames()); got != writeCopies {
		t.Fatalf("restores changed the write-copy count from %d to %d", writeCopies, got)
	}
}

// TestARacedRestoreRefusesWithoutTouchingTheRegistry is acceptance 3's race
// row. Each case is a different thing moving underneath the operator.
func TestARacedRestoreRefusesWithoutTouchingTheRegistry(t *testing.T) {
	t.Parallel()

	newStore := func(t *testing.T) (*Store, RecoveryInspection, RecoverySource) {
		t.Helper()
		store, _ := seedRecoveryStore(t, 2)
		inspection, err := store.InspectRecovery()
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		return store, inspection, inspection.EligibleSources()[0]
	}

	t.Run("stale source checksum", func(t *testing.T) {
		t.Parallel()
		store, inspection, source := newStore(t)
		before := readFile(t, store.Path())
		_, err := store.RestoreFrom(RestoreRequest{
			SourcePath:           source.Path,
			ExpectSourceChecksum: checksumOf([]byte("some other planned content")),
		})
		if !errors.Is(err, ErrRecoveryRaced) {
			t.Fatalf("error = %v, want ErrRecoveryRaced", err)
		}
		if got := readFile(t, store.Path()); got != before {
			t.Fatalf("a raced restore replaced the registry")
		}
		if !strings.Contains(err.Error(), "re-run the preview") {
			t.Fatalf("error %q does not tell the operator what to do", err)
		}
		_ = inspection
	})

	t.Run("stale current checksum", func(t *testing.T) {
		t.Parallel()
		store, _, source := newStore(t)
		before := readFile(t, store.Path())
		_, err := store.RestoreFrom(RestoreRequest{
			SourcePath:            source.Path,
			ExpectCurrentChecksum: checksumOf([]byte("the registry the operator planned against")),
		})
		if !errors.Is(err, ErrRecoveryRaced) {
			t.Fatalf("error = %v, want ErrRecoveryRaced", err)
		}
		if got := readFile(t, store.Path()); got != before {
			t.Fatalf("a raced restore replaced the registry")
		}
	})

	t.Run("current registry replaced under the lock", func(t *testing.T) {
		t.Parallel()
		store, inspection, source := newStore(t)
		// syncDir runs after the source and current bytes are verified and
		// after the copy is staged, and before the only rename. Replacing the
		// registry there is exactly the window the final guard exists for.
		injected := `{"apiVersion":"projmux.io/v1alpha1","schemaVersion":1,"updatedAt":"2026-08-18T00:00:00Z"}` + "\n"
		store.hooks.syncDir = func(string) error {
			writeRegistryFile(t, store, injected)
			store.hooks.syncDir = nil
			return nil
		}
		_, err := store.RestoreFrom(RestoreRequest{
			SourcePath:            source.Path,
			ExpectCurrentChecksum: inspection.Current.Checksum,
		})
		if !errors.Is(err, ErrRecoveryRaced) {
			t.Fatalf("error = %v, want ErrRecoveryRaced", err)
		}
		if got := readFile(t, store.Path()); got != injected {
			t.Fatalf("the raced restore published over the concurrent write")
		}
		if names := store.preservedCopyNames(); len(names) != 0 {
			t.Fatalf("the refused restore left preserved copies behind: %v", names)
		}
		if leaked := stagedTempFiles(t, filepath.Dir(store.Path())); len(leaked) != 0 {
			t.Fatalf("the refused restore leaked staged files: %v", leaked)
		}
	})

	t.Run("source rewritten under the lock", func(t *testing.T) {
		t.Parallel()
		store, _, source := newStore(t)
		before := readFile(t, store.Path())
		store.hooks.syncDir = func(string) error {
			if err := os.WriteFile(source.Path, []byte("{ rewritten"), 0o600); err != nil {
				return err
			}
			store.hooks.syncDir = nil
			return nil
		}
		_, err := store.RestoreFrom(RestoreRequest{SourcePath: source.Path})
		if !errors.Is(err, ErrRecoveryRaced) {
			t.Fatalf("error = %v, want ErrRecoveryRaced", err)
		}
		if got := readFile(t, store.Path()); got != before {
			t.Fatalf("a source rewritten mid-restore was still published")
		}
		if names := store.preservedCopyNames(); len(names) != 0 {
			t.Fatalf("the refused restore left preserved copies behind: %v", names)
		}
	})
}

// TestARestoreRefusesToPublishTheLiveRegistryOverItself keeps the operation from
// degenerating into a self-copy that would preserve the registry as its own
// backup and report a change that never happened.
func TestARestoreRefusesToPublishTheLiveRegistryOverItself(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 1)
	if _, err := store.RestoreFrom(RestoreRequest{SourcePath: store.Path()}); !errors.Is(err, ErrRecoverySourceRejected) {
		t.Fatalf("error = %v, want ErrRecoverySourceRejected", err)
	}
	if _, err := store.InspectExplicitSource(store.Path()); !errors.Is(err, ErrRecoverySourceRejected) {
		t.Fatalf("inspect error = %v, want ErrRecoverySourceRejected", err)
	}
	if _, err := store.RestoreFrom(RestoreRequest{SourcePath: "recovery/registry.json"}); err == nil {
		t.Fatalf("a relative source path was accepted")
	}
}

// TestSelectSourceNeverBreaksATie is the ambiguity row of acceptance 3.
func TestSelectSourceNeverBreaksATie(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 3)
	inspection, err := store.InspectRecovery()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	exact := inspection.Sources[0].Name

	if got, err := inspection.SelectSource(exact); err != nil || got.Name != exact {
		t.Fatalf("exact name select = %q, %v", got.Name, err)
	}
	// A fragment that is unique selects; the whole shared stamp does not.
	stamp, seq, ok := parseRecoveryName(strings.TrimPrefix(exact, ""))
	if !ok {
		t.Fatalf("unparsable candidate name %q", exact)
	}
	if got, err := inspection.SelectSource(fmt.Sprintf("%s-%02d", stamp, seq)); err != nil || got.Name != exact {
		t.Fatalf("unique fragment select = %q, %v", got.Name, err)
	}
	_, err = inspection.SelectSource(stamp)
	if !errors.Is(err, ErrRecoverySourceAmbiguous) {
		t.Fatalf("shared stamp error = %v, want ErrRecoverySourceAmbiguous", err)
	}
	if !strings.Contains(err.Error(), "name one exactly") {
		t.Fatalf("ambiguity error %q is not actionable", err)
	}
	if _, err := inspection.SelectSource("no-such-copy"); !errors.Is(err, ErrRecoverySourceNotFound) {
		t.Fatalf("missing selector error = %v, want ErrRecoverySourceNotFound", err)
	}
	if _, err := inspection.SelectSource("  "); !errors.Is(err, ErrRecoverySourceNotFound) {
		t.Fatalf("blank selector error = %v, want ErrRecoverySourceNotFound", err)
	}
}

// TestAnExplicitPathSourceGetsTheSameVerification proves the operator escape
// hatch is not a weaker path.
func TestAnExplicitPathSourceGetsTheSameVerification(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 1)
	valid := readFile(t, store.Path())
	outside := filepath.Join(t.TempDir(), "carried-from-another-machine.json")
	if err := os.WriteFile(outside, []byte(valid), 0o600); err != nil {
		t.Fatalf("write outside source: %v", err)
	}

	source, err := store.InspectExplicitSource(outside)
	if err != nil {
		t.Fatalf("inspect explicit: %v", err)
	}
	if !source.Eligible || source.Kind != RecoverySourceExplicitPath {
		t.Fatalf("explicit source = %+v", source)
	}
	if source.Checksum != checksumOf([]byte(valid)) {
		t.Fatalf("explicit source checksum does not match its bytes")
	}

	broken := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(broken, []byte("{ nope"), 0o600); err != nil {
		t.Fatalf("write broken source: %v", err)
	}
	rejected, err := store.InspectExplicitSource(broken)
	if err != nil {
		t.Fatalf("inspect broken: %v", err)
	}
	if rejected.Eligible {
		t.Fatalf("a malformed explicit path was offered as restorable")
	}
	if _, err := store.InspectExplicitSource("relative/path.json"); err == nil {
		t.Fatalf("a relative explicit path was accepted")
	}
}

// TestAnOlderSchemaSourceRestoresAndStaysSafeToRead keeps the Phase 0
// old-schema contract intact through recovery: the older bytes are published as
// they are, the safe read migrates them in memory, and the durable migration
// stays a property of the next semantic write rather than a side effect of the
// restore.
func TestAnOlderSchemaSourceRestoresAndStaysSafeToRead(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 1)
	withOlderEnvelopeStep(store)
	name := "registry-20260814T000000Z-00.json"
	writeCopyFile(t, store, name, olderEnvelopeRegistry)

	inspection, err := store.InspectRecovery()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	source := sourceNamed(t, inspection, name)
	if !source.Eligible {
		t.Fatalf("a known older envelope was refused: %s", source.Reason)
	}
	if source.SchemaVersion != 0 {
		t.Fatalf("candidate schemaVersion = %d, want the older 0", source.SchemaVersion)
	}

	if _, err := store.RestoreFrom(RestoreRequest{SourcePath: source.Path, ExpectSourceChecksum: source.Checksum}); err != nil {
		t.Fatalf("restore older schema: %v", err)
	}
	if got := readFile(t, store.Path()); got != olderEnvelopeRegistry {
		t.Fatalf("the restore rewrote the older-schema bytes instead of publishing them verbatim")
	}
	loaded, err := store.LoadReadOnly()
	if err != nil {
		t.Fatalf("safe read after restore: %v", err)
	}
	if loaded.SchemaVersion != coremetadata.SchemaVersion {
		t.Fatalf("in-memory migration did not run: schemaVersion = %d", loaded.SchemaVersion)
	}
	// The durable migration still belongs to a write, so the file on disk is
	// untouched by the read.
	if got := readFile(t, store.Path()); got != olderEnvelopeRegistry {
		t.Fatalf("a read after the restore rewrote the registry")
	}
}

// TestASchemaTooNewSourceIsRefusedEvenWhenItIsTheOnlyCandidate is the
// fail-closed row that matters most: with nothing else to restore, the
// temptation is to accept the only file available.
func TestASchemaTooNewSourceIsRefusedEvenWhenItIsTheOnlyCandidate(t *testing.T) {
	t.Parallel()

	store, _ := seedRecoveryStore(t, 1)
	for _, name := range store.recoveryCopyNames() {
		if err := os.Remove(filepath.Join(store.recoveryDir, name)); err != nil {
			t.Fatalf("clear copies: %v", err)
		}
	}
	name := "registry-20261231T235959Z-00.json"
	writeCopyFile(t, store, name, newerSchemaRegistry)
	if err := os.Remove(store.Path()); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}

	inspection, err := store.InspectRecovery()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	source := sourceNamed(t, inspection, name)
	if source.State != RegistryStateSchemaTooNew || source.Eligible {
		t.Fatalf("newer envelope = %q eligible=%t", source.State, source.Eligible)
	}
	if len(inspection.EligibleSources()) != 0 {
		t.Fatalf("a newer envelope was counted as an eligible source")
	}
	if _, err := store.RestoreFrom(RestoreRequest{SourcePath: source.Path}); !errors.Is(err, ErrRecoverySourceRejected) {
		t.Fatalf("error = %v, want ErrRecoverySourceRejected", err)
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused restore created a registry: %v", err)
	}
}

// TestRestoringIntoAFirstUseStateDirEstablishesTheBoundary covers an operator
// recovering onto a machine that has no marker yet, which is what carrying a
// copy to a fresh install looks like.
func TestRestoringIntoAFirstUseStateDirEstablishesTheBoundary(t *testing.T) {
	t.Parallel()

	seeded, _ := seedRecoveryStore(t, 1)
	carried := filepath.Join(t.TempDir(), "carried.json")
	if err := os.WriteFile(carried, []byte(readFile(t, seeded.Path())), 0o600); err != nil {
		t.Fatalf("write carried copy: %v", err)
	}

	fresh := testStore(t)
	if _, err := fresh.RestoreFrom(RestoreRequest{SourcePath: carried}); err != nil {
		t.Fatalf("restore into a first-use state dir: %v", err)
	}
	if _, err := os.Stat(fresh.markerPath); err != nil {
		t.Fatalf("restore did not establish the initialized boundary: %v", err)
	}
	loaded, err := fresh.LoadReadOnly()
	if err != nil {
		t.Fatalf("read after restore: %v", err)
	}
	if len(loaded.Projects) != 1 {
		t.Fatalf("restored projects = %d", len(loaded.Projects))
	}
	// Losing the registry again must now read as state loss rather than as a
	// fresh first use, which is the boundary the restore just established.
	if err := os.Remove(fresh.Path()); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}
	if _, err := fresh.LoadReadOnly(); !errors.Is(err, ErrRegistryStateLost) {
		t.Fatalf("read after loss = %v, want ErrRegistryStateLost", err)
	}
}

// TestAFailedRestoreLeavesTheRegistryByteIdentical walks the failure injection
// points of the publish sequence, the same shape as the write-path matrix.
func TestAFailedRestoreLeavesTheRegistryByteIdentical(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		hook func(*Store)
	}{
		{name: "staged fsync", hook: func(s *Store) {
			s.hooks.syncFile = func(*os.File) error { return errors.New("injected fsync failure") }
		}},
		{name: "directory sync", hook: func(s *Store) {
			s.hooks.syncDir = func(string) error { return errors.New("injected dir sync failure") }
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := seedRecoveryStore(t, 2)
			inspection, err := store.InspectRecovery()
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			source := inspection.EligibleSources()[0]
			before := readFile(t, store.Path())
			markerBefore := readFile(t, store.markerPath)
			tt.hook(store)

			if _, err := store.RestoreFrom(RestoreRequest{SourcePath: source.Path}); err == nil {
				t.Fatalf("the injected failure did not fail the restore")
			}
			if got := readFile(t, store.Path()); got != before {
				t.Fatalf("a failed restore replaced the registry")
			}
			if got := readFile(t, store.markerPath); got != markerBefore {
				t.Fatalf("a failed restore rewrote the marker")
			}
			if names := store.preservedCopyNames(); len(names) != 0 {
				t.Fatalf("a failed restore left preserved copies behind: %v", names)
			}
			if leaked := stagedTempFiles(t, filepath.Dir(store.Path())); len(leaked) != 0 {
				t.Fatalf("a failed restore leaked staged files: %v", leaked)
			}
			if _, err := os.Stat(store.lockPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a failed restore left the lock behind: %v", err)
			}
		})
	}
}

func mutateRegistryJSON(t *testing.T, contents string, mutate func(map[string]any)) string {
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

func stagedTempFiles(t *testing.T, dirs ...string) []string {
	t.Helper()
	var leaked []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".tmp-") {
				leaked = append(leaked, filepath.Join(dir, entry.Name()))
			}
			if entry.IsDir() {
				leaked = append(leaked, stagedTempFiles(t, filepath.Join(dir, entry.Name()))...)
			}
		}
	}
	return leaked
}

func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func equalStrings(a, b []string) bool {
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
