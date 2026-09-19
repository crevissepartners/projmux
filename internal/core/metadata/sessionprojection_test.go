package metadata

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	endedSessionSocket = "/tmp/projmux-test/reconciled.sock"
	otherSessionSocket = "/tmp/projmux-test/other.sock"
)

// sessionProjectionFixture registers one Project per root and records the
// given projection on each, in order.
func sessionProjectionFixture(t *testing.T, projections []SessionProjection) (Mutator, Registry, []string) {
	t.Helper()
	roots := dirSet{}
	for i := range projections {
		roots["/src/p"+string(rune('a'+i))] = true
	}
	m := testMutator(roots)
	reg := NewRegistry()
	uids := make([]string, 0, len(projections))
	for i, projection := range projections {
		registered, err := registerFixture(m, &reg, "/src/p"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
		uid := registered.Project.Metadata.UID
		if projection.Live {
			_, err = m.BindLiveProjectSession(&reg, uid, projection.Name, projection.SocketPath)
		} else {
			if _, err = m.BindLiveProjectSession(&reg, uid, projection.Name, projection.SocketPath); err == nil {
				_, err = m.BindOfflineProjectSession(&reg, uid, projection.Name)
			}
		}
		if err != nil {
			t.Fatalf("bind %d: %v", i, err)
		}
		uids = append(uids, uid)
	}
	return m, reg, uids
}

func TestLowerProjectSessionsEndedOnServerLowersOnlyLiveProjectionRecordedOnExactSocketWithAbsentSession(t *testing.T) {
	t.Parallel()

	m, reg, uids := sessionProjectionFixture(t, []SessionProjection{
		{Name: "ended", Live: true, SocketPath: endedSessionSocket},
		{Name: "running", Live: true, SocketPath: endedSessionSocket},
		{Name: "elsewhere", Live: true, SocketPath: otherSessionSocket},
		{Name: "unknown", Live: true},
		{Name: "stopped", Live: false, SocketPath: endedSessionSocket},
	})
	before := reg.Clone()
	later := fixedNow.Add(time.Hour)
	m.Now = func() time.Time { return later }

	lowered := m.LowerProjectSessionsEndedOnServer(&reg, endedSessionSocket, map[string]bool{"running": true, "untagged": true})
	if len(lowered) != 1 || lowered[0].Metadata.UID != uids[0] {
		t.Fatalf("lowered = %+v, want only %s", lowered, uids[0])
	}
	want := SessionProjection{Name: "ended", Live: false, SocketPath: endedSessionSocket}
	got, _ := reg.Project(uids[0])
	if got.Status.Session == nil || *got.Status.Session != want || *lowered[0].Status.Session != want {
		t.Fatalf("lowered projection = %+v (returned %+v), want %+v", got.Status.Session, lowered[0].Status.Session, want)
	}
	for _, uid := range uids[1:] {
		stored, _ := reg.Project(uid)
		previous, _ := before.Project(uid)
		if !reflect.DeepEqual(stored, previous) {
			t.Fatalf("Project %s changed:\nbefore=%+v\nafter=%+v", uid, previous.Status.Session, stored.Status.Session)
		}
	}
	if !reg.UpdatedAt.Equal(later) {
		t.Fatalf("UpdatedAt = %v, want the lower's clock %v", reg.UpdatedAt, later)
	}
	// The value held by the earlier copy is a separate projection.
	if previous, _ := before.Project(uids[0]); !previous.Status.Session.Live {
		t.Fatal("lower wrote through a projection shared with an earlier Registry copy")
	}
}

func TestLowerProjectSessionsEndedOnServerIsIdempotentAndIgnoresEmptySocket(t *testing.T) {
	t.Parallel()

	m, reg, _ := sessionProjectionFixture(t, []SessionProjection{
		{Name: "ended", Live: true, SocketPath: endedSessionSocket},
		{Name: "unknown", Live: true},
	})
	if lowered := m.LowerProjectSessionsEndedOnServer(&reg, "", nil); lowered != nil {
		t.Fatalf("empty socket lowered %+v", lowered)
	}
	if first := m.LowerProjectSessionsEndedOnServer(&reg, endedSessionSocket, nil); len(first) != 1 {
		t.Fatalf("first lower = %+v, want one Project", first)
	}
	settled := reg.Clone()
	m.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	if second := m.LowerProjectSessionsEndedOnServer(&reg, endedSessionSocket, nil); second != nil {
		t.Fatalf("second lower = %+v, want nothing", second)
	}
	if !reflect.DeepEqual(settled, reg) {
		t.Fatal("a lower that lowered nothing changed the Registry")
	}
}

func TestSessionProjectionWritersRecordAndKeepSocketPath(t *testing.T) {
	t.Parallel()

	m, reg, uids := sessionProjectionFixture(t, []SessionProjection{{Name: "projmux", Live: true, SocketPath: endedSessionSocket}})
	uid := uids[0]

	down, err := m.BindOfflineProjectSession(&reg, uid, "projmux")
	if err != nil {
		t.Fatal(err)
	}
	if want := (SessionProjection{Name: "projmux", SocketPath: endedSessionSocket}); *down.Status.Session != want {
		t.Fatalf("offline projection = %+v, want %+v", *down.Status.Session, want)
	}
	// A recomputed offline name still keeps the last live server.
	renamed, err := m.BindOfflineProjectSession(&reg, uid, "projmux-2")
	if err != nil {
		t.Fatal(err)
	}
	if want := (SessionProjection{Name: "projmux-2", SocketPath: endedSessionSocket}); *renamed.Status.Session != want {
		t.Fatalf("renamed offline projection = %+v, want %+v", *renamed.Status.Session, want)
	}
	// A live write records its own route's path, including "unknown".
	moved, err := m.BindLiveProjectSession(&reg, uid, "projmux", otherSessionSocket)
	if err != nil {
		t.Fatal(err)
	}
	if want := (SessionProjection{Name: "projmux", Live: true, SocketPath: otherSessionSocket}); *moved.Status.Session != want {
		t.Fatalf("live projection = %+v, want %+v", *moved.Status.Session, want)
	}
	unknown, err := m.BindLiveProjectSession(&reg, uid, "projmux", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := (SessionProjection{Name: "projmux", Live: true}); *unknown.Status.Session != want {
		t.Fatalf("unverified live projection = %+v, want %+v", *unknown.Status.Session, want)
	}
	for _, bad := range []string{"relative.sock", "/tmp/projmux-test/../x.sock", "/tmp/projmux-test/"} {
		if _, err := m.BindLiveProjectSession(&reg, uid, "projmux", bad); err == nil || !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("socket path %q accepted: %v", bad, err)
		}
	}
}

func TestSessionProjectionSocketPathJSONIsAdditive(t *testing.T) {
	t.Parallel()

	withPath, err := json.Marshal(SessionProjection{Name: "p", Live: true, SocketPath: endedSessionSocket})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(withPath), `{"name":"p","live":true,"socketPath":"`+endedSessionSocket+`"}`; got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
	withoutPath, err := json.Marshal(SessionProjection{Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutPath), "socketPath") {
		t.Fatalf("empty socketPath serialized: %s", withoutPath)
	}
}

func TestImportLegacySessionRecordsObservedSocketPath(t *testing.T) {
	t.Parallel()

	m := testMutator(dirSet{"/src/legacy": true})
	reg := NewRegistry()
	result, err := m.ImportLegacySession(&reg, LegacySession{
		Session: "legacy", Root: "/src/legacy", SocketPath: endedSessionSocket,
		Windows: []LegacyWindow{{Name: "zsh", Panes: []LegacyPane{{Command: "zsh", CWD: "/src/legacy"}}}},
	}, "/bin/zsh", "op-legacy", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, _ := reg.Project(result.Project.Metadata.UID)
	if want := (SessionProjection{Name: "legacy", Live: true, SocketPath: endedSessionSocket}); project.Status.Session == nil || *project.Status.Session != want {
		t.Fatalf("imported projection = %+v, want %+v", project.Status.Session, want)
	}
}
