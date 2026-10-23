package app

// Independent original orchestration reference copied from frozen 67a54c89
// (original archive SHA in the owner-accepted Task3 source freeze). Only test
// symbol names change. Fresh per-test original/candidate operands are identical;
// this is a new pair oracle, never cross-root byte parity with B v4. Original B
// and Task5 paths are never read as writable fixtures or reused.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

type statusbarParityCase struct {
	Name, Value, Fault, ErrorFragment                                  string
	Rich, Skip, MissingRunner, ExpectError, Saved, Generated, Sentinel bool
	Injected                                                           int
	Semantic                                                           []string
}
type statusbarParityError struct {
	Type, Text         string
	Unwrap             *statusbarParityError
	Op, Path, Old, New string
	Errno              *uint64
}

func statusbarParityErrorOf(err error) *statusbarParityError {
	if err == nil {
		return nil
	}
	e := &statusbarParityError{Type: fmt.Sprintf("%T", err), Text: err.Error()}
	switch v := err.(type) {
	case *os.PathError:
		e.Op = v.Op
		e.Path = v.Path
	case *os.LinkError:
		e.Op = v.Op
		e.Old = v.Old
		e.New = v.New
	}
	if v, ok := err.(syscall.Errno); ok {
		n := uint64(v)
		e.Errno = &n
	}
	e.Unwrap = statusbarParityErrorOf(errors.Unwrap(err))
	return e
}

type statusbarParityFile struct {
	Kind                  string
	Mode                  uint32
	Device, Inode         uint64
	Size, MtimeNS         int64
	Bytes                 []byte
	SHA256                string
	ReadError, LstatError *statusbarParityError
}

func statusbarParityInventory(root string) (map[string]statusbarParityFile, error) {
	out := map[string]statusbarParityFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if walkErr != nil {
			out[rel] = statusbarParityFile{LstatError: statusbarParityErrorOf(walkErr)}
			return walkErr
		}
		st, e := os.Lstat(path)
		if e != nil {
			out[rel] = statusbarParityFile{LstatError: statusbarParityErrorOf(e)}
			return e
		}
		row := statusbarParityFile{Mode: uint32(st.Mode()), Size: st.Size(), MtimeNS: st.ModTime().UnixNano()}
		if raw, ok := st.Sys().(*syscall.Stat_t); ok {
			row.Device = uint64(raw.Dev)
			row.Inode = uint64(raw.Ino)
		}
		switch {
		case st.IsDir():
			row.Kind = "directory"
		case st.Mode().IsRegular():
			row.Kind = "regular"
		default:
			row.Kind = "unexpected"
		}
		// Capture type/mode even on read failure. Attempt directories too: raw
		// PathError type/op/path/errno chain is evidence, not an empty byte value.
		row.Bytes, e = os.ReadFile(path)
		row.ReadError = statusbarParityErrorOf(e)
		if e == nil {
			row.SHA256 = fmt.Sprintf("%x", sha256.Sum256(row.Bytes))
		}
		out[rel] = row
		if row.Kind == "unexpected" {
			return fmt.Errorf("unexpected fixture type %s", path)
		}
		return nil
	})
	return out, err
}

type statusbarParityRunner struct {
	base      settingsLiveTestRunner
	fault     string
	injected  error
	journal   [][]string
	semantic  []string
	rootsRead bool
	hits      int
}

