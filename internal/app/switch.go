package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/es5h/projmux/internal/config"
	"github.com/es5h/projmux/internal/core/candidates"
	"github.com/es5h/projmux/internal/core/pins"
	corepreview "github.com/es5h/projmux/internal/core/preview"
	coretags "github.com/es5h/projmux/internal/core/tags"
	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
	intfzf "github.com/es5h/projmux/internal/ui/fzf"
	intrender "github.com/es5h/projmux/internal/ui/render"
)

const (
	switchUIFlag             = "ui"
	switchUIPopup            = "popup"
	switchUISidebar          = "sidebar"
	switchTagExpectKey       = "alt-t"
	switchPinExpectKey       = "alt-p"
	switchSettingsSentinel   = "__projmux_settings__"
	switchContextSessionEnv  = "TMUX_SESSIONIZER_CONTEXT_SESSION"
	managedRootsEnvVar       = "PROJMUX_MANAGED_ROOTS"
	legacyManagedRootsEnvVar = "TMUX_SESSIONIZER_ROOTS"
	repoRootEnvVar           = "RP"
)

type candidateDiscoverer func(inputs candidates.Inputs) ([]string, error)

type switchPinStore interface {
	List() ([]string, error)
	Add(path string) error
	Toggle(path string) (bool, error)
	Clear() error
}

type switchPinStoreFactory func() (switchPinStore, error)

type switchTagStore interface {
	List() ([]string, error)
	Toggle(name string) (bool, error)
}

type switchTagStoreFactory func() (switchTagStore, error)

type switchRunner interface {
	Run(options intfzf.Options) (intfzf.Result, error)
}

type switchSessionExecutor interface {
	EnsureSession(ctx context.Context, sessionName, cwd string) error
	OpenSession(ctx context.Context, sessionName string) error
}

type switchSessionInspector interface {
	SessionExists(ctx context.Context, sessionName string) (bool, error)
}

type switchPreviewStore interface {
	ReadSelection(sessionName string) (corepreview.Selection, bool, error)
	CyclePaneSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
	CycleWindowSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
}

type switchCommand struct {
	discover        candidateDiscoverer
	pinStore        switchPinStoreFactory
	tagStore        switchTagStoreFactory
	runner          switchRunner
	sessions        switchSessionExecutor
	previewStore    switchPreviewStore
	previewStoreErr error
	inventory       previewInventory
	inventoryErr    error
	executable      func() (string, error)
	identity        sessionIdentityResolver
	identityErr     error
	validate        func(path string) error
	homeDir         func() (string, error)
	workingDir      func() (string, error)
	lookupEnv       func(string) string
	gitBranch       func(string) string
}

type switchPlan struct {
	UI            string
	Candidates    []string
	Rows          []intfzf.Entry
	SessionNames  map[string]string
	Action        string
	Selection     string
	SessionName   string
	HomeDir       string
	CurrentPath   string
	OriginSession string
}

func newSwitchCommand() *switchCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	identity, err := newDefaultCurrentIdentityResolver()
	paths, pathsErr := config.DefaultPathsFromEnv()

	cmd := &switchCommand{
		discover:    candidates.Discover,
		pinStore:    newDefaultSwitchPinStore,
		tagStore:    newDefaultSwitchTagStore,
		runner:      intfzf.NewRunner(),
		sessions:    client,
		inventory:   tmuxPreviewInventory{client: client},
		executable:  os.Executable,
		identity:    identity,
		identityErr: err,
		validate:    validateDirectory,
		homeDir:     os.UserHomeDir,
		workingDir:  os.Getwd,
		lookupEnv:   os.Getenv,
		gitBranch:   detectGitBranch,
	}
	if pathsErr != nil {
		cmd.previewStoreErr = fmt.Errorf("resolve default config paths: %w", pathsErr)
		return cmd
	}
	cmd.previewStore = corepreview.NewDefaultStore(paths)
	return cmd
}

func newDefaultSwitchPinStore() (switchPinStore, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, err
	}

	store := pins.NewDefaultStore(paths)
	return store, nil
}

func newDefaultSwitchTagStore() (switchTagStore, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, err
	}

	return coretags.NewDefaultStore(paths), nil
}

// Run resolves the first sessionizer candidate list and opens the first
// interactive picker surface.
func (c *switchCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "toggle-tag":
			return c.runToggleTag(args[1:], stdout, stderr)
		case "toggle-pin":
			return c.runTogglePin(args[1:], stdout, stderr)
		case "settings":
			return c.runSettings(stdout, stderr)
		case "preview":
			return c.runPreview(args[1:], stdout, stderr)
		case "cycle-pane":
			return c.runCyclePane(args[1:], stderr)
		case "cycle-window":
			return c.runCycleWindow(args[1:], stderr)
		case "sidebar-focus":
			return c.runSidebarFocus(args[1:], stdout, stderr)
		}
	}

	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printSwitchUsage(stderr)
	}

	ui := fs.String(switchUIFlag, switchUIPopup, "future sessionizer surface to prepare")
	if err := fs.Parse(args); err != nil {
		printSwitchUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printSwitchUsage(stderr)
		return fmt.Errorf("switch does not accept positional arguments")
	}
	if err := validateSwitchUI(*ui); err != nil {
		printSwitchUsage(stderr)
		return err
	}

	ctx := context.Background()
	for {
		plan, err := c.plan(*ui)
		if err != nil {
			return err
		}

		reopen, err := c.execute(ctx, plan, stdout)
		if err != nil {
			return err
		}
		if !reopen {
			return nil
		}
	}
}

