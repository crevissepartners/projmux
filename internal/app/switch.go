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
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	"github.com/crevissepartners/projmux/internal/core/pins"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/core/projectidentity"
	coretags "github.com/crevissepartners/projmux/internal/core/tags"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

const (
	switchUIFlag                 = "ui"
	switchUIPopup                = "popup"
	switchUISidebar              = "sidebar"
	switchKillExpectKey          = "ctrl-x"
	switchPinExpectKey           = "alt-p"
	switchSettingsSentinel       = "__projmux_settings__"
	switchContextSessionEnv      = "TMUX_SESSIONIZER_CONTEXT_SESSION"
	switchInitialQueryEnv        = "PROJMUX_SWITCH_INITIAL_QUERY"
	switchInitialSelectionEnv    = "PROJMUX_SWITCH_INITIAL_SELECTION"
	switchStatusMessageEnv       = "PROJMUX_SWITCH_STATUS_MESSAGE"
	managedRootsEnvVar           = "PROJMUX_MANAGED_ROOTS"
	legacyManagedRootsEnvVar     = "TMUX_SESSIONIZER_ROOTS"
	projdirEnvVar                = "PROJMUX_PROJDIR"
	defaultSwitchGitCommandLimit = 500 * time.Millisecond
)

var switchPinHiddenWhitelist = []string{
	".claude",
	".codex",
	".config",
	".docker",
	".kube",
	".local",
	".ssh",
}

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
	Run(options intpickercompat.Options) (intpickercompat.Result, error)
}

type switchSessionExecutor interface {
	EnsureSession(ctx context.Context, sessionName, cwd string) error
	OpenSession(ctx context.Context, sessionName string) error
}

type switchSessionInspector interface {
	SessionExists(ctx context.Context, sessionName string) (bool, error)
}

type switchBulkSessionInspector interface {
	ExistingSessions(ctx context.Context) (map[string]bool, error)
}

type switchSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type switchRecentSessionsResolver interface {
	RecentSessions(ctx context.Context) ([]string, error)
}

type switchPreviewStore interface {
	ReadSelection(sessionName string) (corepreview.Selection, bool, error)
	CyclePaneSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
	CycleWindowSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
}

type switchCommand struct {
	discover             candidateDiscoverer
	pinStore             switchPinStoreFactory
	tagStore             switchTagStoreFactory
	runner               switchRunner
	tmuxRunner           tmuxRunner
	sessions             switchSessionExecutor
	previewStore         switchPreviewStore
	previewStoreErr      error
	inventory            previewInventory
	inventoryErr         error
	executable           func() (string, error)
	rawExecutable        func() (string, error)
	identity             sessionIdentityResolver
	identityErr          error
	validate             func(path string) error
	homeDir              func() (string, error)
	workingDir           func() (string, error)
	lookupEnv            func(string) string
	gitBranch            func(string) string
	kubeInfo             func(sessionName string) switchKubeInfo
	loadProjdir          func(homeDir string) (string, error)
	saveProjdir          func(homeDir, value string) error
	loadWorkdirs         func(homeDir string) ([]string, error)
	tmuxProjdir          func() string
	nativePicker         intpicker.Runner
	focusSession         string
	sidebarResume        switchSidebarResume
	cleanupKilledSession func(string)
}

type switchKubeInfo struct {
	Context   string
	Namespace string
}

type switchPlan struct {
	UI             string
	Candidates     []string
	Rows           []intpickercompat.Entry
	Items          []intpicker.Item
	SessionNames   map[string]string
	Action         string
	Selection      string
	SessionName    string
	HomeDir        string
	CurrentPath    string
	OriginSession  string
	Query          string
	InitialQuery   string
	StatusMessage  string
	DeferredUpdate func() (intpicker.DeferredUpdate, error)
}

type switchSidebarResume struct {
	Query     string
	Selection string
	Message   string
}

func newSwitchCommand(recorders ...*diagnostics.LifecycleRecorder) *switchCommand {
	client := defaultTmuxClient(recorders...)
	identity, err := newDefaultCurrentIdentityResolver()
	paths, pathsErr := config.DefaultPathsFromEnv()

	cmd := &switchCommand{
		discover:      candidates.Discover,
		pinStore:      newDefaultSwitchPinStore,
		tagStore:      newDefaultSwitchTagStore,
		tmuxRunner:    inttmux.ExecRunner{},
		sessions:      client,
		inventory:     tmuxPreviewInventory{client: client},
		executable:    resolveExecutablePath,
		rawExecutable: rawExecutablePath,
		identity:      identity,
		identityErr:   err,
		validate:      validateDirectory,
		homeDir:       os.UserHomeDir,
		workingDir:    os.Getwd,
		lookupEnv:     os.Getenv,
		gitBranch:     detectGitBranch,
		kubeInfo:      defaultSwitchKubeInfo,
		loadProjdir:   config.LoadProjdir,
		saveProjdir:   config.SaveProjdir,
		loadWorkdirs:  config.LoadWorkdirs,
		tmuxProjdir:   tmuxProjdirOption,
		nativePicker:  intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
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
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, "")()
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "toggle-tag":
			return c.runToggleTag(args[1:], stdout, stderr)
		case "toggle-pin":
			return c.runTogglePin(args[1:], stdout, stderr)
		case "kill":
			return c.runKill(args[1:], stdout, stderr)
		case "open":
			return c.runOpen(args[1:], stderr)
		case "sidebar-open":
			return c.runSidebarOpen(args[1:], stderr)
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

func (c *switchCommand) runKill(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("switch kill", flag.ContinueOnError)
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
		return fmt.Errorf("switch kill accepts at most 1 [path] argument")
	}

	target, err := c.resolveSwitchTarget(fs.Args(), "switch kill")
	if err != nil {
		if strings.Contains(err.Error(), "switch kill requires") {
			printSwitchUsage(stderr)
		}
		return err
	}
	if target == switchSettingsSentinel {
		return nil
	}
	if c.identityErr != nil {
		return fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return fmt.Errorf("switch session identity resolver is not configured")
	}
	sessionName, err := c.identity.SessionIdentityForPath(target)
	if err != nil {
		return fmt.Errorf("resolve switch kill session identity: %w", err)
	}

	return c.killFocusedSession(context.Background(), sessionName, "", stdout)
}

func (c *switchCommand) runOpen(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("switch open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printSwitchUsage(stderr)
	}
	if err := fs.Parse(args); err != nil {
		printSwitchUsage(stderr)
		return err
	}
	if fs.NArg() != 1 {
		printSwitchUsage(stderr)
		return fmt.Errorf("switch open requires exactly 1 argument: <path>")
	}
	return c.openProjectTargetPath(context.Background(), cleanOptionalPath(fs.Arg(0)))
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

	_, err = io.WriteString(stdout, intrender.RenderSwitchPreviewWithAIBadgeStyle(model, *ui, string(loadAIBadgeStyle(c.homeDir, c.lookupEnv))))
	return err
}