func (r *statusbarParityRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.journal = append(r.journal, append([]string{name}, args...))
	if handled, out, e := answerTmuxReadSequence(ctx, name, args, r.Run); handled {
		return out, e
	}
	a := tmuxCommandArgv(args)
	joined := strings.Join(a, " ")
	if r.fault == "no-server" {
		r.hits++
		return nil, r.injected
	}
	if strings.Contains(joined, "show-options -gqv @projmux_app") && r.fault == "foreign" {
		r.hits++
		return []byte("0\n"), nil
	}
	if r.fault == "standalone" && (strings.Contains(joined, "show-options -gqv @projmux_app") || strings.Contains(joined, "show-options -gqv "+runtimeMutationSocketNameOption)) {
		r.hits++
		return nil, nil
	}
	if r.rootsRead {
		if r.fault == "pid-drift" && strings.Contains(joined, "#{pid}") {
			r.hits++
			return []byte("9999\n"), nil
		}
		if r.fault == "socket-drift" && strings.Contains(joined, "#{socket_path}") {
			r.hits++
			return []byte("/task3/foreign\n"), nil
		}
		if r.fault == "marker-drift" && strings.Contains(joined, "show-options -gqv "+runtimeMutationSocketNameOption) {
			r.hits++
			return []byte("foreign\n"), nil
		}
	}
	if len(a) > 0 && a[len(a)-1] == tmuxSequenceRootsOption {
		r.semantic = append(r.semantic, "roots")
		if r.fault == "roots-read" {
			r.hits++
			return nil, r.injected
		}
		r.rootsRead = true
		return []byte("C-k\n"), nil
	}
	if len(a) > 0 && a[len(a)-1] == tmuxSequenceTablesOption {
		r.semantic = append(r.semantic, "tables")
		if r.fault == "tables-read" {
			r.hits++
			return nil, r.injected
		}
		return []byte(keySequenceTableName([]string{"C-k"}) + "\n"), nil
	}
	if len(a) > 0 && a[0] == "unbind-key" {
		if len(a) > 1 && a[1] == "-q" {
			r.semantic = append(r.semantic, "unbind-root")
		} else {
			r.semantic = append(r.semantic, "unbind-table")
		}
		if r.fault == "cleanup" {
			r.hits++
			return nil, r.injected
		}
	}
	if len(a) > 0 && a[0] == "source-file" {
		r.semantic = append(r.semantic, "source")
		if r.fault == "reload" {
			r.hits++
			return nil, r.injected
		}
	}
	return r.base.Run(ctx, name, args...)
}

type statusbarParitySpan struct {
	Pointer          string
	Start, End       int
	Raw, Replacement string
}

// The only volatile error operand is os.CreateTemp's numeric suffix on the
// rename-failure row. No root/helper/config/error text is globally replaced.
func statusbarParityCompareError(raw *statusbarParityError, configDir, saved string) (*statusbarParityError, []statusbarParitySpan, error) {
	if raw == nil || raw.Type != "*fmt.wrapError" || raw.Unwrap == nil || raw.Unwrap.Type != "*os.LinkError" {
		return nil, nil, errors.New("rename comparison chain differs")
	}
	link := raw.Unwrap
	old := link.Old
	prefix := filepath.Join(configDir, "live-resources.tmp-")
	if !strings.HasPrefix(old, prefix) || len(old) <= len(prefix) || len(old) > len(prefix)+10 {
		return nil, nil, errors.New("unexpected rename temp operand")
	}
	for _, c := range old[len(prefix):] {
		if c < '0' || c > '9' {
			return nil, nil, errors.New("temp suffix is not numeric")
		}
	}
	if link.Op != "rename" || link.New != saved || link.Unwrap == nil || link.Text != "rename "+old+" "+saved+": "+link.Unwrap.Text || raw.Text != "rename live resources temp file: "+link.Text {
		return nil, nil, errors.New("rename error context differs")
	}
	spans := []statusbarParitySpan{}
	replace := func(text, pointer string) (string, error) {
		at := strings.Index(text, old)
		if at < 0 || strings.Count(text, old) != 1 {
			return "", errors.New("temp slot count differs")
		}
		spans = append(spans, statusbarParitySpan{Pointer: pointer, Start: at, End: at + len(old), Raw: old, Replacement: "<SAVED_TEMP>"})
		return text[:at] + "<SAVED_TEMP>" + text[at+len(old):], nil
	}
	outer := *raw
	inner := *link
	outer.Unwrap = &inner
	var e error
	outer.Text, e = replace(raw.Text, "/Error/Text")
	if e != nil {
		return nil, nil, e
	}
	inner.Text, e = replace(link.Text, "/Error/Unwrap/Text")
	if e != nil {
		return nil, nil, e
	}
	inner.Old = "<SAVED_TEMP>"
	spans = append(spans, statusbarParitySpan{Pointer: "/Error/Unwrap/Old", Start: 0, End: len(old), Raw: old, Replacement: "<SAVED_TEMP>"})
	return &outer, spans, nil
}