func (c *switchCommand) plan(ui string) (switchPlan, error) {
	inputs, err := c.candidateInputs("")
	if err != nil {
		return switchPlan{}, err
	}

	return c.planFromInputs(ui, inputs)
}

func (c *switchCommand) runToggleTag(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("switch toggle-tag", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printSwitchUsage(stderr)
	}

	if err := fs.Parse(args); err != nil {
		printSwitchUsage(stderr)
		return err
	}
	if fs.NArg() > 1 {
		printSwitchUsage(stderr)
		return fmt.Errorf("switch toggle-tag accepts at most 1 [path] argument")
	}

	target, err := c.resolveToggleTagTarget(fs.Args())
	if err != nil {
		if strings.Contains(err.Error(), "switch toggle-tag requires") {
			printSwitchUsage(stderr)
		}
		return err
	}

	return c.toggleTag(target, stdout)
}

func (c *switchCommand) runTogglePin(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("switch toggle-pin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printSwitchUsage(stderr)
	}

	if err := fs.Parse(args); err != nil {
		printSwitchUsage(stderr)
		return err
	}
	if fs.NArg() > 1 {
		printSwitchUsage(stderr)
		return fmt.Errorf("switch toggle-pin accepts at most 1 [path] argument")
	}

	target, err := c.resolveSwitchTarget(fs.Args(), "switch toggle-pin")
	if err != nil {
		if strings.Contains(err.Error(), "switch toggle-pin requires") {
			printSwitchUsage(stderr)
		}
		return err
	}

	return c.togglePin(target, stdout)
}

func (c *switchCommand) runPreview(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("switch preview", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printSwitchUsage(stderr)
	}
	ui := fs.String(switchUIFlag, switchUIPopup, "preview surface to render")

	if err := fs.Parse(args); err != nil {
		printSwitchUsage(stderr)
		return err
	}
	if err := validateSwitchUI(*ui); err != nil {
		printSwitchUsage(stderr)
		return err
	}
	if fs.NArg() > 1 {
		printSwitchUsage(stderr)
		return fmt.Errorf("switch preview accepts at most 1 [path] argument")
	}
	if fs.NArg() == 1 && strings.TrimSpace(fs.Arg(0)) == switchSettingsSentinel {
		return c.writeSettingsPreview(stdout)
	}

	target, err := c.resolveSwitchTarget(fs.Args(), "switch preview")
	if err != nil {
		if strings.Contains(err.Error(), "switch preview requires") {
			printSwitchUsage(stderr)
		}
		return err
	}
	if target == switchSettingsSentinel {
		return c.writeSettingsPreview(stdout)
	}

	model, err := c.previewModel(context.Background(), target)
	if err != nil {
		return err
	}

	_, err = io.WriteString(stdout, intrender.RenderSwitchPreview(model, *ui))
	return err
}

func (c *switchCommand) runSettings(stdout, stderr io.Writer) error {
	if c.runner == nil {
		return fmt.Errorf("switch runner is not configured")
	}

	for {
		entries, err := c.settingsEntries()
		if err != nil {
			return err
		}

		result, err := c.runner.Run(intfzf.Options{
			UI:      "settings",
			Entries: entries,
		})
		if err != nil {
			return fmt.Errorf("run switch settings picker: %w", err)
		}

		action := cleanOptionalPath(result.Value)
		if action == "" {
			return nil
		}

		switch {
		case action == "add-interactive":
			if err := c.runAddPinInteractive(stdout); err != nil {
				return err
			}
		case strings.HasPrefix(action, "add:"):
			target := strings.TrimPrefix(action, "add:")
			if err := c.addPin(target, stdout); err != nil {
				return err
			}
		case action == "clear":
			if err := c.clearPins(); err != nil {
				return err
			}
			if stdout != nil {
				_, _ = fmt.Fprintln(stdout, "cleared pins")
			}
		case strings.HasPrefix(action, "pin:"):
			target := strings.TrimPrefix(action, "pin:")
			if err := c.togglePin(target, stdout); err != nil {
				return err
			}
		default:
			printSwitchUsage(stderr)
			return fmt.Errorf("unknown switch settings action: %s", action)
		}
	}
}

func (c *switchCommand) runAddPinInteractive(stdout io.Writer) error {
	if c.runner == nil {
		return fmt.Errorf("switch runner is not configured")
	}

	entries, err := c.addPinEntries()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	result, err := c.runner.Run(intfzf.Options{
		UI:      "pin",
		Entries: entries,
	})
	if err != nil {
		return fmt.Errorf("run switch add-pin picker: %w", err)
	}

	target := cleanOptionalPath(result.Value)
	if target == "" {
		return nil
	}

	return c.addPin(target, stdout)
}

func (c *switchCommand) runCyclePane(args []string, stderr io.Writer) error {
	return c.runCycle("switch cycle-pane", args, stderr, func(store switchPreviewStore, sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error) {
		return store.CyclePaneSelection(sessionName, windows, panes, direction)
	})
}

func (c *switchCommand) runCycleWindow(args []string, stderr io.Writer) error {
	return c.runCycle("switch cycle-window", args, stderr, func(store switchPreviewStore, sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error) {
		return store.CycleWindowSelection(sessionName, windows, panes, direction)
	})
}

