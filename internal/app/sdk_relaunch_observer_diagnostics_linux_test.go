package app

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Only the selected PID's stat and status are opened, once each per snapshot.
func relaunchReadKernelSnapshot(pid int) relaunchKernelSnapshot {
	s := relaunchKernelSnapshot{Begin: relaunchSnapshotTime(), PID: pid}
	read := func(name string) ([]byte, error) {
		f, err := os.Open(fmt.Sprintf("/proc/%d/%s", pid, name))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return relaunchReadDiagnosticFile(f)
	}
	stat, err := read("stat")
	s.Stat = relaunchDiagnosticReadState(err)
	if err == nil {
		s.Parent, s.State, s.Start, err = relaunchParseDiagnosticStat(stat)
		s.Stat = relaunchDiagnosticReadState(err)
	}
	status, err := read("status")
	s.Status = relaunchDiagnosticReadState(err)
	if err == nil {
		s.UIDs, err = relaunchParseDiagnosticUIDs(status)
		s.Status = relaunchDiagnosticReadState(err)
	}
	s.End = relaunchSnapshotTime()
	return s
}

func relaunchParseDiagnosticStat(raw []byte) (parent int, state, start uint64, err error) {
	invalid := errors.New("diagnostic stat parse")
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, 0, 0, invalid
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, 0, 0, invalid
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return 0, 0, 0, invalid
	}
	start, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, 0, invalid
	}
	return parent, uint64(fields[0][0]), start, nil
}

func relaunchParseDiagnosticUIDs(raw []byte) ([4]uint64, error) {
	var ids [4]uint64
	for line := range strings.SplitSeq(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 || f[0] != "Uid:" {
			continue
		}
		for i := range ids {
			value, err := strconv.ParseUint(f[i+1], 10, 32)
			if err != nil {
				return [4]uint64{}, errors.New("diagnostic UID parse")
			}
			ids[i] = value
		}
		return ids, nil
	}
	return ids, errors.New("diagnostic UID missing")
}

func TestRelaunchObserverDiagnosticsLinux(t *testing.T) {
	t.Run("numeric-only-parser", func(t *testing.T) {
		raw := "42 (secret ) comm) Z 7" + strings.Repeat(" 0", 17) + " 123"
		parent, state, start, err := relaunchParseDiagnosticStat([]byte(raw))
		if err != nil || parent != 7 || state != uint64('Z') || start != 123 {
			t.Fatalf("parent=%d state=%d start=%d err=%v", parent, state, start, err)
		}
		ids, err := relaunchParseDiagnosticUIDs([]byte("Name: secret\nUid:\t1\t2\t3\t4\n"))
		if err != nil || ids != [4]uint64{1, 2, 3, 4} {
			t.Fatalf("%v %v", ids, err)
		}
	})
	t.Run("incomplete-is-unavailable", func(t *testing.T) {
		if _, _, _, err := relaunchParseDiagnosticStat([]byte("42 (incomplete) Z")); err == nil {
			t.Fatal("incomplete stat accepted")
		}
		if _, err := relaunchParseDiagnosticUIDs([]byte("Uid: 1 broken 3 4")); err == nil {
			t.Fatal("invalid UID accepted")
		}
	})
}