type statusbarParityOperation struct {
	Ordinal                       int
	Helper                        string
	Error                         *statusbarParityError
	CompareError                  *statusbarParityError
	CompareSpans                  []statusbarParitySpan
	Panic                         string
	SentinelIs                    bool
	Hits, HomeFaultHits           int
	Journal                       [][]string
	Semantic                      []string
	Before, After                 map[string]statusbarParityFile
	SnapshotError                 *statusbarParityError
	Findings                      []string
	GuardEnvBefore, GuardEnvAfter map[string]string
}

func statusbarParityGuardEnv() map[string]string {
	out := map[string]string{}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "TMUX_TMPDIR", "PATH", "PMX_TEST_LIVE_MACHINE_GUARD_ROOT", "TMUX", "TMUX_PANE", "__PROJMUX_RUNTIME_ANCHOR_PANE"} {
		out[k] = os.Getenv(k)
	}
	return out
}
func statusbarParityJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}
func statusbarParitySide(t *testing.T, fixture, output, casesPath string, legacy bool) {
	liveguard.RequireActive(t)
	if !systemstatus.Supported() {
		t.Fatal("supported Linux build required; unsupported is a separate future service row")
	}
	for _, p := range []string{fixture, output, casesPath} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			t.Fatal("explicit clean absolute roots/cases required")
		}
	}
	raw, e := os.ReadFile(casesPath)
	if e != nil {
		t.Fatal(e)
	}
	var cases []statusbarParityCase
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	if len(cases) != 24 {
		t.Fatal("case inventory must be 24")
	}
	helper, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range cases {
		if !t.Run(tc.Name, func(t *testing.T) {
			root := filepath.Join(fixture, tc.Name)
			dest := filepath.Join(output, tc.Name)
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			must(os.Mkdir(root, 0700))
			must(os.Mkdir(dest, 0700))
			home := filepath.Join(root, "home")
			must(os.Mkdir(home, 0700))
			paths, e := (config.Homes{HomeDir: home}).Paths()
			must(e)
			must(os.MkdirAll(paths.ConfigDir, 0700))
			write := func(p, s string) { must(os.WriteFile(p, []byte(s), 0600)) }
			if tc.Rich {
				write(paths.KeymapFile(), "[bindings.ProjectSidebarToggle]\nkeys = [\"M-z\"]\n")
				write(filepath.Join(paths.ConfigDir, "config.toml"), "[theme]\npreset = \"daylight\"\n")
				write(paths.StatusbarNotificationsHUDVisibilityFile(), "off\n")
				write(paths.StatusbarGitVisibilityFile(), "off\n")
				write(paths.AIBadgeStyleFile(), "emoji\n")
				write(paths.StatusbarDecorationFile(), "emoji\n")
			}
			configPath := filepath.Join(paths.ConfigDir, "tmux.conf")
			if tc.Fault == "save" {
				must(os.Mkdir(paths.LiveResourcesFile(), 0700))
			}
			if tc.Fault == "keymap" {
				write(paths.KeymapFile(), "schema_version = 999\n")
			}
			if tc.Fault == "generated" {
				must(os.Mkdir(configPath, 0700))
			}
			before, e := statusbarParityInventory(root)
			must(e)
			must(statusbarParityJSON(filepath.Join(dest, "seed.json"), before))
			n := 1
			if !tc.ExpectError {
				n = 2
			}
			for ordinal := 1; ordinal <= n; ordinal++ {
				injected := errors.New("task3 injected " + tc.Fault)
				r := &statusbarParityRunner{base: settingsLiveTestRunner{socketPath: "/task3/fake/projmux"}, fault: tc.Fault, injected: injected}
				homeHits := 0
				cmd := &settingsCommand{homeDir: func() (string, error) {
					if tc.Fault == "home" {
						homeHits++
						return "", injected
					}
					return home, nil
				}, lookupEnv: func(k string) string {
					switch k {
					case "TMUX":
						if !tc.Skip {
							return "/task3/fake/projmux,4242,0"
						}
					case "TMUX_PANE", runtimeMutationAnchorPaneEnv:
						return "%1"
					case "SHELL":
						return "/bin/zsh"
					}
					return ""
				}, tmuxRunner: r}
				if tc.MissingRunner {
					cmd.tmuxRunner = nil
				}
				op := statusbarParityOperation{Ordinal: ordinal, Helper: helper, Before: before, GuardEnvBefore: statusbarParityGuardEnv()}
				var callErr error
				func() {
					defer func() {
						if x := recover(); x != nil {
							op.Panic = fmt.Sprint(x) + "\n" + string(debug.Stack())
						}
					}()
					if legacy {
						callErr = cmd.statusbarParityLegacySet(tc.Value)
					} else {
						callErr = cmd.setLiveResourcesMode(tc.Value)
					}
				}()
				op.Error = statusbarParityErrorOf(callErr)
				op.SentinelIs = errors.Is(callErr, injected)
				op.Hits = r.hits
				op.HomeFaultHits = homeHits
				op.Journal = r.journal
				op.Semantic = r.semantic
				after, snapErr := statusbarParityInventory(root)
				op.After = after
				op.SnapshotError = statusbarParityErrorOf(snapErr)
				op.GuardEnvAfter = statusbarParityGuardEnv()
				check := func(ok bool, why string) {
					if !ok {
						op.Findings = append(op.Findings, why)
					}
				}
				check(op.Panic == "", "panic")
				check(snapErr == nil, "snapshot error")
				check((callErr != nil) == tc.ExpectError, "error presence differs")
				check(!tc.ExpectError || (callErr != nil && strings.Contains(callErr.Error(), tc.ErrorFragment)), "wrong error fragment")
				check(op.SentinelIs == tc.Sentinel, "sentinel Is differs")
				expectedHome := 0
				if tc.Fault == "home" {
					expectedHome = 1
				}
				check(homeHits == expectedHome, "home fault count")
				check(r.hits == tc.Injected, "runner fault count")
				if tc.Skip || tc.MissingRunner || !tc.Generated {
					check(len(r.journal) == 0, "unexpected tmux read/write before preparation/skip boundary")
				}
				check(strings.Join(r.semantic, "\x00") == strings.Join(tc.Semantic, "\x00"), "effect/stop order")
				check(reflect.DeepEqual(op.GuardEnvBefore, op.GuardEnvAfter), "ambient guard env mutated")
				savedRel, e := filepath.Rel(root, paths.LiveResourcesFile())
				must(e)
				configRel, e := filepath.Rel(root, configPath)
				must(e)
				saved, haveSaved := after[savedRel]
				gen, haveGen := after[configRel]
				if tc.Saved {
					check(haveSaved && saved.Kind == "regular" && saved.ReadError == nil && string(saved.Bytes) == string(config.NormalizeLiveResourcesMode(tc.Value))+"\n" && os.FileMode(saved.Mode).Perm() == 0600, "saved bytes/type/mode")
				} else if tc.Fault == "save" {
					check(haveSaved && saved.Kind == "directory", "save target directory replaced")
				} else {
					check(!haveSaved, "unexpected saved file")
				}
				if tc.Generated {
					check(haveGen && gen.Kind == "regular" && gen.ReadError == nil && len(gen.Bytes) > 0 && os.FileMode(gen.Mode).Perm() == 0644, "generated bytes/type/mode")
					if haveGen && gen.Kind == "regular" {
						check(strings.Contains(string(gen.Bytes), "set -g @projmux_live_resources "+string(config.NormalizeLiveResourcesMode(tc.Value))), "generated mode missing")
						check(strings.Contains(string(gen.Bytes), "set -g default-shell \"/bin/zsh\""), "captured shell missing")
					}
				} else if tc.Fault == "generated" {
					check(haveGen && gen.Kind == "directory", "generated directory replaced")
				} else {
					check(!haveGen, "unexpected generated file")
				}
				// Only these two regular artifacts and their parent directory mtime/size
				// may change. Full raw metadata (including parent mtime) is still retained.
				allowed := map[string]bool{savedRel: tc.Saved, configRel: tc.Generated}
				for name, was := range before {
					now, ok := after[name]
					check(ok, "removed fixture "+name)
					if !ok {
						continue
					}
					if allowed[name] {
						continue
					}
					if was.Kind == "directory" && filepath.Join(root, name) == paths.ConfigDir {
						check(now.Kind == was.Kind && now.Device == was.Device && now.Inode == was.Inode && now.Mode == was.Mode, "save parent identity/mode changed "+name)
					} else {
						check(reflect.DeepEqual(was, now), "input changed "+name)
					}
				}
				for name := range after {
					if _, ok := before[name]; !ok {
						check(allowed[name], "unexpected created fixture "+name)
					}
				}
				if tc.Fault == "keymap" {
					check(callErr != nil && strings.Contains(callErr.Error(), paths.KeymapFile()) && strings.Contains(callErr.Error(), "schema version 999 is newer than the supported version"), "keymap parser fault not reached")
				}
				op.CompareError = op.Error
				op.CompareSpans = []statusbarParitySpan{}
				if tc.Fault == "save" {
					var ce error
					op.CompareError, op.CompareSpans, ce = statusbarParityCompareError(op.Error, paths.ConfigDir, paths.LiveResourcesFile())
					check(ce == nil && len(op.CompareSpans) == 3, "bounded rename comparison failed")
				}
				if tc.Fault == "save" {
					var le *os.LinkError
					check(errors.As(callErr, &le) && le.Op == "rename" && le.New == paths.LiveResourcesFile(), "wrong save failure cause")
				}
				if tc.Fault == "generated" {
					var pe *os.PathError
					check(errors.As(callErr, &pe) && pe.Op == "open" && pe.Path == configPath, "wrong generated failure cause")
				}
				if ordinal == 2 {
					was := before[configRel]
					check(was.Device == gen.Device && was.Inode == gen.Inode && was.MtimeNS == gen.MtimeNS && reflect.DeepEqual(was.Bytes, gen.Bytes), "repeat rewrote generated artifact")
					oldSaved := before[savedRel]
					check(oldSaved.Inode != saved.Inode || oldSaved.Device != saved.Device, "repeat did not atomically replace saved artifact")
				}
				// Persist both operations BEFORE failing any assertion, with all raw
				// error chains, complete journal, full before/after fixture and findings.
				must(statusbarParityJSON(filepath.Join(dest, fmt.Sprintf("operation-%d.json", ordinal)), op))
				if len(op.Findings) > 0 {
					t.Fatalf("operation %d: %v", ordinal, op.Findings)
				}
				before = after
			}
			liveguard.RequireActive(t)
			must(statusbarParityJSON(filepath.Join(dest, "pass.json"), map[string]any{"case": tc.Name, "operations": n, "comparison_schema": "statusbarParity-raw-fixed-path-v2", "replacement_policy": "error-rename-temp-only-v2"}))
		}) {
			return
		}
	}
}

