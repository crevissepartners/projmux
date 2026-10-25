package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestCreateAgentOperatorCreatorMatchesCLIProcess is the process half of
// TestCreateAgentOperatorCreatorMatchesCLI: a detached SDK create carries
// CreateAgentRequest.Operator to the owner route as a typed request field, and
// the Agent and its Pane record what the CLI's in-process operator seam
// decides for the same Registry, with the CLI's stderr line as a notice.
func TestCreateAgentOperatorCreatorMatchesCLIProcess(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := newProcessCreateCLI(t)
	state := filepath.Join(f.root, "state", "projmux")
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: filepath.Join(f.root, "config", "projmux"), StateDir: state, ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	storage := intmetadata.NewStore(intmetadata.PathFor(state))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var created []core.CreateAgentResult
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		for _, result := range created {
			if _, e := c.StopAgent(clean, core.StopAgentRequest{Ref: "uid:" + result.Agent.Metadata.UID, Binding: result.Binding}); e != nil {
				_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("operator creator Stop unconfirmed"), 0600)
				t.Errorf("cleanup %v", e)
			}
		}
	})
	declared := ""
	for _, name := range []string{"parent", "child"} {
		req := core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: "claude", Profile: "none", Model: "stub-model", Effort: "low", Host: "process", Detach: true, Name: name, Payload: []string{"initial"}, Operator: core.NewOperatorCreator(sdkOperatorCreatorClient)}
		if declared != "" {
			req.Creator = "uid:" + declared
		}
		result, err := c.CreateAgent(ctx, req)
		if err != nil {
			if strings.Contains(err.Error(), "cleanup-unconfirmed") {
				_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK startup cleanup unconfirmed"), 0600)
			}
			t.Fatalf("SDK create %s: %v", name, err)
		}
		created = append(created, result)
		reg, err := storage.LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		// The CLI's in-process operator seam, deciding for the same Registry.
		cli := &createCommand{}
		cli.withoutCreatorProvenance()
		if err := cli.recordOperatorCreator(sdkOperatorCreatorClient); err != nil {
			t.Fatal(err)
		}
		record, err := cli.decideCreator(ctx, canonicalCreateAgent, &reg, declared)
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		record.report(&stderr)
		agent, ok := reg.Agent(result.Agent.Metadata.UID)
		if !ok {
			t.Fatalf("Agent %s missing", name)
		}
		pane, ok := reg.Pane(result.Binding.PaneUID)
		if !ok {
			t.Fatalf("Pane of %s missing", name)
		}
		assertCreatorRecord(t, *agent, *pane, record.annotations())
		var lines []string
		for _, notice := range result.Notices {
			if strings.HasPrefix(notice, "creator declaration not recorded:") {
				lines = append(lines, notice+"\n")
			}
		}
		if got := strings.Join(lines, ""); got != stderr.String() {
			t.Fatalf("%s declaration notices %q, want the CLI's stderr %q", name, got, stderr.String())
		}
		if declared != "" && stderr.String() != "creator declaration not recorded: operator-client (--creator uid:"+declared+")\n" {
			t.Fatalf("CLI declaration line %q", stderr.String())
		}
		declared = result.Agent.Metadata.UID
	}
}
