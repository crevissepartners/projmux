package mutations

import (
	"context"
	"errors"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type renameProofRunner struct {
	mode           string
	probes, writes int
	stable, tab    string
	cause          error
}

func (r *renameProofRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) < 3 || args[0] != "-S" || args[1] != "/tmp/explicit.sock" {
		return nil, errors.New("unexpected implicit transport")
	}
	a := args[2:]
	switch a[0] {
	case "display-message":
		r.probes++
		pid := "123"
		if (r.mode == "pid" && r.probes >= 2) || (r.mode == "pid-after-write" && r.writes > 0) {
			pid = "124"
		}
		return []byte("/tmp/explicit.sock\\037" + pid + "\n"), nil
	case "list-windows":
		uid := "win-one"
		project := "prj-one"
		if r.mode == "uid" {
			uid = "other"
		}
		if r.mode == "owner" {
			project = "other"
		}
		row := "@1\\037" + uid + "\\037$1\\037" + project + "\\037\\037" + r.stable + "\\037" + r.tab + "\n"
		if r.mode == "duplicate" {
			row += row
		}
		return []byte(row), nil
	case "set-option":
		r.writes++
		if r.cause != nil {
			return nil, r.cause
		}
		r.stable = a[len(a)-1]
		return nil, nil
	case "rename-window":
		r.writes++
		if len(a) != 5 || a[1] != "-t" || a[2] != "@1" || a[3] != "--" {
			return nil, errors.New("unsafe rename argv")
		}
		r.tab = a[4]
		return nil, nil
	}
	return nil, errors.New("unexpected command " + strings.Join(a, " "))
}
func renameProofRegistry() metadata.Registry {
	return metadata.Registry{Projects: []metadata.Project{{Metadata: metadata.ObjectMeta{UID: "prj-one", Name: "project"}}}, Windows: []metadata.Window{{Metadata: metadata.ObjectMeta{UID: "win-one", Name: "old", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindProject, UID: "prj-one"}}}}}
}
func TestRenameExplicitSocketRefusesUnprovenTargets(t *testing.T) {
	for _, mode := range []string{"pid", "uid", "owner", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			reg := renameProofRegistry()
			r := &renameProofRunner{mode: mode, stable: "old", tab: "old"}
			renamer := ExplicitSocketRenamer(Store{}, func() (metadata.Registry, error) { return reg, nil }, "/tmp/explicit.sock", r)
			_, err := renamer.MirrorRenamed(context.Background(), metadata.KindWindow, "win-one", "new")
			if err == nil || r.writes != 0 {
				t.Fatalf("unproven %s: writes=%d err=%v", mode, r.writes, err)
			}
		})
	}
}
func TestRenameExplicitSocketConvergesAndWrapsCause(t *testing.T) {
	reg := renameProofRegistry()
	r := &renameProofRunner{stable: "old", tab: "old"}
	renamer := ExplicitSocketRenamer(Store{}, func() (metadata.Registry, error) { return reg, nil }, "/tmp/explicit.sock", r)
	if _, err := renamer.MirrorRenamed(context.Background(), metadata.KindWindow, "win-one", "-new"); err != nil {
		t.Fatal(err)
	}
	if r.writes != 2 || r.stable != "-new" || r.tab != "-new" {
		t.Fatalf("unconverged: %+v", r)
	}
	cause := errors.New("injected write failure")
	r = &renameProofRunner{stable: "old", tab: "old", cause: cause}
	renamer = ExplicitSocketRenamer(Store{}, func() (metadata.Registry, error) { return reg, nil }, "/tmp/explicit.sock", r)
	if _, err := renamer.MirrorRenamed(context.Background(), metadata.KindWindow, "win-one", "new"); !errors.Is(err, cause) {
		t.Fatalf("lost cause: %v", err)
	}
}
func TestRenameWithoutSocketCallsNoTmux(t *testing.T) {
	reg := renameProofRegistry()
	r := &renameProofRunner{}
	renamer := ExplicitSocketRenamer(Store{}, func() (metadata.Registry, error) { return reg, nil }, "", r)
	notice, err := renamer.MirrorRenamed(context.Background(), metadata.KindWindow, "win-one", "new")
	if err != nil || notice != WindowDisplayNotConvergedNotice("win-one", "new") || r.probes != 0 || r.writes != 0 {
		t.Fatalf("registry-only: %q %v %+v", notice, err, r)
	}
}

type renameContainmentRunner struct {
	control        bool
	mode           string
	writes, probes int
	name           string
}