func (c *settingsCommand) statusbarParityLegacySet(value string) error {
	if !systemstatus.Supported() {
		return fmt.Errorf("live system resources are unavailable on this platform")
	}
	mode := config.NormalizeLiveResourcesMode(value)
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveLiveResourcesFile(paths.LiveResourcesFile(), mode); err != nil {
		return err
	}
	prepared, live, err := c.statusbarParityLegacyApply()
	if err != nil {
		if prepared.Status == keymapApplyFailed {
			return fmt.Errorf("update status bar runtime config: %w", err)
		}
		if live.Status == keymapApplyFailed {
			return fmt.Errorf("reload active status bar: %w", err)
		}
		return err
	}
	return nil
}

func (c *settingsCommand) statusbarParityLegacyApply() (prepared keymapApplyStage, live keymapApplyStage, err error) {
	configPath, genErr := c.writeTmuxAppConfig()
	if genErr != nil {
		prepared = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("generated tmux config", genErr)}
		live = keymapApplyStage{Status: keymapApplySkipped, Detail: "generated tmux config failed"}
		return prepared, live, genErr
	}
	prepared = keymapApplyStage{Status: keymapApplyOK, Detail: "generated tmux config: " + configPath}
	if (c.lookupEnv == nil || strings.TrimSpace(c.lookupEnv("TMUX")) == "") && !c.reloadAppServer {
		live = keymapApplyStage{Status: keymapApplySkipped, Detail: "Settings is not running inside tmux"}
		return prepared, live, nil
	}
	if c.tmuxRunner == nil {
		live = keymapApplyStage{Status: keymapApplySkipped, Detail: "exact tmux command runner is not configured"}
		return prepared, live, nil
	}
	ctx := context.Background()
	route, routeErr := resolveInvocationRuntimeMutationRoute(ctx, c.tmuxRunner, c.lookupEnv)
	if routeErr != nil || route.ExpectedSocketPath == "" {
		if routeErr == nil {
			routeErr = errors.New("Settings live reload has no exact physical socket")
		}
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("exact live tmux route", routeErr)}
		return prepared, live, routeErr
	}
	if route.Authority != nil && route.Authority.Class == runtimeMutationRouteStandalone {
		routeErr = errors.New("Settings live reload refuses an operator-owned standalone tmux server")
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("exact live tmux route", routeErr)}
		return prepared, live, routeErr
	}
	if retireErr := c.statusbarParityLegacyRetire(ctx, route); retireErr != nil {
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("live tmux sequence cleanup", retireErr)}
		return prepared, live, retireErr
	}
	if guardErr := guardResolvedRuntimeMutationRoute(ctx, c.tmuxRunner, route); guardErr != nil {
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("live tmux reload guard", guardErr)}
		return prepared, live, guardErr
	}
	if _, reloadErr := c.tmuxRunner.Run(ctx, "tmux", "-S", route.ExpectedSocketPath, "source-file", configPath); reloadErr != nil {
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("live tmux reload", reloadErr)}
		return prepared, live, reloadErr
	}
	live = keymapApplyStage{Status: keymapApplyOK}
	return prepared, live, nil
}

