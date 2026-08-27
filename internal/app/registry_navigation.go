package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Registry-first primary navigation seam.
//
// Every primary surface -- the Projects picker, the recent-session picker, the
// recent-window picker -- used to enumerate the machine and then ask the
// Registry about what it found. That inverted the authority: a Project whose
// session was closed disappeared from the list whose whole purpose is reopening
// it, and the same Registry produced different rows depending on which tmux
// server happened to answer.
//
// This seam supplies the other direction. It builds one resolved graph of the
// exact host, projects it through the pure view model, and hands the surfaces
// rows that came from the Registry with the runtime as an overlay.
//
// Three properties are contractual, and they are the same three the Runtime
// escape hatch established -- deliberately, because the two surfaces are two
// projections of one read and a difference between them would be a difference
// in what projmux believes:
//
//   - One exact host. The inherited `$TMUX` socket path, or an explicit socket
//     flag where a caller has one, and nothing else. No default-server probe
//     and no second socket, so a Project's status can never be reported from a
//     server the operator is not on.
//   - No transport is an answer. Outside tmux the rows are still the Registry's
//     rows, in the same order, with their status downgraded to unknown and the
//     reason carried alongside. A read has nothing to protect by failing.
//   - Zero writes. Read-only Registry load, the bounded observation adapter
//     that owns no write verb, a pure projection. A navigation refresh calls no
//     reconciler and materializes nothing.

// registryNavigationReader resolves the exact host into a navigation view.
//
// It is a thin wrapper over the runtime diagnostics reader rather than a second
// implementation of the same three properties. Sharing the reader is what
// guarantees the Main surfaces and the Runtime surface describe one machine:
// two readers would eventually disagree about which socket answered.
type registryNavigationReader struct {
	reader *runtimeDiagnosticsReader
}

func newRegistryNavigationReader(runner tmuxCommandRunner) *registryNavigationReader {
	return &registryNavigationReader{reader: newRuntimeDiagnosticsReader(runner)}
}

// graph resolves one observation of the exact host onto the Registry.
func (r *registryNavigationReader) graph(ctx context.Context) (resourcegraph.Graph, error) {
	if r == nil || r.reader == nil {
		return resourcegraph.Graph{}, errors.New("registry navigation reader is not configured")
	}
	transport, err := r.reader.transport(runtimeTransportRequest{})
	if err != nil {
		return resourcegraph.Graph{}, err
	}
	return r.reader.resolve(ctx, transport)
}

// view builds the navigation model for one invocation.
func (r *registryNavigationReader) view(ctx context.Context, candidates []registryview.Candidate) (registryview.View, error) {
	graph, err := r.graph(ctx)
	if err != nil {
		return registryview.View{}, err
	}
	if err := graph.ValidateWindowRuntimeState(); err != nil {
		return registryview.View{}, fmt.Errorf("validate resource graph Window runtime state: %w", err)
	}
	view := registryview.Build(registryview.Input{Graph: graph, Candidates: candidates})
	if err := view.ValidateWindowRuntimeState(); err != nil {
		return registryview.View{}, fmt.Errorf("validate registry view Window runtime state: %w", err)
	}
	return view, nil
}

// socketPath is the exact `#{socket_path}` of the observed server, used only to
// pin a handoff to the same server the observation was taken through.
func (r *registryNavigationReader) socketPath(ctx context.Context) string {
	if r == nil || r.reader == nil {
		return ""
	}
	transport, err := r.reader.transport(runtimeTransportRequest{})
	if err != nil {
		return ""
	}
	path, _ := r.reader.socketPath(ctx, transport)
	return path
}

// registryNavigationExpectKey is the default key that opens the Registry
// hierarchy of the selected Project from a primary picker.
//
// It is paired with an action id the shipped keybinding catalog does not
// define, which resolves to exactly this literal until a later change gives the
// action a catalog entry. That is the same shape `sessions` uses for its state
// overview, and it is what keeps a new surface from having to land a keybinding
// migration in the same change as the surface itself.
const registryNavigationExpectKey = "ctrl-r"

