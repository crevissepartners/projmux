package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixtureEvent(id string) Event {
	return Event{At: "2026-08-12T00:00:00Z", Level: "info", Component: "cli", Event: "command.outcome", Result: "success", DurationMS: 1, RunID: id, Version: "0.8.4", MuxBackend: "tmux", Command: "pin", Subcommand: "add"}
}

func TestDefaultPath(t *testing.T) {
	t.Parallel()
	path, err := DefaultPath(func(string) string { return "/state" }, func() (string, error) { return "/home/test", nil })
	if err != nil || path != filepath.Join("/state", "projmux", "logs", "operations.jsonl") {
		t.Fatalf("DefaultPath() = %q, %v", path, err)
	}
	path, err = DefaultPath(func(string) string { return "" }, func() (string, error) { return "/home/test", nil })
	if err != nil || path != filepath.Join("/home/test", ".local", "state", "projmux", "logs", "operations.jsonl") {
		t.Fatalf("fallback DefaultPath() = %q, %v", path, err)
	}
}

func TestStoreRepairsPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission modes are not enforced on Windows")
	}
	path := filepath.Join(t.TempDir(), "projmux", "logs", LogFileName)
	store := NewStore(path)
	if err := store.Append(fixtureEvent("first")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(filepath.Dir(path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(fixtureEvent("second")); err != nil {
		t.Fatal(err)
	}
	dirInfo, _ := os.Stat(filepath.Dir(path))
	stateInfo, _ := os.Stat(filepath.Dir(filepath.Dir(path)))
	fileInfo, _ := os.Stat(path)
	if got := stateInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("state directory mode = %o", got)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o", got)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o", got)
	}
}

func TestStoreRefusesSymlinkWithoutChangingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "outside.jsonl")
	if err := os.WriteFile(target, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(root, "projmux", "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logs, LogFileName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if err := store.Append(fixtureEvent("unsafe")); err == nil {
		t.Fatal("expected symlink refusal")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "outside\n" {
		t.Fatalf("target data = %q, err = %v", data, err)
	}
	info, _ := os.Stat(target)
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("external target mode = %o", got)
	}
}

func TestStoreTrimRetainsRecentCompleteValidRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(fixtureEvent("old"))
	record = append(record, '\n')
	seed := bytes.Repeat(record, MaxLogSize/len(record)+20)
	seed = append(seed, []byte("corrupt\n{\"truncated\"")...)
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if err := store.Append(fixtureEvent("newest")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > RetainLogSize+len(record)*2 {
		t.Fatalf("trimmed size = %d, want near <= %d", len(data), RetainLogSize)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("trimmed log does not end with a complete record")
	}
	for lineNo, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
		if !json.Valid(line) {
			t.Fatalf("line %d is malformed: %q", lineNo, line)
		}
	}
	events, err := NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := events[len(events)-1].RunID; got != "newest" {
		t.Fatalf("last run id = %q", got)
	}
}

func TestStoreReadSkipsMalformedAndTruncatedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), LogFileName)
	valid, _ := json.Marshal(fixtureEvent("valid"))
	data := append(append(valid, '\n'), []byte("bad\n{\"at\":")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RunID != "valid" {
		t.Fatalf("events = %#v", events)
	}
}

func TestStoreReadOnlySharesDecoderWithoutRepairingSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission modes are not enforced on Windows")
	}
	root := t.TempDir()
	logs := filepath.Join(root, "projmux", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logs, LogFileName)
	valid, _ := json.Marshal(fixtureEvent("strict-read"))
	if err := os.WriteFile(path, append(append(valid, '\n'), []byte("malformed\n{\"truncated\"")...), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeDir, _ := os.Stat(logs)
	beforeFile, _ := os.Stat(path)

	result, err := NewStore(path).ReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].RunID != "strict-read" || result.Malformed != 1 || !result.Truncated || result.Missing {
		t.Fatalf("ReadOnly result = %#v", result)
	}
	afterDir, _ := os.Stat(logs)
	afterFile, _ := os.Stat(path)
	if afterDir.Mode().Perm() != beforeDir.Mode().Perm() || afterFile.Mode().Perm() != beforeFile.Mode().Perm() {
		t.Fatalf("ReadOnly changed modes dir %o->%o file %o->%o", beforeDir.Mode().Perm(), afterDir.Mode().Perm(), beforeFile.Mode().Perm(), afterFile.Mode().Perm())
	}
	if _, err := os.Stat(path + lockSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadOnly lock stat = %v, want missing", err)
	}
}