func (c *switchCommand) runSettings(stdout, stderr io.Writer) error {
	if c.nativePicker == nil {
		return fmt.Errorf("native picker is not configured")
	}

	for {
		entries, err := c.settingsEntries()
		if err != nil {
			return err
		}

		result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, intpickercompat.Options{
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

		if err := c.executeSettingsAction(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *switchCommand) executeSettingsAction(action string, stdout, stderr io.Writer) error {
	switch {
	case action == "add-interactive":
		return c.runAddPinInteractive(stdout)
	case strings.HasPrefix(action, "add:"):
		target := strings.TrimPrefix(action, "add:")
		return c.addPin(target, stdout)
	case action == "clear":
		if err := c.clearPins(); err != nil {
			return err
		}
		if stdout != nil {
			_, _ = fmt.Fprintln(stdout, "cleared pins")
		}
		return nil
	case strings.HasPrefix(action, "pin:"):
		target := strings.TrimPrefix(action, "pin:")
		return c.togglePin(target, stdout)
	default:
		printSwitchUsage(stderr)
		return fmt.Errorf("unknown switch settings action: %s", action)
	}
}

func (c *switchCommand) runAddPinInteractive(stdout io.Writer) error {
	if c.nativePicker == nil {
		return fmt.Errorf("native picker is not configured")
	}

	entries, err := c.addPinEntries()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, intpickercompat.Options{
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
	resume := switchSidebarResume{}
	if ui == switchUISidebar && c.sidebarResume != (switchSidebarResume{}) {
		resume = c.sidebarResume
		c.sidebarResume = switchSidebarResume{}
	}
	if ui == switchUISidebar && resume == (switchSidebarResume{}) && c.lookupEnv != nil {
		resume = switchSidebarResume{
			Query:     strings.TrimSpace(c.lookupEnv(switchInitialQueryEnv)),
			Selection: cleanOptionalPath(c.lookupEnv(switchInitialSelectionEnv)),
			Message:   strings.TrimSpace(c.lookupEnv(switchStatusMessageEnv)),
		}
	}
	if ui == switchUISidebar {
		if strings.TrimSpace(resume.Selection) != "" {
			inputs.CurrentPath = resume.Selection
		}
	}

	paths, err := c.discover(inputs)
	if err != nil {
		return switchPlan{}, fmt.Errorf("discover switch candidates: %w", err)
	}

	plan := switchPlan{
		UI:            ui,
		Candidates:    paths,
		HomeDir:       homeDir,
		CurrentPath:   cleanOptionalPath(inputs.CurrentPath),
		OriginSession: c.originSession(),
		InitialQuery:  strings.TrimSpace(resume.Query),
		StatusMessage: strings.TrimSpace(resume.Message),
	}

	return c.completePlan(plan)
}

func (c *switchCommand) candidateInputs(currentPath string) (candidates.Inputs, error) {
	return c.candidateInputsWithMemoize(currentPath, true)
}

func (c *switchCommand) candidateInputsWithMemoize(currentPath string, memoize bool) (candidates.Inputs, error) {
	inputs, err := c.projectDiscoveryInputs(memoize)
	if err != nil {
		return candidates.Inputs{}, err
	}

	if currentPath == "" {
		currentPath, err = c.resolveWorkingDir()
		if err != nil {
			return candidates.Inputs{}, err
		}
	}
	inputs.CurrentPath = currentPath
	return inputs, nil
}

// projectDiscoveryInputs reads the same pins and configured search roots as
// the project picker without injecting its home/current-path convenience
// candidates. With memoize=false the entire path is read-only.
func (c *switchCommand) projectDiscoveryInputs(memoize bool) (candidates.Inputs, error) {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return candidates.Inputs{}, err
	}

	pins, err := c.loadPins()
	if err != nil {
		return candidates.Inputs{}, err
	}

	var repoRoot string
	if memoize {
		repoRoot = c.switchRepoRoot(homeDir)
	} else {
		repoRoot, _ = resolveProjdir(homeDir, c.lookupEnv, c.tmuxProjdir, c.loadProjdir)
	}
	extraRoots := extraProjdirRoots(c.lookupEnv)
	return candidates.Inputs{
		HomeDir:      homeDir,
		RepoRoot:     repoRoot,
		ManagedRoots: switchManagedRoots(homeDir, repoRoot, extraRoots, c.lookupEnv, c.loadWorkdirs),
		Pins:         pins,
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

	return c.resolveSwitchTargetFromInput(inputPath, true)
}

func (c *switchCommand) resolveSwitchTargetNoMemoize(args []string, command string) (string, error) {
	inputPath, err := c.resolveSwitchInput(args, command)
	if err != nil {
		return "", err
	}

	return c.resolveSwitchTargetFromInput(inputPath, false)
}

func (c *switchCommand) resolveSwitchTargetFromInput(inputPath string, memoize bool) (string, error) {
	inputs, err := c.candidateInputsWithMemoize(inputPath, memoize)
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
	repoRoot := c.switchRepoRoot(homeDir)

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
	if c.kubeInfo != nil {
		kube := c.kubeInfo(sessionName)
		modelInputs.KubeContext = kube.Context
		modelInputs.KubeNamespace = kube.Namespace
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
	model := corepreview.BuildSwitchReadModel(modelInputs)
	model.Popup.PaneSnapshot = capturePaneSnapshot(ctx, inventory, model.Popup, -60)
	return model, nil
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

func (c *switchCommand) originSession() string {
	if sessionName := strings.TrimSpace(c.focusSession); sessionName != "" {
		return sessionName
	}
	return strings.TrimSpace(c.env(switchContextSessionEnv))
}

// projdirSource labels the origin of a resolved repo root for display in
// settings. Strings are stable identifiers (also used by tests).
const (
	projdirSourcePROJDIRenv = "PROJMUX_PROJDIR env"
	projdirSourceTmuxOption = "@projmux_projdir tmux"
	projdirSourceSaved      = "saved"
	projdirSourceUnresolved = ""
)

type projdirSettingsInfo struct {
	EffectiveValue  string
	EffectiveSource string
	SavedValue      string
}

func (c *switchCommand) switchRepoRoot(homeDir string) string {
	return switchRepoRoot(homeDir, c.lookupEnv, c.tmuxProjdir, c.loadProjdir, c.saveProjdir)
}

func switchRepoRoot(
	homeDir string,
	lookup func(string) string,
	tmuxOption func() string,
	load func(string) (string, error),
	save func(string, string) error,
) string {
	value, source := resolveProjdir(homeDir, lookup, tmuxOption, load)
	switch source {
	case projdirSourcePROJDIRenv:
		memoizeProjdir(homeDir, value, load, save)
	}
	return value
}

// resolveProjdir applies the same priority chain as switchRepoRoot but is
// side-effect free: it does not memoize. Returns the resolved cleaned path
// and a label identifying the source that supplied it. When no source
// produces a value (e.g. empty home with no env), the returned source is
// projdirSourceUnresolved and value is empty.
func resolveProjdir(
	homeDir string,
	lookup func(string) string,
	tmuxOption func() string,
	load func(string) (string, error),
) (string, string) {
	if raw := envValue(lookup, projdirEnvVar); strings.TrimSpace(raw) != "" {
		// PROJMUX_PROJDIR is a PATH-style multi-value: the first
		// non-empty entry is the primary repo root; extra entries are
		// fed to managed roots elsewhere via extraProjdirRoots.
		for _, entry := range filepath.SplitList(raw) {
			if repoRoot := cleanOptionalPath(entry); repoRoot != "" {
				return repoRoot, projdirSourcePROJDIRenv
			}
		}
	}

	if tmuxOption != nil {
		if raw := strings.TrimSpace(tmuxOption()); raw != "" {
			if repoRoot := cleanOptionalPath(raw); repoRoot != "" {
				return repoRoot, projdirSourceTmuxOption
			}
		}
	}

	if load != nil && homeDir != "" {
		if saved, err := load(homeDir); err == nil {
			if repoRoot := cleanOptionalPath(saved); repoRoot != "" {
				return repoRoot, projdirSourceSaved
			}
		}
	}

	return "", projdirSourceUnresolved
}

// currentProjdirInfo returns the currently resolved repo root path and the
// label of the source that supplied it, without performing memoization.
// Callers (e.g. settings UX) use this to surface the value to the user as
// a read-only preview.
func (c *switchCommand) currentProjdirInfo() (string, string, error) {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return "", "", err
	}
	value, source := resolveProjdir(homeDir, c.lookupEnv, c.tmuxProjdir, c.loadProjdir)
	return value, source, nil
}

func (c *switchCommand) projdirSettingsInfo() (projdirSettingsInfo, error) {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return projdirSettingsInfo{}, err
	}
	value, source := resolveProjdir(homeDir, c.lookupEnv, c.tmuxProjdir, c.loadProjdir)
	info := projdirSettingsInfo{
		EffectiveValue:  value,
		EffectiveSource: source,
	}
	if c.loadProjdir != nil && homeDir != "" {
		saved, err := c.loadProjdir(homeDir)
		if err != nil {
			return projdirSettingsInfo{}, fmt.Errorf("load saved project root: %w", err)
		}
		info.SavedValue = cleanOptionalPath(saved)
	}
	return info, nil
}

func (c *switchCommand) saveSavedProjdir(target string, stdout io.Writer) error {
	target = cleanOptionalPath(target)
	if target == "" {
		return fmt.Errorf("project root is empty")
	}
	if !filepath.IsAbs(target) {
		return fmt.Errorf("project root must be an absolute path: %s", target)
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return err
	}
	if c.saveProjdir == nil {
		return fmt.Errorf("project root persistence is not configured")
	}
	if err := c.saveProjdir(homeDir, target); err != nil {
		return fmt.Errorf("save project root: %w", err)
	}
	if stdout == nil {
		return nil
	}
	_, err = fmt.Fprintf(stdout, "saved project root: %s\n", target)
	return err
}

func (c *switchCommand) clearSavedProjdir(stdout io.Writer) error {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return err
	}
	if c.saveProjdir == nil {
		return fmt.Errorf("project root persistence is not configured")
	}
	if err := c.saveProjdir(homeDir, ""); err != nil {
		return fmt.Errorf("clear saved project root: %w", err)
	}
	if stdout == nil {
		return nil
	}
	_, err = fmt.Fprintln(stdout, "cleared saved project root")
	return err
}

func (c *switchCommand) executeProjdirSettingsAction(action string, stdout, stderr io.Writer) error {
	switch action {
	case "set-current":
		target, err := c.resolveSwitchTargetNoMemoize(nil, "settings project root")
		if err != nil {
			return err
		}
		if target == "" || target == switchSettingsSentinel {
			return fmt.Errorf("no current project context to save as project root")
		}
		return c.saveSavedProjdir(target, stdout)
	case "clear":
		return c.clearSavedProjdir(stdout)
	default:
		printSwitchUsage(stderr)
		return fmt.Errorf("unknown project root settings action: %s", action)
	}
}

// preferredProjdirEnv returns the effective env value for the repo root and
// the variable name that supplied it. The value may be a PATH-style
// multi-value; callers that persist a single canonical path should pass it
// through firstProjdirPath first. An empty source string means the variable
// was not set.
func preferredProjdirEnv(lookup func(string) string) (string, string) {
	raw := envValue(lookup, projdirEnvVar)
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	return raw, projdirEnvVar
}

// firstProjdirPath returns the first non-empty entry of a PATH-style
// PROJMUX_PROJDIR value. It returns an empty string when raw contains no
// usable path. The entry is not cleaned; callers that want the canonical
// path should run cleanOptionalPath afterwards.
func firstProjdirPath(raw string) string {
	for _, entry := range filepath.SplitList(raw) {
		if strings.TrimSpace(entry) != "" {
			return entry
		}
	}
	return ""
}

// extraProjdirRoots returns the additional roots that follow the primary
// entry in a PATH-style PROJMUX_PROJDIR value. The primary (first
// non-empty) entry is excluded because it is already surfaced via
// switchRepoRoot. Each remaining entry is cleaned and empty entries are
// dropped. Order is preserved.
func extraProjdirRoots(lookup func(string) string) []string {
	raw := envValue(lookup, projdirEnvVar)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	entries := filepath.SplitList(raw)
	primaryFound := false
	extras := make([]string, 0, len(entries))
	for _, entry := range entries {
		cleaned := cleanOptionalPath(entry)
		if cleaned == "" {
			continue
		}
		if !primaryFound {
			primaryFound = true
			continue
		}
		extras = append(extras, cleaned)
	}
	return extras
}

// tmuxProjdirOption returns the tmux user-option @projmux_projdir value when
// running inside a tmux client. The TMUX env gate keeps us off the default
// socket when projmux runs outside tmux, where the option read may either fail
// or return a stale value from another server. Errors are swallowed because
// the caller falls through to the next priority source.
func tmuxProjdirOption() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := mux.ShowOption(context.Background(), mux.ShowOptionOptions{
		Global:    true,
		Quiet:     true,
		ValueOnly: true,
		Option:    "@projmux_projdir",
	})
	if err != nil {
		return ""
	}
	return out
}

// memoizeProjdir best-effort persists value to the saved-projdir file. It
// skips work when the saved value already matches and swallows errors so
// command exit codes are unaffected.
func memoizeProjdir(
	homeDir, value string,
	load func(string) (string, error),
	save func(string, string) error,
) {
	if save == nil || homeDir == "" {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if load != nil {
		if saved, err := load(homeDir); err == nil && strings.TrimSpace(saved) == value {
			return
		}
	}
	_ = save(homeDir, value)
}

func switchManagedRoots(homeDir, repoRoot string, extraProjdirRoots []string, lookup func(string) string, loadWorkdirs func(string) ([]string, error)) []string {
	roots := make([]string, 0)
	seen := make(map[string]struct{})

	appendRoot := func(root string) {
		root = cleanOptionalPath(root)
		if root == "" {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}

	// Extra PROJMUX_PROJDIR roots (everything past the primary entry)
	// are prepended so they take priority over env- or saved-managed
	// roots and the default fallback list. Like PROJMUX_MANAGED_ROOTS,
	// any explicit env-driven entry suppresses the saved-workdirs and
	// default fallback below.
	managedFromEnv := false
	for _, root := range extraProjdirRoots {
		if cleanOptionalPath(root) == "" {
			continue
		}
		managedFromEnv = true
		appendRoot(root)
	}

	for _, value := range []string{
		envValue(lookup, managedRootsEnvVar),
		envValue(lookup, legacyManagedRootsEnvVar),
	} {
		for _, root := range filepath.SplitList(value) {
			if cleanOptionalPath(root) == "" {
				continue
			}
			managedFromEnv = true
			appendRoot(root)
		}
	}

	if !managedFromEnv && loadWorkdirs != nil {
		saved, err := loadWorkdirs(homeDir)
		if err == nil {
			for _, root := range saved {
				appendRoot(root)
			}
		}
	}

	if len(roots) == 0 {
		for _, root := range defaultManagedRoots(homeDir, repoRoot) {
			appendRoot(root)
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

// bestSwitchCandidateMatch finds the candidate whose directory contains (or
// equals) path, comparing on canonical real paths so a session cwd spelled via
// a symlink matches its real-path candidate row (and vice versa). The returned
// value is the candidate's original display spelling, never the resolved real
// path, so callers keep the user's form for display and cd.
func bestSwitchCandidateMatch(path string, candidatePaths []string) string {
	canonicalPath := candidates.CanonicalPath(path)
	if canonicalPath == "" {
		return ""
	}

	best := ""
	bestLen := -1

	for _, candidatePath := range candidatePaths {
		canonicalCandidate := candidates.CanonicalPath(candidatePath)
		if canonicalCandidate == "" {
			continue
		}
		if canonicalCandidate == canonicalPath {
			return candidatePath
		}

		prefix := canonicalCandidate + string(filepath.Separator)
		if !strings.HasPrefix(canonicalPath, prefix) {
			continue
		}
		if len(canonicalCandidate) > bestLen {
			bestLen = len(canonicalCandidate)
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
	if c.nativePicker == nil {
		return switchPlan{}, fmt.Errorf("native picker is not configured")
	}
	if c.identityErr != nil {
		return switchPlan{}, fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return switchPlan{}, fmt.Errorf("switch session identity resolver is not configured")
	}

	rows, items, sessionNames, err := c.renderRows(context.Background(), plan.UI, plan.Candidates)
	if err != nil {
		return switchPlan{}, err
	}
	plan.Rows = rows
	plan.Items = items
	plan.SessionNames = sessionNames
	if plan.UI == switchUISidebar {
		candidates := append([]string(nil), plan.Candidates...)
		plan.DeferredUpdate = func() (intpicker.DeferredUpdate, error) {
			_, fullItems, _, err := c.renderFullRows(context.Background(), switchUISidebar, candidates)
			if err != nil {
				return intpicker.DeferredUpdate{}, err
			}
			surface, err := c.switchPickerSurface(plan, true)
			if err != nil {
				return intpicker.DeferredUpdate{}, err
			}
			update := intpicker.DeferredUpdate{Items: fullItems}
			if surface.PreviewCommand != "" {
				update.Preview = intpicker.Preview{Command: surface.PreviewCommand, Window: switchPreviewWindow(plan.UI)}
			}
			return update, nil
		}
	}

	result, err := c.runPicker(plan)
	if err != nil {
		return switchPlan{}, err
	}
	plan.Action = strings.TrimSpace(result.Key)
	plan.Query = strings.TrimSpace(result.Query)
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

	sessionName, err := c.identity.SessionIdentityForPath(selection)
	if err != nil {
		return switchPlan{}, fmt.Errorf("resolve session identity: %w", err)
	}
	plan.SessionName = sessionName

	return plan, nil
}

func (c *switchCommand) execute(ctx context.Context, plan switchPlan, stdout io.Writer) (bool, error) {
	if plan.Selection == "" {
		if plan.UI == switchUISidebar {
			c.cancelSidebarPreview(ctx, plan.OriginSession)
		}
		return false, nil
	}
	if plan.Selection == switchSettingsSentinel {
		if err := c.runSettings(stdout, io.Discard); err != nil {
			return false, err
		}
		return false, nil
	}
	if pickerKeyMatchesAction(c.homeDir, c.lookupEnv, plan.Action, "Sidebar:KillSession", switchKillExpectKey) {
		if cleanOptionalPath(plan.Selection) == cleanOptionalPath(plan.HomeDir) {
			return true, nil
		}
		fallbackSession, err := c.previousActiveSession(ctx, plan.SessionName)
		if err != nil {
			return false, err
		}
		if fallbackSession == "" {
			return true, nil
		}
		if err := c.killFocusedSession(ctx, plan.SessionName, fallbackSession, nil); err != nil {
			return false, err
		}
		c.focusSession = fallbackSession
		return true, nil
	}
	if pickerKeyMatchesAction(c.homeDir, c.lookupEnv, plan.Action, "Sidebar:PinProject", switchPinExpectKey) {
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

	if plan.UI == switchUISidebar {
		if err := c.openProjectTargetPathFromSidebar(ctx, plan); err != nil {
			if errors.Is(err, errProjectStartupBack) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	}

	if err := c.openTarget(ctx, plan.Selection); err != nil {
		if errors.Is(err, errProjectTrustDenied) {
			return false, nil
		}
		return false, err
	}
	return false, nil
}

func (c *switchCommand) resolveTargetSession(target string) (string, error) {
	target = cleanOptionalPath(target)
	if target == "" || target == switchSettingsSentinel {
		return "", nil
	}
	if c.identityErr != nil {
		return "", fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return "", fmt.Errorf("switch session identity resolver is not configured")
	}
	if c.sessions == nil {
		return "", fmt.Errorf("switch session executor is not configured")
	}

	sessionName, err := c.identity.SessionIdentityForPath(target)
	if err != nil {
		return "", fmt.Errorf("resolve switch session identity: %w", err)
	}
	if sessionName == "" {
		return "", fmt.Errorf("switch command requires a target session")
	}
	return sessionName, nil
}

func (c *switchCommand) openTarget(ctx context.Context, target string) error {
	target = cleanOptionalPath(target)
	sessionName, err := c.resolveTargetSession(target)
	if err != nil || sessionName == "" {
		return err
	}
	exists, err := c.switchSessionExists(ctx, sessionName)
	if err != nil {
		return err
	}
	if exists {
		return c.openProjectSession(ctx, sessionName)
	}
	return c.openProjectTarget(ctx, target, sessionName)
}

func (c *switchCommand) openProjectTargetPath(ctx context.Context, target string) error {
	target = cleanOptionalPath(target)
	sessionName, err := c.resolveTargetSession(target)
	if err != nil || sessionName == "" {
		return err
	}
	return c.openProjectTarget(ctx, target, sessionName)
}

func (c *switchCommand) openProjectTargetPathFromSidebar(ctx context.Context, plan switchPlan) error {
	target := cleanOptionalPath(plan.Selection)
	if target == "" {
		return nil
	}
	sessionName := strings.TrimSpace(plan.SessionName)
	if sessionName == "" {
		var err error
		sessionName, err = c.resolveTargetSession(target)
		if err != nil || sessionName == "" {
			return err
		}
	}
	plan.SessionName = sessionName
	exists, err := c.switchSessionExists(ctx, sessionName)
	if err != nil {
		return err
	}
	if exists {
		if err := c.openProjectSession(ctx, sessionName); err != nil {
			return err
		}
		c.commitSidebarPreview(ctx)
		return nil
	}
	mode := projectStartupCandidate{Kind: projectStartupKindEmpty}
	if sidebarStartupPickerEnabled(c.homeDir, c.lookupEnv) {
		mode = c.pickProjectStartupMode(sessionName, target)
	}
	if mode.Kind == projectStartupKindBack {
		return errProjectStartupBack
	}
	return c.launchSidebarOpenContinuation(ctx, plan, mode)
}

// ephemeralBinary returns the resolver for immediate, in-process re-exec
// (sidebar continuation/reopen, preview command, one-shot window record). It
// prefers rawExecutable (un-canonicalized) and falls back to executable only
// when rawExecutable is not wired, e.g. in tests.
func (c *switchCommand) ephemeralBinary() func() (string, error) {
	if c.rawExecutable != nil {
		return c.rawExecutable
	}
	return c.executable
}

func (c *switchCommand) launchSidebarOpenContinuation(ctx context.Context, plan switchPlan, mode projectStartupCandidate) error {
	if c.tmuxRunner == nil {
		return fmt.Errorf("switch sidebar continuation runner is not configured")
	}
	if c.ephemeralBinary() == nil {
		return fmt.Errorf("switch sidebar continuation executable is not configured")
	}
	binaryPath, err := c.ephemeralBinary()()
	if err != nil {
		return fmt.Errorf("resolve switch sidebar continuation executable: %w", err)
	}
	client := firstNonEmpty(
		c.lookupEnvValue(inttmux.SwitchTargetClientEnv),
		c.lookupEnvValue(hookTrustPopupTargetClientEnv),
	)
	args := []string{
		"switch",
		"sidebar-open",
		"--path", cleanOptionalPath(plan.Selection),
		"--session", strings.TrimSpace(plan.SessionName),
		"--mode", strings.TrimSpace(mode.Kind),
		"--query", strings.TrimSpace(plan.Query),
	}
	if strings.TrimSpace(mode.Name) != "" {
		args = append(args, "--name", strings.TrimSpace(mode.Name))
	}
	if strings.TrimSpace(client) != "" {
		args = append(args, "--client", strings.TrimSpace(client))
	}
	env := map[string]string{}
	if strings.TrimSpace(client) != "" {
		env[hookTrustPopupTargetClientEnv] = strings.TrimSpace(client)
		env[inttmux.SwitchTargetClientEnv] = strings.TrimSpace(client)
	}
	command := buildShellCommand(binaryPath, args, env)
	if _, err := c.tmuxRunner.Run(ctx, "tmux", "run-shell", "-b", command); err != nil {
		return fmt.Errorf("launch sidebar open continuation: %w", err)
	}
	return nil
}

func (c *switchCommand) lookupEnvValue(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return strings.TrimSpace(c.lookupEnv(name))
}

func (c *switchCommand) runSidebarOpen(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("switch sidebar-open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("path", "", "project path to open")
	sessionName := fs.String("session", "", "target session name")
	mode := fs.String("mode", projectStartupKindEmpty, "startup mode")
	name := fs.String("name", "", "named snapshot name")
	query := fs.String("query", "", "sidebar query to restore on deny")
	client := fs.String("client", "", "tmux client to restore sidebar popup")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("switch sidebar-open does not accept positional arguments")
	}
	openTarget := cleanOptionalPath(*target)
	if openTarget == "" {
		return fmt.Errorf("switch sidebar-open requires --path")
	}
	openSession := strings.TrimSpace(*sessionName)
	if openSession == "" {
		var err error
		openSession, err = c.resolveTargetSession(openTarget)
		if err != nil || openSession == "" {
			return err
		}
	}
	targetClient := strings.TrimSpace(*client)
	if targetClient != "" {
		restoreLookup := c.withSidebarOpenClientEnv(targetClient)
		defer restoreLookup()
	}
	c.closeSidebarPopupForTrust(context.Background(), targetClient)
	openMode := projectStartupCandidate{Kind: strings.TrimSpace(*mode), Name: strings.TrimSpace(*name)}
	if openMode.Kind == "" {
		openMode.Kind = projectStartupKindEmpty
	}
	exists, err := c.switchSessionExists(context.Background(), openSession)
	if err != nil {
		return err
	}
	if exists {
		return c.openProjectSession(context.Background(), openSession)
	}
	err = c.authorizeAndContinueProjectOpen(context.Background(), openTarget, openSession, openMode)
	if err == nil {
		return nil
	}
	resume := switchSidebarResume{
		Query:     strings.TrimSpace(*query),
		Selection: openTarget,
		Message:   sidebarTrustStatusMessage(err),
	}
	if reopenErr := c.reopenSidebarAfterTrust(context.Background(), targetClient, resume); reopenErr != nil {
		return reopenErr
	}
	return nil
}

func (c *switchCommand) withSidebarOpenClientEnv(client string) func() {
	previous := c.lookupEnv
	c.lookupEnv = func(name string) string {
		switch name {
		case hookTrustPopupTargetClientEnv, inttmux.SwitchTargetClientEnv, "PROJMUX_POPUP_TARGET_CLIENT":
			return client
		default:
			if previous == nil {
				return ""
			}
			return previous(name)
		}
	}
	return func() {
		c.lookupEnv = previous
	}
}

func sidebarTrustStatusMessage(err error) string {
	if errors.Is(err, errProjectTrustDenied) {
		return "Trust denied"
	}
	var trustErr errProjectTrustGate
	if errors.As(err, &trustErr) {
		return "Trust error: " + trustErr.Unwrap().Error()
	}
	if err != nil {
		return "Project open error: " + err.Error()
	}
	return ""
}

func (c *switchCommand) reopenSidebarAfterTrust(ctx context.Context, client string, resume switchSidebarResume) error {
	if c.tmuxRunner == nil {
		return fmt.Errorf("switch sidebar reopen runner is not configured")
	}
	if c.ephemeralBinary() == nil {
		return fmt.Errorf("switch sidebar reopen executable is not configured")
	}
	binaryPath, err := c.ephemeralBinary()()
	if err != nil {
		return fmt.Errorf("resolve switch sidebar reopen executable: %w", err)
	}
	if strings.TrimSpace(client) == "" {
		_, _ = c.tmuxRunner.Run(ctx, "tmux", "display-message", strings.TrimSpace(resume.Message))
		return nil
	}
	env := map[string]string{
		switchInitialQueryEnv:     strings.TrimSpace(resume.Query),
		switchInitialSelectionEnv: cleanOptionalPath(resume.Selection),
		switchStatusMessageEnv:    strings.TrimSpace(resume.Message),
	}
	command := buildShellCommand(binaryPath, []string{"tmux", "popup-toggle", "--client", strings.TrimSpace(client), "sessionizer-sidebar"}, env)
	if _, err := c.tmuxRunner.Run(ctx, "tmux", "run-shell", "-b", command); err != nil {
		return fmt.Errorf("reopen sidebar after trust: %w", err)
	}
	return nil
}

func (c *switchCommand) closeSidebarPopupForTrust(ctx context.Context, targetClient string) {
	targetClient = strings.TrimSpace(targetClient)
	if targetClient == "" || c.tmuxRunner == nil {
		return
	}
	marker := popupMarkerPath(sanitizePopupKey(targetClient), "sessionizer-sidebar")
	_ = os.Remove(marker)
	_ = mux.NewRunner(c.tmuxRunner).ClosePopup(ctx, mux.ClosePopupOptions{
		Client: targetClient,
	})
}

func buildShellCommand(binaryPath string, args []string, env map[string]string) string {
	command := []string{}
	for _, key := range sortedStringKeys(env) {
		value := env[key]
		if strings.TrimSpace(value) == "" {
			continue
		}
		command = append(command, key+"="+tmuxShellQuote(value))
	}
	command = append(command, tmuxShellQuote(binaryPath))
	for _, arg := range args {
		command = append(command, tmuxShellQuote(arg))
	}
	return strings.Join(command, " ")
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

// sidebarPreviewMarkerPath resolves this client's sessionizer-sidebar popup
// marker. Empty when the switch command runs outside a sidebar popup (no
// target-client env), which disables the commit/cancel marker handling.
func (c *switchCommand) sidebarPreviewMarkerPath() string {
	client := strings.TrimSpace(c.env(inttmux.SwitchTargetClientEnv))
	if client == "" {
		return ""
	}
	return popupMarkerPath(sanitizePopupKey(client), "sessionizer-sidebar")
}

// removeSidebarPreviewMarker deletes this client's sidebar popup marker and
// reports whether one was present. The marker gates `window record`, so it
// must be gone before the commit/cancel paths trigger any recording.
func (c *switchCommand) removeSidebarPreviewMarker() bool {
	marker := c.sidebarPreviewMarkerPath()
	if marker == "" {
		return false
	}
	return os.Remove(marker) == nil
}

// commitSidebarPreview finalizes an Enter-confirmed sidebar selection. The
// live preview already switched the client to that session, so no
// session-changed hook fires on commit; record the window once explicitly
// after the gating marker is removed, detached like the tmux record hooks.
func (c *switchCommand) commitSidebarPreview(ctx context.Context) {
	if !c.removeSidebarPreviewMarker() {
		return
	}
	if c.tmuxRunner == nil || c.ephemeralBinary() == nil {
		return
	}
	binaryPath, err := c.ephemeralBinary()()
	if err != nil {
		return
	}
	command := buildShellCommand(binaryPath, []string{"window", "record"}, nil)
	_, _ = c.tmuxRunner.Run(ctx, "tmux", "run-shell", "-b", command)
}

// cancelSidebarPreview restores the origin session after the sidebar closes
// without a selection (Esc). The marker is removed first so the restore
// switch records origin naturally via the session-changed hook while peeked
// sessions stay unrecorded. No-op outside a sidebar popup; a missing origin
// session (killed while peeking) keeps the client on its current session.
func (c *switchCommand) cancelSidebarPreview(ctx context.Context, originSession string) {
	hadMarker := c.removeSidebarPreviewMarker()
	originSession = strings.TrimSpace(originSession)
	if !hadMarker || originSession == "" || c.sessions == nil {
		return
	}
	exists, err := c.switchSessionExists(ctx, originSession)
	if err != nil || !exists {
		return
	}
	_ = c.sessions.OpenSession(ctx, originSession)
}

func (c *switchCommand) runPicker(plan switchPlan) (intpicker.Result, error) {
	if c.nativePicker == nil {
		return intpicker.Result{}, fmt.Errorf("native picker is not configured")
	}

	sidebarKillActions := intpicker.CustomActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"Sidebar:KillSession"}, []string{switchKillExpectKey})...)
	if plan.UI == switchUISidebar {
		sidebarKillActions = c.switchSidebarKillActions()
	}
	options := intpicker.Options{
		UI:             plan.UI,
		Items:          plan.Items,
		MultiLine:      true,
		Prompt:         "› ",
		Footer:         switchPickerFooter(plan.UI, plan.StatusMessage, c.homeDir, c.lookupEnv),
		InitialQuery:   plan.InitialQuery,
		DeferredUpdate: plan.DeferredUpdate,
		Actions:        pickerCloseActionsForPopupToggleMode(c.homeDir, c.lookupEnv, popupToggleModeForSwitchUI(plan.UI), "esc", "ctrl-n"),
	}
	options.Actions = append(options.Actions, sidebarKillActions...)
	options.Actions = append(options.Actions, intpicker.CustomActions(effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"Sidebar:PinProject"}, []string{switchPinExpectKey})...)...)
	if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, plan.CurrentPath); err == nil {
		options = source.pickerOptions(options)
	} else {
		options = fallbackRenderThemeSource().pickerOptions(options)
	}
	if plan.UI == switchUISidebar {
		options.Title = "Projects"
	}
	surface, err := c.switchPickerSurface(plan, !(plan.UI == switchUISidebar && plan.DeferredUpdate != nil))
	if err != nil {
		return intpicker.Result{}, err
	}
	options.Actions = append(options.Actions, surface.Actions...)
	options.InitialIndex = surface.InitialIndex
	options.InitialIndexSet = surface.InitialIndexSet
	if surface.PreviewCommand != "" {
		options.Preview = intpicker.Preview{Command: surface.PreviewCommand, Window: switchPreviewWindow(plan.UI)}
	} else if plan.UI == switchUISidebar && plan.DeferredUpdate != nil {
		options.Preview = intpicker.Preview{Window: switchPreviewWindow(plan.UI)}
	}

	return c.runNativePicker(options)
}

func (c *switchCommand) runNativePicker(pickerOptions intpicker.Options) (intpicker.Result, error) {
	if c.nativePicker == nil {
		return intpicker.Result{}, fmt.Errorf("native picker is not configured")
	}
	result, err := c.nativePicker.Run(pickerOptions)
	if err != nil {
		return intpicker.Result{}, fmt.Errorf("run native switch picker: %w", err)
	}
	return result, nil
}

func (c *switchCommand) switchSidebarKillActions() []intpicker.Action {
	keys := effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"Sidebar:KillSession"}, []string{switchKillExpectKey})
	actions := make([]intpicker.Action, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		actions = append(actions, intpicker.Action{
			Key:    key,
			Intent: intpicker.ActionCustom,
			Mutate: func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
				return c.mutateSwitchSidebarKill(context.Background(), ctx)
			},
		})
	}
	return actions
}

func (c *switchCommand) mutateSwitchSidebarKill(ctx context.Context, action intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
	target := cleanOptionalPath(action.Value)
	if target == "" || target == switchSettingsSentinel {
		return c.switchSidebarRefreshUpdate(ctx)
	}
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	if target == cleanOptionalPath(homeDir) {
		return c.switchSidebarRefreshUpdate(ctx)
	}
	if c.validate == nil {
		return intpicker.DeferredUpdate{}, fmt.Errorf("switch directory validator is not configured")
	}
	if err := c.validate(target); err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	if c.identityErr != nil {
		return intpicker.DeferredUpdate{}, fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}
	if c.identity == nil {
		return intpicker.DeferredUpdate{}, fmt.Errorf("switch session identity resolver is not configured")
	}
	sessionName, err := c.identity.SessionIdentityForPath(target)
	if err != nil {
		return intpicker.DeferredUpdate{}, fmt.Errorf("resolve session identity: %w", err)
	}
	fallbackSession, err := c.previousActiveSession(ctx, sessionName)
	if err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	if fallbackSession == "" {
		return c.switchSidebarRefreshUpdate(ctx)
	}
	if err := c.killFocusedSession(ctx, sessionName, fallbackSession, nil); err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	c.focusSession = fallbackSession
	return c.switchSidebarRefreshUpdate(ctx)
}

func (c *switchCommand) switchSidebarRefreshUpdate(ctx context.Context) (intpicker.DeferredUpdate, error) {
	inputs, err := c.candidateInputs("")
	if err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	if c.discover == nil {
		return intpicker.DeferredUpdate{}, fmt.Errorf("switch candidate discovery is not configured")
	}
	paths, err := c.discover(inputs)
	if err != nil {
		return intpicker.DeferredUpdate{}, fmt.Errorf("discover switch candidates: %w", err)
	}
	_, items, sessionNames, err := c.renderFullRows(ctx, switchUISidebar, paths)
	if err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	update := intpicker.DeferredUpdate{Items: items}
	// After a kill, tmux switches to c.focusSession; move the sidebar cursor
	// to match by reverse-mapping the active session name back to its row
	// value. sessionNames is keyed by cleaned candidate path, so match items
	// through the same cleaning to keep FocusValue byte-identical to the row
	// Value the picker compares against. Empty focusSession or an absent row
	// leaves FocusValue empty, preserving the prior cursor-follow behaviour.
	if focus := strings.TrimSpace(c.focusSession); focus != "" {
		for _, item := range items {
			if strings.TrimSpace(sessionNames[cleanOptionalPath(item.Value)]) == focus {
				update.FocusValue = item.Value
				break
			}
		}
	}
	surface, err := c.switchPickerSurface(switchPlan{UI: switchUISidebar}, true)
	if err != nil {
		return intpicker.DeferredUpdate{}, err
	}
	if surface.PreviewCommand != "" {
		update.Preview = intpicker.Preview{Command: surface.PreviewCommand, Window: switchPreviewWindow(switchUISidebar)}
	}
	return update, nil
}

type switchPickerSurface struct {
	PreviewCommand  string
	Actions         []intpicker.Action
	InitialIndex    int
	InitialIndexSet bool
}

func (c *switchCommand) switchPickerSurface(plan switchPlan, includePreview bool) (switchPickerSurface, error) {
	if c.ephemeralBinary() == nil {
		return switchPickerSurface{}, nil
	}

	binaryPath, err := c.ephemeralBinary()()
	if err != nil {
		return switchPickerSurface{}, fmt.Errorf("resolve switch preview executable: %w", err)
	}

	surface := switchPickerSurface{}
	if includePreview {
		previewCommand, err := inttmux.BuildSwitchPreviewCommand(binaryPath, plan.UI)
		if err != nil {
			return switchPickerSurface{}, fmt.Errorf("build switch preview command: %w", err)
		}
		surface.PreviewCommand = previewCommand
	}

	if plan.UI == switchUISidebar {
		sidebarFocus, err := inttmux.BuildSwitchSidebarFocusCommand(binaryPath)
		if err != nil {
			return switchPickerSurface{}, fmt.Errorf("build switch sidebar-focus command: %w", err)
		}
		surface.Actions = append(surface.Actions, intpicker.Action{
			Key:     "focus",
			Intent:  intpicker.ActionCustom,
			Command: sidebarFocus,
		})
		if pos := switchSidebarInitialPos(plan); pos > 0 {
			surface.InitialIndex = pos - 1
			surface.InitialIndexSet = true
		}
		if pos := switchSidebarInitialFilteredPos(plan); pos > 0 {
			surface.InitialIndex = pos - 1
			surface.InitialIndexSet = true
		}
		return surface, nil
	}

	windowPrev, err := inttmux.BuildSwitchCycleWindowCommand(binaryPath, string(corepreview.DirectionPrev))
	if err != nil {
		return switchPickerSurface{}, fmt.Errorf("build switch window-prev command: %w", err)
	}
	windowNext, err := inttmux.BuildSwitchCycleWindowCommand(binaryPath, string(corepreview.DirectionNext))
	if err != nil {
		return switchPickerSurface{}, fmt.Errorf("build switch window-next command: %w", err)
	}
	panePrev, err := inttmux.BuildSwitchCyclePaneCommand(binaryPath, string(corepreview.DirectionPrev))
	if err != nil {
		return switchPickerSurface{}, fmt.Errorf("build switch pane-prev command: %w", err)
	}
	paneNext, err := inttmux.BuildSwitchCyclePaneCommand(binaryPath, string(corepreview.DirectionNext))
	if err != nil {
		return switchPickerSurface{}, fmt.Errorf("build switch pane-next command: %w", err)
	}

	surface.Actions = append(surface.Actions,
		intpicker.Action{Key: "left", Intent: intpicker.ActionCustom, Command: windowPrev, Refresh: true},
		intpicker.Action{Key: "right", Intent: intpicker.ActionCustom, Command: windowNext, Refresh: true},
		intpicker.Action{Key: "alt-up", Intent: intpicker.ActionCustom, Command: panePrev, Refresh: true},
		intpicker.Action{Key: "alt-down", Intent: intpicker.ActionCustom, Command: paneNext, Refresh: true},
	)

	return surface, nil
}

func pickerCloseActions(keys ...string) []intpicker.Action {
	return intpicker.CloseActions(keys...)
}

func pickerCloseBindings(keys ...string) []string {
	bindings := make([]string, 0, len(keys))
	for _, action := range pickerCloseActions(keys...) {
		bindings = append(bindings, action.Key+":abort")
	}
	return bindings
}

func pickerCloseActionsForPopupToggleMode(homeDir func() (string, error), lookupEnv func(string) string, mode string, fallback ...string) []intpicker.Action {
	return pickerCloseActions(effectivePickerKeysForPopupToggleMode(homeDir, lookupEnv, mode, fallback)...)
}

func pickerCloseBindingsForPopupToggleMode(homeDir func() (string, error), lookupEnv func(string) string, mode string, fallback ...string) []string {
	return pickerCloseBindings(effectivePickerKeysForPopupToggleMode(homeDir, lookupEnv, mode, fallback)...)
}

func popupToggleModeForSwitchUI(ui string) string {
	if ui == switchUISidebar {
		return "sessionizer-sidebar"
	}
	return "sessionizer"
}

func pickerKeyMatchesAction(homeDir func() (string, error), lookupEnv func(string) string, key, actionID string, fallback ...string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	return slices.Contains(effectivePickerKeysForActions(homeDir, lookupEnv, []string{actionID}, fallback), key)
}

func effectivePickerKeysForPopupToggleMode(homeDir func() (string, error), lookupEnv func(string) string, mode string, fallback []string) []string {
	actionID, ok := popupToggleActionIDForMode(mode)
	if !ok {
		return uniqueNonEmptyStrings(fallback)
	}
	return effectivePickerKeysForActions(homeDir, lookupEnv, []string{actionID}, fallback)
}

func effectivePickerKeysForActions(homeDir func() (string, error), lookupEnv func(string) string, actionIDs []string, fallback []string) []string {
	actions := defaultKeyBindingCatalog()
	if homeDir != nil {
		if merged, _, err := loadMergedKeyBindingCatalog(keymapLoader{homeDir: homeDir, lookupEnv: lookupEnv}); err == nil {
			actions = merged
		}
	}
	defaultActionKeys := map[string]bool{}
	for _, id := range actionIDs {
		action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), id)
		if !ok {
			continue
		}
		for _, chord := range keyBindingEffectivePlainChords(action) {
			if key := pickerKeyFromTmuxChord(chord); key != "" {
				defaultActionKeys[key] = true
			}
		}
	}
	var keys []string
	for _, key := range fallback {
		if defaultActionKeys[key] {
			continue
		}
		keys = append(keys, key)
	}
	for _, id := range actionIDs {
		action, ok := keyBindingActionByID(actions, id)
		if !ok {
			continue
		}
		for _, chord := range keyBindingEffectivePlainChords(action) {
			if key := pickerKeyFromTmuxChord(chord); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return uniqueNonEmptyStrings(keys)
}

func pickerKeyFromTmuxChord(chord string) string {
	chord = strings.TrimSpace(chord)
	switch chord {
	case "Enter":
		return "enter"
	case "Left", "Right", "Up", "Down":
		return strings.ToLower(chord)
	}
	if after, ok := strings.CutPrefix(chord, "M-S-"); ok {
		return "alt-shift-" + strings.ToLower(after)
	}
	if after, ok := strings.CutPrefix(chord, "M-"); ok {
		return "alt-" + strings.ToLower(after)
	}
	if after, ok := strings.CutPrefix(chord, "C-"); ok {
		return "ctrl-" + strings.ToLower(after)
	}
	if len(chord) == 1 && chord[0] >= 'A' && chord[0] <= 'Z' {
		return chord
	}
	return strings.ToLower(chord)
}

func switchPreviewWindow(ui string) string {
	switch ui {
	case switchUISidebar:
		return "down,25%,border-top"
	case switchUIPopup:
		return "right,60%,border-left"
	default:
		return ""
	}
}

func switchPickerFooter(ui, status string, homeDir func() (string, error), lookupEnv func(string) string) string {
	status = strings.TrimSpace(status)
	if ui == switchUISidebar {
		footer := pickerActionKeyGuide(homeDir, lookupEnv, []pickerActionKeyGuideItem{
			{ActionID: "Sidebar:PinProject", Label: "pin project"},
			{ActionID: "Sidebar:KillSession", Label: "kill session"},
		})
		if status != "" {
			footer += " | " + status
		}
		return projmuxFooter(footer)
	}
	parts := []string{
		"Preview follows the focused target.",
		"Destructive actions keep the current confirmation policy.",
	}
	if status != "" {
		parts = append(parts, status)
	}
	return projmuxFooter(strings.Join(parts, "\n"))
}

func projmuxFooter(text string) string {
	return strings.TrimSpace(settingsCatalogText(text))
}

type pickerActionKeyGuideItem struct {
	ActionID string
	Label    string
}

func pickerActionKeyGuide(homeDir func() (string, error), lookupEnv func(string) string, items []pickerActionKeyGuideItem) string {
	actions := defaultKeyBindingCatalog()
	if homeDir != nil {
		if merged, _, err := loadMergedKeyBindingCatalog(keymapLoader{homeDir: homeDir, lookupEnv: lookupEnv}); err == nil {
			actions = merged
		}
	}
	locale := appLocale(homeDir, lookupEnv)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		chord := pickerActionGuideChord(actions, item.ActionID)
		if chord == "" {
			continue
		}
		label := strings.TrimSpace(item.Label)
		if label == "" {
			label = item.ActionID
		}
		label = localizeUIText(locale, label)
		parts = append(parts, pickerActionGuideReadableChord(chord)+": "+label)
	}
	return strings.Join(parts, "  |  ")
}

func pickerActionGuideReadableChord(chord string) string {
	chord = strings.TrimSpace(chord)
	if len(chord) == 1 {
		return chord
	}
	return keybindingReadableChord(chord)
}

func pickerActionGuideChord(actions []keyBindingAction, actionID string) string {
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return ""
	}
	chords := keyBindingEffectivePlainChords(action)
	defaultAction, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	if ok {
		defaultChord := firstNonEmptyString(keyBindingEffectivePlainChords(defaultAction))
		if defaultChord != "" && slices.Contains(chords, defaultChord) {
			return defaultChord
		}
	}
	return firstNonEmptyString(chords)
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

func switchSidebarInitialFilteredPos(plan switchPlan) int {
	if strings.TrimSpace(plan.InitialQuery) == "" || strings.TrimSpace(plan.CurrentPath) == "" {
		return 0
	}
	filtered := intpicker.FilterItems(plan.Items, plan.InitialQuery)
	for idx, item := range filtered {
		if cleanOptionalPath(item.Value) == cleanOptionalPath(plan.CurrentPath) {
			return idx + 1
		}
	}
	return 0
}

func switchSessionNameForRow(plan switchPlan, value string) string {
	if plan.SessionNames == nil {
		return ""
	}
	return strings.TrimSpace(plan.SessionNames[cleanOptionalPath(value)])
}

func (c *switchCommand) previousActiveSession(ctx context.Context, targetSession string) (string, error) {
	resolver, ok := c.sessions.(switchRecentSessionsResolver)
	if !ok || resolver == nil {
		return "", nil
	}
	recent, err := resolver.RecentSessions(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve previous active tmux session: %w", err)
	}
	targetSession = strings.TrimSpace(targetSession)
	for _, sessionName := range recent {
		sessionName = strings.TrimSpace(sessionName)
		if sessionName == "" || sessionName == targetSession {
			continue
		}
		exists, err := c.switchSessionExists(ctx, sessionName)
		if err != nil {
			return "", err
		}
		if exists {
			return sessionName, nil
		}
	}
	return "", nil
}

func (c *switchCommand) killFocusedSession(ctx context.Context, sessionName, fallbackSession string, stdout io.Writer) error {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return fmt.Errorf("switch kill requires a target session")
	}
	if c.sessions == nil {
		return fmt.Errorf("switch session executor is not configured")
	}

	inspector, _ := c.sessions.(switchSessionInspector)
	if inspector != nil {
		exists, err := inspector.SessionExists(ctx, sessionName)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
	}

	killer, ok := c.sessions.(switchSessionKiller)
	if !ok || killer == nil {
		return fmt.Errorf("switch session killer is not configured")
	}
	fallbackSession = strings.TrimSpace(fallbackSession)
	if fallbackSession != "" && fallbackSession != sessionName {
		if err := c.sessions.OpenSession(ctx, fallbackSession); err != nil {
			return fmt.Errorf("open fallback tmux session %q before kill: %w", fallbackSession, err)
		}
	}
	if err := killer.KillSession(ctx, sessionName); err != nil {
		return fmt.Errorf("kill tmux session %q: %w", sessionName, err)
	}
	if c.cleanupKilledSession != nil {
		c.cleanupKilledSession(sessionName)
	}
	if stdout != nil {
		_, err := fmt.Fprintf(stdout, "killed: %s\n", sessionName)
		return err
	}
	return nil
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

type switchRowRenderMode int

const (
	switchRowRenderCheap switchRowRenderMode = iota
	switchRowRenderFull
)

func (c *switchCommand) renderRows(ctx context.Context, ui string, candidatePaths []string) ([]intpickercompat.Entry, []intpicker.Item, map[string]string, error) {
	mode := switchRowRenderFull
	if ui == switchUISidebar {
		mode = switchRowRenderCheap
	}
	return c.renderRowsWithMode(ctx, ui, candidatePaths, mode)
}

func (c *switchCommand) renderFullRows(ctx context.Context, ui string, candidatePaths []string) ([]intpickercompat.Entry, []intpicker.Item, map[string]string, error) {
	return c.renderRowsWithMode(ctx, ui, candidatePaths, switchRowRenderFull)
}

func (c *switchCommand) renderRowsWithMode(ctx context.Context, ui string, candidatePaths []string, mode switchRowRenderMode) ([]intpickercompat.Entry, []intpicker.Item, map[string]string, error) {
	renderCandidates := make([]intrender.SwitchCandidate, 0, len(candidatePaths))
	existingBySession, err := c.lookupExistingSessions(ctx, candidatePaths)
	if err != nil {
		return nil, nil, nil, err
	}
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, nil, nil, err
	}
	repoRoot := c.switchRepoRoot(homeDir)
	pinnedSet, err := c.loadPinnedSet()
	if err != nil {
		return nil, nil, nil, err
	}
	sessionNames := make(map[string]string, len(candidatePaths))
	attentionRanks := map[string]int(nil)
	aiBadgeKinds := map[string]string(nil)
	aiBadgeStyle := string(loadAIBadgeStyle(c.homeDir, c.lookupEnv))
	if mode == switchRowRenderFull {
		attentionRanks, aiBadgeKinds = c.switchAttentionBadges(ctx)
	}

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
			return nil, nil, nil, fmt.Errorf("render switch rows: resolve session identity for %q: %w", candidatePath, err)
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
		if ui == switchUIPopup && modeLabel == "new" {
			continue
		}

		gitBranch := ""
		var windowTabs []intrender.SwitchWindowTab
		if mode == switchRowRenderFull {
			gitBranch = c.resolveGitBranch(candidatePath)
			windowTabs = c.switchCardWindowTabs(ctx, sessionName, modeLabel)
		}
		renderCandidates = append(renderCandidates, intrender.SwitchCandidate{
			Path:          candidatePath,
			DisplayPath:   intrender.PrettyPath(candidatePath, homeDir, repoRoot),
			DisplayName:   switchProjectName(candidatePath, sessionName),
			SessionName:   sessionName,
			ModeLabel:     modeLabel,
			GitBranch:     gitBranch,
			WindowTabs:    windowTabs,
			UI:            ui,
			AttentionRank: attentionRanks[sessionName],
			AIBadgeKind:   aiBadgeKinds[sessionName],
			AIBadgeStyle:  aiBadgeStyle,
			Pinned:        pinnedSet[cleanOptionalPath(candidatePath)],
		})
	}

	sortSwitchCandidates(renderCandidates, homeDir)
	rows := intrender.BuildSwitchRows(renderCandidates)
	entries := make([]intpickercompat.Entry, 0, len(rows))
	items := make([]intpicker.Item, 0, len(rows))
	for _, row := range rows {
		item := row.Item
		if item.Label == "" {
			item.Label = intrender.FormatSwitchCardLabel(row.Item)
			item.MetaLines = nil
		}
		items = append(items, item)
		entries = append(entries, intpickercompat.Entry{
			Label:     item.EffectiveLabel(),
			Value:     row.Value,
			SearchKey: item.EffectiveSearchText(),
		})
	}

	return entries, items, sessionNames, nil
}

