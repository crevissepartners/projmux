package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

const (
	relaunchDiagnosticFileLimit     = 16 * 1024
	relaunchDiagnosticLogLimit      = 8 * 1024
	relaunchDiagnosticObserverLimit = 16
	relaunchDiagnosticCauseLimit    = 8
)

// These observations grant no identity, absence, cleanup, or retry authority.
// At most 16 selected identities get one pre-snapshot, immediately before their
// first retirement Process call. A later failure may therefore have an older
// pre-snapshot; timestamps and the label make that gap explicit. Only the failed
// PID gets one post-snapshot. No snapshots are printed on success.
type relaunchObserverDiagnostics struct {
	before map[metadata.ProcessIdentity]relaunchKernelSnapshot
}

// State is the Linux state byte as an integer, or Darwin P_stat. Start is
// Linux boot-relative ticks, or Darwin epoch microseconds; never compare them.
type relaunchKernelSnapshot struct {
	Begin, End   string
	Stat, Status string
	PID, Parent  int
	State, Start uint64
	UIDs         [4]uint64
}

func (d *relaunchObserverDiagnostics) captureBefore(expected metadata.ProcessIdentity) {
	if d == nil {
		return
	}
	if d.before == nil {
		d.before = make(map[metadata.ProcessIdentity]relaunchKernelSnapshot)
	}
	if _, ok := d.before[expected]; !ok && len(d.before) < relaunchDiagnosticObserverLimit {
		d.before[expected] = relaunchReadKernelSnapshot(expected.PID)
	}
}

func (d *relaunchObserverDiagnostics) report(t *testing.T, expected metadata.ProcessIdentity, births map[string]metadata.ProcessIdentity, err error) {
	t.Helper()
	if d == nil {
		return
	}
	before, ok := d.before[expected]
	if !ok {
		before.Stat, before.Status = "unavailable:pre-snapshot-budget", "unavailable:pre-snapshot-budget"
	}
	after := relaunchReadKernelSnapshot(expected.PID)
	tuple := "unavailable"
	for receipt, identity := range births {
		if identity != expected {
			continue
		}
		fields := strings.Split(receipt, "|")
		if len(fields) == 6 {
			// Do not print the receipt, state path, executable, argv, or environment.
			tuple = fmt.Sprintf("agent=%q pane=%q generation=%q", relaunchDiagnosticToken(fields[1]), relaunchDiagnosticToken(fields[2]), relaunchDiagnosticToken(fields[3]))
		}
		break
	}
	t.Log(relaunchFormatObserverDiagnostic(expected, tuple, err, before, after))
}

func relaunchDiagnosticToken(s string) string {
	if len(s) > 128 {
		return s[:128] + "[truncated]"
	}
	return s
}

// Read no more than 16 KiB plus one overflow byte; never emit raw file data.
func relaunchReadDiagnosticFile(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, relaunchDiagnosticFileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > relaunchDiagnosticFileLimit {
		return nil, errors.New("diagnostic file limit")
	}
	return data, nil
}

func relaunchDiagnosticReadState(err error) string {
	if err == nil {
		return "observed"
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return fmt.Sprintf("absent:type=%T", err)
	}
	// Record only actual typed errno, never infer an internal Process stage.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("read-error:type=%T errno=%d", err, errno)
	}
	return fmt.Sprintf("unavailable:type=%T", err)
}

