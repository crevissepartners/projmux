package app

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func marshalExactCreateComparisonCalls(calls [][]string) ([]byte, error) {
	// The create lease's wall-clock second is not a route or operation identity.
	// Compare every other byte and leave unknown marker shapes untouched.
	stable := make([][]string, len(calls))
	for index, call := range calls {
		stable[index] = append([]string(nil), call...)
		if len(call) != 7 || call[2] != "set-environment" || call[3] != "-t" || call[5] != createOperationEnvironment {
			continue
		}
		parts := strings.Split(call[6], ":")
		if len(parts) != 4 || parts[0] != "v1" || parts[3] == "" {
			continue
		}
		pid, pidErr := strconv.ParseUint(parts[1], 10, 64)
		started, timeErr := strconv.ParseInt(parts[2], 10, 64)
		if pidErr != nil || pid == 0 || timeErr != nil || started < 0 || strconv.FormatInt(started, 10) != parts[2] {
			continue
		}
		parts[2] = "<clock>"
		stable[index][6] = strings.Join(parts, ":")
	}
	return json.Marshal(stable)
}

func TestExactCreateComparisonOnlyNormalizesValidLeaseTimestamp(t *testing.T) {
	call := []string{"-S", "/tmp/fake-tmux/phase2-exact-create", "set-environment", "-t", "$1", createOperationEnvironment, "v1:123:1700000000:op-test"}
	before, err := marshalExactCreateComparisonCalls([][]string{call})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		index int
		value string
		equal bool
	}{
		{"next-second", 6, "v1:123:1700000001:op-test", true},
		{"different-pid", 6, "v1:124:1700000000:op-test", false},
		{"different-operation", 6, "v1:123:1700000000:op-other", false},
		{"different-socket", 1, "/tmp/fake-tmux/other", false},
		{"different-route", 0, "-L", false},
		{"different-session", 4, "$2", false},
		{"unknown-version", 6, "v2:123:1700000001:op-test", false},
		{"invalid-time", 6, "v1:123:unknown:op-test", false},
		{"overflow-time", 6, "v1:123:18446744073709551615:op-test", false},
		{"negative-time", 6, "v1:123:-1:op-test", false},
		{"noncanonical-time", 6, "v1:123:01700000000:op-test", false},
		{"extra-field", 6, "v1:123:1700000001:op-test:extra", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := append([]string(nil), call...)
			changed[test.index] = test.value
			after, err := marshalExactCreateComparisonCalls([][]string{changed})
			if err != nil || (string(before) == string(after)) != test.equal {
				t.Fatalf("comparison before=%s after=%s equal=%t err=%v", before, after, test.equal, err)
			}
			if changed[test.index] != test.value || call[6] != "v1:123:1700000000:op-test" {
				t.Fatal("comparison changed original call evidence")
			}
		})
	}
}