func (r *renameContainmentRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	a := args[2:]
	switch a[0] {
	case "display-message":
		r.probes++
		pid := "123"
		if r.mode == "pid" && r.probes > 1 {
			pid = "124"
		}
		return []byte("/tmp/explicit.sock\\037" + pid + "\n"), nil
	case "list-sessions":
		role := ""
		if r.mode == "owner" {
			role = "control"
		}
		row := "$1\\037prj-one\\037" + role + "\n"
		if r.mode == "uid" {
			row = "$1\\037other\\037\n"
		}
		if r.mode == "duplicate" {
			row += row
		}
		return []byte(row), nil
	case "list-panes":
		root := "prj-one"
		if r.mode == "owner" {
			root = "other"
		}
		role := ""
		if r.control {
			root = ""
			role = "control"
		}
		if r.control && r.mode == "owner" {
			root = "other"
		}
		if r.mode == "role" {
			role = ""
		}
		row := "%1\\037pan-one\\037@1\\037win-one\\037$1\\037" + root + "\\037" + role + "\n"
		if r.mode == "uid" {
			row = strings.ReplaceAll(row, "pan-one", "other")
		}
		if r.mode == "duplicate" {
			row += row
		}
		return []byte(row), nil
	case "set-option":
		r.writes++
		r.name = a[len(a)-1]
		return nil, nil
	case "show-options":
		return []byte(r.name + "\n"), nil
	}
	return nil, errors.New("unexpected command")
}
func TestRenameProjectPaneExplicitContainment(t *testing.T) {
	for _, kind := range []metadata.Kind{metadata.KindProject, metadata.KindPane} {
		for _, mode := range []string{"", "owner", "uid", "duplicate", "pid"} {
			t.Run(string(kind)+mode, func(t *testing.T) {
				reg := renameProofRegistry()
				reg.Panes = []metadata.Pane{{Metadata: metadata.ObjectMeta{UID: "pan-one", Name: "old", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "win-one"}}}}
				r := &renameContainmentRunner{mode: mode}
				renamer := ExplicitSocketRenamer(Store{}, func() (metadata.Registry, error) { return reg, nil }, "/tmp/explicit.sock", r)
				uid := "prj-one"
				if kind == metadata.KindPane {
					uid = "pan-one"
				}
				_, err := renamer.MirrorRenamed(context.Background(), kind, uid, "new")
				if mode == "" {
					if err != nil || r.writes != 1 {
						t.Fatalf("proof failed: %v writes %d", err, r.writes)
					}
				} else if err == nil || r.writes != 0 {
					t.Fatalf("proof accepted: %v writes %d", err, r.writes)
				}
			})
		}
	}
}

func TestAcceptedRenameFinishesAfterCancellation(t *testing.T) {
	reg := renameProofRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := Store{Mutator: func() metadata.Mutator { return metadata.Mutator{} }, Update: func(fn func(*metadata.Registry) error) (metadata.Registry, error) {
		cancel()
		err := fn(&reg)
		return reg, err
	}}
	renamer := Renamer{Store: writer, Load: func() (metadata.Registry, error) { return reg, nil }}
	committed, _, err := renamer.CommitRename(ctx, metadata.KindProject, "prj-one", "new", func(ctx context.Context, _ metadata.Registry, _ metadata.Mutator) error { return ctx.Err() })
	if err != nil || !committed || reg.Projects[0].Metadata.Name != "new" || ctx.Err() == nil {
		t.Fatalf("accepted cancellation: %v %v", committed, err)
	}
}

func TestRenameExplicitSocketStopsAfterServerReplacement(t *testing.T) {
	reg := renameProofRegistry()
	r := &renameProofRunner{mode: "pid-after-write", stable: "old", tab: "old"}
	renamer := ExplicitSocketRenamer(Store{}, func() (metadata.Registry, error) { return reg, nil }, "/tmp/explicit.sock", r)
	_, err := renamer.MirrorRenamed(context.Background(), metadata.KindWindow, "win-one", "new")
	if err == nil || r.writes != 1 || r.tab != "old" {
		t.Fatalf("replacement did not stop remaining writes: %+v %v", r, err)
	}
}

func TestRenameControlSessionPaneExplicitContainment(t *testing.T) {
	for _, mode := range []string{"", "owner", "role", "uid", "duplicate", "pid"} {
		t.Run(mode, func(t *testing.T) {
			reg := renameProofRegistry()
			reg.Projects = nil
			reg.ControlSessions = []metadata.ControlSession{{Metadata: metadata.ObjectMeta{UID: "ctl-one"}}}
			reg.Windows[0].Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindControlSession, UID: "ctl-one"}
			reg.Panes = []metadata.Pane{{Metadata: metadata.ObjectMeta{UID: "pan-one", Name: "old", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "win-one"}}}}
			runner := &renameContainmentRunner{mode: mode, control: true}
			renamer := ExplicitSocketRenamer(Store{}, func() (metadata.Registry, error) { return reg, nil }, "/tmp/explicit.sock", runner)
			_, err := renamer.MirrorRenamed(t.Context(), metadata.KindPane, "pan-one", "new")
			if mode == "" {
				if err != nil || runner.writes != 1 {
					t.Fatalf("ControlSession proof failed: %v writes=%d", err, runner.writes)
				}
			} else if err == nil || runner.writes != 0 {
				t.Fatalf("unproven ControlSession accepted: %v writes=%d", err, runner.writes)
			}
		})
	}
}
