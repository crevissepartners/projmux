package processhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixtureOwner() {
	tx := Transactions{Reserve: func(context.Context, Binding) error { return nil }, Commit: func(context.Context, Binding, string) error { return nil }, Current: func(context.Context, Binding) error { return nil }}
	limits := DefaultLimits()
	limits.Grace = 200 * time.Millisecond
	h, err := NewHost("host", Command{Path: os.Args[0], Args: []string{"processhost-supervisor"}, Env: os.Environ()}, tx, limits)
	if err != nil {
		panic(err)
	}
	var p *Handle
	if os.Getenv("PROCESSHOST_OWNER_CODEX") == "1" {
		var owned *CodexHandle
		owned, err = h.StartCodex(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("codex-group")}, CodexConfig{})
		if owned != nil {
			p = owned.handle
		}
	} else {
		p, err = h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("group")})
	}
	if err != nil {
		panic(err)
	}
	s, _ := p.Observe(binding())
	_ = json.NewEncoder(os.Stdout).Encode(s)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan() // normal input or owner EOF
	_ = p.Stop(binding())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := p.Wait(ctx, binding()); err != nil {
		panic(err)
	}
}

func TestOwnerLifetimeReclaimsOnlyOwnedGroup(t *testing.T)      { testOwnerLifetime(t, false) }
func TestCodexOwnerLifetimeReclaimsOnlyOwnedGroup(t *testing.T) { testOwnerLifetime(t, true) }

func testOwnerLifetime(t *testing.T, codex bool) {
	// Linux: this test process reaps its orphaned supervisor after killing the
	// owner, emulating init without relying on a container PID1's zombie policy.
	if err := prepareReaper(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"normal", "EOF", "TERM", "KILL"} {
		t.Run(mode, func(t *testing.T) {
			sibling := exec.Command(os.Args[0], "processhost-leaf")
			if err := sibling.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
			leafPath := filepath.Join(t.TempDir(), "leaf")
			owner := exec.Command(os.Args[0], "processhost-owner")
			owner.Env = append(os.Environ(), "PROCESSHOST_LEAF_FILE="+leafPath)
			if codex {
				owner.Env = append(owner.Env, "PROCESSHOST_OWNER_CODEX=1")
			}
			stdin, err := owner.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := owner.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic strings.Builder
			owner.Stderr = &diagnostic
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			var s Snapshot
			if err := json.NewDecoder(stdout).Decode(&s); err != nil {
				_ = owner.Process.Kill()
				_ = owner.Wait()
				t.Fatalf("owner: %v %s", err, diagnostic.String())
			}
			deadline := time.Now().Add(3 * time.Second)
			leaf := 0
			for time.Now().Before(deadline) {
				raw, _ := os.ReadFile(leafPath)
				leaf, _ = strconv.Atoi(string(raw))
				if leaf > 0 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if leaf <= 0 {
				_ = owner.Process.Kill()
				_ = owner.Wait()
				t.Fatal("leaf not started")
			}
			began := time.Now()
			switch mode {
			case "normal":
				_, _ = fmt.Fprintln(stdin, "stop")
			case "EOF":
				_ = stdin.Close()
			case "TERM":
				_ = owner.Process.Signal(syscall.SIGTERM)
			case "KILL":
				_ = owner.Process.Kill()
			}
			_ = owner.Wait()
			// The supervisor exits after its own real child/group Wait. Reap only this
			// known descendant (never a discovered provider or sibling).
			deadline = time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) {
				var status syscall.WaitStatus
				_, _ = syscall.Wait4(s.SupervisorPID, &status, syscall.WNOHANG, nil)
				if syscall.Kill(s.SupervisorPID, 0) == syscall.ESRCH && syscall.Kill(s.PID, 0) == syscall.ESRCH && syscall.Kill(leaf, 0) == syscall.ESRCH {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			for _, pid := range []int{s.SupervisorPID, s.PID, leaf} {
				if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
					t.Errorf("%s residual owned pid %d: %v (%s)", mode, pid, err, diagnostic.String())
				}
			}
			if err := sibling.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("sibling damaged: %v", err)
			}
			t.Logf("%s: owned supervisor/child/descendant absent; sibling alive; cleanup=%s", mode, time.Since(began))
		})
	}
}

