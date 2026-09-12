package codexinstalled

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

func residualStat(pid int, birth, state string) []byte {
	return fmt.Appendf(nil, "%d (PRIVATE-COMMAND) %s 7860 %s %s 0\n", pid, state, strings.Repeat("0 ", 17), birth)
}

func residualReader(t *testing.T, stat func(int) ([]byte, error)) processReader {
	t.Helper()
	return processReader{
		readDir:       func(string) ([]os.DirEntry, error) { return fs.ReadDir(fstest.MapFS{"7861": &fstest.MapFile{}}, ".") },
		readFile:      func(string) ([]byte, error) { return nil, syscall.EACCES },
		readlink:      func(string) (string, error) { t.Fatal("residual must not grant executable ownership"); return "", nil },
		birth:         func(int) (string, error) { t.Fatal("residual must not become a live owned process"); return "", nil },
		stat:          stat,
		verifyPrivate: func() error { return nil },
	}
}

func TestOwnedProcessesPermissionDeniedZombieRemainsNonemptyResidual(t *testing.T) {
	fixture := &Fixture{CodexHome: "/private/codex-home"}
	statReads := 0
	reader := residualReader(t, func(pid int) ([]byte, error) {
		statReads++
		return residualStat(pid, "2768830", "Z"), nil
	})
	processes, err := fixture.ownedProcesses(reader)
	if err != nil || len(processes) != 1 {
		t.Fatalf("environment EACCES followed by exact zombie must retain an unresolved process: processes=%v err=%v statReads=%d", processes, err, statReads)
	}
	if processes[0].PID != 7861 || processes[0].Birth != "2768830" || processes[0].Executable != "" || statReads != 2 {
		t.Fatalf("residual incorrectly grants ownership or loses exact birth: %+v statReads=%d", processes, statReads)
	}
	if processes[0].Kind != ProcessUnreapedResidual || processes[0].State != "Z" || processes[0].ParentPID != 7860 {
		t.Fatalf("residual lost explicit unknown ownership/state: %+v", processes)
	}
	raw, err := json.Marshal(processes)
	if err != nil || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "codex-home") || strings.Contains(string(raw), "executable") {
		t.Fatalf("residual leaked process content: %s", raw)
	}
}

func TestOwnedProcessesPermissionFailuresRemainErrorsUnlessExactPrivateZombie(t *testing.T) {
	for _, kind := range []string{"live", "unknown-state", "malformed", "oversized", "wrong-pid", "zero-birth", "disappeared", "permission", "second-disappeared", "reused", "became-live", "unknown-namespace", "non-permission"} {
		t.Run(kind, func(t *testing.T) {
			fixture := &Fixture{strictManaged: true, strictManagedStopped: true}
			reads := 0
			reader := residualReader(t, func(pid int) ([]byte, error) {
				reads++
				switch kind {
				case "live":
					return residualStat(pid, "2768830", "S"), nil
				case "unknown-state":
					return residualStat(pid, "2768830", "?"), nil
				case "malformed":
					return []byte("PRIVATE-BAD-STAT"), nil
				case "oversized":
					return []byte(strings.Repeat("x", 4097)), nil
				case "wrong-pid":
					return residualStat(pid+1, "2768830", "Z"), nil
				case "zero-birth":
					return residualStat(pid, "0", "Z"), nil
				case "disappeared":
					return nil, fs.ErrNotExist
				case "permission":
					return nil, syscall.EACCES
				case "second-disappeared":
					if reads == 2 {
						return nil, fs.ErrNotExist
					}
				case "reused":
					if reads == 2 {
						return residualStat(pid, "2768831", "Z"), nil
					}
				case "became-live":
					if reads == 2 {
						return residualStat(pid, "2768830", "S"), nil
					}
				}
				return residualStat(pid, "2768830", "Z"), nil
			})
			cause := error(syscall.EACCES)
			if kind == "non-permission" {
				cause = syscall.EIO
			}
			reader.readFile = func(string) ([]byte, error) { return nil, cause }
			if kind == "unknown-namespace" {
				reader.verifyPrivate = func() error { return errors.New("PRIVATE-NAMESPACE") }
			}
			processes, err := fixture.ownedProcesses(reader)
			if err == nil || !errors.Is(err, cause) || processes != nil || fixture.Cleanup() == nil {
				t.Fatalf("uncertain process falsely became empty/clean: processes=%v err=%v", processes, err)
			}
			if (kind == "non-permission" || kind == "unknown-namespace") && reads != 0 {
				t.Fatal("stat read without residual admission")
			}
			if reads > 2 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("unbounded reads or content leak")
			}
		})
	}
}

