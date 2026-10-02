package processhost

import (
	"bufio"
	"context"
	"encoding/json"
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
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("group")})
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

func TestOwnerLifetimeReclaimsOnlyOwnedGroup(t *testing.T) {
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