func (c *switchCommand) planFromInputs(ui string, inputs candidates.Inputs) (switchPlan, error) {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return switchPlan{}, err
	}

	if c.discover == nil {
		return switchPlan{}, fmt.Errorf("switch candidate discovery is not configured")
	}

	if inputs.HomeDir == "" {
		inputs.HomeDir = homeDir
	}

	paths, err := c.discover(inputs)
	if err != nil {
		return switchPlan{}, fmt.Errorf("discover switch candidates: %w", err)
	}
	paths = append(paths, switchSettingsSentinel)

	plan := switchPlan{
		UI:            ui,
		Candidates:    paths,
		HomeDir:       homeDir,
		CurrentPath:   cleanOptionalPath(inputs.CurrentPath),
		OriginSession: strings.TrimSpace(c.env(switchContextSessionEnv)),
	}

	return c.completePlan(plan)
}

func (c *switchCommand) candidateInputs(currentPath string) (candidates.Inputs, error) {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return candidates.Inputs{}, err
	}

	pins, err := c.loadPins()
	if err != nil {
		return candidates.Inputs{}, err
	}

	if currentPath == "" {
		currentPath, err = c.resolveWorkingDir()
		if err != nil {
			return candidates.Inputs{}, err
		}
	}

	return candidates.Inputs{
		HomeDir:      homeDir,
		RepoRoot:     cleanOptionalPath(c.env(repoRootEnvVar)),
		ManagedRoots: switchManagedRoots(homeDir, c.env(repoRootEnvVar), c.lookupEnv),
		Pins:         pins,
		CurrentPath:  currentPath,
	}, nil
}

func (c *switchCommand) resolveHomeDir() (string, error) {
	if c.homeDir == nil {
		return "", fmt.Errorf("switch home directory resolver is not configured")
	}

	homeDir, err := c.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Clean(homeDir), nil
}

func (c *switchCommand) loadPins() ([]string, error) {
	store, err := c.loadPinStore()
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, nil
	}

	paths, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("load pin set: %w", err)
	}

	return paths, nil
}

func (c *switchCommand) loadPinStore() (switchPinStore, error) {
	if c.pinStore == nil {
		return nil, nil
	}

	store, err := c.pinStore()
	if err != nil {
		return nil, fmt.Errorf("configure pin store: %w", err)
	}
	if store == nil {
		return nil, nil
	}

	return store, nil
}

func (c *switchCommand) resolveWorkingDir() (string, error) {
	if c.workingDir == nil {
		return "", fmt.Errorf("switch working directory resolver is not configured")
	}

	path, err := c.workingDir()
	if err != nil {
		return "", fmt.Errorf("resolve current working directory: %w", err)
	}

	return filepath.Clean(path), nil
}

func (c *switchCommand) resolveSwitchTarget(args []string, command string) (string, error) {
	inputPath, err := c.resolveSwitchInput(args, command)
	if err != nil {
		return "", err
	}

	inputs, err := c.candidateInputs(inputPath)
	if err != nil {
		return "", err
	}
	if c.discover == nil {
		return "", fmt.Errorf("switch candidate discovery is not configured")
	}

	candidatePaths, err := c.discover(inputs)
	if err != nil {
		return "", fmt.Errorf("discover switch candidates: %w", err)
	}

	target := bestSwitchCandidateMatch(inputPath, candidatePaths)
	if target == "" {
		return "", fmt.Errorf("resolve switch tag target: no switch candidate matched %q", inputPath)
	}

	return target, nil
}

func (c *switchCommand) resolveToggleTagTarget(args []string) (string, error) {
	return c.resolveSwitchTarget(args, "switch toggle-tag")
}

func (c *switchCommand) resolveSwitchInput(args []string, command string) (string, error) {
	var path string

	switch len(args) {
	case 0:
		var err error
		path, err = c.resolveWorkingDir()
		if err != nil {
			return "", err
		}
	case 1:
		if strings.TrimSpace(args[0]) == "" {
			return "", fmt.Errorf("%s requires a non-empty [path] argument", command)
		}
		path = args[0]
	default:
		return "", fmt.Errorf("%s accepts at most 1 [path] argument", command)
	}

	if !filepath.IsAbs(path) {
		workingDir, err := c.resolveWorkingDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(workingDir, path)
	}
	path = filepath.Clean(path)

	if c.validate == nil {
		return "", fmt.Errorf("switch directory validator is not configured")
	}
	if err := c.validate(path); err != nil {
		return "", fmt.Errorf("validate switch tag path %q: %w", path, err)
	}

	return path, nil
}