func (c *switchCommand) switchCardWindowTabs(ctx context.Context, sessionName, modeLabel string) []intrender.SwitchWindowTab {
	if modeLabel != "existing" {
		return nil
	}
	inventory, err := c.requireSwitchPreviewInventory()
	if err != nil {
		return nil
	}
	windows, err := inventory.SessionWindows(ctx, sessionName)
	if err != nil {
		return nil
	}
	panes, err := inventory.SessionPanes(ctx, sessionName)
	if err != nil {
		panes = nil
	}
	attentionRanks, aiBadgeKinds := switchWindowAttentionBadges(panes)
	aiBadgeStyle := string(loadAIBadgeStyle(c.homeDir, c.lookupEnv))
	tabs := make([]intrender.SwitchWindowTab, 0, len(windows))
	for _, window := range windows {
		name := strings.TrimSpace(window.Name)
		if name == "" {
			name = strings.TrimSpace(window.Index)
		}
		if name == "" {
			continue
		}
		tabs = append(tabs, intrender.SwitchWindowTab{
			Name:          name,
			AttentionRank: attentionRanks[strings.TrimSpace(window.Index)],
			AIBadgeKind:   aiBadgeKinds[strings.TrimSpace(window.Index)],
			AIBadgeStyle:  aiBadgeStyle,
			Active:        window.Active,
		})
	}
	return tabs
}

