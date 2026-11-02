package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// The wire form and digest below were computed from origin/main 6470c147,
// before the record moved out of internal/app. They pin sidecar compatibility.
const (
	deferredLaunchGoldenJSON   = "{\"Version\":1,\"Agent\":\"agent-g\",\"Retired\":{\"provider\":\"claude\",\"binding\":{\"hostInstanceID\":\"\",\"projectUID\":\"\",\"windowUID\":\"win-g\",\"agentUID\":\"agent-g\",\"paneUID\":\"pane-g\",\"generation\":\"gen-g\",\"operationID\":\"op-0000000000000001\"},\"sessionID\":\"session-g\",\"connectionID\":\"op-0000000000000001\",\"resumeState\":\"resumable\"},\"OldSpec\":{\"provider\":\"claude\",\"workspace\":{\"cwd\":\"/work\"}},\"NewSpec\":{\"provider\":\"claude\",\"workspace\":{\"cwd\":\"/work\"}},\"OldAnnotations\":{\"projmux.io/model\":\"old\"},\"NewAnnotations\":{\"projmux.io/model\":\"new\"},\"Command\":{\"Path\":\"/bin/claude\",\"Args\":[\"--permission-mode\",\"auto\",\"--resume\",\"session-g\"],\"Dir\":\"/work\",\"Env\":null},\"Files\":{},\"Model\":\"model-g\",\"Effort\":\"effort-g\",\"Attempt\":{\"Source\":{\"provider\":\"claude\",\"binding\":{\"hostInstanceID\":\"\",\"projectUID\":\"\",\"windowUID\":\"win-g\",\"agentUID\":\"agent-g\",\"paneUID\":\"pane-g\",\"generation\":\"gen-g\",\"operationID\":\"op-0000000000000001\"},\"sessionID\":\"session-g\",\"resumeState\":\"\"},\"Target\":{\"hostInstanceID\":\"\",\"projectUID\":\"\",\"windowUID\":\"win-g\",\"agentUID\":\"agent-g\",\"paneUID\":\"pane-g\",\"generation\":\"gen-g\",\"operationID\":\"op-0000000000000001\"}},\"Previous\":null}"
	deferredLaunchGoldenDigest = "63abfb811edd227331fe749a4aa699870f5be5769b5c9527adf2e14139270971"
)

func deferredLaunchGoldenRecord() *DeferredLaunchRecord {
	binding := coremetadata.ProcessBinding{AgentUID: "agent-g", PaneUID: "pane-g", WindowUID: "win-g", Generation: "gen-g", OperationID: "op-0000000000000001"}
	return &DeferredLaunchRecord{
		Version: 1,
		Agent:   "agent-g",
		Retired: coremetadata.ProcessSessionRecord{Provider: "claude", Binding: binding, SessionID: "session-g", ConnectionID: "op-0000000000000001", ResumeState: coremetadata.ProcessResumable},
		OldSpec: coremetadata.AgentSpec{Provider: "claude", Workspace: coremetadata.AgentWorkspace{CWD: "/work"}},
		NewSpec: coremetadata.AgentSpec{Provider: "claude", Workspace: coremetadata.AgentWorkspace{CWD: "/work"}},
		OldAnnotations: map[string]string{"projmux.io/model": "old"},
		NewAnnotations: map[string]string{"projmux.io/model": "new"},
		Command:        processhost.Command{Path: "/bin/claude", Args: []string{"--permission-mode", "auto", "--resume", "session-g"}, Dir: "/work"},
		Files:          map[string]string{},
		Model:          "model-g",
		Effort:         "effort-g",
		Attempt:        &DeferredLaunchAttempt{Source: coremetadata.ProcessSessionRecord{Provider: "claude", Binding: binding, SessionID: "session-g"}, Target: binding},
	}
}

func TestDeferredLaunchDigestMatchesTheRecordBeforeTheMove(t *testing.T) {
	record := deferredLaunchGoldenRecord()
	data, err := json.Marshal(record)
	if err != nil || string(data) != deferredLaunchGoldenJSON {
		t.Fatalf("record wire form changed: %v\n%s", err, data)
	}
	if got := DeferredLaunchDigest(record); got != deferredLaunchGoldenDigest {
		t.Fatalf("digest = %s, want %s", got, deferredLaunchGoldenDigest)
	}
}

func TestReadDeferredLaunchReadsASidecarWrittenBeforeTheMove(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "state", "registry", "registry.json")
	path := DeferredStatePath(registryPath, "deferred-launches", "agent-g")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(deferredLaunchGoldenJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDeferredLaunch(registryPath, "agent-g")
	if err != nil || !reflect.DeepEqual(got, deferredLaunchGoldenRecord()) || DeferredLaunchDigest(got) != deferredLaunchGoldenDigest {
		t.Fatalf("legacy sidecar: %+v err=%v", got, err)
	}
	if _, err = ReadDeferredLaunch(registryPath, "agent-other"); err != nil {
		t.Fatalf("absent sidecar: %v", err)
	}
}

func TestDeferredRefusalKeepsTheResumeRefusedClassification(t *testing.T) {
	err := DeferredRefused("detail")
	if !errors.Is(err, processhost.ErrResumeRefused) || err.Error() != "process-resume-refused: "+processhost.ErrResumeRefused.Error()+": detail" {
		t.Fatalf("refusal = %v", err)
	}
}
