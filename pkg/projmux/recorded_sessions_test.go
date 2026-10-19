package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

func TestRecordedSessionsCOREParityAndReadOnly(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "metadata"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(state, "metadata", "registry.json")
	if err := os.WriteFile(registry, []byte("corrupt registry"), 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	last := at.Add(time.Hour)
	rows := []sessionhistory.Record{
		{AgentUID: "deleted-b", Provider: "claude", SessionID: "duplicate", ObservedAt: at.Add(2 * time.Hour), Source: sessionhistory.SourceObserved, ProjectUID: "gone", WindowUID: "gone-window", AgentName: "deleted", TranscriptPath: "/never/open"},
		{AgentUID: "deleted-a", Provider: "codex", SessionID: "earlier", ObservedAt: at.Add(-time.Hour), Source: sessionhistory.SourceObserved},
		{AgentUID: "deleted-b", Provider: "claude", SessionID: "duplicate", ObservedAt: at, Source: sessionhistory.SourceEstimated, LastRecordAt: &last},
		{AgentUID: "deleted-b", Provider: "claude", SessionID: "tie-first", ObservedAt: at, Source: sessionhistory.SourceObserved},
		{AgentUID: "deleted-a", Provider: "other", SessionID: "unknown-provider-kept-by-CORE", ObservedAt: at, Source: sessionhistory.SourceObserved},
		{AgentUID: "deleted-b", Provider: "codex", SessionID: "tie-second", ObservedAt: at, Source: sessionhistory.SourceObserved},
	}
	data := []byte("\ninvalid\n{}\n{\"agentUID\":\"missing-session\"}\n{\"sessionId\":\"missing-agent\"}\n")
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, b...)
		data = append(data, '\n')
	}
	path := sessionhistory.Path(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: state, SocketPath: filepath.Join(root, "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, root)
	read, err := sessionhistory.Read(state, "")
	if err != nil {
		t.Fatal(err)
	}
	wantRows := []sessionhistory.Record{}
	for _, uid := range []string{"deleted-b", "deleted-a"} {
		group := []sessionhistory.Record{}
		for _, row := range read.Records {
			if row.AgentUID == uid {
				group = append(group, row)
			}
		}
		wantRows = append(wantRows, sessionhistory.Merge(group, nil)...)
	}
	want, err := json.Marshal(struct {
		Sessions     []sessionhistory.Record `json:"sessions"`
		CorruptLines int                     `json:"corruptLines"`
	}{wantRows, read.Corrupt})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ListRecordedSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(got)
	if err != nil || string(body) != string(want) || got.CorruptLines != 4 {
		t.Fatalf("CORE mismatch: %s want %s: %v", body, want, err)
	}
	if len(got.Sessions) != 5 || got.Sessions[0].SessionID != "tie-first" || got.Sessions[1].SessionID != "tie-second" || got.Sessions[2].ProjectUID != "gone" {
		t.Fatalf("order/merge: %+v", got)
	}
	got.Sessions[2].SessionID = "mutated"
	*got.Sessions[2].LastRecordAt = time.Time{}
	again, err := c.ListRecordedSessions(context.Background())
	body, marshalErr := json.Marshal(again)
	if err != nil || marshalErr != nil || string(body) != string(want) {
		t.Fatalf("result aliases: %s %v %v", body, err, marshalErr)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, root)) {
		t.Fatal("read changed filesystem or Registry")
	}
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListRecordedSessions(context.Background()); err != nil {
		t.Fatalf("missing Registry: %v", err)
	}
}

func TestRecordedSessionsAdmissionAndEmpty(t *testing.T) {
	root := t.TempDir()
	c, err := NewClient(Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "absent")})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, root)
	got, err := c.ListRecordedSessions(context.Background())
	body, _ := json.Marshal(got)
	if err != nil || string(body) != `{"sessions":[],"corruptLines":0}` {
		t.Fatalf("empty: %s %v", body, err)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, root)) {
		t.Fatal("missing history created state")
	}
	sentinel := errors.New("captured paths")
	c.pathsErr = sentinel
	if _, err := c.ListRecordedSessions(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("pathsErr: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListRecordedSessions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel precedence: %v", err)
	}
	c.pathsErr = nil
	c.paths.StateDir = "\x00"
	if _, err := c.ListRecordedSessions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled invalid path: %v", err)
	}
	if _, err := c.ListRecordedSessions(context.Background()); err == nil {
		t.Fatal("invalid path accepted")
	}
	if !reflect.DeepEqual(before, snapshotTree(t, root)) {
		t.Fatal("admission changed state")
	}
}
