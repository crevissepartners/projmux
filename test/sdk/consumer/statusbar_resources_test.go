package consumer_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

var _ func(*projmux.Client, string) (core.StatusbarResourcesApplyResult, error) = (*projmux.Client).SetStatusbarResources

func TestExternalStatusbarResourcesConsumer(t *testing.T) {
	base := t.TempDir()
	if root := os.Getenv("SDK_STATUSBAR_RESOURCES_EVIDENCE_ROOT"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("absolute evidence root required")
		}
		var err error
		base, err = os.MkdirTemp(root, "public-consumer-")
		if err != nil {
			t.Fatal(err)
		}
	}
	helper, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	// No tmux executable is captured. This proves apply failure through the
	// public facade without launching a CLI, helper, provider, or tmux server.
	t.Setenv("PATH", "")
	options := projmux.Options{ConfigDir: filepath.Join(base, "arbitrary-settings-basename"), StateDir: filepath.Join(base, "state"), HomeDir: filepath.Join(base, "home"), DefaultShell: "/bin/zsh", ProjmuxExecutable: helper, SocketPath: filepath.Join(base, "exact.sock")}
	c, err := projmux.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(base, "uncaptured-trap-bin"))
	t.Setenv("TMUX", "poisoned")
	t.Setenv("TMUX_PANE", "poisoned")
	t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "poisoned")
	codes := []projmux.Code{projmux.CodeStatusbarResourcesUnsupported, projmux.CodeStatusbarResourcesSaveFailed, projmux.CodeStatusbarResourcesPrepareFailed, projmux.CodeStatusbarResourcesApplyFailed}
	wantCodes := []projmux.Code{"statusbar-resources-unsupported", "statusbar-resources-save-failed", "statusbar-resources-prepare-failed", "statusbar-resources-apply-failed"}
	if !reflect.DeepEqual(codes, wantCodes) {
		t.Fatal(codes)
	}
	before := consumerPathTree(t, base)
	out, err := c.SetStatusbarResources("ON")
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeConfigValueInvalid || out.Saved || out.Prepared.Status != "skipped" || out.Live.Status != "skipped" || !reflect.DeepEqual(before, consumerPathTree(t, base)) {
		t.Fatal(out, err)
	}
	writeEvidence := func(name string, out core.StatusbarResourcesApplyResult, err error) {
		t.Helper()
		errType, text, unwrap := fmt.Sprintf("%T", err), fmt.Sprint(err), fmt.Sprint(errors.Unwrap(err))
		raw, e := json.MarshalIndent(map[string]any{"result": out, "errorType": errType, "error": text, "unwrap": unwrap, "files": consumerPathTree(t, base)}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		f, e := os.OpenFile(filepath.Join(base, name+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.Write(raw)
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			t.Fatal(e, closeErr)
		}
	}
	if !projmux.ResourcesSupported() {
		out, err = c.SetStatusbarResources("on")
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeStatusbarResourcesUnsupported || out.Saved || out.Mode != "on" || !reflect.DeepEqual(before, consumerPathTree(t, base)) {
			t.Fatal(out, err)
		}
		writeEvidence("unsupported", out, err)
		return
	}
	out, err = c.SetStatusbarResources(" on ")
	writeEvidence("apply-failed", out, err)
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeStatusbarResourcesApplyFailed || !out.Saved || out.Mode != "on" || out.Prepared.Status != "ok" || out.Live.Status != "failed" || errors.Unwrap(refusal) == nil || !strings.HasPrefix(err.Error(), "reload active status bar: ") || !strings.Contains(err.Error(), "tmux unavailable on captured PATH") {
		t.Fatal(out, err)
	}
	raw, readErr := os.ReadFile(filepath.Join(options.ConfigDir, "live-resources"))
	info, statErr := os.Stat(filepath.Join(options.ConfigDir, "live-resources"))
	if readErr != nil || statErr != nil || string(raw) != "on\n" || info.Mode().Perm() != 0600 {
		t.Fatal(string(raw), readErr, statErr)
	}
	if _, err := os.Stat(options.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected state/sample/Registry effect", err)
	}
	options.ConfigDir = filepath.Join(base, "prepare-settings")
	options.ProjmuxExecutable = filepath.Join(base, "missing-explicit-helper")
	c, err = projmux.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	out, err = c.SetStatusbarResources("off")
	writeEvidence("prepare-failed", out, err)
	var path *os.PathError
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeStatusbarResourcesPrepareFailed || !out.Saved || out.Prepared.Status != "failed" || out.Live.Status != "skipped" || !errors.As(err, &path) || path.Path != options.ProjmuxExecutable || !errors.Is(err, path.Err) {
		t.Fatal(out, err)
	}
	options.ConfigDir = filepath.Join(base, "save-settings")
	c, err = projmux.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(options.ConfigDir, "live-resources")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	out, err = c.SetStatusbarResources("on")
	writeEvidence("save-failed", out, err)
	var link *os.LinkError
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeStatusbarResourcesSaveFailed || out.Saved || out.Prepared.Status != "skipped" || out.Live.Status != "skipped" || !errors.As(err, &link) || link.Op != "rename" || link.New != target || !errors.Is(err, link.Err) {
		t.Fatal(out, err)
	}
}