func relaunchFormatObserverDiagnostic(expected metadata.ProcessIdentity, tuple string, err error, before, after relaunchKernelSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "observer exit evidence (diagnostic only) expected_pid=%d expected_uid=%d expected_birth=%q selected={%s}\n", expected.PID, expected.OwnerUID, relaunchDiagnosticToken(expected.Start), tuple)
	fmt.Fprintf(&b, "original_type=%T is_absent=%t is_not_exist=%t is_esrch=%t internal_stage=unknown internal_errno=unknown\n", err, errors.Is(err, localipc.ErrProcessAbsent), errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ESRCH))
	pending := []error{err}
	for n := 0; len(pending) > 0 && n < relaunchDiagnosticCauseLimit; n++ {
		current := pending[0]
		pending = pending[1:]
		if current == nil {
			continue
		}
		fmt.Fprintf(&b, "cause[%d]=%T", n, current)
		switch w := current.(type) {
		case localipc.ParentTransitionWitness:
			fmt.Fprintf(&b, " witness_pid=%d witness_uid=%d witness_birth=%q old_parent=%d new_parent=%d", w.Process.PID, w.Process.OwnerUID, relaunchDiagnosticToken(w.Process.Start), w.OldParent, w.NewParent)
		}
		if w, ok := current.(*localipc.ParentTransitionWitness); ok && w != nil {
			fmt.Fprintf(&b, " witness_pid=%d witness_uid=%d witness_birth=%q old_parent=%d new_parent=%d", w.Process.PID, w.Process.OwnerUID, relaunchDiagnosticToken(w.Process.Start), w.OldParent, w.NewParent)
		}
		if errno, ok := current.(syscall.Errno); ok {
			fmt.Fprintf(&b, " errno=%d", errno)
		}
		b.WriteByte('\n')
		switch u := current.(type) {
		case interface{ Unwrap() []error }:
			children := u.Unwrap()
			// Bound both traversal and retained children, including joined errors.
			available := relaunchDiagnosticCauseLimit - n - 1 - len(pending)
			if available > len(children) {
				available = len(children)
			}
			if available > 0 {
				pending = append(pending, children[:available]...)
			}
		case interface{ Unwrap() error }:
			pending = append(pending, u.Unwrap())
		}
	}
	fmt.Fprintf(&b, "before-first-retirement-process=%+v\nafter-failed-process=%+v\n", before, after)
	result := b.String()
	if len(result) > relaunchDiagnosticLogLimit {
		const marker = "\n[diagnostic log truncated]"
		result = result[:relaunchDiagnosticLogLimit-len(marker)] + marker
	}
	return result
}

func relaunchSnapshotTime() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func TestRelaunchObserverDiagnostics(t *testing.T) {
	t.Run("typed-cause-and-witness", func(t *testing.T) {
		expected := metadata.ProcessIdentity{PID: 42, OwnerUID: 1000, Start: "linux:boot:123"}
		witness := localipc.ParentTransitionWitness{Process: expected, OldParent: 12, NewParent: 1}
		out := relaunchFormatObserverDiagnostic(expected, "selected", fmt.Errorf("wrapped: %w", witness), relaunchKernelSnapshot{Begin: "before"}, relaunchKernelSnapshot{Begin: "after"})
		for _, want := range []string{"original_type=*fmt.wrapError", "localipc.ParentTransitionWitness", "old_parent=12 new_parent=1", "expected_pid=42", "internal_stage=unknown", "is_absent=false", "Begin:before", "Begin:after"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q: %s", want, out)
			}
		}
	})
	t.Run("generic-is-not-absence", func(t *testing.T) {
		out := relaunchFormatObserverDiagnostic(metadata.ProcessIdentity{}, "", errors.New("process identity unavailable SECRET"), relaunchKernelSnapshot{}, relaunchKernelSnapshot{})
		if !strings.Contains(out, "original_type=*errors.errorString") || !strings.Contains(out, "is_absent=false") || strings.Contains(out, "SECRET") || strings.Contains(out, "old_parent=") {
			t.Fatal(out)
		}
	})
	t.Run("read-errors-and-bounds", func(t *testing.T) {
		if !strings.HasPrefix(relaunchDiagnosticReadState(os.ErrNotExist), "absent:") || !strings.Contains(relaunchDiagnosticReadState(syscall.EACCES), "errno=13") {
			t.Fatal("read classification lost")
		}
		if data, err := relaunchReadDiagnosticFile(relaunchDiagnosticErrorReader{}); err == nil || data != nil {
			t.Fatal("read failure hidden")
		}
		if _, err := relaunchReadDiagnosticFile(strings.NewReader(strings.Repeat("x", relaunchDiagnosticFileLimit))); err != nil {
			t.Fatal(err)
		}
		if _, err := relaunchReadDiagnosticFile(strings.NewReader(strings.Repeat("x", relaunchDiagnosticFileLimit+1))); err == nil {
			t.Fatal("oversize accepted")
		}
		out := relaunchFormatObserverDiagnostic(metadata.ProcessIdentity{}, strings.Repeat("x", relaunchDiagnosticLogLimit*2), syscall.ESRCH, relaunchKernelSnapshot{}, relaunchKernelSnapshot{})
		if len(out) > relaunchDiagnosticLogLimit || !strings.HasSuffix(out, "[diagnostic log truncated]") {
			t.Fatal("log budget lost")
		}
	})
}

// An independent reader failure must remain unavailable, never absence.
type relaunchDiagnosticErrorReader struct{}

func (relaunchDiagnosticErrorReader) Read([]byte) (int, error) { return 0, syscall.EACCES }