func TestExitObservationRetainsOwnedGroupUntilWait(t *testing.T) {
	if err := prepareReaper(); err != nil {
		t.Fatal(err)
	}
	leafFile := filepath.Join(t.TempDir(), "leaf")
	cmd := exec.Command(os.Args[0], "processhost-provider", "exit-with-leaf")
	cmd.Env = append(os.Environ(), "PROCESSHOST_LEAF_FILE="+leafFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			_ = reapGroup(cmd.Process.Pid, time.Second)
		}
	})
	observed := make(chan error, 1)
	go func() { observed <- observeChildExit(cmd.Process.Pid) }()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit observation timeout")
	}
	// A second successful observation proves the exact child is still waitable.
	// kill(0) on a zombie-only group is not a portable identity check (Darwin
	// returns EPERM). Keep a real descendant alive to test the group signal.
	if err := observeChildExit(cmd.Process.Pid); err != nil || cmd.ProcessState != nil {
		t.Fatalf("child was reaped before group cleanup: %v", err)
	}
	raw, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if group, err := syscall.Getpgid(leaf); err != nil || group != cmd.Process.Pid {
		t.Fatalf("descendant not in exact owned group: %d %v", group, err)
	}
	if err := syscall.Kill(leaf, 0); err != nil {
		t.Fatalf("descendant not alive before product cleanup: %v", err)
	}
	// Call the product finalizer: signal the held group, actually Wait the
	// leader, then verify descendant reaping. The leader's status stays exit0.
	exit, err := finishOwnedChild(cmd, nil, time.Second)
	if err != nil || exit.Code != 0 || exit.Signal != "" || cmd.ProcessState == nil {
		t.Fatalf("lost real Wait status: %+v %v", exit, err)
	}
	if err := syscall.Kill(leaf, 0); err != syscall.ESRCH {
		t.Fatalf("descendant survived product cleanup: %v", err)
	}
	t.Logf("child %d observed twice while waitable; descendant %d in owned group killed/reaped by product finalizer; actual Wait exit0", cmd.Process.Pid, leaf)
}

func TestFailedExitObservationGrantsNoCleanupAuthority(t *testing.T) {
	cmd := exec.Command(os.Args[0], "processhost-leaf")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for _, failure := range []error{syscall.ENOSYS, syscall.ECHILD} {
		observed := make(chan error, 1)
		observed <- failure
		if err := stopGroup(cmd.Process.Pid, time.Second, observed); !errors.Is(err, failure) {
			t.Fatalf("cancellation ignored failed observation: %v", err)
		}
		if _, err := finishOwnedChild(cmd, failure, time.Second); !errors.Is(err, failure) {
			t.Fatalf("observation failure lost: %v", err)
		}
		if cmd.ProcessState != nil {
			t.Fatal("failed observation called Wait")
		}
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("failed observation signalled saved PID/group: %v", err)
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	// A genuine, already-consumed child status cannot restore signal authority.
	if err := observeChildExit(cmd.Process.Pid); err == nil {
		t.Fatal("already reaped child observed as owned")
	} else if _, got := finishOwnedChild(nil, err, time.Second); got == nil {
		t.Fatal("lost ownership yielded an exit receipt")
	}
	// Even a present process is not evidence of an owned child.
	if err := observeChildExit(os.Getpid()); err == nil {
		t.Fatal("non-child PID granted ownership")
	}
}

func TestEscapedDescendantCannotHoldWait(t *testing.T) {
	if err := prepareReaper(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"escaped-stdout", "escaped-stderr", "escaped-both"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			leafFile, release := filepath.Join(dir, "leaf"), filepath.Join(dir, "release")
			command := fixtureCommand(mode)
			command.Env = append(command.Env, "PROCESSHOST_LEAF_FILE="+leafFile, "PROCESSHOST_RELEASE_FILE="+release)
			p, err := testHost(t, nil).Start(context.Background(), Launch{Binding: binding(), Command: command})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// Only the fixture's explicit release marker ends the escaped leaf.
				// The product never adopts or signals its stored PID.
				_ = os.WriteFile(release, nil, 0600)
				_ = p.Stop(binding())
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = p.Wait(ctx, binding())
				raw, err := os.ReadFile(leafFile)
				if err != nil {
					t.Error(err)
					return
				}
				leaf, err := strconv.Atoi(string(raw))
				if err != nil {
					t.Error(err)
					return
				}
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					var status syscall.WaitStatus
					_, _ = syscall.Wait4(leaf, &status, syscall.WNOHANG, nil)
					if syscall.Kill(leaf, 0) == syscall.ESRCH {
						return
					}
					time.Sleep(time.Millisecond)
				}
				t.Error("fixture leaf did not exit after release")
			})
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			s, err := p.Wait(ctx, binding())
			if err != nil || s.State != "exited" || s.Exit == nil || s.Exit.Code != 0 || s.Exit.Signal != "" || s.Failure != "" {
				t.Fatalf("output retained actual Wait: %+v %v", s, err)
			}
			raw, err := os.ReadFile(leafFile)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if group, err := syscall.Getpgid(leaf); err != nil || group != leaf || group == s.PID {
				t.Fatalf("escaped leaf not independently alive: group=%d err=%v", group, err)
			}
			if !hasEvent(p, "stream-gap") || !hasEvent(p, "process-exited") {
				t.Fatal("truncation or actual exit was hidden")
			}
			events, _, _ := p.Events(binding(), 0)
			var gap, exited uint64
			for _, event := range events {
				if event.Kind == "stream-gap" {
					gap = event.Sequence
				}
				if event.Kind == "process-exited" {
					exited = event.Sequence
				}
			}
			if gap == 0 || gap >= exited {
				t.Fatalf("gap does not precede actual exit: %d >= %d", gap, exited)
			}
			t.Logf("%s: actual Wait0 returned while escaped leaf %d remained alive; gap explicit; fixture release follows assertion", mode, leaf)
		})
	}
}