// statusbarParityLegacyRetire brings the Settings save path under the
// same Phase 0 stale-cleanup ordering as `tmux apply`: read the state recorded
// by the previous successful source, unbind exactly those roots/tables, then
// let the caller source the newly generated config. Production Settings always
// has runOutput; nil remains a compatibility seam for narrow unit commands
// that only record the historical source-file call.
func (c *settingsCommand) statusbarParityLegacyRetire(ctx context.Context, route runtimeMutationRoute) error {
	read := func(option string) (string, error) {
		if err := guardResolvedRuntimeMutationRoute(ctx, c.tmuxRunner, route); err != nil {
			return "", err
		}
		out, err := c.tmuxRunner.Run(ctx, "tmux", "-S", route.ExpectedSocketPath, "show-options", "-gqv", option)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", option, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	roots, err := read(tmuxSequenceRootsOption)
	if err != nil {
		return err
	}
	tables, err := read(tmuxSequenceTablesOption)
	if err != nil {
		return err
	}
	for _, command := range keySequenceRetireCommandsWithPrefix([]string{"tmux"}, roots, tables) {
		if err := guardResolvedRuntimeMutationRoute(ctx, c.tmuxRunner, route); err != nil {
			return err
		}
		if _, err := c.tmuxRunner.Run(ctx, "tmux", append([]string{"-S", route.ExpectedSocketPath}, command[1:]...)...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(command[1:], " "), err)
		}
	}
	return nil
}

