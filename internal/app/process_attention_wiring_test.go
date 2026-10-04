package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func processAttentionWiringFixture(t *testing.T) (coremetadata.Registry, *processAttentionStore, string) {
	t.Helper()
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err = json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	panes := reg.Panes[:0]
	for _, pane := range reg.Panes {
		if pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
			panes = append(panes, pane)
		}
	}
	reg.Panes = panes
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	processPane, _ := reg.Pane("pane-02")
	processPane.Status.Activation.Process.HostProcess.OwnerUID = identity.OwnerUID
	processPane.Status.Activation.Process.Child.OwnerUID = identity.OwnerUID
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	path := intmetadata.PathFor(paths.StateDir)
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	writeProcessAttentionRegistry(t, path, reg)
	store := newProcessAttentionStore(paths.StateDir)
	pane, _ := reg.Pane("pane-02")
	b := processSchemaBinding(pane.Status.Activation.Process.Binding)
	data, _ := json.Marshal(map[string]processAttentionRecord{b.Pane: {Binding: b, Provider: "codex", Sequence: 9, Pending: map[string]processAttentionPending{"request": {Kind: aibadge.InputRequired, Sequence: 9}}}})
	if err = os.WriteFile(store.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return reg, store, path
}

func writeProcessAttentionRegistry(t *testing.T, path string, reg coremetadata.Registry) {
	t.Helper()
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProcessAttentionDefaultConsumersRefreshRegistryReadOnly(t *testing.T) {
	reg, store, path := processAttentionWiringFixture(t)
	before, _ := os.ReadFile(path)
	attentionBefore, _ := os.ReadFile(store.path)
	command := newAttentionCommand()
	var out, stderr bytes.Buffer
	if err := command.Run([]string{"list", "--json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pane-02") || !strings.Contains(out.String(), "reply") {
		t.Fatalf("default attention omitted process: %s", &out)
	}
	lister := newDefaultLivePaneLister()
	rows, err := lister.ListLivePanes()
	if err != nil || len(rows) != 1 || rows[0].processNotice == nil {
		t.Fatalf("default notify inventory: %+v %v", rows, err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("registry changed")
	}
	if after, _ := os.ReadFile(store.path); !bytes.Equal(attentionBefore, after) {
		t.Fatal("attention read changed store")
	}
	pane, _ := reg.Pane("pane-02")
	pane.Status.Activation.Generation = "replacement"
	pane.Status.Activation.Process.Binding.Generation = "replacement"
	writeProcessAttentionRegistry(t, path, reg)
	rows, err = lister.ListLivePanes()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.processNotice != nil || row.ReplyState {
			t.Fatalf("stale generation visible: %+v", row)
		}
	}
}

func TestProcessAttentionDefaultConsumersDamagedReadDoesNotRepair(t *testing.T) {
	_, store, _ := processAttentionWiringFixture(t)
	if err := os.WriteFile(store.path, []byte("{damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err := newAttentionCommand().Run([]string{"list", "--json"}, &out, &stderr)
	if !errors.Is(err, errProcessAttentionDamaged) {
		t.Fatalf("attention read error: %v", err)
	}
	_, err = newDefaultLivePaneLister().ListLivePanes()
	if !errors.Is(err, errProcessAttentionDamaged) {
		t.Fatalf("notify read error: %v", err)
	}
	data, _ := os.ReadFile(store.path)
	if string(data) != "{damaged" {
		t.Fatal("reader repaired damaged store")
	}
	matches, _ := filepath.Glob(store.path + ".damaged-*")
	if len(matches) != 0 {
		t.Fatal("reader created backup")
	}
}

func TestProcessDescribeReadableRows(t *testing.T) {
	reg, _, _ := processAttentionWiringFixture(t)
	pane, _ := reg.Pane("pane-02")
	rows := describeRuntimeRows(*pane)
	values := map[string]string{}
	for _, row := range rows {
		values[row[0]] = row[1]
		if strings.Contains(row[1], "{") {
			t.Fatalf("process JSON block: %+v", row)
		}
	}
	if values["HostPID"] != "11" || values["ChildPID"] != "12" || values["ResumeState"] != "resumable" || values["PendingControls"] != "1" {
		t.Fatalf("readable fields: %+v", rows)
	}
}

func TestProcessAttentionDefaultMixedTmuxErrorRetainsProcess(t *testing.T) {
	reg, _, path := processAttentionWiringFixture(t)
	raw, _ := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	var mixed coremetadata.Registry
	if err := json.Unmarshal(raw, &mixed); err != nil {
		t.Fatal(err)
	}
	reg.Panes = append(reg.Panes, mixed.Panes[0])
	writeProcessAttentionRegistry(t, path, reg)
	boom := errors.New("tmux unavailable")
	runner := &processWiringErrorRunner{err: boom}
	rows, err := newGenerationAwareLivePaneLister(newAttentionLivePaneLister(runner), snapshotResourceRegistry).ListLivePanes()
	if err == nil || len(rows) == 0 || rows[0].processNotice == nil || rows[0].tmuxObservationError == nil {
		t.Fatalf("mixed failure erased: %+v %v", rows, err)
	}
	attention := newAttentionCommand()
	attention.runner = runner
	paneRows, err := attention.listAttentionPanes()
	if err == nil || len(paneRows) != 1 {
		t.Fatalf("mixed attention: %+v %v", paneRows, err)
	}
}

func TestProcessAttentionActualCLIIsolated(t *testing.T) {
	binary := os.Getenv("PMX_TEST_CLI")
	if binary == "" {
		t.Skip("run with isolated copied CLI binary")
	}
	reg, store, path := processAttentionWiringFixture(t)
	records, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	queue := notify.NewDefaultStore(paths)
	r := records["pane-02"]
	entry, _, err := queue.Push(processAttentionInput(r, 9, aibadge.InputRequired))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "tmux.trace")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 0\n", trace)
	if err = os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := processWiringStateFiles(t, filepath.Dir(path))
	cases := [][]string{{"attention", "list", "--json"}, {"attention", "window", "window-01"}, {"get", "notifications", "--live", "--json"}, {"describe", "pane", "--project", "uid:project-01", "--window", "uid:window-01", "--pane", "uid:pane-02"}, {"internal", "statusbar", "click", "notify"}}
	for _, args := range cases {
		if args[0] == "internal" {
			if err := os.WriteFile(trace, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, output)
		}
		t.Logf("CLI %v: %s", args, output)
		if args[0] == "attention" && args[1] == "list" && !bytes.Contains(output, []byte("pane-02")) {
			t.Fatal("CLI attention missing process")
		}
		if args[0] == "get" && (!bytes.Contains(output, []byte(entry.ID)) || !bytes.Contains(output, []byte("live"))) {
			t.Fatalf("CLI notify missing live process: %s", output)
		}
	}
	after := processWiringStateFiles(t, filepath.Dir(path))
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("CLI reads wrote state")
	}
	data, _ := os.ReadFile(trace)
	if !strings.Contains(string(data), "process Pane") || strings.Contains(string(data), "select-pane") || strings.Contains(string(data), "list-panes") {
		t.Fatalf("statusbar process routing: %s", data)
	}
	pane, _ := reg.Pane("pane-02")
	pane.Status.Activation.Generation = "replacement"
	pane.Status.Activation.Process.Binding.Generation = "replacement"
	writeProcessAttentionRegistry(t, path, reg)
	staleBefore := processWiringStateFiles(t, filepath.Dir(path))
	for _, args := range [][]string{{"attention", "list", "--json"}, {"get", "notifications", "--live", "--json"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		data, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("stale CLI %v: %s %v", args, data, err)
		}
		if args[0] == "attention" && bytes.Contains(data, []byte("reply")) {
			t.Fatalf("stale attention exposed: %s", data)
		}
		if args[0] == "get" {
			var report struct {
				Live []json.RawMessage `json:"live"`
			}
			if err := json.Unmarshal(data, &report); err != nil || len(report.Live) != 0 {
				t.Fatalf("stale CLI live row: %s %v", data, err)
			}
		}
	}
	if after := processWiringStateFiles(t, filepath.Dir(path)); fmt.Sprint(staleBefore) != fmt.Sprint(after) {
		t.Fatal("stale CLI read wrote state")
	}
	if err := os.WriteFile(store.path, []byte("{damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	damagedBefore := processWiringStateFiles(t, filepath.Dir(path))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	damagedOutput, damagedErr := exec.CommandContext(ctx, binary, "attention", "list", "--json").CombinedOutput()
	cancel()
	if damagedErr == nil || !bytes.Contains(damagedOutput, []byte("damaged process attention store")) {
		t.Fatalf("CLI damaged read: %s %v", damagedOutput, damagedErr)
	}
	if after := processWiringStateFiles(t, filepath.Dir(path)); fmt.Sprint(damagedBefore) != fmt.Sprint(after) {
		t.Fatal("damaged CLI read repaired state")
	}
	t.Log("actual CLI: current/stale/damaged reads byte-identical, terminal focus zero")

}

func processWiringStateFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type processWiringErrorRunner struct{ err error }

func (r *processWiringErrorRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, r.err
}

func TestProcessAttentionDefaultNoActivationKeepsDeclaration(t *testing.T) {
	reg, _, path := processAttentionWiringFixture(t)
	pane, _ := reg.Pane("pane-02")
	pane.Status.Activation = coremetadata.PaneActivation{}
	writeProcessAttentionRegistry(t, path, reg)
	rows, err := newDefaultLivePaneLister().ListLivePanes()
	if err != nil || len(rows) != 1 || rows[0].Pane != "pane-02" || rows[0].processNotice != nil {
		t.Fatalf("missing declaration without activation: %+v %v", rows, err)
	}
}

func TestProcessAttentionDefaultNotifyFocusDoesNotInvokeTmux(t *testing.T) {
	_, store, _ := processAttentionWiringFixture(t)
	records, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	in := processAttentionInput(records["pane-02"], 9, aibadge.InputRequired)
	command := newNotifyCommand(newDefaultLivePaneLister())
	err = command.focusNotification(notify.Notification{ID: in.ID}, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "no terminal focus target") {
		t.Fatalf("process focus fell through: %v", err)
	}
}

func TestProcessAttentionStatusbarUsesExactProjectionAndReportsReadError(t *testing.T) {
	reg, store, path := processAttentionWiringFixture(t)
	records, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	queue := notify.NewDefaultStore(paths)
	head, _, err := queue.Push(processAttentionInput(records["pane-02"], 9, aibadge.InputRequired))
	if err != nil {
		t.Fatal(err)
	}
	command := newStatusbarCommand()
	runner := &statusbarFakeRunner{}
	command.runner = runner
	if got := command.classifyHeadDisplayBestEffort(head); got != notifyDisplayLive {
		t.Fatalf("statusbar live projection: %v", got)
	}
	pane, _ := reg.Pane("pane-02")
	pane.Status.Activation.Generation = "replacement"
	pane.Status.Activation.Process.Binding.Generation = "replacement"
	writeProcessAttentionRegistry(t, path, reg)
	if got := command.classifyHeadDisplayBestEffort(head); got != notifyDisplayStale {
		t.Fatalf("statusbar stale projection: %v", got)
	}
	if err = os.WriteFile(store.path, []byte("{damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err = command.Run([]string{"click", "notify"}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "damaged process attention store") {
		t.Fatalf("statusbar suppressed read error: %s", &stderr)
	}
	for _, call := range runner.calls {
		if call.name != "tmux" || len(call.args) == 0 || call.args[0] != "display-message" {
			t.Fatalf("process statusbar attempted terminal focus: %+v", call)
		}
	}
}