func TestStoreConcurrentGoroutineWritersKeepCompleteRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	store := NewStore(path)
	var wg sync.WaitGroup
	for worker := range 12 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for record := range 30 {
				if err := store.Append(fixtureEvent(fmt.Sprintf("g-%d-%d", worker, record))); err != nil {
					t.Errorf("Append: %v", err)
				}
			}
		}(worker)
	}
	wg.Wait()
	events, err := NewStore(path).Read()
	if err != nil || len(events) != 360 {
		t.Fatalf("Read() count = %d, err = %v", len(events), err)
	}
}

func TestStoreConcurrentProcessesKeepCompleteRecords(t *testing.T) {
	if os.Getenv("PROJMUX_DIAGNOSTICS_HELPER") == "1" {
		path := os.Getenv("PROJMUX_DIAGNOSTICS_PATH")
		prefix := os.Getenv("PROJMUX_DIAGNOSTICS_PREFIX")
		store := NewStore(path)
		for i := range 80 {
			if err := store.Append(fixtureEvent(prefix + "-" + strconv.Itoa(i))); err != nil {
				os.Exit(3)
			}
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(fixtureEvent("old-before-rotation"))
	old = append(old, '\n')
	if err := os.WriteFile(path, bytes.Repeat(old, MaxLogSize/len(old)+10), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 8)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestStoreConcurrentProcessesKeepCompleteRecords$")
		commands[i].Env = append(os.Environ(), "PROJMUX_DIAGNOSTICS_HELPER=1", "PROJMUX_DIAGNOSTICS_PATH="+path, fmt.Sprintf("PROJMUX_DIAGNOSTICS_PREFIX=p%d", i))
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	events, err := NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, 640)
	for _, event := range events {
		seen[event.RunID] = true
	}
	for process := range 8 {
		for record := range 80 {
			id := fmt.Sprintf("p%d-%d", process, record)
			if !seen[id] {
				t.Fatalf("missing concurrent post-rotation record %q", id)
			}
		}
	}
	data, _ := os.ReadFile(path)
	if len(data) > RetainLogSize+640*len(old) {
		t.Fatalf("concurrent rotated size = %d", len(data))
	}
	for line := range bytes.SplitSeq(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
		if !json.Valid(line) {
			t.Fatalf("malformed concurrent record: %q", line)
		}
	}
}

func TestStoreLockWaitUsesSmallExplicitBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := path + lockSuffix
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := tryPlatformLock(held); err != nil {
		t.Fatal(err)
	}
	defer unlockPlatformLock(held)

	store := NewStore(path)
	if store.lockBudget != lockBudget || store.lockBudget > 250*time.Millisecond {
		t.Fatalf("default lock budget = %s, want %s and <=250ms", store.lockBudget, lockBudget)
	}
	store.lockRetry = 25 * time.Millisecond
	clock := time.Unix(0, 0)
	store.now = func() time.Time { return clock }
	waited := time.Duration(0)
	store.sleep = func(delay time.Duration) {
		waited += delay
		clock = clock.Add(delay)
	}
	realStart := time.Now()
	if err := store.Append(fixtureEvent("blocked")); !errors.Is(err, errLockBusy) {
		t.Fatalf("Append error = %v, want lock busy", err)
	}
	if waited != store.lockBudget {
		t.Fatalf("bounded wait = %s, want %s", waited, store.lockBudget)
	}
	if elapsed := time.Since(realStart); elapsed > 100*time.Millisecond {
		t.Fatalf("injected bounded wait took real time %s", elapsed)
	}
}

func TestStoreOrphanedLockFileDoesNotBlockContenders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := path + lockSuffix
	if err := os.WriteFile(lockPath, []byte("orphaned legacy token"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"contender-a", "contender-b"} {
		go func(id string) {
			<-start
			errs <- NewStore(path).Append(fixtureEvent(id))
		}(id)
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	events, err := NewStore(path).Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %#v, err = %v", events, err)
	}
}