func switchWindowAttentionBadges(panes []corepreview.Pane) (map[string]int, map[string]string) {
	ranks := make(map[string]int)
	kinds := make(map[string]string)
	for _, pane := range panes {
		windowIndex := strings.TrimSpace(pane.WindowIndex)
		if windowIndex == "" {
			continue
		}
		kinds[windowIndex] = aggregateAIBadgeKind(kinds[windowIndex], semanticBadgeKindForPreviewPane(pane))
		ranks[windowIndex] = attentionRankForBadgeKind(kinds[windowIndex])
	}
	return ranks, kinds
}

func (c *switchCommand) switchAttentionBadges(ctx context.Context) (map[string]int, map[string]string) {
	inventory, err := c.requireSwitchPreviewInventory()
	if err != nil {
		return nil, nil
	}

	panes, err := inventory.SessionPanes(ctx, "")
	if err != nil {
		return nil, nil
	}

	ranks := make(map[string]int)
	kinds := make(map[string]string)
	for _, pane := range panes {
		sessionName := strings.TrimSpace(pane.SessionName)
		if sessionName == "" {
			continue
		}
		kinds[sessionName] = aggregateAIBadgeKind(kinds[sessionName], semanticBadgeKindForPreviewPane(pane))
		ranks[sessionName] = attentionRankForBadgeKind(kinds[sessionName])
	}
	return ranks, kinds
}

