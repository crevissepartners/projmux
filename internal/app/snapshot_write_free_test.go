package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// snapshotWriteFreeHomes is one isolated HOME/XDG layout whose legacy
// sessions directory already holds a snapshot file written by an older
// release, so any later read-triggered rewrite or removal under it is
// observable. projmux no longer knows this directory; the tests name it by its
// literal historical path.
type snapshotWriteFreeHomes struct {
	root        string
	home        string
	stateHome   string
	configHome  string
	sessionsDir string
}

func newSnapshotWriteFreeHomes(t *testing.T, session, snapshotRoot string) snapshotWriteFreeHomes {
	t.Helper()
	root := t.TempDir()
	homes := snapshotWriteFreeHomes{
		root:       root,
		home:       filepath.Join(root, "home"),
		stateHome:  filepath.Join(root, "state"),
		configHome: filepath.Join(root, "config"),
	}
	for _, dir := range []string{homes.home, homes.stateHome, homes.configHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", homes.home)
	t.Setenv("USERPROFILE", homes.home)
	t.Setenv("XDG_STATE_HOME", homes.stateHome)
	t.Setenv("XDG_CONFIG_HOME", homes.configHome)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	homes.sessionsDir = filepath.Join(paths.StateDir, "sessions")
	if want := filepath.Join(homes.stateHome, "projmux", "sessions"); homes.sessionsDir != want {
		t.Fatalf("sessions dir = %q, want isolated %q", homes.sessionsDir, want)
	}
	writeLegacyProjectSnapshotFile(t, homes.sessionsDir, session, snapshotRoot)
	return homes
}

// writeLegacyProjectSnapshotFile writes one snapshot file in the shape older
// releases saved under `${XDG_STATE_HOME}/projmux/sessions`, and returns its
// path. Current projmux must neither read nor write it.
func writeLegacyProjectSnapshotFile(t *testing.T, sessionsDir, session, root string) string {
	t.Helper()
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":1,"session":%q,"source":"autosave","default_cwd":%q,"saved_at":"2026-08-23T10:00:00Z","windows":[{"index":0,"name":"main","active_pane_index":0,"panes":[{"index":0,"cwd":%q,"recipe":{"kind":"shell"}}]}]}`+"\n", session, root, root)
	path := filepath.Join(sessionsDir, session+".json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed legacy snapshot: %v", err)
	}
	return path
}

