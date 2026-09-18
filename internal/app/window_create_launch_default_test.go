package app

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// window_create_launch_default_test.go holds the shared fixtures of the saved
// launch default, the value seam a Window create fills its first Pane through,
// and the boundary that keeps the public `create window` route away from the
// saved mode. The ask-then-fill order is covered in
// window_create_launch_choice_test.go (Window create) and
// project_startup_fresh_test.go (fresh Project open).

const (
	launchDefaultOriginPane = "%41"
	launchDefaultClient     = "/dev/pts/2"
)

// replaceRecorder is the create and delete pair of a replacing producer,
// recorded in the order they were called. Order is the contract: an Agent is
// committed before the shell it replaces is deleted, so a Window is never left
// without a Pane.
type replaceRecorder struct {
	intents   []agentPaneIntent
	created   createdPaneRuntime
	createErr error
	deleted   []string
	deleteErr error
	events    []string
}

func (r *replaceRecorder) createFromIntent(intent agentPaneIntent, _, _ io.Writer) (createdPaneRuntime, error) {
	r.intents = append(r.intents, intent)
	r.events = append(r.events, "create")
	return r.created, r.createErr
}

func (r *replaceRecorder) deletePane(paneID string, _, _ io.Writer) error {
	r.deleted = append(r.deleted, paneID)
	r.events = append(r.events, "delete")
	return r.deleteErr
}

// launchDefaultAICommand is the AI command a Window producer reaches: a
// recorded canonical create, a recorded canonical Pane delete, and a temporary
// home holding the saved mode file.
func launchDefaultAICommand(t *testing.T, home string) (*aiCommand, *replaceRecorder) {
	t.Helper()
	cmd := testAICommand(home)
	recorder := &replaceRecorder{}
	cmd.panes = recorder
	cmd.paneDelete = recorder.deletePane
	return cmd, recorder
}

// TestCanonicalWindowCreateCarriesTheCommittedShellPane runs the real canonical
// Window create over the fake server and proves the runtime placement it
// returns names the shell Pane the transaction committed -- the value seam the
// answer is filled into, with no stdout parsing and no Pane-order guessing.
func TestCanonicalWindowCreateCarriesTheCommittedShellPane(t *testing.T) {
	route := newWindowCreateIntentRoute(t, false, true)
	before := route.windowUIDs()
	origins := [][2]string{}
	route.cmd.launchApply = func(originPaneID, client string, _ launchChoice) launchDefaultResult {
		origins = append(origins, [2]string{originPaneID, client})
		return launchDefaultResult{}
	}

	if err := route.run(); err != nil {
		t.Fatalf("window-create route: %v", err)
	}
	created := route.keptWindow(t, before)
	panes := route.store.registry.PanesOf(created.Metadata.UID)
	if len(panes) != 1 {
		t.Fatalf("created Window Panes = %+v, want its one initial Pane", panes)
	}
	want := [][2]string{{livePaneWithUID(t, route.tmux, panes[0].Metadata.UID), windowCreatePressingClient}}
	if !reflect.DeepEqual(origins, want) {
		t.Fatalf("answer filled into %v, want the committed shell Pane %v", origins, want)
	}
}

// TestPublicCreateWindowNeverReadsTheSavedLaunchDefault is the boundary this
// whole feature sits behind: the saved mode belongs to the UI producers, and a
// typed `projmux create window` still makes the one shell Pane it always made.
//
// The probe is the mode file itself. It is a FIFO with no writer, so any
// process that opens it blocks forever instead of reading a mode; the create
// below returns, which is the evidence that nothing on that route consulted it.
func TestPublicCreateWindowNeverReadsTheSavedLaunchDefault(t *testing.T) {
	configHome := t.TempDir()
	modeDir := filepath.Join(configHome, "projmux")
	if err := os.MkdirAll(modeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modeFile := filepath.Join(modeDir, "tmux-ai-split-mode")
	if err := syscall.Mkfifo(modeFile, 0o600); err != nil {
		t.Skipf("the mode-file read probe needs a FIFO: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, create, "window", "--project", "beta"); err != nil {
		t.Fatalf("create window error = %v", err)
	}

	windows := store.registry.WindowsOf("prj-beta")
	created := windows[len(windows)-1]
	panes := store.registry.PanesOf(created.Metadata.UID)
	if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("created Window Panes = %+v, want exactly one shell Pane", panes)
	}
	if len(store.registry.AgentsOf(created.Metadata.UID)) != 0 {
		t.Fatalf("typed create window opened an Agent: %+v", store.registry.AgentsOf(created.Metadata.UID))
	}
	if _, _, live := tmux.pane(livePaneWithUID(t, tmux, panes[0].Metadata.UID)); live == nil {
		t.Fatalf("the created shell Pane has no live mirror; tmux:\n%s", tmux.state())
	}
}
