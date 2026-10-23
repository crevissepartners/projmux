package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Only tests that read the helper identity right after proof opt in: the lease
// readiness test and the delayed helper cleanup test. Registration can be
// discoverable before SessionStart's hook exits and its helper finishes
// changing parents.
// This marker follows subprocess.run's actual Wait, not the first stream init
// (which this provider emits only after input). It grants no lease authority.
func processClaudeLeaseReadyProvider(root, _ string) processhost.Command {
	const hook = "hook('claude-endpoint-register',{'hook_event_name':'SessionStart','session_id':'process-session'})"
	script := strings.Replace(processClaudeProviderFixture, hook, hook+`
with open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'session-start-wait-complete'),'x') as completed:
 completed.write('complete\n')`, 1)
	return processhost.Command{Path: "python3", Args: []string{"-u", "-c", script}, Env: append(os.Environ(), "HOME="+root), Dir: root}
}

func processClaudeLeaseHookWaitComplete(root string) bool {
	marker, err := os.ReadFile(filepath.Join(root, "session-start-wait-complete"))
	return err == nil && string(marker) == "complete\n"
}

// Hold the real subprocess.run at its Wait boundary after registration is
// discoverable. No sleep or kernel stat race is needed to establish that the
// old proof could return before the provider completed its SessionStart Wait.
func TestClaudeLeaseProofWaitsForSessionStartWait(t *testing.T) {
	var gate *os.File
	released := false
	release := func() {
		if gate != nil && !released {
			released = true
			if _, err := gate.Write([]byte{1}); err != nil {
				t.Error("could not release SessionStart Wait")
			}
		}
	}
	// A defer runs before the fixture's testing cleanup, even if construction
	// calls Fatal. Release and close must precede fixture Stop/actual Wait.
	defer func() {
		release()
		if gate != nil {
			if err := gate.Close(); err != nil {
				t.Error("could not close SessionStart Wait gate")
			}
		}
	}()
	f := newProcessClaudeFixture(t, func(root, binary string) processhost.Command {
		gatePath := filepath.Join(root, "session-start-wait.fifo")
		if err := syscall.Mkfifo(gatePath, 0600); err != nil {
			t.Fatal("could not create SessionStart Wait gate")
		}
		var err error
		gate, err = os.OpenFile(gatePath, os.O_RDWR, 0600)
		if err != nil {
			t.Fatal("could not open SessionStart Wait gate")
		}
		command := processClaudeLeaseReadyProvider(root, binary)
		const hook = "hook('claude-endpoint-register',{'hook_event_name':'SessionStart','session_id':'process-session'})"
		// subprocess.run still sends the real hook input and collects its output.
		// Gate only the first actual wait; later hooks keep the original method.
		beforeHook := fmt.Sprintf(`fixture_original_wait=subprocess.Popen.wait
fixture_wait_entered=False
def fixture_wait(self,timeout=None):
 global fixture_wait_entered
 if not fixture_wait_entered:
  fixture_wait_entered=True
  with open(%q,'rb',buffering=0) as gate:
   with open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'hook-wait-entered'),'x') as entered:
    entered.write('waiting\n')
   if gate.read(1)!=b'\x01': raise Exception('fixture Wait gate closed')
 return fixture_original_wait(self,timeout=timeout)
subprocess.Popen.wait=fixture_wait
`, gatePath)
		command.Args[2] = strings.Replace(command.Args[2], hook, beforeHook+hook+"\nsubprocess.Popen.wait=fixture_original_wait", 1)
		return command
	})
	observedBeforeWait := false
	proof := f.proof(t, func() bool {
		if processClaudeLeaseHookWaitComplete(f.root) {
			return true
		}
		if _, err := os.Stat(filepath.Join(f.root, "hook-wait-entered")); err != nil {
			return false
		}
		reg, _ := f.store.LoadDegradedReadOnly()
		if _, ok := discoverProcessClaudeProof(f.path, reg, f.binding.Agent); !ok {
			return false
		}
		observedBeforeWait = true
		release()
		return false
	})
	if !observedBeforeWait || !processClaudeLeaseHookWaitComplete(f.root) {
		t.Fatal("proof returned before SessionStart Wait completion")
	}
	reg, _ := f.store.LoadDegradedReadOnly()
	route, reason := processClaudeRouteResolver(f.path, proof)(reg, f.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	if !probeClaudeRegistrationLease(f.path, route) {
		t.Fatal("lease not ready after SessionStart Wait")
	}
}