func semanticBadgeKindForPreviewPane(pane corepreview.Pane) string {
	if kind := normalizeAIBadgeKind(pane.AIBadgeKind); kind != "" {
		return kind
	}
	switch {
	case pane.AttentionState == attentionStateBusy || strings.TrimSpace(pane.AIState) == "thinking" || hasBraillePrefix(pane.Title):
		return aiBadgeKindInProgress
	case pane.AttentionState == attentionStateReply || strings.TrimSpace(pane.AIState) == "waiting" || hasAttentionPrefix(pane.Title):
		return aiBadgeKindResponseComplete
	default:
		return ""
	}
}

func aggregateAIBadgeKind(current, next string) string {
	return aibadge.Aggregate(current, next)
}

func attentionRankForBadgeKind(kind string) int {
	switch normalizeAIBadgeKind(kind) {
	case aiBadgeKindApprovalRequired, aiBadgeKindInputRequired, aiBadgeKindResponseComplete:
		return 1
	case aiBadgeKindInProgress:
		return 2
	default:
		return 0
	}
}

func sortSwitchCandidates(candidates []intrender.SwitchCandidate, homeDir string) {
	homeDir = cleanOptionalPath(homeDir)
	slices.SortStableFunc(candidates, func(a, b intrender.SwitchCandidate) int {
		if a.Path == switchSettingsSentinel && b.Path != switchSettingsSentinel {
			return 1
		}
		if b.Path == switchSettingsSentinel && a.Path != switchSettingsSentinel {
			return -1
		}

		aHome := homeDir != "" && cleanOptionalPath(a.Path) == homeDir
		bHome := homeDir != "" && cleanOptionalPath(b.Path) == homeDir
		if aHome != bHome {
			if aHome {
				return -1
			}
			return 1
		}

		aExisting := a.ModeLabel == "existing"
		bExisting := b.ModeLabel == "existing"
		if aExisting != bExisting {
			if aExisting {
				return -1
			}
			return 1
		}

		aPinned := a.Pinned
		bPinned := b.Pinned
		if aPinned != bPinned {
			if aPinned {
				return -1
			}
			return 1
		}

		aName := strings.ToLower(strings.TrimSpace(a.DisplayName))
		bName := strings.ToLower(strings.TrimSpace(b.DisplayName))
		if aName < bName {
			return -1
		}
		if aName > bName {
			return 1
		}
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
}

// switchProjectName resolves a switch sidebar row's project display name via
// the unified project-identity resolver. The candidate path is the canonical
// project directory (a sessionizer root, not a live drifting pane cwd), and the
// row already knows the session name, so a worktree candidate normalizes to its
// main repo and a regular-repo candidate shows the de-slugged session name —
// the same name the statusbar and notify sidebar now show. Falls back to the
// former cwd basename only when the resolver yields nothing.
func switchProjectName(path, sessionName string) string {
	path = cleanOptionalPath(path)
	if name := resolveProjectDisplayName(projectidentity.Inputs{
		PaneCWD:     path,
		SessionName: sessionName,
	}, projectidentity.OSFS); name != "" {
		return name
	}
	if path == "" {
		return ""
	}
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return path
	}
	return name
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

func (c *switchCommand) lookupExistingSessions(ctx context.Context, candidatePaths []string) (map[string]bool, error) {
	sessionNames := make(map[string]struct{}, len(candidatePaths))
	for _, candidatePath := range candidatePaths {
		if candidatePath == switchSettingsSentinel {
			continue
		}
		sessionName, err := c.identity.SessionIdentityForPath(candidatePath)
		if err != nil {
			return nil, fmt.Errorf("check existing switch sessions: resolve session identity for %q: %w", candidatePath, err)
		}
		if strings.TrimSpace(sessionName) == "" {
			continue
		}
		sessionNames[sessionName] = struct{}{}
	}
	if len(sessionNames) == 0 {
		return nil, nil
	}

	if bulk, ok := c.sessions.(switchBulkSessionInspector); ok && bulk != nil {
		existing, err := bulk.ExistingSessions(ctx)
		if err == nil {
			existingBySession := make(map[string]bool, len(sessionNames))
			for sessionName := range sessionNames {
				existingBySession[sessionName] = existing[sessionName]
			}
			return existingBySession, nil
		}
	}

	inspector, ok := c.sessions.(switchSessionInspector)
	if !ok || inspector == nil {
		return nil, nil
	}

	existingBySession := make(map[string]bool, len(sessionNames))
	for sessionName := range sessionNames {
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
	fmt.Fprintln(w, "  projmux switch kill [path]")
	fmt.Fprintln(w, "  projmux switch open <path>")
	fmt.Fprintln(w, "  projmux switch settings")
	fmt.Fprintln(w, "  projmux switch preview [path]")
	fmt.Fprintln(w, "  projmux switch cycle-pane <path> <next|prev>")
	fmt.Fprintln(w, "  projmux switch cycle-window <path> <next|prev>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  --ui string   Candidate surface to prepare (popup or sidebar) (default \"popup\")")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Picker Actions:")
	fmt.Fprintln(w, "  ctrl-x        Kill the focused existing session and reopen the picker")
	fmt.Fprintln(w, "  alt-p         Toggle a pin on the focused candidate and reopen the picker")
}

func (c *switchCommand) settingsEntries() ([]intpickercompat.Entry, error) {
	pins, err := c.loadPins()
	if err != nil {
		return nil, err
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	repoRoot := c.switchRepoRoot(homeDir)

	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := make([]intpickercompat.Entry, 0, len(pins)+3)
	entries = append(entries, intpickercompat.Entry{
		Label: localizeUIText(locale, "+ Add pin..."),
		Value: "add-interactive",
	})
	currentTarget, err := c.resolveSwitchTarget(nil, "switch settings")
	if err == nil && currentTarget != "" && currentTarget != switchSettingsSentinel && !containsString(pins, currentTarget) {
		entries = append(entries, intpickercompat.Entry{
			Label: localizeUIText(locale, "+ Add current pin  ") + intrender.PrettyPath(currentTarget, homeDir, repoRoot),
			Value: "add:" + currentTarget,
		})
	}
	if len(pins) != 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: localizeUIText(locale, "x Clear all pins"),
			Value: "clear",
		})
	}
	for _, pin := range pins {
		entries = append(entries, intpickercompat.Entry{
			Label: localizeUIText(locale, "x Remove  ") + intrender.PrettyPath(pin, homeDir, repoRoot),
			Value: "pin:" + pin,
		})
	}
	return entries, nil
}

func (c *switchCommand) addPinEntries() ([]intpickercompat.Entry, error) {
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
	repoRoot := c.switchRepoRoot(homeDir)

	entries := make([]intpickercompat.Entry, 0, len(paths))
	for _, path := range paths {
		if path == switchSettingsSentinel || containsString(pins, path) {
			continue
		}

		entries = append(entries, intpickercompat.Entry{
			Label: intrender.PrettyPath(path, homeDir, repoRoot),
			Value: path,
		})
	}

	return entries, nil
}

func (c *switchCommand) filesystemPinEntries() ([]intpickercompat.Entry, error) {
	paths, err := c.filesystemPinCandidates()
	if err != nil {
		return nil, err
	}

	pins, err := c.loadPins()
	if err != nil {
		return nil, err
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	repoRoot := c.switchRepoRoot(homeDir)

	entries := make([]intpickercompat.Entry, 0, len(paths))
	for _, path := range paths {
		if containsString(pins, path) {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, intrender.PrettyPath(path, homeDir, repoRoot), ""),
			Value: "switch:add:" + path,
		})
	}
	return entries, nil
}