func TestNormalExitDoesNotWaitForDrainDeadline(t *testing.T) {
	p := start(t, testHost(t, func(_ *Transactions, limits *Limits) { limits.Grace = 4 * time.Second }), "exit0")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := p.Wait(ctx, binding())
	if err != nil || s.Exit == nil || s.Exit.Code != 0 || s.Failure != "" || hasEvent(p, "protocol-error") || hasEvent(p, "stream-gap") {
		t.Fatalf("normal exit delayed/misclassified: %+v %v", s, err)
	}
}

func TestStopAllowsSessionEnd(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "session-end")
	t.Setenv("PROCESSHOST_SESSION_END_FILE", marker)
	p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Grace = 2 * time.Second }), "session-end")
	turn(t, p, "q", "question")
	observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
	stale := binding()
	stale.Generation = "old"
	if err := p.Stop(stale); err != ErrStale {
		t.Fatalf("stale Stop: %v", err)
	}
	began := time.Now()
	for range 2 {
		if err := p.Stop(binding()); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := p.Observe(binding())
	if len(s.Pending) != 0 {
		t.Fatal("Stop retained pending control")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	s, err := p.Wait(ctx, binding())
	if err != nil {
		t.Fatal(err)
	}
	raw, markerErr := os.ReadFile(marker)
	evidence, ok := s.Termination(time.Now())
	t.Logf("Stop elapsed=%s marker=%q markerErr=%v state=%s exit=%+v classification=%s", time.Since(began), raw, markerErr, s.State, s.Exit, evidence.Classification)
	if markerErr != nil || string(raw) != "complete" || s.State != "exited" || s.Exit == nil || s.Exit.Code != 0 || s.Exit.Signal != "" || !ok || string(evidence.Classification) != "normal" {
		t.Fatalf("SessionEnd did not finish normally: %+v", s)
	}
	if err := p.Stop(binding()); err != nil {
		t.Fatal(err)
	}
}

func TestStopEscalatesUnresponsiveProvider(t *testing.T) {
	for _, tc := range []struct {
		mode, signal string
		stages       int
	}{
		{"ignore-eof", "TERM", 1},
		{"ignore-eof-term", "KILL", 2},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			const grace = 200 * time.Millisecond
			p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Grace = grace }), tc.mode)
			turn(t, p, "ready", "normal")
			observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
			began := time.Now()
			if err := p.Stop(binding()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := p.Wait(ctx, binding())
			elapsed := time.Since(began)
			if err != nil || s.State != "exited" || s.Exit == nil || s.Exit.Signal != tc.signal || elapsed < time.Duration(tc.stages)*grace {
				t.Fatalf("escalation elapsed=%s snapshot=%+v exit=%+v err=%v", elapsed, s, s.Exit, err)
			}
			t.Logf("bounded escalation: elapsed=%s actual Wait=%+v", elapsed, s.Exit)
		})
	}
}

