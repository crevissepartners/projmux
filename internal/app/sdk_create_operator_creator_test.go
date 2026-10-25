package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// sdkOperatorCreatorClient is the operator client name these tests record.
const sdkOperatorCreatorClient = "tester"

// sdkOperatorCreatorFixture is one isolated Project with a Window named extra
// on a private tmux server, reachable from the SDK and from a CLI command.
type sdkOperatorCreatorFixture struct {
	client                      *projmux.Client
	state, config, socket, home string
	binary                      string
}

func newSDKOperatorCreatorFixture(t *testing.T, binary, home string) sdkOperatorCreatorFixture {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "projmux")
	config := filepath.Join(t.TempDir(), "projmux")
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(config))
	t.Setenv("XDG_STATE_HOME", filepath.Dir(state))
	socket, run := sdkCreateSocket(t)
	run("set-option", "-w", "-t", "control:0", "automatic-rename", "off")
	run("rename-window", "-t", "control:0", "control")
	client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: config, HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: binary})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sample"}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "extra"}); err != nil {
		t.Fatal(err)
	}
	return sdkOperatorCreatorFixture{client: client, state: state, config: config, socket: socket, home: home, binary: binary}
}

// agentNamed returns the stored Agent named name and its managed Pane.
func (f sdkOperatorCreatorFixture) agentNamed(t *testing.T, name string) (metadata.Agent, metadata.Pane) {
	t.Helper()
	reg := projectRegistry(t, f.state)
	for _, agent := range reg.Agents {
		if agent.Metadata.Name != name {
			continue
		}
		pane, ok := reg.Pane(agent.Status.PaneRef)
		if !ok {
			t.Fatalf("Agent %s has no managed Pane", name)
		}
		return agent, *pane
	}
	t.Fatalf("no Agent named %s", name)
	return metadata.Agent{}, metadata.Pane{}
}

// TestCreateAgentOperatorCreatorMatchesCLI runs the same tmux create once
// through the CLI's in-process operator seam (recordOperatorCreator) and once
// through CreateAgentRequest.Operator, and requires the same Registry, the
// operator creator on the Agent and its Pane, and, with a declaration, the
// CLI's stderr line as an SDK notice. The process path is
// TestCreateAgentOperatorCreatorMatchesCLIProcess.
func TestCreateAgentOperatorCreatorMatchesCLI(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	for _, declare := range []bool{false, true} {
		name := "operator"
		if declare {
			name = "operator-with-declaration"
		}
		t.Run(name, func(t *testing.T) {
			var snapshots, lines []string
			for _, sdk := range []bool{false, true} {
				f := newSDKOperatorCreatorFixture(t, binary, home)
				req := core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Name: "worker", Operator: core.NewOperatorCreator(sdkOperatorCreatorClient)}
				args := []string{"agent", "--project", "sample", "--window", "extra", "--provider", "claude", "--name", "worker"}
				if declare {
					parent, err := f.client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Name: "parent"})
					if err != nil {
						t.Fatal(err)
					}
					req.Creator = "uid:" + parent.Agent.Metadata.UID
					args = append(args, "--creator", req.Creator)
				}
				var line string
				if sdk {
					result, err := f.client.CreateAgent(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					for _, notice := range result.Notices {
						if strings.HasPrefix(notice, "creator declaration not recorded:") {
							line = notice
						}
					}
				} else {
					c := sdkAgentCLI(t, f.state, f.config, f.socket, f.binary, f.home)
					if err := c.recordOperatorCreator(sdkOperatorCreatorClient); err != nil {
						t.Fatal(err)
					}
					var stdout, stderr bytes.Buffer
					if err := c.Run(append(args, "-o", "receipt"), &stdout, &stderr); err != nil {
						t.Fatalf("CLI: %v %s", err, &stderr)
					}
					for _, raw := range strings.SplitAfter(stderr.String(), "\n") {
						if strings.HasPrefix(raw, "creator declaration not recorded:") {
							if !strings.HasSuffix(raw, "\n") {
								t.Fatalf("CLI declaration line %q is not a whole line", raw)
							}
							line = strings.TrimSuffix(raw, "\n")
						}
					}
				}
				waitSDKClaudeProcesses(t, f.state)
				agent, pane := f.agentNamed(t, "worker")
				assertCreatorRecord(t, agent, pane, metadata.OperatorCreatorAnnotations(sdkOperatorCreatorClient))
				reg := projectRegistry(t, f.state)
				snapshots = append(snapshots, normalizeAgentCreateJSON(t, projectRegistryBytes(t, f.state), reg, f.socket))
				for i, a := range reg.Agents {
					line = strings.ReplaceAll(line, a.Metadata.UID, "AGENT-"+string(rune('A'+i)))
				}
				lines = append(lines, line)
			}
			if snapshots[0] != snapshots[1] {
				t.Fatalf("SDK/CLI Registry mismatch\nCLI %s\nSDK %s", snapshots[0], snapshots[1])
			}
			want := ""
			if declare {
				want = "creator declaration not recorded: operator-client (--creator uid:AGENT-A)"
			}
			if lines[0] != want || lines[1] != want {
				t.Fatalf("declaration line CLI %q SDK %q, want %q", lines[0], lines[1], want)
			}
		})
	}
}