func (c *switchCommand) previewModel(ctx context.Context, target string) (corepreview.SwitchReadModel, error) {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return corepreview.SwitchReadModel{}, err
	}
	repoRoot := cleanOptionalPath(c.env(repoRootEnvVar))

	if c.identityErr != nil {
		return corepreview.SwitchReadModel{}, fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return corepreview.SwitchReadModel{}, fmt.Errorf("switch session identity resolver is not configured")
	}

	sessionName, err := c.identity.SessionIdentityForPath(target)
	if err != nil {
		return corepreview.SwitchReadModel{}, fmt.Errorf("resolve switch preview session identity: %w", err)
	}

	modelInputs := corepreview.SwitchReadModelInputs{
		Path:        target,
		DisplayPath: intrender.PrettyPath(target, homeDir, repoRoot),
		SessionName: sessionName,
		GitBranch:   c.resolveGitBranch(target),
	}

	exists, err := c.switchSessionExists(ctx, sessionName)
	if err != nil {
		return corepreview.SwitchReadModel{}, err
	}
	modelInputs.SessionExists = exists
	if !exists {
		return corepreview.BuildSwitchReadModel(modelInputs), nil
	}

	store, err := c.requireSwitchPreviewStore()
	if err != nil {
		return corepreview.SwitchReadModel{}, err
	}
	inventory, err := c.requireSwitchPreviewInventory()
	if err != nil {
		return corepreview.SwitchReadModel{}, err
	}

	selection, hasSelection, err := store.ReadSelection(sessionName)
	if err != nil {
		return corepreview.SwitchReadModel{}, fmt.Errorf("load switch preview selection for %q: %w", sessionName, err)
	}
	windows, err := inventory.SessionWindows(ctx, sessionName)
	if err != nil {
		return corepreview.SwitchReadModel{}, fmt.Errorf("load switch preview windows for %q: %w", sessionName, err)
	}
	panes, err := inventory.SessionPanes(ctx, sessionName)
	if err != nil {
		return corepreview.SwitchReadModel{}, fmt.Errorf("load switch preview panes for %q: %w", sessionName, err)
	}

	modelInputs.StoredSelection = selection
	modelInputs.HasStoredSelection = hasSelection
	modelInputs.Windows = windows
	modelInputs.Panes = panes
	return corepreview.BuildSwitchReadModel(modelInputs), nil
}

type switchCycleFunc func(store switchPreviewStore, sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)

