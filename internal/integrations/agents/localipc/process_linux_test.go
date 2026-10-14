package localipc

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestLinuxProcessSnapshot(t *testing.T) {
	stat := func(state, start string, parent int) string {
		fields := []string{state, fmt.Sprint(parent)}
		for len(fields) < 19 {
			fields = append(fields, "0")
		}
		return "42 (fixture) " + strings.Join(append(fields, start), " ")
	}
	for _, tc := range []struct {
		name        string
		stats, uids []string
		failure     string
		absent      bool
		wantUID     uint32
	}{
		{name: "normal", stats: []string{stat("S", "123", 7)}, uids: []string{"1000"}, wantUID: 1000},
		{name: "foreign credential", stats: []string{stat("S", "123", 7)}, uids: []string{"42"}, wantUID: 42},
		{name: "root credential", stats: []string{stat("S", "123", 7)}, uids: []string{"0"}},
		{name: "exit between reads", stats: []string{stat("S", "123", 7), stat("Z", "123", 7)}, uids: []string{"1000"}, failure: "identity"},
		{name: "pid reused", stats: []string{stat("S", "123", 7), stat("S", "124", 7)}, uids: []string{"1000"}, failure: "identity"},
		{name: "reparented", stats: []string{stat("S", "123", 7), stat("S", "123", 8)}, uids: []string{"1000"}, failure: "identity"},
		{name: "live state changed", stats: []string{stat("S", "123", 7), stat("R", "123", 7)}, uids: []string{"1000"}, wantUID: 1000},
		{name: "live stopped", stats: []string{stat("R", "123", 7), stat("T", "123", 7)}, uids: []string{"1000"}, wantUID: 1000},
		{name: "live idle", stats: []string{stat("D", "123", 7), stat("I", "123", 7)}, uids: []string{"1000"}, wantUID: 1000},
		{name: "late terminal", stats: []string{stat("S", "123", 7), stat("S", "123", 7), stat("X", "123", 7)}, uids: []string{"1000"}, failure: "identity"},
		{name: "credential changed", stats: []string{stat("S", "123", 7)}, uids: []string{"1000", "0"}, failure: "identity"},
		{name: "stat unreadable", failure: "stat-denied"},
		{name: "boot unreadable", stats: []string{stat("S", "123", 7)}, uids: []string{"1000"}, failure: "boot"},
		{name: "malformed credential", stats: []string{stat("S", "123", 7)}, uids: []string{"invalid"}, failure: "identity"},
		{name: "credential unreadable", stats: []string{stat("S", "123", 7)}, failure: "status"},
		{name: "stable zombie", stats: []string{stat("Z", "123", 7)}, absent: true},
		{name: "stable dead", stats: []string{stat("X", "123", 7)}, absent: true},
		{name: "zombie reused", stats: []string{stat("Z", "123", 7), stat("S", "124", 7)}, failure: "identity"},
		{name: "absent", failure: "stat", absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			si, ui := 0, 0
			read := func(path string) ([]byte, error) {
				switch {
				case strings.HasSuffix(path, "/stat"):
					if tc.failure == "stat-denied" {
						return nil, os.ErrPermission
					}
					if tc.failure == "stat" {
						return nil, os.ErrNotExist
					}
					i := min(si, len(tc.stats)-1)
					si++
					return []byte(tc.stats[i]), nil
				case strings.HasSuffix(path, "/status"):
					if tc.failure == "status" {
						return nil, os.ErrNotExist
					}
					i := min(ui, len(tc.uids)-1)
					ui++
					return []byte("Uid:\t1000\t" + tc.uids[i] + "\t1000\t1000\n"), nil
				default:
					if tc.failure == "boot" {
						return nil, os.ErrPermission
					}
					return []byte("boot\n"), nil
				}
			}
			got, parent, err := readLinuxProcess(42, read)
			if tc.absent {
				if !errors.Is(err, ErrProcessAbsent) {
					t.Fatalf("got %v", err)
				}
				return
			}
			if tc.failure != "" {
				if err == nil || errors.Is(err, ErrProcessAbsent) || got.PID != 0 || parent != 0 {
					t.Fatalf("unsafe result: %+v parent=%d err=%v", got, parent, err)
				}
				return
			}
			if err != nil || got.PID != 42 || got.OwnerUID != tc.wantUID || got.Start != "linux:boot:123" || parent != 7 {
				t.Fatalf("got %+v parent=%d err=%v", got, parent, err)
			}
		})
	}
}

func TestLinuxProcessNormalRecordedIdentityCompatibility(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("/usr/bin/python3", "-u", "-c", "import sys; print('ready', flush=True); sys.stdin.read()")
	cmd.Env = []string{"HOME=" + root, "XDG_CONFIG_HOME=" + root, "XDG_STATE_HOME=" + root}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("fixture readiness %q %v", line, err)
	}
	path := "/proc/" + strconv.Itoa(cmd.Process.Pid) + "/stat"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	inode, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no procfs inode owner")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	got, parent, err := Process(cmd.Process.Pid)
	if err != nil || got.OwnerUID != inode.Uid || got.Start != "linux:"+strings.TrimSpace(string(boot))+":"+fields[19] || parent != os.Getpid() {
		t.Fatalf("normal recorded identity changed: %+v parent=%d err=%v oldUID=%d oldStart=%s", got, parent, err, inode.Uid, fields[19])
	}
	t.Logf("exact fixture PID=%d UID=%d Start=%s; own cleanup registered", got.PID, got.OwnerUID, got.Start)
}

func TestLinuxParentTransitionRequiresCompleteCredentialEvidence(t *testing.T) {
	for _, change := range []string{"parent-only", "uid", "birth", "terminal", "credential", "third-parent"} {
		t.Run(change, func(t *testing.T) {
			reads, creds := 0, 0
			read := func(path string) ([]byte, error) {
				if strings.HasSuffix(path, "/stat") {
					reads++
					parent, start, state := 7, "123", "S"
					if reads > 1 {
						parent = 8
					}
					if change == "third-parent" && reads == 3 {
						parent = 9
					}
					if change == "birth" && reads > 1 {
						start = "124"
					}
					if change == "terminal" && reads > 1 {
						state = "Z"
					}
					fields := []string{state, fmt.Sprint(parent)}
					for len(fields) < 19 {
						fields = append(fields, "0")
					}
					return []byte("42 (fixture) " + strings.Join(append(fields, start), " ")), nil
				}
				if strings.HasSuffix(path, "/status") {
					creds++
					if change == "credential" && creds == 2 {
						return nil, os.ErrPermission
					}
					uid := "1000"
					if change == "uid" && creds == 2 {
						uid = "0"
					}
					return []byte("Uid: 1000 " + uid + " 1000 1000\n"), nil
				}
				return []byte("boot"), nil
			}
			identity, parent, err := readLinuxProcess(42, read)
			witness, ok := ProcessParentTransition(err)
			if err == nil || identity.PID != 0 || parent != 0 || errors.Is(err, ErrProcessAbsent) {
				t.Fatalf("transition was accepted: %+v %d %v", identity, parent, err)
			}
			if ok != (change == "parent-only") {
				t.Fatalf("witness=%+v ok=%v change=%s", witness, ok, change)
			}
			if ok && (witness.OldParent != 7 || witness.NewParent != 8 || witness.Process.Start != "linux:boot:123" || witness.Process.OwnerUID != 1000 || creds != 2) {
				t.Fatal("incomplete witness", witness)
			}
		})
	}
}
