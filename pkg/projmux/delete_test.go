package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type deleteFailureRunner struct {
	path, uid, project      string
	failure                 string
	pidReads, kills, queued int
}

func (r *deleteFailureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.path {
		return nil, errors.New("unbound runner")
	}
	args = args[2:]
	var out strings.Builder
	for len(args) > 0 {
		at := len(args)
		for i, a := range args {
			if a == ";" {
				at = i
				break
			}
		}
		cmd := args[:at]
		if at == len(args) {
			args = nil
		} else {
			args = args[at+1:]
		}
		if len(cmd) == 0 {
			continue
		}
		format := cmd[len(cmd)-1]
		switch cmd[0] {
		case "kill-window":
			r.kills++
			return nil, errors.New("injected kill failure")
		case "run-shell":
			r.queued++
			return nil, errors.New("unexpected deferred self kill")
		case "show-options":
			if strings.Contains(format, "socket_name") {
				out.WriteString("projmux")
			} else if strings.Contains(format, "app") {
				out.WriteString("1")
			} else if strings.Contains(format, "window_uid") {
				out.WriteString(r.uid)
			}
		case "display-message":
			switch format {
			case "#{socket_path}":
				out.WriteString(r.path)
			case "#{pid}":
				r.pidReads++
				pid := 4242
				if r.failure == "pid" && r.pidReads > 1 {
					pid = 5555
				}
				out.WriteString(strconv.Itoa(pid))
			case mutationruntime.RouteReadBoundary:
				out.WriteString(format)
			default:
				uid := r.uid
				if r.failure == "uid" {
					uid = "foreign"
				}
				out.WriteString(strings.Join([]string{"$1", "alpha", "@1", r.project, uid}, "\x1f"))
			}
		case "list-windows":
			if strings.Count(format, "\\037") == 4 {
				out.WriteString(strings.Join([]string{"$1", "alpha", "@1", r.project, r.uid}, "\x1f"))
			} else {
				out.WriteString(strings.Join([]string{"$1", "@1", r.uid}, "\x1f"))
			}
		default:
			return nil, errors.New("unexpected tmux read " + strings.Join(cmd, " "))
		}
		out.WriteByte('\n')
	}
	return []byte(out.String()), nil
}
func deleteFailureFixture(t *testing.T) (metadata.Registry, string, string, *Client) {
	t.Helper()
	reg := metadata.NewRegistry()
	m := store.DefaultMutator()
	created, err := m.RegisterProject(&reg, metadata.RegisterProjectOptions{Root: t.TempDir(), Name: "alpha", DefaultShell: "/bin/sh", OperationID: "op-test"})
	if err != nil {
		t.Fatal(err)
	}
	project, _ := reg.Project(created.Project.Metadata.UID)
	project.Status.Session = &metadata.SessionProjection{Name: "alpha", Live: true}
	state := t.TempDir()
	path := store.PathFor(state)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Options{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	return reg, created.Project.Spec.PrimaryWindowRef, state, c
}
func TestDeleteSDKInjectedFailuresAndNoSelfKill(t *testing.T) {
	for _, failure := range []string{"pid", "uid", "kill"} {
		t.Run(failure, func(t *testing.T) {
			reg, uid, state, c := deleteFailureFixture(t)
			path := store.PathFor(state)
			before, _ := os.ReadFile(path)
			runner := &deleteFailureRunner{path: filepath.Join(t.TempDir(), "s"), uid: uid, project: reg.Projects[0].Metadata.UID, failure: failure}
			result, err := c.deleteWithRunner(context.Background(), core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:" + uid, SocketPath: runner.path, Confirm: true}, runner)
			var refusal *Refusal
			if !errors.As(err, &refusal) || result.Committed || (refusal.Code != CodeDeleteExecutionFailed && refusal.Code != CodeDeleteRuntimeRefused) {
				t.Fatalf("%+v %v", result, err)
			}
			after, _ := os.ReadFile(path)
			var oldReg, newReg metadata.Registry
			if e := json.Unmarshal(before, &oldReg); e != nil {
				t.Fatal(e)
			}
			if e := json.Unmarshal(after, &newReg); e != nil {
				t.Fatal(e)
			}
			// Durable intent/withdrawal refreshes only the store clock, as on the CLI.
			newReg.UpdatedAt = oldReg.UpdatedAt
			if !reflect.DeepEqual(oldReg, newReg) {
				t.Fatalf("Registry/intent rollback differs: %v", err)
			}
			if runner.queued != 0 {
				t.Fatal("SDK queued self kill")
			}
			if failure == "kill" && runner.kills != 1 {
				t.Fatalf("kill injection not reached: %v", err)
			}
			if failure != "kill" && runner.kills != 0 {
				t.Fatalf("guard failed after kill: %v", err)
			}
		})
	}
}
func TestDeleteSDKProcessRejectionBeforeAnyEffect(t *testing.T) {
	reg, uid, state, c := deleteFailureFixture(t)
	m := store.DefaultMutator()
	project := reg.Projects[0].Metadata.UID
	if err := m.DeleteWindow(&reg, uid); err != nil {
		t.Fatal(err)
	}
	window, _, pane, err := m.CreateProcessWindow(&reg, project, metadata.BootstrapWindow{Name: "process"}, metadata.CreateAgentOptions{Name: "worker", Provider: "codex", OperationID: "op-process"}, metadata.ProcessBinding{HostInstanceID: "host", Generation: "gen", OperationID: "op-process"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(reg)
	path := store.PathFor(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind core.Kind
		uid  string
	}{{core.KindWindow, window.Metadata.UID}, {core.KindPane, pane.Metadata.UID}} {
		runner := &deleteFailureRunner{path: filepath.Join(t.TempDir(), "s")}
		result, err := c.deleteWithRunner(context.Background(), core.DeleteRequest{Kind: tc.kind, Ref: "uid:" + tc.uid, SocketPath: runner.path, Confirm: true}, runner)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeDeleteProcessUnsupported || result.Committed {
			t.Fatalf("%s %+v %v", tc.kind, result, err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(data, after) || runner.pidReads+runner.kills+runner.queued != 0 {
			t.Fatal("process refusal touched state/runtime")
		}
	}
}

func TestDeleteQueryPreservesRequestNamesInUserValues(t *testing.T) {
	_, _, _, c := deleteFailureFixture(t)
	for _, req := range []core.DeleteRequest{
		{Kind: core.KindPane, Ref: "RenameRequest.Ref-user-value"},
		{Kind: core.KindAgent, Ref: "worker", Project: "RenameRequest.Project-user-value"},
		{Kind: core.KindPane, Ref: "shell", Window: "RenameRequest.Window-user-value"},
	} {
		_, err := c.Delete(context.Background(), req)
		var r *Refusal
		if !errors.As(err, &r) || r.Code != CodeResourceNotFound || !strings.Contains(err.Error(), "RenameRequest.") {
			t.Fatalf("user value changed: %+v %v", req, err)
		}
		if strings.Contains(err.Error(), "DeleteRequest.Ref-user-value") || strings.Contains(err.Error(), "DeleteRequest.Project-user-value") || strings.Contains(err.Error(), "DeleteRequest.Window-user-value") {
			t.Fatal(err)
		}
	}
	for _, kind := range []core.Kind{core.KindWindow, core.KindPane, core.KindAgent} {
		_, err := deleteQuery(core.DeleteRequest{Kind: kind, Ref: "uid:"})
		if err == nil || !strings.Contains(err.Error(), "DeleteRequest.Ref") || strings.Contains(err.Error(), "RenameRequest.Ref") {
			t.Fatalf("input field: %v", err)
		}
	}
}

type protectingDeleteRunner struct {
	*deleteFailureRunner
	protect func()
	once    bool
}

func (r *protectingDeleteRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !r.once {
		r.once = true
		r.protect()
	}
	return r.deleteFailureRunner.Run(ctx, name, args...)
}

func TestDeleteSDKLockedProtectionCarriesAgentUID(t *testing.T) {
	reg, window, state, client := deleteFailureFixture(t)
	agent, err := store.DefaultMutator().CreateAgent(&reg, window, metadata.CreateAgentOptions{Provider: "codex", OperationID: "op-agent"})
	if err != nil {
		t.Fatal(err)
	}
	path := store.PathFor(state)
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var admittedBytes []byte
	runner := &protectingDeleteRunner{deleteFailureRunner: &deleteFailureRunner{path: filepath.Join(t.TempDir(), "s"), uid: window, project: reg.Projects[0].Metadata.UID}}
	runner.protect = func() {
		_, err := store.NewStore(path).Update(func(working *metadata.Registry) error {
			_, _, err := store.DefaultMutator().SetLabels(working, metadata.KindAgent, agent.Metadata.UID, map[string]string{metadata.LabelProtected: "true"}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		admittedBytes, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := client.deleteWithRunner(context.Background(), core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:" + window, SocketPath: runner.path, Confirm: true}, runner)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeAgentProtected || refusal.AgentUID != agent.Metadata.UID || result.Committed {
		t.Fatalf("result=%+v refusal=%+v err=%v", result, refusal, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(admittedBytes, after) || runner.kills+runner.queued != 0 {
		t.Fatal("locked protection allowed writes or runtime effects")
	}
	raw, err := json.Marshal(refusal)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields["agentUID"] != agent.Metadata.UID {
		t.Fatalf("structured UID %s %v", raw, err)
	}
}