func (c *switchCommand) runCycle(command string, args []string, stderr io.Writer, cycle switchCycleFunc) error {
	target, direction, err := c.parseCycleArgs(command, args, stderr)
	if err != nil {
		return err
	}

	ctx := context.Background()

	if c.identityErr != nil {
		return fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return fmt.Errorf("switch session identity resolver is not configured")
	}

	sessionName, err := c.identity.SessionIdentityForPath(target)
	if err != nil {
		return fmt.Errorf("resolve switch cycle session identity: %w", err)
	}

	exists, err := c.switchSessionExists(ctx, sessionName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	store, err := c.requireSwitchPreviewStore()
	if err != nil {
		return err
	}
	inventory, err := c.requireSwitchPreviewInventory()
	if err != nil {
		return err
	}

	windows, err := inventory.SessionWindows(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("load switch cycle windows for %q: %w", sessionName, err)
	}
	panes, err := inventory.SessionPanes(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("load switch cycle panes for %q: %w", sessionName, err)
	}

	if _, err := cycle(store, sessionName, windows, panes, direction); err != nil {
		return fmt.Errorf("%s for %q: %w", command, sessionName, err)
	}

	return nil
}

func (c *switchCommand) parseCycleArgs(command string, args []string, stderr io.Writer) (string, corepreview.Direction, error) {
	if len(args) != 2 {
		printSwitchUsage(stderr)
		return "", "", fmt.Errorf("%s requires exactly 2 arguments: <path> <next|prev>", command)
	}

	target, err := c.resolveSwitchTarget(args[:1], command)
	if err != nil {
		if strings.Contains(err.Error(), "requires a non-empty") {
			printSwitchUsage(stderr)
		}
		return "", "", err
	}

	direction, err := parsePreviewDirection(args[1])
	if err != nil {
		printSwitchUsage(stderr)
		return "", "", fmt.Errorf("%s: %w", command, err)
	}

	return target, direction, nil
}

func (c *switchCommand) switchSessionExists(ctx context.Context, sessionName string) (bool, error) {
	inspector, ok := c.sessions.(switchSessionInspector)
	if !ok || inspector == nil {
		return false, nil
	}

	exists, err := inspector.SessionExists(ctx, sessionName)
	if err != nil {
		return false, fmt.Errorf("check existing switch session %q: %w", sessionName, err)
	}
	return exists, nil
}

func (c *switchCommand) requireSwitchPreviewStore() (switchPreviewStore, error) {
	if c.previewStoreErr != nil {
		return nil, fmt.Errorf("configure switch preview store: %w", c.previewStoreErr)
	}
	if c.previewStore == nil {
		return nil, errors.New("configure switch preview store: switch preview store is not configured")
	}
	return c.previewStore, nil
}

func (c *switchCommand) requireSwitchPreviewInventory() (previewInventory, error) {
	if c.inventoryErr != nil {
		return nil, fmt.Errorf("configure switch preview inventory: %w", c.inventoryErr)
	}
	if c.inventory == nil {
		return nil, errors.New("configure switch preview inventory: switch preview inventory is not configured")
	}
	return c.inventory, nil
}

func (c *switchCommand) loadTagStore() (switchTagStore, error) {
	if c.tagStore == nil {
		return nil, errors.New("configure switch tag store: tag store is not configured")
	}

	store, err := c.tagStore()
	if err != nil {
		return nil, fmt.Errorf("configure switch tag store: %w", err)
	}
	if store == nil {
		return nil, errors.New("configure switch tag store: tag store is not configured")
	}

	return store, nil
}

func (c *switchCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func switchManagedRoots(homeDir, repoRoot string, lookup func(string) string) []string {
	roots := make([]string, 0)
	seen := make(map[string]struct{})

	for _, value := range []string{
		envValue(lookup, managedRootsEnvVar),
		envValue(lookup, legacyManagedRootsEnvVar),
	} {
		for _, root := range filepath.SplitList(value) {
			root = cleanOptionalPath(root)
			if root == "" {
				continue
			}
			if _, ok := seen[root]; ok {
				continue
			}
			seen[root] = struct{}{}
			roots = append(roots, root)
		}
	}

	if len(roots) == 0 {
		for _, root := range defaultManagedRoots(homeDir, repoRoot) {
			if _, ok := seen[root]; ok {
				continue
			}
			seen[root] = struct{}{}
			roots = append(roots, root)
		}
	}

	return roots
}

func defaultManagedRoots(homeDir, repoRoot string) []string {
	roots := make([]string, 0, 6)
	for _, root := range []string{
		filepath.Join(homeDir, "source"),
		filepath.Join(homeDir, "work"),
		filepath.Join(homeDir, "projects"),
		filepath.Join(homeDir, "src"),
		filepath.Join(homeDir, "code"),
		repoRoot,
	} {
		root = cleanOptionalPath(root)
		if root == "" {
			continue
		}
		roots = append(roots, root)
	}

	return roots
}

func envValue(lookup func(string) string, name string) string {
	if lookup == nil {
		return ""
	}
	return lookup(name)
}

func cleanOptionalPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Clean(path)
}

func bestSwitchCandidateMatch(path string, candidatePaths []string) string {
	cleanPath := filepath.Clean(path)
	best := ""

	for _, candidatePath := range candidatePaths {
		candidatePath = filepath.Clean(candidatePath)
		if candidatePath == cleanPath {
			return candidatePath
		}

		prefix := candidatePath + string(filepath.Separator)
		if !strings.HasPrefix(cleanPath, prefix) {
			continue
		}
		if len(candidatePath) > len(best) {
			best = candidatePath
		}
	}

	return best
}

func validateSwitchUI(ui string) error {
	switch ui {
	case switchUIPopup, switchUISidebar:
		return nil
	default:
		return fmt.Errorf("invalid --ui value %q: expected %q or %q", ui, switchUIPopup, switchUISidebar)
	}
}

func (c *switchCommand) completePlan(plan switchPlan) (switchPlan, error) {
	if c.runner == nil {
		return switchPlan{}, fmt.Errorf("switch picker is not configured")
	}
	if c.identityErr != nil {
		return switchPlan{}, fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return switchPlan{}, fmt.Errorf("switch session identity resolver is not configured")
	}

	rows, sessionNames, err := c.renderRows(context.Background(), plan.UI, plan.Candidates)
	if err != nil {
		return switchPlan{}, err
	}
	plan.Rows = rows
	plan.SessionNames = sessionNames

	result, err := c.runPicker(plan)
	if err != nil {
		return switchPlan{}, err
	}
	plan.Action = strings.TrimSpace(result.Key)
	selection := result.Value
	selection = cleanOptionalPath(selection)
	plan.Selection = selection
	if selection == "" {
		return plan, nil
	}
	if selection == switchSettingsSentinel {
		return plan, nil
	}

	if c.validate == nil {
		return switchPlan{}, fmt.Errorf("switch directory validator is not configured")
	}
	if err := c.validate(selection); err != nil {
		return switchPlan{}, err
	}

	if plan.Action != switchTagExpectKey {
		sessionName, err := c.identity.SessionIdentityForPath(selection)
		if err != nil {
			return switchPlan{}, fmt.Errorf("resolve session identity: %w", err)
		}
		plan.SessionName = sessionName
	}

	return plan, nil
}

func (c *switchCommand) execute(ctx context.Context, plan switchPlan, stdout io.Writer) (bool, error) {
	if plan.Selection == "" {
		return false, nil
	}
	if plan.Selection == switchSettingsSentinel {
		if err := c.runSettings(stdout, io.Discard); err != nil {
			return false, err
		}
		return false, nil
	}
	if plan.Action == switchTagExpectKey {
		if err := c.toggleTag(plan.Selection, nil); err != nil {
			return false, err
		}
		return true, nil
	}
	if plan.Action == switchPinExpectKey {
		if plan.Selection == switchSettingsSentinel {
			return false, nil
		}
		if err := c.togglePin(plan.Selection, nil); err != nil {
			return false, err
		}
		return true, nil
	}
	if plan.SessionName == "" {
		return false, fmt.Errorf("switch command requires a target session")
	}
	if c.sessions == nil {
		return false, fmt.Errorf("switch session executor is not configured")
	}

	if err := c.sessions.EnsureSession(ctx, plan.SessionName, plan.Selection); err != nil {
		return false, fmt.Errorf("ensure tmux session %q: %w", plan.SessionName, err)
	}
	if err := c.sessions.OpenSession(ctx, plan.SessionName); err != nil {
		return false, fmt.Errorf("open tmux session %q: %w", plan.SessionName, err)
	}

	return false, nil
}

func (c *switchCommand) runSidebarFocus(args []string, _ io.Writer, stderr io.Writer) error {
	if len(args) != 1 {
		printSwitchUsage(stderr)
		return fmt.Errorf("switch sidebar-focus requires exactly 1 argument: <path>")
	}

	target := cleanOptionalPath(args[0])
	if target == "" || target == switchSettingsSentinel {
		return nil
	}

	if c.identityErr != nil {
		return fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return fmt.Errorf("switch session identity resolver is not configured")
	}
	if c.sessions == nil {
		return fmt.Errorf("switch session executor is not configured")
	}

	sessionName, err := c.identity.SessionIdentityForPath(target)
	if err != nil {
		return fmt.Errorf("resolve sidebar focus session identity: %w", err)
	}
	exists, err := c.switchSessionExists(context.Background(), sessionName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := c.sessions.OpenSession(context.Background(), sessionName); err != nil {
		return fmt.Errorf("open tmux session %q on sidebar focus: %w", sessionName, err)
	}
	return nil
}

func (c *switchCommand) runPicker(plan switchPlan) (intfzf.Result, error) {
	if c.runner == nil {
		return intfzf.Result{}, fmt.Errorf("switch runner is not configured")
	}

	options := intfzf.Options{
		UI:         plan.UI,
		Candidates: plan.Candidates,
		Entries:    plan.Rows,
		Prompt:     "› ",
		Footer:     switchPickerFooter(plan.UI),
		ExpectKeys: []string{switchTagExpectKey, switchPinExpectKey},
	}
	if previewCommand, bindings, err := c.switchPickerSurface(plan); err != nil {
		return intfzf.Result{}, err
	} else if previewCommand != "" {
		options.PreviewCommand = previewCommand
		options.PreviewWindow = switchPreviewWindow(plan.UI)
		options.Bindings = bindings
	}

	result, err := c.runner.Run(options)
	if err != nil {
		return intfzf.Result{}, fmt.Errorf("run switch picker: %w", err)
	}

	return result, nil
}

func (c *switchCommand) switchPickerSurface(plan switchPlan) (string, []string, error) {
	if c.executable == nil {
		return "", nil, nil
	}

	binaryPath, err := c.executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolve switch preview executable: %w", err)
	}

	previewCommand, err := inttmux.BuildSwitchPreviewCommand(binaryPath, plan.UI)
	if err != nil {
		return "", nil, fmt.Errorf("build switch preview command: %w", err)
	}

	windowPrev, err := inttmux.BuildSwitchCycleWindowCommand(binaryPath, string(corepreview.DirectionPrev))
	if err != nil {
		return "", nil, fmt.Errorf("build switch window-prev command: %w", err)
	}
	windowNext, err := inttmux.BuildSwitchCycleWindowCommand(binaryPath, string(corepreview.DirectionNext))
	if err != nil {
		return "", nil, fmt.Errorf("build switch window-next command: %w", err)
	}
	panePrev, err := inttmux.BuildSwitchCyclePaneCommand(binaryPath, string(corepreview.DirectionPrev))
	if err != nil {
		return "", nil, fmt.Errorf("build switch pane-prev command: %w", err)
	}
	paneNext, err := inttmux.BuildSwitchCyclePaneCommand(binaryPath, string(corepreview.DirectionNext))
	if err != nil {
		return "", nil, fmt.Errorf("build switch pane-next command: %w", err)
	}

	bindings := []string{}
	if plan.UI == switchUISidebar {
		if pos := switchSidebarInitialPos(plan); pos > 0 {
			bindings = append(bindings, fmt.Sprintf("start:pos(%d)", pos))
		}
		sidebarFocus, err := inttmux.BuildSwitchSidebarFocusCommand(binaryPath)
		if err != nil {
			return "", nil, fmt.Errorf("build switch sidebar-focus command: %w", err)
		}
		bindings = append(bindings, "focus:execute-silent("+sidebarFocus+")")
		return previewCommand, bindings, nil
	}

	bindings = append(bindings,
		"left:execute-silent("+windowPrev+")+refresh-preview",
		"right:execute-silent("+windowNext+")+refresh-preview",
		"alt-up:execute-silent("+panePrev+")+refresh-preview",
		"alt-down:execute-silent("+paneNext+")+refresh-preview",
	)

	return previewCommand, bindings, nil
}

func switchPreviewWindow(ui string) string {
	switch ui {
	case switchUISidebar:
		return "down,35%,border-top"
	case switchUIPopup:
		return "right,60%,border-left"
	default:
		return ""
	}
}

func switchPickerFooter(ui string) string {
	if ui == switchUISidebar {
		return strings.Join([]string{
			"Enter: switch/create",
			"Alt-T: tag focused directory",
			"Alt-P: pin/unpin focused directory",
		}, "\n")
	}
	return strings.Join([]string{
		"Enter: switch/create previewed target",
		"Alt-T: tag focused directory",
		"Alt-P: pin/unpin focused directory",
		"Left/Right: preview window",
		"Alt-Up/Alt-Down: preview pane",
	}, "\n")
}

func switchSidebarInitialPos(plan switchPlan) int {
	idx := 0
	homeIdx := 0
	pathMatchIdx := 0
	currentTarget := bestSwitchCandidateMatch(plan.CurrentPath, plan.Candidates)

	for _, entry := range plan.Rows {
		value := cleanOptionalPath(entry.Value)
		if value == "" || value == switchSettingsSentinel {
			continue
		}
		idx++

		if homeIdx == 0 && value == cleanOptionalPath(plan.HomeDir) {
			homeIdx = idx
		}
		if plan.OriginSession != "" {
			if sessionName := switchSessionNameForRow(plan, value); sessionName == plan.OriginSession {
				return idx
			}
		}
		if pathMatchIdx == 0 && currentTarget != "" && value == cleanOptionalPath(currentTarget) {
			pathMatchIdx = idx
		}
	}

	if pathMatchIdx != 0 {
		return pathMatchIdx
	}
	return homeIdx
}

func switchSessionNameForRow(plan switchPlan, value string) string {
	if plan.SessionNames == nil {
		return ""
	}
	return strings.TrimSpace(plan.SessionNames[cleanOptionalPath(value)])
}

func (c *switchCommand) toggleTag(target string, stdout io.Writer) error {
	store, err := c.loadTagStore()
	if err != nil {
		return err
	}

	tagged, err := store.Toggle(target)
	if err != nil {
		return fmt.Errorf("toggle switch candidate tag: %w", err)
	}
	if stdout == nil {
		return nil
	}

	if tagged {
		_, err = fmt.Fprintf(stdout, "tagged: %s\n", target)
		return err
	}

	_, err = fmt.Fprintf(stdout, "untagged: %s\n", target)
	return err
}

func (c *switchCommand) togglePin(target string, stdout io.Writer) error {
	store, err := c.loadPinStore()
	if err != nil {
		return err
	}

	pinned, err := store.Toggle(target)
	if err != nil {
		return fmt.Errorf("toggle switch candidate pin: %w", err)
	}
	if stdout == nil {
		return nil
	}

	if pinned {
		_, err = fmt.Fprintf(stdout, "pinned: %s\n", target)
		return err
	}

	_, err = fmt.Fprintf(stdout, "unpinned: %s\n", target)
	return err
}

func (c *switchCommand) addPin(target string, stdout io.Writer) error {
	store, err := c.loadPinStore()
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}

	if err := store.Add(target); err != nil {
		return fmt.Errorf("add switch pin: %w", err)
	}

	if stdout == nil {
		return nil
	}

	_, err = fmt.Fprintf(stdout, "pinned: %s\n", target)
	return err
}

