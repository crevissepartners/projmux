package processhost

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// procParent reports a live or zombie process's parent and state; ok is false
// once the PID is gone.
func procParent(pid int) (parent int, state string, ok bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, "", false
	}
	end := strings.LastIndexByte(string(raw), ')')
	fields := strings.Fields(string(raw[end+1:]))
	parent, _ = strconv.Atoi(fields[1])
	return parent, fields[0], true
}

func TestSupervisorReapsOrphansOutsideProviderGroup(t *testing.T) {
	const orphans = 50
	dir := t.TempDir()
	leafFile, release := filepath.Join(dir, "orphans"), filepath.Join(dir, "release")
	t.Setenv("PROCESSHOST_ORPHAN_COUNT", strconv.Itoa(orphans))
	t.Setenv("PROCESSHOST_LEAF_FILE", leafFile)
	t.Setenv("PROCESSHOST_RELEASE_FILE", release)
	h := testHost(t, nil)
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("orphans")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.WriteFile(release, nil, 0600) }()
	s := observeUntil(t, p, func(s Snapshot) bool { return s.SupervisorPID > 0 && s.PID > 0 })

	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for len(pids) < orphans && time.Now().Before(deadline) {
		raw, _ := os.ReadFile(leafFile)
		pids = pids[:0]
		for field := range strings.FieldsSeq(string(raw)) {
			pid, _ := strconv.Atoi(field)
			pids = append(pids, pid)
		}
		time.Sleep(time.Millisecond)
	}
	if len(pids) != orphans {
		t.Fatalf("orphans started: %d of %d", len(pids), orphans)
	}
	// Each leaf runs in its own session, outside the provider group, and only
	// becomes the supervisor's child once its intermediate parent exits.
	adopted := 0
	for time.Now().Before(deadline) {
		adopted = 0
		for _, pid := range pids {
			if parent, _, ok := procParent(pid); ok && parent == s.SupervisorPID {
				adopted++
			}
		}
		if adopted == orphans {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if adopted != orphans {
		t.Fatalf("orphans adopted by supervisor %d: %d of %d", s.SupervisorPID, adopted, orphans)
	}

	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	remaining, zombies := orphans, 0
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		remaining, zombies = 0, 0
		for _, pid := range pids {
			if parent, state, ok := procParent(pid); ok && parent == s.SupervisorPID {
				remaining++
				if state == "Z" {
					zombies++
				}
			}
		}
		if remaining == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if remaining != 0 || zombies != 0 {
		t.Fatalf("orphans left under supervisor: %d (zombies %d) of %d", remaining, zombies, orphans)
	}

	// Orphan reaping never consumes the provider's own Wait evidence.
	if s, err := p.Observe(binding()); err != nil || s.State == "exited" || s.State == "unknown" || s.Exit != nil {
		t.Fatalf("provider disturbed by orphan reaping: %+v %v", s, err)
	}
	if err := p.Stop(binding()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	final, err := p.Wait(ctx, binding())
	if err != nil {
		t.Fatal(err)
	}
	if final.Exit == nil || final.Exit.Code != 0 || final.Exit.Signal != "" {
		t.Fatalf("provider Wait evidence: %+v", final.Exit)
	}
}