// writeLegacyAutosaveSettings turns every retired autosave switch on in the
// files older releases read, so a test can prove nothing reacts to them.
func writeLegacyAutosaveSettings(t *testing.T, configDir, session string) {
	t.Helper()
	for rel, body := range map[string]string{
		"sessionstate-autosave":                                     "on\n",
		"sessionstate-autosave-interval":                            "1s\n",
		filepath.Join("sessionstate-projects", session, "autosave"): "on\n",
	} {
		path := filepath.Join(configDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// treeFingerprint lists every entry below root with its kind, mode, size,
// modification time, and content hash, so a rewrite with identical bytes is
// still visible through its mtime.
func treeFingerprint(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		sum := "-"
		if entry.Type().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(body)
			sum = hex.EncodeToString(digest[:])
		}
		out = append(out, fmt.Sprintf("%s|%s|%o|%d|%d|%s", path, entry.Type(), info.Mode().Perm(), info.Size(), info.ModTime().UnixNano(), sum))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func assertTreeUnchanged(t *testing.T, label string, before, after []string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Fatalf("%s changed\nbefore: %v\nafter:  %v", label, before, after)
	}
}

// TestAutosaveSessionStateRouteIsWriteFreeNoOp pins the retained hidden route
// that older generated tmux configs still call from status-format. Every
// autosave switch is set ON, yet the route touches neither tmux, the snapshot
// store, nor the diagnostics log, and exits 0 for every historical flag form.
func TestAutosaveSessionStateRouteIsWriteFreeNoOp(t *testing.T) {
	homes := newSnapshotWriteFreeHomes(t, "workspace", "/srv/workspace")
	t.Setenv("PROJMUX_SESSIONSTATE_AUTOSAVE", "on")
	t.Setenv("PROJMUX_SESSIONSTATE_DEBUG", "1")
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyAutosaveSettings(t, paths.ConfigDir, "workspace")
	before := treeFingerprint(t, homes.root)
	sessionsBefore := treeFingerprint(t, homes.sessionsDir)

	writer := &appLifecycleWriter{}
	lifecycle := diagnostics.NewLifecycleRecorder(writer, "autosave-noop", "0.15.3", "tmux")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			recordedTmuxCallKey("tmux", "display-message", "-p", "#{session_name}"): "workspace\n",
		},
	}
	cmd := newTmuxCommand(lifecycle)
	cmd.runner = runner
	cmd.popup = nil

	for _, args := range [][]string{
		{"autosave-session-state"},
		{"autosave-session-state", "--quiet"},
		{"autosave-session-state", "--force"},
		{"autosave-session-state", "--force", "--quiet"},
	} {
		var stdout, stderr bytes.Buffer
		if err := cmd.Run(args, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%q) error = %v, want nil", args, err)
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("Run(%q) stdout=%q stderr=%q, want silence", args, stdout.String(), stderr.String())
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("tmux calls = %#v, want none", runner.calls)
	}
	if len(writer.events) != 0 || lifecycle.RecordedOutcome() {
		t.Fatalf("diagnostics events = %#v, want none", writer.events)
	}
	assertTreeUnchanged(t, "sessions dir", sessionsBefore, treeFingerprint(t, homes.sessionsDir))
	assertTreeUnchanged(t, "isolated HOME/XDG tree", before, treeFingerprint(t, homes.root))
	if _, err := os.Lstat(filepath.Join(homes.stateHome, "projmux", "logs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("route created a diagnostics log directory: %v", err)
	}
}

// TestAppTmuxConfigNeverRendersAutosaveJob covers every HUD visibility
// combination, including the all-off collapse, on both generated surfaces.
func TestAppTmuxConfigNeverRendersAutosaveJob(t *testing.T) {
	t.Parallel()

	effective := fallbackRenderThemeSource().effective
	catalog := defaultKeyBindingCatalog()
	surfaces := map[string]string{
		"app/default": tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(
			"/tmp/projmux", "/bin/sh", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, catalog, false, effective),
		"standalone/default": tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(
			"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, catalog, false, effective),
	}
	for _, notifications := range []config.StatusbarVisibility{config.StatusbarVisibilityOn, config.StatusbarVisibilityOff} {
		for _, usage := range []config.StatusbarVisibility{config.StatusbarVisibilityOn, config.StatusbarVisibilityOff} {
			visibility := statusbarHUDVisibilitySet{Notifications: notifications, AgentUsage: usage}
			name := string(notifications) + "-" + string(usage)
			surfaces["app/"+name] = tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(
				"/tmp/projmux", "/bin/sh", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, visibility, catalog, false, effective)
			surfaces["standalone/"+name] = tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(
				"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, visibility, catalog, false, effective)
			for _, row := range statusbarRowFormatLines("'/tmp/projmux'", visibility) {
				if strings.Contains(row, "autosave-session-state") {
					t.Fatalf("%s row renders the retired autosave job: %q", name, row)
				}
			}
		}
	}
	for name, generated := range surfaces {
		if !strings.Contains(generated, "status-format[0]") {
			t.Fatalf("%s config has no status-format row; the assertion would be vacuous", name)
		}
		if strings.Contains(generated, "autosave-session-state") {
			t.Fatalf("%s config renders the retired autosave job", name)
		}
	}
}

// TestQuitNeverWritesProjectSnapshots pins quit to a two-row picker with no
// save action in either locale, and a confirmed quit that leaves the sessions
// directory untouched.
func TestQuitNeverWritesProjectSnapshots(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, i18n.Locale("ko-KR")} {
		options := localizePickerOptions(nil, nil, func() intpickercompat.Options {
			o := quitActionOptions(locale)
			o.Locale = locale
			return o
		}())
		if got, want := entryValues(options.Entries), []string{quitActionQuit, quitActionCancel}; !slices.Equal(got, want) {
			t.Fatalf("%s quit picker values = %#v, want %#v", locale, got, want)
		}
		for _, entry := range options.Entries {
			lower := strings.ToLower(entry.Label + entry.Value)
			for _, banned := range []string{"snapshot", "save", "스냅샷", "저장"} {
				if strings.Contains(lower, banned) {
					t.Fatalf("%s quit row %#v offers a save action (%q)", locale, entry, banned)
				}
			}
		}
	}

	homes := newSnapshotWriteFreeHomes(t, "workspace", "/srv/workspace")
	before := treeFingerprint(t, homes.root)
	path := "/tmp/projmux-quit-write-free.sock"
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", "@projmux_app"):                  "1\n",
			recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): defaultAppSocket + "\n",
		},
	}
	cmd := newQuitCommand()
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
		return intpickercompat.Result{Key: "enter", Value: quitActionQuit}, nil
	}))
	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("quit stdout = %q, want no capture report", stdout.String())
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, "list-panes") || strings.Contains(joined, "list-windows") || strings.Contains(joined, "capture") {
			t.Fatalf("quit observed topology for capture: %#v", runner.calls)
		}
	}
	last := runner.calls[len(runner.calls)-1]
	if !slices.Contains(last.args, "kill-server") {
		t.Fatalf("quit terminal call = %#v, want the guarded kill", last)
	}
	assertTreeUnchanged(t, "isolated HOME/XDG tree after quit", before, treeFingerprint(t, homes.root))
}