func (c *switchCommand) renderRows(ctx context.Context, ui string, candidatePaths []string) ([]intfzf.Entry, map[string]string, error) {
	renderCandidates := make([]intrender.SwitchCandidate, 0, len(candidatePaths))
	existingBySession, err := c.lookupExistingSessions(ctx, candidatePaths)
	if err != nil {
		return nil, nil, err
	}
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, nil, err
	}
	repoRoot := cleanOptionalPath(c.env(repoRootEnvVar))
	pinnedSet, err := c.loadPinnedSet()
	if err != nil {
		return nil, nil, err
	}
	taggedSet, err := c.loadTaggedSet()
	if err != nil {
		return nil, nil, err
	}
	sessionNames := make(map[string]string, len(candidatePaths))

	for _, candidatePath := range candidatePaths {
		if candidatePath == switchSettingsSentinel {
			renderCandidates = append(renderCandidates, intrender.SwitchCandidate{
				Path:        candidatePath,
				DisplayPath: "Settings",
				UI:          ui,
			})
			continue
		}

		sessionName, err := c.identity.SessionIdentityForPath(candidatePath)
		if err != nil {
			return nil, nil, fmt.Errorf("render switch rows: resolve session identity for %q: %w", candidatePath, err)
		}
		sessionNames[cleanOptionalPath(candidatePath)] = sessionName

		modeLabel := ""
		if exists, ok := existingBySession[sessionName]; ok {
			if exists {
				modeLabel = "existing"
			} else {
				modeLabel = "new"
			}
		}

		renderCandidates = append(renderCandidates, intrender.SwitchCandidate{
			Path:        candidatePath,
			DisplayPath: intrender.PrettyPath(candidatePath, homeDir, repoRoot),
			SessionName: sessionName,
			ModeLabel:   modeLabel,
			UI:          ui,
			Pinned:      pinnedSet[cleanOptionalPath(candidatePath)],
			Tagged:      taggedSet[cleanOptionalPath(candidatePath)],
		})
	}

	rows := intrender.BuildSwitchRows(renderCandidates)
	entries := make([]intfzf.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, intfzf.Entry{
			Label: row.Label,
			Value: row.Value,
		})
	}

	return entries, sessionNames, nil
}

