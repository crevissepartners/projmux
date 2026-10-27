package projmux

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// JSON assertions also compile against the old API, so the baseline overlay
// fails on missing behavior rather than on a newly introduced Go field.
func stopRequestedJSON(t *testing.T, value any) bool {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	var requested bool
	if field, ok := object["stopRequested"]; ok {
		if err = json.Unmarshal(field, &requested); err != nil || !requested {
			t.Fatalf("stopRequested must be true or omitted: %s (%v)", raw, err)
		}
	}
	return requested
}

func TestStopRequestedSDKAgent(t *testing.T) {
	c, _, uid, puid := sdkStopFixture(t)
	done := startProjectReceiptOwner(t, c, uid)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + uid})
	if err != nil || !result.Stopped || result.Termination == nil {
		t.Fatalf("stop %+v: %v", result, err)
	}
	<-done
	storage := store.NewStore(store.PathFor(c.paths.StateDir))
	reg, err := storage.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(puid)
	if !stopRequestedJSON(t, pane.Status.ProcessSession) {
		t.Fatal("successful explicit stop lost stopRequested")
	}
	resource, err := c.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + puid})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	var projected core.Pane
	if err = json.Unmarshal(raw, &projected); err != nil || !stopRequestedJSON(t, projected.Status.ProcessSession) {
		t.Fatalf("SDK resource lost stopRequested: %s %v", raw, err)
	}
	before := reg.Clone()
	result, err = c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + uid})
	if err != nil || !result.Stopped {
		t.Fatalf("repeat stop: %+v %v", result, err)
	}
	after, err := storage.LoadReadOnly()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("repeat changed record: %v", err)
	}
}

func TestStopRequestedSDKProject(t *testing.T) {
	c, reg, uid, puid := sdkStopFixture(t)
	done := startProjectReceiptOwner(t, c, uid)
	runner := &stopFailureRunner{path: filepath.Join(c.paths.StateDir, "socket"), session: c.projectSessionName(reg.Projects[0].Spec.Root), uid: reg.Projects[0].Metadata.UID, failure: "no-session"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.stopProjectWithRunner(ctx, core.StopProjectRequest{Ref: "uid:" + reg.Projects[0].Metadata.UID, SocketPath: runner.path}, runner)
	if err != nil || !result.Stopped {
		t.Fatalf("project stop %+v: %v", result, err)
	}
	<-done
	reg, err = store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(puid)
	if !stopRequestedJSON(t, pane.Status.ProcessSession) {
		t.Fatal("project stop lost explicit stopRequested")
	}
}

func TestStopRequestedJSONCompatibility(t *testing.T) {
	for _, raw := range []string{`{"provider":"codex","resumeState":"unknown"}`, `{"provider":"codex","resumeState":"unknown","stopRequested":false}`, `{"provider":"codex","resumeState":"unknown","stopRequested":true}`} {
		var internal m.ProcessSessionRecord
		var public core.ProcessSessionRecord
		if err := json.Unmarshal([]byte(raw), &internal); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &public); err != nil {
			t.Fatal(err)
		}
		want := raw == `{"provider":"codex","resumeState":"unknown","stopRequested":true}`
		if stopRequestedJSON(t, internal) != want || stopRequestedJSON(t, internal.Clone()) != want || stopRequestedJSON(t, public) != want {
			t.Fatalf("compatibility: %s", raw)
		}
	}
}
