//go:build race

package processhost

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFixtureExitDelayRetainsFatalRaceReport(t *testing.T) {
	cmd := exec.Command(os.Args[0], "processhost-race")
	cmd.Env = fixtureEnv()
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "WARNING: DATA RACE") {
		t.Fatalf("fixture must retain a fatal race report: %v\n%s", err, output)
	}
}
