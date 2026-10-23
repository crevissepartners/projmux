package projmux

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/services/instructions"
	"github.com/crevissepartners/projmux/internal/services/keybinding"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/tmuxconfig"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/internal/theme"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// An explicit evidence root retains every fixture and observation on failure.
// Ordinary repository runs keep the normal testing temporary-directory policy.
func sdkResourcesTestRoot(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	if root := os.Getenv("SDK_STATUSBAR_RESOURCES_EVIDENCE_ROOT"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("absolute evidence root required")
		}
		var err error
		base, err = os.MkdirTemp(root, "sdk-")
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture, observations := filepath.Join(base, "fixture"), filepath.Join(base, "observations")
	for _, path := range []string{fixture, observations} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("retained evidence:", base)
	return fixture, observations
}

type sdkResourcesFile struct {
	Mode                     os.FileMode
	Size, Mtime              int64
	Device, Inode            uint64
	Bytes                    []byte
	ReadErrorType, ReadError string
}

func sdkResourcesInventory(t *testing.T, root string) map[string]sdkResourcesFile {
	t.Helper()
	out := map[string]sdkResourcesFile{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		row := sdkResourcesFile{Mode: info.Mode(), Size: info.Size(), Mtime: info.ModTime().UnixNano()}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			row.Device, row.Inode = uint64(st.Dev), uint64(st.Ino)
		}
		row.Bytes, err = os.ReadFile(path)
		if err != nil {
			row.ReadErrorType, row.ReadError = fmt.Sprintf("%T", err), err.Error()
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = row
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sdkResourcesRecord(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(writeErr, closeErr)
	}
}

type sdkResourcesRunner struct {
	Calls                 [][]string
	Semantic              []string
	Fault                 string
	Cause                 error
	Hits, Guards, DriftAt int
	Drift                 string
	InitialOther          bool
}

func (r *sdkResourcesRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.Calls = append(r.Calls, append([]string{name}, args...))
	if name != "tmux" || len(args) < 3 || !((args[0] == "-S" && args[1] == "/statusbar-resources/fake") || (args[0] == "-L" && args[1] == "projmux")) {
		return nil, fmt.Errorf("unexpected exact transport %s %q", name, args)
	}
	a := args[2:]
	if r.Fault == "no-server" {
		r.Hits++
		return nil, r.Cause
	}
	if slices.Contains(a, ";") {
		r.Guards++
		var out strings.Builder
		start := 0
		for i := 0; i <= len(a); i++ {
			if i < len(a) && a[i] != ";" {
				continue
			}
			part, err := r.answer(a[start:i])
			out.Write(part)
			if len(part) > 0 && part[len(part)-1] != '\n' {
				out.WriteByte('\n')
			}
			if err != nil {
				return []byte(out.String()), err
			}
			start = i + 1
		}
		return []byte(out.String()), nil
	}
	return r.answer(a)
}

func (r *sdkResourcesRunner) answer(a []string) ([]byte, error) {
	last := a[len(a)-1]
	if last == mutationruntime.RouteReadBoundary {
		return []byte(last + "\n"), nil
	}
	drift := r.DriftAt > 0 && r.Guards == r.DriftAt
	switch last {
	case "#{socket_path}":
		if r.InitialOther {
			return []byte("/other/socket\n"), nil
		}
		if drift && r.Drift == "socket" {
			r.Hits++
			return []byte("/other/socket\n"), nil
		}
		return []byte("/statusbar-resources/fake\n"), nil
	case "#{pid}":
		if drift && r.Drift == "pid" {
			r.Hits++
			return []byte("9999\n"), nil
		}
		return []byte("4242\n"), nil
	case "@projmux_app":
		if r.Fault == "marker-read" {
			r.Hits++
			return nil, r.Cause
		}
		if r.Fault == "foreign" {
			r.Hits++
			return []byte("0\n"), nil
		}
		if r.Fault == "standalone" {
			r.Hits++
			return nil, nil
		}
		if drift && r.Drift == "app" {
			r.Hits++
			return []byte("0\n"), nil
		}
		return []byte("1\n"), nil
	case mutationruntime.RuntimeMutationSocketNameOption:
		if r.Fault == "partial" {
			r.Hits++
			return nil, nil
		}
		if r.Fault == "standalone" {
			r.Hits++
			return nil, nil
		}
		if drift && r.Drift == "logical" {
			r.Hits++
			return []byte("other\n"), nil
		}
		return []byte("projmux\n"), nil
	}
	effect := ""
	switch {
	case last == tmuxconfig.TmuxSequenceRootsOption:
		effect = "roots"
	case last == tmuxconfig.TmuxSequenceTablesOption:
		effect = "tables"
	case a[0] == "unbind-key" && a[1] == "-q":
		effect = "unbind-root"
	case a[0] == "unbind-key":
		effect = "unbind-table"
	case a[0] == "source-file":
		effect = "source"
	default:
		return nil, fmt.Errorf("unexpected command %q", a)
	}
	r.Semantic = append(r.Semantic, effect)
	if r.Fault == effect {
		r.Hits++
		return nil, r.Cause
	}
	if effect == "roots" {
		return []byte("C-k\n"), nil
	}
	if effect == "tables" {
		return []byte("projmux-seq-fixture\n"), nil
	}
	return nil, nil
}

func sdkResourcesClient(t *testing.T, root string, logical bool) *Client {
	t.Helper()
	socket := "/statusbar-resources/fake"
	if logical {
		socket = ""
	}
	c, err := NewClient(Options{ConfigDir: filepath.Join(root, "arbitrary-settings-basename"), StateDir: filepath.Join(root, "separate-state"), HomeDir: filepath.Join(root, "captured-home"), DefaultShell: "/bin/zsh", ProjmuxExecutable: os.Args[0], SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.projmuxExecutable) {
		t.Fatal("absolute test executable operand required")
	}
	return c
}

func sdkResourcesError(err error) any {
	if err == nil {
		return nil
	}
	row := map[string]any{"type": fmt.Sprintf("%T", err), "text": err.Error(), "unwrap": sdkResourcesError(errors.Unwrap(err))}
	if p, ok := err.(*os.PathError); ok {
		row["op"], row["path"] = p.Op, p.Path
	}
	if l, ok := err.(*os.LinkError); ok {
		row["op"], row["old"], row["new"] = l.Op, l.Old, l.New
	}
	if e, ok := err.(syscall.Errno); ok {
		row["errno"] = uint64(e)
	}
	return row
}

func sdkResourcesRefusal(t *testing.T, err error, code Code) {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != code || refusal.Error() != errors.Unwrap(refusal).Error() {
		t.Fatalf("refusal %s: %#v %v", code, refusal, err)
	}
}

func TestStatusbarResourcesSDKStrictBeforeEffects(t *testing.T) {
	for _, input := range []string{"", "ON", "Off", "unknown", "on\x00"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			root, dest := sdkResourcesTestRoot(t)
			before := sdkResourcesInventory(t, root)
			c := &Client{instructions: instructions.Service{Paths: func() (config.Paths, error) { t.Fatal("invalid resolved paths"); return config.Paths{}, nil }}}
			out, err := c.setStatusbarResources(input, func() bool { t.Fatal("invalid checked support"); return true }, nil)
			after := sdkResourcesInventory(t, root)
			sdkResourcesRecord(t, filepath.Join(dest, "invalid.json"), map[string]any{"before": before, "after": after, "result": out, "error": sdkResourcesError(err)})
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeConfigValueInvalid || out.Saved || out.Prepared.Status != "skipped" || out.Live.Status != "skipped" || !reflect.DeepEqual(before, after) {
				t.Fatal(out, err)
			}
		})
	}
	c := &Client{instructions: instructions.Service{Paths: func() (config.Paths, error) { t.Fatal("unsupported resolved paths"); return config.Paths{}, nil }}}
	out, err := c.setStatusbarResources(" on ", func() bool { return false }, nil)
	sdkResourcesRefusal(t, err, CodeStatusbarResourcesUnsupported)
	if out.Mode != "on" || out.Saved || out.Prepared.Status != "skipped" || out.Live.Status != "skipped" {
		t.Fatal(out)
	}
	cause := errors.New("designated path failure")
	paths := 0
	c.instructions.Paths = func() (config.Paths, error) { paths++; return config.Paths{}, cause }
	out, err = c.setStatusbarResources("on", func() bool { return true }, nil)
	sdkResourcesRefusal(t, err, CodeStatusbarResourcesSaveFailed)
	if paths != 1 || !errors.Is(err, cause) || err.Error() != cause.Error() || out.Saved {
		t.Fatal(out, err, paths)
	}
}