func TestStatusbarResourcesSharedLegacyParity(t *testing.T) {
	liveguard.RequireActive(t)
	if !systemstatus.Supported() {
		c := &settingsCommand{}
		err := c.setLiveResourcesMode("on")
		if err == nil || err.Error() != "live system resources are unavailable on this platform" {
			t.Fatal(err)
		}
		return
	}
	base := t.TempDir()
	if root := os.Getenv("SDK_STATUSBAR_RESOURCES_EVIDENCE_ROOT"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("absolute evidence root required")
		}
		var err error
		base, err = os.MkdirTemp(root, "app-pair-")
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture := filepath.Join(base, "fixture")
	oldOut := filepath.Join(base, "original-observations")
	newOut := filepath.Join(base, "candidate-observations")
	for _, p := range []string{fixture, oldOut, newOut} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	casesPath := filepath.Join(base, "cases.json")
	if err := os.WriteFile(casesPath, []byte(statusbarParityCasesJSON), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("fresh pair evidence", base)
	statusbarParitySide(t, fixture, oldOut, casesPath, true)
	if t.Failed() {
		return
	}
	// Only this test's fresh fixture moves, retaining original bytes/metadata.
	// The accepted B root is never an operand of this test.
	if err := os.Rename(fixture, filepath.Join(base, "original-fixture-retained")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture, 0700); err != nil {
		t.Fatal(err)
	}
	statusbarParitySide(t, fixture, newOut, casesPath, false)
	if t.Failed() {
		return
	}
	var cases []statusbarParityCase
	if err := json.Unmarshal([]byte(statusbarParityCasesJSON), &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		n := 1
		if !tc.ExpectError {
			n = 2
		}
		for ordinal := 1; ordinal <= n; ordinal++ {
			var old, new statusbarParityOperation
			read := func(root string, out *statusbarParityOperation) {
				raw, e := os.ReadFile(filepath.Join(root, tc.Name, fmt.Sprintf("operation-%d.json", ordinal)))
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(raw, out); e != nil {
					t.Fatal(e)
				}
			}
			read(oldOut, &old)
			read(newOut, &new)
			if !reflect.DeepEqual(old.CompareError, new.CompareError) || !reflect.DeepEqual(old.Journal, new.Journal) || !reflect.DeepEqual(old.Semantic, new.Semantic) || old.Hits != new.Hits || old.HomeFaultHits != new.HomeFaultHits || old.SentinelIs != new.SentinelIs || old.Helper != new.Helper || len(old.Findings) != 0 || len(new.Findings) != 0 {
				t.Fatalf("%s operation%d error/trace parity differs; raw retained at %s", tc.Name, ordinal, base)
			}
			if len(old.After) != len(new.After) {
				t.Fatalf("%s file inventory differs", tc.Name)
			}
			for name, a := range old.After {
				b, ok := new.After[name]
				if !ok || a.Kind != b.Kind || a.Mode != b.Mode || !bytes.Equal(a.Bytes, b.Bytes) || !reflect.DeepEqual(a.ReadError, b.ReadError) {
					t.Fatalf("%s operation%d artifact%s differs", tc.Name, ordinal, name)
				}
			}
		}
	}
}

const statusbarParityCasesJSON = `[
  {
    "Name": "on",
    "Value": "on",
    "Fault": "",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "off",
    "Value": "off",
    "Fault": "",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "trim-on",
    "Value": " on ",
    "Fault": "",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "cli-uppercase",
    "Value": "ON",
    "Fault": "",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "cli-invalid-off",
    "Value": "invalid",
    "Fault": "",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "cli-empty-off",
    "Value": "",
    "Fault": "",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "rich-on",
    "Value": "on",
    "Fault": "",
    "Rich": true,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "rich-off",
    "Value": "off",
    "Fault": "",
    "Rich": true,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "outside-skip",
    "Value": "on",
    "Fault": "",
    "Rich": false,
    "Skip": true,
    "MissingRunner": false,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "runner-skip",
    "Value": "on",
    "Fault": "",
    "Rich": false,
    "Skip": false,
    "MissingRunner": true,
    "ExpectError": false,
    "ErrorFragment": "",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "home-error",
    "Value": "on",
    "Fault": "home",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "resolve home directory: task3 injected home",
    "Sentinel": true,
    "Injected": 0,
    "Semantic": [],
    "Saved": false,
    "Generated": false
  },
  {
    "Name": "save-rename-error",
    "Value": "on",
    "Fault": "save",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "rename live resources temp file:",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [],
    "Saved": false,
    "Generated": false
  },
  {
    "Name": "keymap-error",
    "Value": "on",
    "Fault": "keymap",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "update status bar runtime config:",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [],
    "Saved": true,
    "Generated": false
  },
  {
    "Name": "generated-dir-error",
    "Value": "on",
    "Fault": "generated",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "update status bar runtime config: write tmux app config:",
    "Sentinel": false,
    "Injected": 0,
    "Semantic": [],
    "Saved": true,
    "Generated": false
  },
  {
    "Name": "no-server",
    "Value": "on",
    "Fault": "no-server",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "reload active status bar: runtime mutation route: reobserve inherited socket: task3 injected no-server",
    "Sentinel": true,
    "Injected": 1,
    "Semantic": [],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "foreign",
    "Value": "on",
    "Fault": "foreign",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "partial or foreign ownership markers refuse standalone classification",
    "Sentinel": false,
    "Injected": 1,
    "Semantic": [],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "standalone",
    "Value": "on",
    "Fault": "standalone",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "Settings live reload refuses an operator-owned standalone tmux server",
    "Sentinel": false,
    "Injected": 2,
    "Semantic": [],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "pid-drift",
    "Value": "on",
    "Fault": "pid-drift",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "planned runtime server generation drifted",
    "Sentinel": false,
    "Injected": 1,
    "Semantic": [
      "roots"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "socket-drift",
    "Value": "on",
    "Fault": "socket-drift",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "runtime socket drifted:",
    "Sentinel": false,
    "Injected": 1,
    "Semantic": [
      "roots"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "marker-drift",
    "Value": "on",
    "Fault": "marker-drift",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "planned runtime socket logical route marker drifted",
    "Sentinel": false,
    "Injected": 1,
    "Semantic": [
      "roots"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "roots-read",
    "Value": "on",
    "Fault": "roots-read",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "reload active status bar:",
    "Sentinel": true,
    "Injected": 1,
    "Semantic": [
      "roots"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "tables-read",
    "Value": "on",
    "Fault": "tables-read",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "reload active status bar:",
    "Sentinel": true,
    "Injected": 1,
    "Semantic": [
      "roots",
      "tables"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "cleanup",
    "Value": "on",
    "Fault": "cleanup",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "reload active status bar: unbind-key -q -n C-k: task3 injected cleanup",
    "Sentinel": true,
    "Injected": 1,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root"
    ],
    "Saved": true,
    "Generated": true
  },
  {
    "Name": "reload",
    "Value": "on",
    "Fault": "reload",
    "Rich": false,
    "Skip": false,
    "MissingRunner": false,
    "ExpectError": true,
    "ErrorFragment": "reload active status bar: task3 injected reload",
    "Sentinel": true,
    "Injected": 1,
    "Semantic": [
      "roots",
      "tables",
      "unbind-root",
      "unbind-table",
      "source"
    ],
    "Saved": true,
    "Generated": true
  }
]
`