// registryNavigationActionID is the catalog action id this surface will occupy.
const registryNavigationActionID = "Sidebar:ProjectResources"

const registryNavigationPopupMode = "registry-navigation"

// Navigation action sentinels. They are sentinels rather than argv so a
// selection can never be mistaken for a shell command.
const (
	navActionOpen         = "__projmux_nav_open__"
	navActionStart        = "__projmux_nav_start__"
	navActionStartProject = "__projmux_nav_start_project__"
	navActionResume       = "__projmux_nav_resume__"
	navActionRuntime      = "__projmux_nav_runtime__"
)

type nestedRuntimeCommand interface {
	rawArgvCommand
	nestedNativeArgvCommand
}

// registryNavigationCommand renders the Registry hierarchy of one Project.
//
// It is entered from a primary picker and it lists what the Registry says
// exists: the Project, its Windows, the shell Panes each Window owns, its
// Agents, and each Agent's managed Pane. tmux contributes a status column and
// an exact handle, and nothing else -- which is why the same list appears with
// the same identities and in the same order when there is no tmux server at all.
type registryNavigationCommand struct {
	reader    *registryNavigationReader
	native    intpicker.Runner
	homeDir   func() (string, error)
	lookupEnv func(string) string
	// focus, attach, agent, and runtime are the existing routes this surface
	// hands an exact selector to. They are fields so a test can record which
	// route an action reached and so nothing here grows a second implementation
	// of a shipped behavior.
	focus   rawArgvCommand
	attach  rawArgvCommand
	agent   rawArgvCommand
	runtime nestedRuntimeCommand
}

func newRegistryNavigationCommand(runner tmuxCommandRunner) *registryNavigationCommand {
	return &registryNavigationCommand{
		reader:    newRegistryNavigationReader(runner),
		native:    intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		homeDir:   os.UserHomeDir,
		lookupEnv: os.Getenv,
	}
}

// runProject opens the hierarchy of one Project uid.
//
// The uid is the entry point rather than a path or a session name on purpose:
// the caller resolved a row, and a row's identity is its uid. Re-deriving the
// Project from a path here would reintroduce exactly the filesystem-shaped
// lookup the surface exists to replace.
func (c *registryNavigationCommand) runProject(ctx context.Context, ui, projectUID string, stdout, stderr io.Writer) error {
	if c.reader == nil {
		return errors.New("registry navigation reader is not configured")
	}
	if c.native == nil {
		return errors.New("native picker is not configured")
	}
	locale := appLocale(c.homeDir, c.lookupEnv)
	for {
		view, err := c.reader.view(ctx, nil)
		if err != nil {
			return err
		}
		rows := view.Descendants(registryview.ProjectID(projectUID))
		if len(rows) == 0 {
			return fmt.Errorf("registry navigation: no Registry Project carries uid %q", projectUID)
		}
		nav := registryNavigationView{locale: locale, view: view, rows: rows, now: time.Now().UTC()}
		result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.native, intpickercompat.Options{
			UI:            ui,
			Entries:       nav.entries(),
			Title:         "Projects > Resources",
			Prompt:        "Projects > Resources > ",
			Footer:        registryNavigationFooter(locale),
			ExpectKeys:    []string{"enter"},
			Bindings:      pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, registryNavigationPopupMode, "esc"),
			DisableSearch: false,
		})
		if err != nil {
			return fmt.Errorf("run registry navigation picker: %w", err)
		}
		value := strings.TrimSpace(result.Value)
		if value == "" || value == settingsBackValue {
			return nil
		}
		if value == settingsNoopValue {
			continue
		}
		row, ok := nav.rowByValue(value)
		if !ok {
			continue
		}
		done, err := c.runActions(ctx, nav, row, ui, stdout, stderr)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// runActions opens the action menu of one row.