// filesystemWorkdirEntries renders filesystem-scan entries that map to the
// "workdir:add:<path>" settings action. Already-saved workdirs are skipped.
func (c *switchCommand) filesystemWorkdirEntries() ([]intpickercompat.Entry, error) {
	paths, err := c.filesystemPinCandidates()
	if err != nil {
		return nil, err
	}

	saved, err := c.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}
	savedSet := make(map[string]struct{}, len(saved))
	for _, dir := range saved {
		savedSet[dir] = struct{}{}
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	repoRoot := c.switchRepoRoot(homeDir)

	entries := make([]intpickercompat.Entry, 0, len(paths))
	for _, path := range paths {
		if _, ok := savedSet[path]; ok {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, intrender.PrettyPath(path, homeDir, repoRoot), ""),
			Value: "workdir:add:" + path,
		})
	}
	return entries, nil
}

func (c *switchCommand) loadSavedWorkdirs() ([]string, error) {
	if c.loadWorkdirs == nil {
		return nil, nil
	}
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	saved, err := c.loadWorkdirs(homeDir)
	if err != nil {
		return nil, fmt.Errorf("load saved workdirs: %w", err)
	}
	return saved, nil
}

// envWorkdirSources returns the read-only ManagedRoots/legacy env values
// surfaced for informational rows in the Workdirs picker. The first return
// names the environment variable; the second is its colon-separated value.
func (c *switchCommand) envWorkdirSources() []envWorkdirSource {
	return []envWorkdirSource{
		{Name: managedRootsEnvVar, Value: envValue(c.lookupEnv, managedRootsEnvVar)},
		{Name: legacyManagedRootsEnvVar, Value: envValue(c.lookupEnv, legacyManagedRootsEnvVar)},
	}
}