func TestStatusbarResourcesSDKHelperFailsAfterSave(t *testing.T) {
	for _, kind := range []string{"absent", "relative", "directory", "non-executable", "save", "keymap", "generated"} {
		t.Run(kind, func(t *testing.T) {
			root, dest := sdkResourcesTestRoot(t)
			c := sdkResourcesClient(t, root, false)
			if err := os.MkdirAll(c.paths.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "absent":
				c.projmuxExecutable = filepath.Join(root, "missing-helper")
			case "relative":
				c.projmuxExecutable = "projmux"
			case "directory":
				c.projmuxExecutable = root
			case "non-executable":
				c.projmuxExecutable = filepath.Join(root, "not-executable")
				if err := os.WriteFile(c.projmuxExecutable, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "save":
				if err := os.Mkdir(c.paths.LiveResourcesFile(), 0700); err != nil {
					t.Fatal(err)
				}
			case "keymap":
				if err := os.WriteFile(c.paths.KeymapFile(), []byte("schema_version = 999\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "generated":
				if err := os.Mkdir(filepath.Join(c.paths.ConfigDir, "tmux.conf"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := sdkResourcesInventory(t, root)
			r := &sdkResourcesRunner{}
			out, err := c.setStatusbarResources("on", func() bool { return true }, r)
			after := sdkResourcesInventory(t, root)
			sdkResourcesRecord(t, filepath.Join(dest, "partial.json"), map[string]any{"before": before, "after": after, "result": out, "error": sdkResourcesError(err), "calls": r.Calls})
			code := CodeStatusbarResourcesPrepareFailed
			if kind == "save" {
				code = CodeStatusbarResourcesSaveFailed
			}
			sdkResourcesRefusal(t, err, code)
			if len(r.Calls) != 0 || out.Live.Status != "skipped" {
				t.Fatal(out, err, r.Calls)
			}
			if kind == "save" {
				var link *os.LinkError
				if out.Saved || !errors.As(err, &link) || link.Op != "rename" || link.New != c.paths.LiveResourcesFile() {
					t.Fatal(out, err)
				}
				return
			}
			if !out.Saved || out.Prepared.Status != "failed" || !strings.HasPrefix(err.Error(), "update status bar runtime config: ") {
				t.Fatal(out, err)
			}
			raw, readErr := os.ReadFile(c.paths.LiveResourcesFile())
			if readErr != nil || string(raw) != "on\n" {
				t.Fatal(string(raw), readErr)
			}
			if kind == "keymap" && (!strings.Contains(err.Error(), c.paths.KeymapFile()) || !strings.Contains(err.Error(), "schema version 999 is newer")) {
				t.Fatal("fault not reached", err)
			}
			if kind == "generated" {
				var pe *os.PathError
				if !errors.As(err, &pe) || pe.Op != "open" || pe.Path != filepath.Join(c.paths.ConfigDir, "tmux.conf") {
					t.Fatal("writer fault not reached", err)
				}
			}
		})
	}
}

func TestStatusbarResourcesSDKUsesCapturedInputs(t *testing.T) {
	for _, logical := range []bool{false, true} {
		for _, mode := range []string{"on", "off"} {
			t.Run(fmt.Sprintf("logical-%v-%s", logical, mode), func(t *testing.T) {
				root, dest := sdkResourcesTestRoot(t)
				c := sdkResourcesClient(t, root, logical)
				// Expected inputs come from the known Options/seed, not Client
				// fields, generated output, or the production Generate closure.
				fixturePaths := config.Paths{ConfigDir: filepath.Join(root, "arbitrary-settings-basename"), StateDir: filepath.Join(root, "separate-state")}
				expectedHelper := os.Args[0]
				if err := os.MkdirAll(fixturePaths.ConfigDir, 0700); err != nil {
					t.Fatal(err)
				}
				seed := map[string]string{
					fixturePaths.KeymapFile():                              "[bindings.ProjectSidebarToggle]\nkeys = [\"M-z\"]\n",
					filepath.Join(fixturePaths.ConfigDir, "config.toml"):   "[theme]\npreset = \"daylight\"\n",
					fixturePaths.StatusbarNotificationsHUDVisibilityFile(): "off\n", fixturePaths.StatusbarGitVisibilityFile(): "off\n",
					fixturePaths.AIBadgeStyleFile(): "emoji\n", fixturePaths.StatusbarDecorationFile(): "emoji\n",
				}
				for path, value := range seed {
					if err := os.WriteFile(path, []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
				for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "SHELL", "PATH", "TMUX", "TMUX_PANE", "__PROJMUX_RUNTIME_ANCHOR_PANE"} {
					t.Setenv(key, "poisoned-after-capture")
				}
				parsed, parseErr := keybinding.ParseKeymapFile(fixturePaths.KeymapFile(), seed[fixturePaths.KeymapFile()])
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				catalog, mergeErr := keybinding.MergeKeymapOverrides(keybinding.DefaultKeyBindingCatalog(), parsed)
				if mergeErr != nil {
					t.Fatal(mergeErr)
				}
				hud := tmuxconfig.DefaultStatusbarHUDVisibilitySet()
				hud.Notifications = config.StatusbarVisibilityOff
				rowOne := tmuxconfig.DefaultStatusbarRowOneVisibilitySet()
				rowOne.Git = config.StatusbarVisibilityOff
				expected := []byte(tmuxconfig.NewRenderThemeSource(theme.ResolveTheme(theme.ThemeConfig{Preset: "daylight"})).TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(
					expectedHelper, "/bin/zsh", tmuxconfig.StatusbarDecorationSetFromGlobal(config.StatusbarDecorationEmoji), config.AIBadgeStyleEmoji, config.DefaultDesktopNotifyMode, config.LiveResourcesMode(mode), hud, rowOne, catalog, true))
				expectedSHA := fmt.Sprintf("%x", sha256.Sum256(expected))
				expectedDigest, digestErr := sdkResourcesEmbeddedDigest(expected)
				if digestErr != nil {
					t.Fatal(digestErr)
				}
				sdkResourcesRecord(t, filepath.Join(dest, "expected.json"), map[string]any{"paths": fixturePaths, "home": filepath.Join(root, "captured-home"), "helper": expectedHelper, "shell": "/bin/zsh", "seed": seed, "mode": mode, "logical": logical, "bytes": expected, "sha256": expectedSHA, "embeddedDigest": expectedDigest})
				generated := filepath.Join(fixturePaths.ConfigDir, "tmux.conf")
				var previous os.FileInfo
				var previousBytes []byte
				for operation := 1; operation <= 2; operation++ {
					before := sdkResourcesInventory(t, root)
					r := &sdkResourcesRunner{}
					out, err := c.setStatusbarResources(" "+mode+" ", func() bool { return true }, r)
					after := sdkResourcesInventory(t, root)
					bytes, readErr := os.ReadFile(generated)
					actualDigest, actualDigestErr := sdkResourcesEmbeddedDigest(bytes)
					info, statErr := os.Stat(generated)
					state, stateErr := os.Stat(fixturePaths.LiveResourcesFile())
					sdkResourcesRecord(t, filepath.Join(dest, fmt.Sprintf("operation-%d.json", operation)), map[string]any{"before": before, "after": after, "result": out, "error": sdkResourcesError(err), "calls": r.Calls, "semantic": r.Semantic, "generatedDigest": fmt.Sprintf("%x", sha256.Sum256(bytes)), "helper": c.projmuxExecutable, "expectedBytes": expected, "actualBytes": bytes, "expectedSHA256": expectedSHA, "expectedEmbeddedDigest": expectedDigest, "actualEmbeddedDigest": actualDigest, "actualDigestError": sdkResourcesError(actualDigestErr)})
					if err != nil || readErr != nil || statErr != nil || stateErr != nil || state.Mode().Perm() != 0600 || info.Mode().Perm() != 0644 || out.Mode != mode || !out.Saved || out.Prepared.Status != "ok" || out.Live.Status != "ok" {
						t.Fatal(out, err, readErr, statErr, stateErr)
					}
					if !reflect.DeepEqual(expected, bytes) || expectedSHA != fmt.Sprintf("%x", sha256.Sum256(bytes)) || actualDigestErr != nil || actualDigest != expectedDigest {
						t.Fatal("independent full renderer bytes/digest mismatch", expectedSHA, actualDigest, expectedDigest, actualDigestErr)
					}
					if !slices.Equal(r.Semantic, []string{"roots", "tables", "unbind-root", "unbind-table", "source"}) || r.Guards != 5 {
						t.Fatal(r.Semantic, r.Guards)
					}
					if logical {
						if !slices.Equal(r.Calls[0][:3], []string{"tmux", "-L", "projmux"}) {
							t.Fatal(r.Calls[0])
						}
					} else {
						for _, call := range r.Calls {
							if !slices.Equal(call[:3], []string{"tmux", "-S", "/statusbar-resources/fake"}) {
								t.Fatal(call)
							}
						}
					}
					for _, literal := range []string{"set -g @projmux_live_resources " + mode, "set -g default-shell \"/bin/zsh\"", "bind-key -n M-z", c.projmuxExecutable, "@projmux_config_digest"} {
						if !strings.Contains(string(bytes), literal) {
							t.Fatalf("missing %q", literal)
						}
					}
					for path, value := range seed {
						got, err := os.ReadFile(path)
						if err != nil || string(got) != value {
							t.Fatal("seed changed", path, string(got), err)
						}
					}
					if operation == 2 && (!os.SameFile(previous, info) || !previous.ModTime().Equal(info.ModTime()) || !reflect.DeepEqual(previousBytes, bytes)) {
						t.Fatal("repeat rewrote generated bytes")
					}
					previous, previousBytes = info, bytes
				}
				if _, err := os.Stat(fixturePaths.StateDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("state/sample/Registry effect", err)
				}
			})
		}
	}
}

func TestStatusbarResourcesExactSocketNoAmbientFallback(t *testing.T) {
	for _, fault := range []string{"no-server", "foreign", "standalone", "partial", "marker-read", "roots", "tables", "unbind-root", "unbind-table", "source"} {
		t.Run(fault, func(t *testing.T) {
			root, dest := sdkResourcesTestRoot(t)
			c := sdkResourcesClient(t, root, false)
			for _, key := range []string{"TMUX", "TMUX_PANE", "__PROJMUX_RUNTIME_ANCHOR_PANE"} {
				t.Setenv(key, "poisoned")
			}
			cause := errors.New("designated " + fault)
			r := &sdkResourcesRunner{Fault: fault, Cause: cause}
			before := sdkResourcesInventory(t, root)
			out, err := c.setStatusbarResources("on", func() bool { return true }, r)
			after := sdkResourcesInventory(t, root)
			sdkResourcesRecord(t, filepath.Join(dest, "failure.json"), map[string]any{"before": before, "after": after, "result": out, "error": sdkResourcesError(err), "calls": r.Calls, "semantic": r.Semantic, "hits": r.Hits, "is": errors.Is(err, cause)})
			sdkResourcesRefusal(t, err, CodeStatusbarResourcesApplyFailed)
			if !out.Saved || out.Prepared.Status != "ok" || out.Live.Status != "failed" || !strings.HasPrefix(err.Error(), "reload active status bar: ") {
				t.Fatal(out, err)
			}
			wantHits := 1
			if fault == "standalone" {
				wantHits = 2
			}
			if r.Hits != wantHits {
				t.Fatal("designated fault not reached", r.Hits, wantHits, r.Calls)
			}
			if fault == "foreign" || fault == "standalone" || fault == "partial" || fault == "marker-read" {
				if errors.Is(err, cause) || len(r.Semantic) != 0 {
					t.Fatal(err, r.Semantic)
				}
			} else if !errors.Is(err, cause) {
				t.Fatal("cause lost", err)
			}
			all := []string{"roots", "tables", "unbind-root", "unbind-table", "source"}
			if i := slices.Index(all, fault); i >= 0 && !slices.Equal(r.Semantic, all[:i+1]) {
				t.Fatal("post-failure effect", r.Semantic)
			}
			if fault == "no-server" && (len(r.Calls) != 1 || len(r.Semantic) != 0) {
				t.Fatal(r.Calls)
			}
			if fault == "marker-read" && (!strings.Contains(err.Error(), "ownership markers are unreadable") || !strings.Contains(err.Error(), cause.Error())) {
				t.Fatal("wrong original lost-cause route", err)
			}
			for _, call := range r.Calls {
				if !slices.Equal(call[:3], []string{"tmux", "-S", "/statusbar-resources/fake"}) {
					t.Fatal("fallback", call)
				}
			}
		})
	}
	for _, drift := range []string{"socket", "pid", "app", "logical"} {
		for at := 1; at <= 5; at++ {
			t.Run(fmt.Sprintf("%s-before-%d", drift, at), func(t *testing.T) {
				root, dest := sdkResourcesTestRoot(t)
				c := sdkResourcesClient(t, root, false)
				r := &sdkResourcesRunner{Drift: drift, DriftAt: at}
				out, err := c.setStatusbarResources("on", func() bool { return true }, r)
				sdkResourcesRecord(t, filepath.Join(dest, "drift.json"), map[string]any{"result": out, "error": sdkResourcesError(err), "calls": r.Calls, "semantic": r.Semantic, "hits": r.Hits, "guards": r.Guards})
				sdkResourcesRefusal(t, err, CodeStatusbarResourcesApplyFailed)
				all := []string{"roots", "tables", "unbind-root", "unbind-table", "source"}
				if !out.Saved || out.Prepared.Status != "ok" || out.Live.Status != "failed" || r.Hits != 1 || r.Guards != at || !slices.Equal(r.Semantic, all[:at-1]) {
					t.Fatal(out, err, r.Hits, r.Guards, r.Semantic)
				}
			})
		}
	}
}

func TestStatusbarResourcesBoundSocketRejectsChangedResponse(t *testing.T) {
	root, dest := sdkResourcesTestRoot(t)
	c := sdkResourcesClient(t, root, false)
	r := &sdkResourcesRunner{InitialOther: true}
	out, err := c.setStatusbarResources("on", func() bool { return true }, r)
	sdkResourcesRecord(t, filepath.Join(dest, "mismatch.json"), map[string]any{"result": out, "error": sdkResourcesError(err), "actualRunnerCalls": r.Calls})
	sdkResourcesRefusal(t, err, CodeStatusbarResourcesApplyFailed)
	if !out.Saved || out.Prepared.Status != "ok" || out.Live.Status != "failed" || !reflect.DeepEqual(r.Calls, [][]string{{"tmux", "-S", "/statusbar-resources/fake", "display-message", "-p", "-F", "#{socket_path}"}}) {
		t.Fatal(out, err, r.Calls)
	}
	// Direct adapter negatives do not rely on the resolver choosing these argv.
	for _, args := range [][]string{{}, {"-L", "projmux", "display-message"}, {"-S", "/different", "show-options"}, {"-S", "/statusbar-resources/fake", "-L", "other", "display-message"}, {"-S", "/statusbar-resources/fake"}} {
		_, err := (statusbarResourcesBoundRunner{Runner: r, SocketPath: "/statusbar-resources/fake"}).Run(context.Background(), "tmux", args...)
		if err == nil || len(r.Calls) != 1 {
			t.Fatal(err, r.Calls)
		}
	}
}

var _ func(*Client, string) (core.StatusbarResourcesApplyResult, error) = (*Client).SetStatusbarResources

// Inspect only the digest record; renderer policy stays in tmuxconfig.
func sdkResourcesEmbeddedDigest(raw []byte) (string, error) {
	prefix := "set -g " + tmuxconfig.TmuxConfigDigestOption + " "
	var found []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if after, ok := strings.CutPrefix(line, prefix); ok {
			found = append(found, after)
		}
	}
	if len(found) != 1 || len(found[0]) != sha256.Size*2 {
		return "", fmt.Errorf("generated config has invalid digest records %q", found)
	}
	return found[0], nil
}

// The public setter reaches a real app-owned server only through the absolute
// socket in a pre-created directory; ambient TMUX values point elsewhere.
func TestStatusbarResourcesRealIsolatedSocket(t *testing.T) {
	liveguard.RequireActive(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("PROJMUX_REAL_TMUX_STRICT") == "1" {
			t.Fatal(err)
		}
		t.Skip("tmux unavailable")
	}
	if !ResourcesSupported() {
		t.Skip("live system resources unsupported on this build")
	}
	t.Setenv("HOME", t.TempDir())
	newClient := func(t *testing.T, socket string) *Client {
		t.Helper()
		root := t.TempDir()
		helper := filepath.Join(root, "projmux-helper")
		if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		c, err := NewClient(Options{ConfigDir: filepath.Join(root, "arbitrary-settings-basename"), StateDir: filepath.Join(root, "separate-state"), HomeDir: filepath.Join(root, "captured-home"), DefaultShell: "/bin/sh", ProjmuxExecutable: helper, SocketPath: socket})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	socketRoot := func(t *testing.T) string {
		t.Helper()
		root, err := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-resources-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(root); err != nil {
				t.Error(err)
			}
		})
		if st, err := os.Stat(root); err != nil || !st.IsDir() || !filepath.IsAbs(root) {
			t.Fatal("absolute socket root absent")
		}
		return root
	}
	poison := func(t *testing.T) {
		decoy := filepath.Join(t.TempDir(), "decoy")
		t.Setenv("TMUX", decoy+",999,1")
		t.Setenv("TMUX_PANE", "%999")
		t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "%999")
	}
	t.Run("applied", func(t *testing.T) {
		socket := filepath.Join(socketRoot(t), "s")
		direct := tmux.ExecRunner{}
		run := func(args ...string) string {
			t.Helper()
			out, err := direct.Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
			if err != nil {
				t.Fatalf("%v: %v %s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		run("-f", "/dev/null", "new-session", "-d", "-s", "alpha", "tail -f /dev/null")
		t.Cleanup(func() {
			if run("display-message", "-p", "#{socket_path}") != socket {
				t.Error("cleanup route proof")
				return
			}
			run("kill-server")
		})
		run("set-option", "-g", tmuxopts.AppGlobal, "1")
		run("set-option", "-g", mutationruntime.RuntimeMutationSocketNameOption, "projmux")
		poison(t)
		c := newClient(t, socket)
		paths, err := c.instructions.Paths()
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"on", "off"} {
			out, err := c.SetStatusbarResources(" " + mode + " ")
			if err != nil || out.Mode != mode || !out.Saved || out.Prepared.Status != "ok" || out.Live.Status != "ok" {
				t.Fatalf("%s: %+v %v", mode, out, err)
			}
			if saved, err := config.LoadLiveResourcesFile(paths.LiveResourcesFile()); err != nil || string(saved) != mode {
				t.Fatalf("%s saved %q %v", mode, saved, err)
			}
			generated, err := os.ReadFile(filepath.Join(paths.ConfigDir, "tmux.conf"))
			if err != nil || !slices.Contains(strings.Split(string(generated), "\n"), "set -g "+tmuxconfig.LiveResourcesTmuxOption+" "+mode) {
				t.Fatalf("%s generated config: %v", mode, err)
			}
			if got := run("show-options", "-gqv", tmuxconfig.LiveResourcesTmuxOption); got != mode {
				t.Fatalf("%s not applied to the exact server: %q", mode, got)
			}
		}
		if _, err := os.Stat(strings.TrimSuffix(os.Getenv("TMUX"), ",999,1")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("ambient TMUX socket touched", err)
		}
	})
	t.Run("no-server", func(t *testing.T) {
		socket := filepath.Join(socketRoot(t), "s")
		poison(t)
		c := newClient(t, socket)
		out, err := c.SetStatusbarResources("on")
		sdkResourcesRefusal(t, err, CodeStatusbarResourcesApplyFailed)
		if out.Mode != "on" || !out.Saved || out.Prepared.Status != "ok" || out.Live.Status != "failed" || !strings.HasPrefix(err.Error(), "reload active status bar: ") {
			t.Fatal(out, err)
		}
		if _, err := os.Stat(socket); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("absent server socket was created", err)
		}
	})
}