func (c *switchCommand) loadPinnedSet() (map[string]bool, error) {
	pins, err := c.loadPins()
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(pins))
	for _, pin := range pins {
		set[cleanOptionalPath(pin)] = true
	}
	return set, nil
}

func (c *switchCommand) loadTaggedSet() (map[string]bool, error) {
	if c.tagStore == nil {
		return map[string]bool{}, nil
	}
	store, err := c.loadTagStore()
	if err != nil {
		return nil, err
	}
	items, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("load switch tags: %w", err)
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[cleanOptionalPath(item)] = true
	}
	return set, nil
}

func (c *switchCommand) lookupExistingSessions(ctx context.Context, candidatePaths []string) (map[string]bool, error) {
	inspector, ok := c.sessions.(switchSessionInspector)
	if !ok || inspector == nil {
		return nil, nil
	}

	existingBySession := make(map[string]bool)
	for _, candidatePath := range candidatePaths {
		if candidatePath == switchSettingsSentinel {
			continue
		}
		sessionName, err := c.identity.SessionIdentityForPath(candidatePath)
		if err != nil {
			return nil, fmt.Errorf("check existing switch sessions: resolve session identity for %q: %w", candidatePath, err)
		}
		if _, seen := existingBySession[sessionName]; seen {
			continue
		}

		exists, err := inspector.SessionExists(ctx, sessionName)
		if err != nil {
			return nil, fmt.Errorf("check existing switch sessions for %q: %w", sessionName, err)
		}
		existingBySession[sessionName] = exists
	}

	return existingBySession, nil
}

func printSwitchUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux switch [--ui=popup|sidebar]")
	fmt.Fprintln(w, "  projmux switch toggle-tag [path]")
	fmt.Fprintln(w, "  projmux switch toggle-pin [path]")
	fmt.Fprintln(w, "  projmux switch settings")
	fmt.Fprintln(w, "  projmux switch preview [path]")
	fmt.Fprintln(w, "  projmux switch cycle-pane <path> <next|prev>")
	fmt.Fprintln(w, "  projmux switch cycle-window <path> <next|prev>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  --ui string   Candidate surface to prepare (popup or sidebar) (default \"popup\")")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Picker Actions:")
	fmt.Fprintln(w, "  alt-t         Toggle a tag on the focused candidate and reopen the picker")
	fmt.Fprintln(w, "  alt-p         Toggle a pin on the focused candidate and reopen the picker")
}

func (c *switchCommand) settingsEntries() ([]intfzf.Entry, error) {
	pins, err := c.loadPins()
	if err != nil {
		return nil, err
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	repoRoot := cleanOptionalPath(c.env(repoRootEnvVar))

	entries := make([]intfzf.Entry, 0, len(pins)+3)
	entries = append(entries, intfzf.Entry{
		Label: "+ Add pin...",
		Value: "add-interactive",
	})
	currentTarget, err := c.resolveSwitchTarget(nil, "switch settings")
	if err == nil && currentTarget != "" && currentTarget != switchSettingsSentinel && !containsString(pins, currentTarget) {
		entries = append(entries, intfzf.Entry{
			Label: "+ Add current pin  " + intrender.PrettyPath(currentTarget, homeDir, repoRoot),
			Value: "add:" + currentTarget,
		})
	}
	if len(pins) != 0 {
		entries = append(entries, intfzf.Entry{
			Label: "x Clear all pins",
			Value: "clear",
		})
	}
	for _, pin := range pins {
		entries = append(entries, intfzf.Entry{
			Label: "x Remove  " + intrender.PrettyPath(pin, homeDir, repoRoot),
			Value: "pin:" + pin,
		})
	}
	return entries, nil
}

func (c *switchCommand) addPinEntries() ([]intfzf.Entry, error) {
	inputs, err := c.candidateInputs("")
	if err != nil {
		return nil, err
	}

	if c.discover == nil {
		return nil, fmt.Errorf("switch candidate discovery is not configured")
	}

	paths, err := c.discover(inputs)
	if err != nil {
		return nil, fmt.Errorf("discover switch add-pin candidates: %w", err)
	}

	pins, err := c.loadPins()
	if err != nil {
		return nil, err
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	repoRoot := cleanOptionalPath(c.env(repoRootEnvVar))

	entries := make([]intfzf.Entry, 0, len(paths))
	for _, path := range paths {
		if path == switchSettingsSentinel || containsString(pins, path) {
			continue
		}

		entries = append(entries, intfzf.Entry{
			Label: intrender.PrettyPath(path, homeDir, repoRoot),
			Value: path,
		})
	}

	return entries, nil
}

func (c *switchCommand) writeSettingsPreview(stdout io.Writer) error {
	pins, err := c.loadPins()
	if err != nil {
		return err
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return err
	}
	repoRoot := cleanOptionalPath(c.env(repoRootEnvVar))

	var builder strings.Builder
	builder.WriteString("settings\n")
	builder.WriteString("pins:\n")
	if len(pins) == 0 {
		builder.WriteString("  (no pins yet)\n")
	} else {
		for _, pin := range pins {
			builder.WriteString("  * ")
			builder.WriteString(intrender.PrettyPath(pin, homeDir, repoRoot))
			builder.WriteString("\n")
		}
	}
	builder.WriteString("keys:\n")
	builder.WriteString("  enter  open settings menu\n")
	builder.WriteString("  alt-p  pin/unpin focused directory\n")
	builder.WriteString("menu:\n")
	builder.WriteString("  + add pin...\n")
	builder.WriteString("  + add current pin\n")
	builder.WriteString("  x remove pin\n")
	builder.WriteString("  x clear all pins\n")

	_, err = io.WriteString(stdout, builder.String())
	return err
}

func (c *switchCommand) resolveGitBranch(path string) string {
	if c.gitBranch == nil {
		return ""
	}
	return strings.TrimSpace(c.gitBranch(path))
}

func detectGitBranch(path string) string {
	path = cleanOptionalPath(path)
	if path == "" {
		return ""
	}
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	if output, err := exec.Command("git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD").CombinedOutput(); err == nil {
		return strings.TrimSpace(string(output))
	}
	if output, err := exec.Command("git", "-C", path, "rev-parse", "--short", "HEAD").CombinedOutput(); err == nil {
		return strings.TrimSpace(string(output))
	}
	return ""
}

func (c *switchCommand) clearPins() error {
	store, err := c.loadPinStore()
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	if err := store.Clear(); err != nil {
		return fmt.Errorf("clear switch pins: %w", err)
	}
	return nil
}

func containsString(items []string, target string) bool {
	return slices.Contains(items, target)
}