type envWorkdirSource struct {
	Name  string
	Value string
}

// addWorkdir persists target to the saved workdirs file. Returns an
// already-present message via stdout if the entry is not new.
func (c *switchCommand) addWorkdir(target string, stdout io.Writer) error {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return err
	}
	added, err := config.AddWorkdir(homeDir, target)
	if err != nil {
		return fmt.Errorf("add workdir: %w", err)
	}
	if stdout == nil {
		return nil
	}
	if added {
		_, err = fmt.Fprintf(stdout, "added workdir: %s\n", target)
		return err
	}
	_, err = fmt.Fprintf(stdout, "already saved workdir: %s\n", target)
	return err
}

// removeWorkdir deletes target from the saved workdirs file. Reports a no-op
// message when the entry was not present.
func (c *switchCommand) removeWorkdir(target string, stdout io.Writer) error {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return err
	}
	removed, err := config.RemoveWorkdir(homeDir, target)
	if err != nil {
		return fmt.Errorf("remove workdir: %w", err)
	}
	if stdout == nil {
		return nil
	}
	if removed {
		_, err = fmt.Fprintf(stdout, "removed workdir: %s\n", target)
		return err
	}
	_, err = fmt.Fprintf(stdout, "workdir not saved: %s\n", target)
	return err
}