// TestContinueProjectUnregisteredRootRefusesWithoutReadingSnapshots uses the
// real Registry file and the legacy snapshot directory older releases wrote. A
// same-session, same-root snapshot file is present, and Continue still refuses:
// projmux no longer reads snapshot files at all.
func TestContinueProjectUnregisteredRootRefusesWithoutReadingSnapshots(t *testing.T) {
	workRoot := t.TempDir()
	continued := filepath.Join(workRoot, "continued")
	other := filepath.Join(workRoot, "other")
	for _, dir := range []string{continued, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	homes := newSnapshotWriteFreeHomes(t, "continued", continued)

	// Register an unrelated Project so registry.json exists on disk and its
	// bytes can be compared.
	resources := newResourceStore()
	if _, err := resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		_, err := mutator.RegisterProject(working, coremetadata.RegisterProjectOptions{Root: other, SessionName: "other", DefaultShell: "/bin/sh"})
		return err
	}); err != nil {
		t.Fatalf("seed Registry: %v", err)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	registryPath := intmetadata.PathFor(paths.StateDir)
	if !strings.HasPrefix(registryPath, homes.stateHome) {
		t.Fatalf("registry path %q escaped isolated state home %q", registryPath, homes.stateHome)
	}
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read seeded registry: %v", err)
	}
	registry, err := resources.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.ProjectByRoot(continued); ok {
		t.Fatal("fixture registered the continued root")
	}
	sessionsBefore := treeFingerprint(t, homes.sessionsDir)
	stateBefore := treeFingerprint(t, homes.stateHome)

	starter := newRegistryProjectFreshStarter()
	starter.runner = &recordingTmuxRunner{}
	opened, err := starter.ContinueProject(context.Background(), continued, "continued")
	if err == nil {
		t.Fatalf("ContinueProject() = %+v, want refusal", opened)
	}
	var staged projectLifecycleStageError
	if !errors.As(err, &staged) || staged.action != coremetadata.ProjectLifecycleContinue || staged.stage != "state-table" {
		t.Fatalf("ContinueProject() error = %#v, want typed state-table Continue refusal", err)
	}
	if !strings.Contains(err.Error(), "choose Clear layout and open") || !strings.Contains(err.Error(), "is not a registered Project") {
		t.Fatalf("ContinueProject() error = %q, want Clear layout and open guidance", err)
	}
	// The temp root itself may spell the test name; only the message matters.
	if strings.Contains(strings.ToLower(strings.ReplaceAll(err.Error(), continued, "<root>")), "snapshot") {
		t.Fatalf("ContinueProject() error = %q, want no snapshot wording", err)
	}
	if opened.project.Metadata.UID != "" || opened.bootstrapped || opened.materializeTopology {
		t.Fatalf("refused Continue returned a Project: %+v", opened)
	}

	registryAfter, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(registryBefore, registryAfter) {
		t.Fatalf("registry.json changed\nbefore: %s\nafter:  %s", registryBefore, registryAfter)
	}
	assertTreeUnchanged(t, "sessions dir", sessionsBefore, treeFingerprint(t, homes.sessionsDir))
	assertTreeUnchanged(t, "state home", stateBefore, treeFingerprint(t, homes.stateHome))
	if calls := starter.runner.(*recordingTmuxRunner).calls; len(calls) != 0 {
		t.Fatalf("refused Continue reached tmux: %#v", calls)
	}
}