func TestCompletionRunsBeforeWaitAndPreservesExit(t *testing.T) {
	h := testHost(t, func(_ *Transactions, limits *Limits) { limits.Write = 5 * time.Second })
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	completion := &Completion{Cleanup: func(ctx context.Context) error {
		// Both locks must be available to cleanup; actual exit evidence is already
		// visible, while Wait must still be fenced behind completion.
		h.mu.Lock()
		owned := h.operations[binding().Operation]
		h.mu.Unlock()
		snapshot, err := owned.Observe(binding())
		if err != nil || snapshot.Exit == nil {
			return fmt.Errorf("cleanup lacks actual exit: %+v %v", snapshot, err)
		}
		calls++
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	launch := Launch{Binding: binding(), Command: fixtureCommand("normal"), Completion: completion}
	p, err := h.Start(context.Background(), launch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = p.Stop(binding())
	})
	if err = p.Stop(binding()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("cleanup did not enter")
	}
	select {
	case <-p.done:
		t.Fatal("Wait opened before cleanup finished")
	default:
	}
	close(release)
	s, err := p.Wait(ctx, binding())
	if err != nil || s.Exit == nil || s.Exit.Code != 0 || s.Failure != "" || s.Diagnostic != "" {
		t.Fatalf("completion changed actual exit: %+v %v", s, err)
	}
	again, err := h.Start(context.Background(), launch)
	if err != nil || again != p || calls != 1 {
		t.Fatalf("same cleanup owner retried: %p %v calls=%d", again, err, calls)
	}
	launch.Completion = &Completion{Cleanup: completion.Cleanup}
	if _, err := h.Start(context.Background(), launch); err != ErrStale {
		t.Fatalf("changed cleanup owner: %v", err)
	}
}

func TestCompletionFailureAndTimeoutAreDiagnosticOnly(t *testing.T) {
	for _, mode := range []string{"failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			h := testHost(t, nil)
			release, returned := make(chan struct{}), make(chan struct{})
			completion := &Completion{Cleanup: func(context.Context) error {
				defer close(returned)
				if mode == "timeout" {
					<-release
				}
				return errors.New("cleanup fixture failure")
			}}
			p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal"), Completion: completion})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { close(release); <-returned })
			if err = p.Stop(binding()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := p.Wait(ctx, binding())
			if err != nil || s.Exit == nil || s.Exit.Code != 0 || s.Failure != "" || s.State != "exited" {
				t.Fatalf("cleanup overwrote actual exit: %+v %v", s, err)
			}
			expected := "cleanup fixture failure"
			if mode == "timeout" {
				expected = "context deadline exceeded"
			}
			if !strings.Contains(s.Diagnostic, "owned completion cleanup: "+expected) {
				t.Fatalf("cleanup diagnostic: %q", s.Diagnostic)
			}
		})
	}
}

func TestCompletionAlsoReclaimsLaunchRollback(t *testing.T) {
	calls := 0
	reserveErr := errors.New("reservation fixture failure")
	h := testHost(t, func(tx *Transactions, _ *Limits) {
		tx.Reserve = func(context.Context, Binding) error { return reserveErr }
	})
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal"), Completion: &Completion{Cleanup: func(context.Context) error { calls++; return nil }}})
	if !errors.Is(err, reserveErr) || p == nil || calls != 1 {
		t.Fatalf("rollback cleanup: %v calls=%d", err, calls)
	}
	s, err := p.Wait(context.Background(), binding())
	if err != nil || s.Exit != nil || s.Failure != reserveErr.Error() {
		t.Fatalf("rollback fabricated exit: %+v %v", s, err)
	}
}