// executeWorkdirSettingsAction handles "add:<path>" and "remove:<path>"
// actions emitted from the settings UX.
func (c *switchCommand) executeWorkdirSettingsAction(action string, stdout, stderr io.Writer) error {
	switch {
	case strings.HasPrefix(action, "add:"):
		return c.addWorkdir(strings.TrimPrefix(action, "add:"), stdout)
	case strings.HasPrefix(action, "remove:"):
		return c.removeWorkdir(strings.TrimPrefix(action, "remove:"), stdout)
	default:
		printSwitchUsage(stderr)
		return fmt.Errorf("unknown workdir settings action: %s", action)
	}
}

func (c *switchCommand) filesystemPinCandidates() ([]string, error) {
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	repoRoot := c.switchRepoRoot(homeDir)
	roots := switchFilesystemPinRoots(homeDir, repoRoot)

	builder := orderedPathSet{}
	for _, root := range roots {
		if err := appendScannedDirs(&builder, root, 3); err != nil {
			return nil, err
		}
	}
	for _, name := range switchPinHiddenWhitelist {
		path := filepath.Join(homeDir, name)
		if dirExistsForSwitch(path) {
			builder.append(path)
		}
	}
	return builder.values, nil
}

func switchFilesystemPinRoots(homeDir, repoRoot string) []string {
	roots := []string{
		repoRoot,
		filepath.Join(homeDir, "source"),
		filepath.Join(homeDir, "work"),
		filepath.Join(homeDir, "projects"),
		filepath.Join(homeDir, "code"),
		filepath.Join(homeDir, "src"),
		homeDir,
	}
	builder := orderedPathSet{}
	for _, root := range roots {
		if dirExistsForSwitch(root) {
			builder.append(root)
		}
	}
	return builder.values
}

func appendScannedDirs(builder *orderedPathSet, root string, maxDepth int) error {
	root = cleanOptionalPath(root)
	if root == "" || !dirExistsForSwitch(root) {
		return nil
	}
	return scanDirs(builder, root, 0, maxDepth)
}

func scanDirs(builder *orderedPathSet, dir string, depth, maxDepth int) error {
	if depth > maxDepth {
		return nil
	}
	builder.append(dir)
	if depth == maxDepth {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == ".git" || strings.HasPrefix(name, ".") {
			continue
		}
		if err := scanDirs(builder, filepath.Join(dir, name), depth+1, maxDepth); err != nil {
			return err
		}
	}
	return nil
}

type orderedPathSet struct {
	values []string
	seen   map[string]struct{}
}

func (s *orderedPathSet) append(path string) {
	path = cleanOptionalPath(path)
	if path == "" {
		return
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	if _, ok := s.seen[path]; ok {
		return
	}
	s.seen[path] = struct{}{}
	s.values = append(s.values, path)
}

func dirExistsForSwitch(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
	repoRoot := c.switchRepoRoot(homeDir)

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
	return detectGitBranchWithRunner(path, defaultSwitchGitCommandLimit, runSwitchGitCommand)
}

func detectGitBranchWithRunner(path string, limit time.Duration, runner func(context.Context, string, ...string) ([]byte, error)) string {
	path = cleanOptionalPath(path)
	if path == "" || runner == nil {
		return ""
	}
	if limit <= 0 {
		limit = defaultSwitchGitCommandLimit
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	if output, err := runner(ctx, "git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(string(output))
	}
	if output, err := runner(ctx, "git", "-C", path, "rev-parse", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(string(output))
	}
	return ""
}

func runSwitchGitCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func defaultSwitchKubeInfo(sessionName string) switchKubeInfo {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return switchKubeInfo{}
	}
	path := switchKubeSessionPath(sessionName)
	if path == "" {
		return switchKubeInfo{}
	}
	if _, err := os.Stat(path); err != nil {
		return switchKubeInfo{}
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return switchKubeInfo{}
	}
	return switchKubeInfo{
		Context:   runKubectlConfig(path, "current-context"),
		Namespace: runKubectlConfig(path, "view", "--minify", "--output", "jsonpath={..namespace}"),
	}
}

func switchKubeSessionPath(sessionName string) string {
	root := strings.TrimRight(os.Getenv("XDG_RUNTIME_DIR"), string(filepath.Separator))
	if root == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			return ""
		}
		root = filepath.Join(homeDir, ".cache")
	}
	return filepath.Join(root, "kube-sessions", sessionName+".yaml")
}

func runKubectlConfig(kubeconfig string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	fullArgs := append([]string{"config"}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", fullArgs...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
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