//
// It returns done=true when an action ran, because every action that runs moves
// the operator somewhere else, and returning to rows resolved before that
// happened would show a machine state that is no longer current.
func (c *registryNavigationCommand) runActions(ctx context.Context, nav registryNavigationView, row registryview.Row, ui string, stdout, stderr io.Writer) (bool, error) {
	socket := ""
	if row.IsLive() {
		socket = c.reader.socketPath(ctx)
	}
	for {
		result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.native, intpickercompat.Options{
			UI:            ui,
			Entries:       nav.actionEntries(row, socket, c.insideTmux()),
			Title:         "Projects > Resources > Actions",
			Prompt:        "Projects > Resources > Actions > ",
			Footer:        registryNavigationActionFooter(nav.locale),
			ExpectKeys:    []string{"enter"},
			Bindings:      pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, registryNavigationPopupMode, "esc"),
			DisableSearch: true,
		})
		if err != nil {
			return false, fmt.Errorf("run registry navigation action picker: %w", err)
		}
		switch strings.TrimSpace(result.Value) {
		case "", settingsBackValue:
			return false, nil
		case settingsNoopValue:
			continue
		case navActionOpen:
			return true, c.runFocus(row, socket, stdout, stderr)
		case navActionStart:
			return true, c.runAttach(row.UID, stdout, stderr)
		case navActionStartProject:
			return true, c.runAttach(registryNavigationProjectUID(nav.view, row), stdout, stderr)
		case navActionResume:
			return true, c.runResume(row, stdout, stderr)
		case navActionRuntime:
			return true, c.runRuntime(stdout, stderr)
		default:
			continue
		}
	}
}

// runFocus hands the existing focus route the exact coordinate and the exact
// socket the observation was taken through.
func (c *registryNavigationCommand) runFocus(row registryview.Row, socket string, stdout, stderr io.Writer) error {
	if c.focus == nil {
		return errors.New("registry navigation: the focus handler is not configured")
	}
	if row.Runtime == nil || strings.TrimSpace(row.Runtime.Target) == "" {
		return fmt.Errorf("registry navigation: %s %s has no exact runtime coordinate", row.Kind, row.Name)
	}
	args := []string{"--target", row.Runtime.Target}
	if socket != "" {
		args = append(args, "--socket", socket)
	}
	return c.focus.Run(args, stdout, stderr)
}

// runAttach forwards to the shipped outside-tmux Project entry point, which is
// the one route that already owns offline Project activation.
func (c *registryNavigationCommand) runAttach(projectUID string, stdout, stderr io.Writer) error {
	if c.attach == nil {
		return errors.New("registry navigation: the attach handler is not configured")
	}
	projectUID = strings.TrimSpace(projectUID)
	if projectUID == "" {
		return errors.New("registry navigation: this row has no owning Registry Project")
	}
	return c.attach.Run([]string{"project", "uid:" + projectUID}, stdout, stderr)
}

// runResume forwards to the shipped `agent resume` route unchanged.
func (c *registryNavigationCommand) runResume(row registryview.Row, stdout, stderr io.Writer) error {
	if c.agent == nil {
		return errors.New("registry navigation: the agent handler is not configured")
	}
	return c.agent.Run([]string{"resume", "uid:" + row.UID}, stdout, stderr)
}

// runRuntime opens the Runtime diagnostics surface unchanged.
func (c *registryNavigationCommand) runRuntime(stdout, stderr io.Writer) error {
	if c.runtime == nil {
		return errors.New("registry navigation: the runtime diagnostics handler is not configured")
	}
	return c.runtime.RunNested([]string{"diagnostics"}, stdout, stderr)
}

func (c *registryNavigationCommand) insideTmux() bool {
	if c.lookupEnv == nil {
		return false
	}
	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

// registryNavigationProjectUID walks a row back to the Project that owns it.
func registryNavigationProjectUID(view registryview.View, row registryview.Row) string {
	for row.Kind != registryview.RowKindProject {
		parent, ok := view.Row(row.ParentID)
		if !ok {
			return ""
		}
		row = parent
	}
	return row.UID
}