// TestCreateAgentOperatorCreatorCannotBeDecoded decodes request data that
// spells an operator in every shape a JSON body can, creates from what it
// decoded, and requires no operator creator: the client name is unexported, so
// neither JSON nor reflection can set it.
func TestCreateAgentOperatorCreatorCannotBeDecoded(t *testing.T) {
	field, ok := reflect.TypeFor[core.CreateAgentRequest]().FieldByName("Operator")
	if !ok || field.Type != reflect.TypeFor[core.OperatorCreator]() {
		t.Fatal("CreateAgentRequest.Operator is not a core.OperatorCreator")
	}
	operatorType := reflect.TypeFor[core.OperatorCreator]()
	for i := range operatorType.NumField() {
		if operatorType.Field(i).IsExported() {
			t.Fatalf("core.OperatorCreator exports field %s", operatorType.Field(i).Name)
		}
	}
	var probe core.CreateAgentRequest
	if reflect.ValueOf(&probe).Elem().FieldByName("Operator").Field(0).CanSet() {
		t.Fatal("reflection can set the operator client name")
	}
	if (core.CreateAgentRequest{}).Operator != (core.OperatorCreator{}) || core.NewOperatorCreator("").Client() != "" {
		t.Fatal("the zero request names an operator")
	}

	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	f := newSDKOperatorCreatorFixture(t, binary, home)
	bodies := []string{
		`{"Project":"sample","Window":"extra","Provider":"claude","Name":"w1","Operator":{"client":"tester","Client":"tester"}}`,
		`{"Project":"sample","Window":"extra","Provider":"claude","Name":"w2","operator":{"client":"tester"},"OperatorClient":"tester","CreatorBasis":"operator","creator-basis":"operator"}`,
		`{"Project":"sample","Window":"extra","Provider":"claude","Name":"w3","Operator":"tester"}`,
	}
	created := 0
	for _, body := range bodies {
		var req core.CreateAgentRequest
		err := json.Unmarshal([]byte(body), &req)
		if req.Operator.Client() != "" {
			t.Fatalf("decoding %s named operator %q", body, req.Operator.Client())
		}
		if err != nil {
			// A body the type refuses outright decodes nothing to create from.
			continue
		}
		if _, err := f.client.CreateAgent(context.Background(), req); err != nil {
			t.Fatalf("create from %s: %v", body, err)
		}
		created++
		waitSDKClaudeProcesses(t, f.state)
		agent, pane := f.agentNamed(t, req.Name)
		assertCreatorRecord(t, agent, pane, map[string]string{})
	}
	if created == 0 {
		t.Fatal("no decoded request reached CreateAgent, so the test proves nothing")
	}
	for _, a := range projectRegistry(t, f.state).Agents {
		if a.Metadata.Annotations[metadata.AnnotationCreatorBasis] == metadata.CreatorBasisOperator {
			t.Fatalf("Agent %s records the operator basis", a.Metadata.Name)
		}
	}
}

// TestCreateAgentDeclaredCreatorCannotClaimOperator declares operator-looking
// creators as strings: each is refused before anything changes, and a
// declaration naming a real Agent records only the explicit basis.
func TestCreateAgentDeclaredCreatorCannotClaimOperator(t *testing.T) {
	binary := sdkCreateBinary(t)
	home := sdkAgentHome(t)
	f := newSDKOperatorCreatorFixture(t, binary, home)
	before := projectRegistryBytes(t, f.state)
	for _, creator := range []string{"web", "operator", "operator-client", "ui", "uid:web", "uid:operator", "uid:ui", "uid:" + sdkOperatorCreatorClient, "operator:web", "uid:operator:web"} {
		_, err := f.client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Creator: creator})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateCreatorInvalid {
			t.Fatalf("Creator %q: %T %v, want %s", creator, err, err, projmux.CodeCreateCreatorInvalid)
		}
		if after := projectRegistryBytes(t, f.state); after != before {
			t.Fatalf("Creator %q refusal changed the Registry", creator)
		}
	}
	for _, client := range []string{"Web", "2x", "a b", strings.Repeat("a", operatorclient.MaxBytes+1)} {
		_, err := f.client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Operator: core.NewOperatorCreator(client)})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateCreatorInvalid || !errors.Is(err, operatorclient.ErrInvalid) {
			t.Fatalf("Operator %q: %T %v, want %s", client, err, err, projmux.CodeCreateCreatorInvalid)
		}
		if after := projectRegistryBytes(t, f.state); after != before {
			t.Fatalf("Operator %q refusal changed the Registry", client)
		}
	}
	parent, err := f.client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Name: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.client.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "extra", Provider: "claude", Name: "child", Creator: "uid:" + parent.Agent.Metadata.UID})
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(child.Notices, func(n string) bool { return strings.HasPrefix(n, "creator declaration not recorded:") }) {
		t.Fatalf("a declaration with no operator was declined: %v", child.Notices)
	}
	waitSDKClaudeProcesses(t, f.state)
	agent, pane := f.agentNamed(t, "child")
	assertCreatorRecord(t, agent, pane, metadata.ExplicitCreatorAnnotations(parent.Agent.Metadata.UID))
}