func TestOwnedProcessesResidualRequiresNormalReapingBeforeCleanup(t *testing.T) {
	for _, later := range []string{"still-zombie", "live", "malformed", "reused", "permission", "second-disappeared", "gone", "namespace-changed"} {
		t.Run(later, func(t *testing.T) {
			root := t.TempDir()
			fixture := &Fixture{Root: root, CodexHome: filepath.Join(root, "codex-home"), Workspace: filepath.Join(root, "workspace"), ownsState: true, ledger: &Ledger{path: filepath.Join(root, "ledger")}, shimPath: filepath.Join(root, "bin", "codex"), startResultPath: filepath.Join(root, "start-result")}
			if err := os.MkdirAll(fixture.CodexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			fixture.strictManaged, fixture.strictManagedStopped = true, true
			marker := filepath.Join(fixture.CodexHome, "preserved")
			if err := os.WriteFile(marker, []byte("PRIVATE"), 0o600); err != nil {
				t.Fatal(err)
			}
			reader := residualReader(t, func(pid int) ([]byte, error) { return residualStat(pid, "2768830", "Z"), nil })
			if processes, err := fixture.ownedProcesses(reader); err != nil || len(processes) != 1 {
				t.Fatalf("initial residual=%v err=%v", processes, err)
			}
			if fixture.Cleanup() == nil {
				t.Fatal("unreaped residual permitted cleanup")
			}
			// A later environ becoming empty cannot hide the previously observed zombie.
			reader.readFile = func(string) ([]byte, error) { return nil, nil }
			reads := 0
			reader.stat = func(pid int) ([]byte, error) {
				reads++
				switch later {
				case "gone":
					return nil, fs.ErrNotExist
				case "live":
					return residualStat(pid, "2768830", "S"), nil
				case "malformed":
					return []byte("PRIVATE"), nil
				case "reused":
					return residualStat(pid, "2768831", "Z"), nil
				case "permission":
					return nil, syscall.EACCES
				case "second-disappeared":
					if reads == 2 {
						return nil, fs.ErrNotExist
					}
				}
				return residualStat(pid, "2768830", "Z"), nil
			}
			if later == "namespace-changed" {
				reader.verifyPrivate = func() error { return errors.New("private namespace changed") }
			}
			processes, err := fixture.ownedProcesses(reader)
			switch later {
			case "gone":
				if err != nil || len(processes) != 0 || fixture.Cleanup() != nil {
					t.Fatalf("normal reaping failed: %v %v", processes, err)
				}
				if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("successful cleanup failed to remove owned fixture state")
				}
			default:
				if later == "still-zombie" && (err != nil || len(processes) != 1) {
					t.Fatal("zombie disappeared from inventory")
				}
				if later != "still-zombie" && err == nil {
					t.Fatal("uncertain later stat permitted a successful inventory")
				}
				if fixture.Cleanup() == nil {
					t.Fatal("unresolved residual permitted cleanup")
				}
				if raw, err := os.ReadFile(marker); err != nil || string(raw) != "PRIVATE" {
					t.Fatal("failed cleanup mutated fixture state")
				}
			}
		})
	}
}

func TestResidualAdmissionRequiresRecordedPrivateLifecycleProof(t *testing.T) {
	for _, fixture := range []*Fixture{
		{},
		{ownsState: true, strictManaged: true, strictManagedStopped: true},
		{ownsState: true, strictManaged: true, strictManagedStopped: true, processNamespaces: map[string]string{"pid": "pid:private", "mnt": "mnt:private", "net": "net:private"}},
	} {
		if fixture.verifyResidualIsolation() == nil {
			t.Fatal("ambient/unproved lifecycle granted residual admission")
		}
	}
}

func TestOwnedProcessesFailedCensusCannotClearEarlierResidual(t *testing.T) {
	fixture := &Fixture{strictManaged: true, strictManagedStopped: true}
	reader := residualReader(t, func(pid int) ([]byte, error) { return residualStat(pid, "2768830", "Z"), nil })
	if processes, err := fixture.ownedProcesses(reader); err != nil || len(processes) != 1 {
		t.Fatal("initial residual missing")
	}
	reader.readDir = func(string) ([]os.DirEntry, error) { return fs.ReadDir(fstest.MapFS{"9999": &fstest.MapFile{}}, ".") }
	reader.stat = func(int) ([]byte, error) { return nil, fs.ErrNotExist }
	reader.readFile = func(string) ([]byte, error) { return nil, syscall.EIO }
	if processes, err := fixture.ownedProcesses(reader); !errors.Is(err, syscall.EIO) || processes != nil {
		t.Fatal("later failed census was accepted")
	}
	if len(fixture.processInventory.residuals) != 1 || fixture.Cleanup() == nil {
		t.Fatal("failed census cleared residual or permitted cleanup")
	}
}
