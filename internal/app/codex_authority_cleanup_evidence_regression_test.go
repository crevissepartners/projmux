package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

func cleanupStatFixture(pid int, state string) []byte {
	fields := strings.Fields(state + " 1 " + strings.Repeat("0 ", 17) + "12345")
	return []byte(strconv.Itoa(pid) + " (PRIVATE-COMMAND with ) bracket) " + strings.Join(fields, " "))
}

func TestCleanupEvidenceRetainsErrorsWithoutRetryOrFalseSuccess(t *testing.T) {
	for _, failure := range []error{
		&os.PathError{Op: "open", Path: "/proc/321/environ", Err: syscall.EACCES},
		&os.PathError{Op: "readlink", Path: "/proc/321/exe", Err: syscall.EPERM},
		&os.PathError{Op: "open", Path: "/proc/321/stat", Err: syscall.ENOENT},
		&codexinstalled.OwnedProcessObservationError{Operation: "read-birth", PID: 321, Leaf: "stat"},
	} {
		for _, vanished := range []bool{false, true} {
			inspections, reads := 0, 0
			processes, evidence, err := inspectInstalledCleanupProcesses(func() ([]codexinstalled.OwnedProcess, error) {
				inspections++
				return nil, failure
			}, func(pid int) ([]byte, error) {
				reads++
				if pid != 321 {
					t.Fatal("diagnostic read a different process")
				}
				if vanished {
					return nil, &os.PathError{Op: "open", Path: "/proc/321/stat", Err: syscall.ENOENT}
				}
				return cleanupStatFixture(pid, "Z"), nil
			})
			if err != failure || processes != nil || evidence == nil || evidence.PID != 321 || inspections != 1 || reads != 1 {
				t.Fatal("failed inventory was retried/skipped or lost its exact PID")
			}
			if errors.Is(failure, syscall.EACCES) && evidence.Errno != int(syscall.EACCES) {
				t.Fatal("original permission error lost its numeric errno")
			}
			if vanished && evidence.LaterStat.Errno != int(syscall.ENOENT) {
				t.Fatal("vanished later observation lost errno")
			}
			if !vanished && (evidence.LaterStat.State != "Z" || evidence.LaterStat.Birth != "12345") {
				t.Fatal("later process state/birth missing")
			}
			// A vanished or zombie later observation never changes the original
			// error into an empty successful inventory in either harness.
			for _, ledger := range []any{installedConnectionLedger{Result: "FAIL", CleanupFailure: evidence}, installedRecoveryLedger{Result: "FAIL", CleanupFailure: evidence}} {
				raw, marshalErr := json.Marshal(ledger)
				if marshalErr != nil || strings.Contains(string(raw), "PRIVATE-") || !strings.Contains(string(raw), `"cleanup":false`) || !strings.Contains(string(raw), `"result":"FAIL"`) {
					t.Fatalf("failure was lost or content leaked: %s %v", raw, marshalErr)
				}
			}
		}
	}
}

func TestCleanupEvidenceReadsOnlyOneStatForValidatedProcIdentity(t *testing.T) {
	for _, failure := range []error{
		errors.New("PRIVATE-ERROR /proc/321/stat"),
		&os.PathError{Op: "PRIVATE-OP", Path: "/PRIVATE-PATH", Err: syscall.EACCES},
		&os.PathError{Op: "open", Path: "/proc/self/stat", Err: syscall.EACCES},
		&os.PathError{Op: "open", Path: "/proc/321/../123/stat", Err: syscall.EACCES},
		&os.PathError{Op: "open", Path: "/proc/0321/stat", Err: syscall.EACCES},
		&os.PathError{Op: "open", Path: "/proc/321/cmdline", Err: syscall.EACCES},
		&codexinstalled.OwnedProcessObservationError{Operation: "PRIVATE-OP", PID: 321, Leaf: "stat"},
		&codexinstalled.OwnedProcessObservationError{Operation: "read-birth", PID: -1, Leaf: "stat"},
	} {
		reads := 0
		out := observeInstalledCleanupFailure("owned-processes", fmt.Errorf("PRIVATE-WRAPPER: %w", failure), func(int) ([]byte, error) {
			reads++
			return nil, errors.New("forbidden read")
		})
		raw, err := json.Marshal(out)
		if err != nil || reads != 0 || out.PID != 0 || out.LaterStat != nil || strings.Contains(string(raw), "PRIVATE-") {
			t.Fatalf("unsafe cleanup diagnostic: reads=%d out=%s err=%v", reads, raw, err)
		}
	}
	reads := 0
	processes, failure, err := inspectInstalledCleanupProcesses(func() ([]codexinstalled.OwnedProcess, error) {
		return []codexinstalled.OwnedProcess{{PID: 321}}, nil
	}, func(int) ([]byte, error) { reads++; return nil, nil })
	if err != nil || failure != nil || reads != 0 || len(processes) != 1 {
		t.Fatal("successful inspection triggered diagnostics or lost live processes")
	}
}

func TestCleanupStatProjectionRejectsMalformedOrDifferentPIDWithoutContent(t *testing.T) {
	for _, raw := range [][]byte{[]byte("PRIVATE-CONTENT"), cleanupStatFixture(999, "Z"), cleanupStatFixture(321, "PRIVATE-STATE"), []byte(strings.Repeat("x", 4097))} {
		out := projectInstalledCleanupStat(321, raw, nil)
		encoded, err := json.Marshal(out)
		if err != nil || out.Code != "invalid-stat" || out.Birth != "" || out.State != "" || strings.Contains(string(encoded), "PRIVATE-") {
			t.Fatalf("unvalidated stat content escaped: %s %v", encoded, err)
		}
	}
}