func TestPlatformLockOldOwnerCannotUnlockSuccessor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.lock")
	open := func() *os.File {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	oldOwner := open()
	defer oldOwner.Close()
	successor := open()
	defer successor.Close()
	contender := open()
	defer contender.Close()

	if err := tryPlatformLock(oldOwner); err != nil {
		t.Fatal(err)
	}
	if err := unlockPlatformLock(oldOwner); err != nil {
		t.Fatal(err)
	}
	if err := tryPlatformLock(successor); err != nil {
		t.Fatal(err)
	}
	defer unlockPlatformLock(successor)
	// A delayed/double release from the old handle cannot release the lock held
	// by the successor handle. This was unsafe with path-based lock deletion.
	if err := unlockPlatformLock(oldOwner); err != nil {
		t.Fatal(err)
	}
	if err := tryPlatformLock(contender); !errors.Is(err, errLockBusy) {
		t.Fatalf("contender lock error = %v, want busy", err)
	}
}

func TestRecordOutcomePolicyAndBestEffort(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
	start := time.Now().Add(-time.Millisecond)
	if err := RecordOutcome(store, []string{"internal", "status", "usage"}, "read-ok", "0.8.4", "tmux", start, nil, false, false); err != nil {
		t.Fatal(err)
	}
	if err := RecordOutcome(store, []string{"diagnostics", "log"}, "viewer-ok", "0.8.4", "tmux", start, nil, false, false); err != nil {
		t.Fatal(err)
	}
	readOnlySuccesses := []struct {
		args  []string
		runID string
	}{
		{args: []string{"attach", "--help"}, runID: "help-ok"},
		{args: []string{"update", "apply", "--dry-run"}, runID: "update-preview-ok"},
		{args: []string{"doctor", "--json", "--section", "deps"}, runID: "doctor-read-ok"},
		{args: []string{"diagnostics", "report", "--output", "/private/report"}, runID: "report-read-ok"},
		{args: []string{"agent", "integrate", "codex", "--dry-run"}, runID: "integration-preview-ok"},
		{args: []string{"restore", "snapshot", "--dry-run"}, runID: "restore-preview-ok"},
	}
	for _, success := range readOnlySuccesses {
		if err := RecordOutcome(store, success.args, success.runID, "0.8.4", "tmux", start, nil, false, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecordOutcome(store, []string{"pin", "project", "add", "/secret"}, "write-ok", "0.8.4", "tmux", start, nil, false, false); err != nil {
		t.Fatal(err)
	}
	forbidden := "pane=%42 title=private-topic body=notification-secret transcript=raw-conversation config_secret=hunter2"
	if err := RecordOutcome(store, []string{"internal", "status", "usage"}, "read-error", "0.8.4", "tmux", start, fmt.Errorf("failed: %s", forbidden), false, false); err != nil {
		t.Fatal(err)
	}
	if err := RecordOutcome(store, []string{"doctor", "--install-missing"}, "doctor-usage-error", "0.8.4", "tmux", start, errors.New("removed flag"), true, false); err != nil {
		t.Fatal(err)
	}
	if err := RecordOutcome(store, []string{"diagnostics", "report", "--unknown"}, "report-usage-error", "0.8.4", "tmux", start, errors.New("private output"), true, false); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("events count = %d, err = %v", len(events), err)
	}
	if events[0].RunID != "write-ok" || events[0].Result != "success" || events[0].Message != "" {
		t.Fatalf("success event = %#v", events[0])
	}
	if events[1].RunID != "read-error" || events[1].Result != "error" || events[1].Command != "status" || events[1].Subcommand != "usage" {
		t.Fatalf("error event = %#v", events[1])
	}
	if events[1].Message != "command failed" {
		t.Fatalf("lossy error message = %q", events[1].Message)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for secret := range strings.FieldsSeq(forbidden) {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("operational record leaked forbidden dynamic literal %q", secret)
		}
	}
	blockedPath := filepath.Join(t.TempDir(), "missing", "nested", LogFileName)
	blocked := NewStore(blockedPath)
	if err := os.WriteFile(filepath.Dir(filepath.Dir(blockedPath)), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordOutcome(blocked, []string{"pin", "project", "add"}, "ignored-failure", "0.8.4", "tmux", start, nil, false, false); err == nil {
		t.Fatal("expected injected writer failure")
	}
}

func TestRecordOutcomeAutomaticHookAndPollSuccessZeroErrorOne(t *testing.T) {
	start := time.Now().Add(-time.Millisecond)
	tests := []struct {
		name string
		args []string
	}{
		{name: "agent hook ingest", args: []string{"internal", "agent-hook", "ingest", "codex-hook"}},
		{name: "attention arm", args: []string{"attention", "arm", "%1"}},
		{name: "attention clear", args: []string{"attention", "clear", "%1"}},
		{name: "attention window", args: []string{"attention", "window", "@1"}},
		{name: "session state autosave", args: []string{"internal", "tmux", "autosave-session-state", "--quiet"}},
		{name: "recent window record", args: []string{"window", "record"}},
		{name: "generated controller converge", args: []string{"internal", "tmux", "converge", "--socket-path", "/private/socket", "--session", "$1", "--reason", "runtime-exited"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
			if err := RecordOutcome(store, tt.args, "automatic-success", "0.8.4", "tmux", start, nil, false, false); err != nil {
				t.Fatal(err)
			}
			events, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 0 {
				t.Fatalf("successful automatic path appended %d events, want 0: %#v", len(events), events)
			}

			if err := RecordOutcome(store, tt.args, "automatic-error", "0.8.4", "tmux", start, errors.New("private hook payload"), false, false); err != nil {
				t.Fatal(err)
			}
			events, err = store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("failed automatic path appended %d events, want 1: %#v", len(events), events)
			}
			if events[0].Result != "error" || events[0].Level != "error" || events[0].Kind != "runtime" || events[0].Message != "command failed" {
				t.Fatalf("failed automatic path event = %#v, want one safe runtime error", events[0])
			}
			class := Classify(tt.args)
			if events[0].Command != class.Command || events[0].Subcommand != class.Subcommand {
				t.Fatalf("failed automatic path classification = %#v, event = %#v", class, events[0])
			}
		})
	}
}

func TestRecordOutcomeExplicitMutationSuccessStillAppendsOne(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
	if err := RecordOutcome(store, []string{"attention", "toggle", "%1"}, "explicit-success", "0.8.4", "tmux", time.Now(), nil, false, false); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RunID != "explicit-success" || events[0].Result != "success" || events[0].Command != "attention" || events[0].Subcommand != "toggle" {
		t.Fatalf("explicit mutation events = %#v, want one attention toggle success", events)
	}
}

type outcomeExitError struct{ code int }

func (e outcomeExitError) Error() string { return "target unavailable" }
func (e outcomeExitError) ExitCode() int { return e.code }

func TestRecordOutcomeClassifiesUsageAndExitErrorsOnce(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
	start := time.Now()
	if err := RecordOutcome(store, []string{"internal", "status", "usage"}, "usage-run", "0.8.4", "tmux", start, errors.New("bad flag"), true, false); err != nil {
		t.Fatal(err)
	}
	if err := RecordOutcome(store, []string{"focus", "project", "secret"}, "exit-run", "0.8.4", "tmux", start, outcomeExitError{code: 2}, false, false); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %#v, err = %v", events, err)
	}
	if events[0].Kind != "usage" || events[1].Kind != "exit" {
		t.Fatalf("error kinds = %q, %q", events[0].Kind, events[1].Kind)
	}
}

func TestRecordOutcomeRetiredCLIIsStrictlyNoWrite(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "logs", LogFileName))
	for _, args := range [][]string{
		{"ai", "ingest", "log"},
		{"ai", "ingest", "antigravity-hook", "--event", "FutureEvent"},
		{"attach", "auto"},
		{"current"},
		{"focus", "--target", "secret"},
		{"notify", "push"},
		{"pin", "add", "/secret"},
		{"prune", "ephemeral"},
		{"session-state", "save"},
		{"tmux", "apply"},
	} {
		if err := RecordOutcome(store, args, "retired", "0.8.4", "tmux", time.Now(), errors.New("removed"), true, false); err != nil {
			t.Fatalf("RecordOutcome(%q): %v", args, err)
		}
	}
	if _, err := os.Stat(store.path); !os.IsNotExist(err) {
		t.Fatalf("retired argv created diagnostics state: %v", err)
	}
}
