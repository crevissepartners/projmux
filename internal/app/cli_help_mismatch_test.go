package app

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// These routes advertise receipt and none for every rename kind. Compare the
// explicit receipt with the existing receipt writer, not a new JSON schema.
func TestCLIHelpRenameOutput(t *testing.T) {
	for _, target := range []struct {
		kind string
		uid  string
	}{
		{"project", "prj-alpha"},
		{"window", "win-alpha-review"},
		{"pane", "pan-alpha-log"},
		{"agent", "agt-alpha-codex"},
	} {
		for _, flag := range []string{"-o", "--output"} {
			for _, mode := range []string{"receipt", "none"} {
				t.Run(target.kind+"/"+flag+"/"+mode, func(t *testing.T) {
					store := newFakeResourceStore(t)
					stdout, stderr, err := runRoute(t, newTestRenameCommand(store),
						target.kind, "uid:"+target.uid, "--name", "renamed", flag, mode)
					if err != nil {
						t.Fatalf("advertised rename output refused: %v (stderr %q)", err, stderr)
					}
					_, meta, ok := resourceFor(store.registry, resourceKindTokens[target.kind], target.uid)
					if !ok || meta.Name != "renamed" || store.writes != 1 {
						t.Fatalf("rename did not commit exactly one name change: meta=%+v writes=%d", meta, store.writes)
					}
					var want bytes.Buffer
					if mode == "receipt" {
						if err := renameReceipt(resourceKindTokens[target.kind], target.uid, "renamed").WriteJSON(&want); err != nil {
							t.Fatal(err)
						}
					}
					if stdout != want.String() {
						t.Fatalf("output = %q, want existing %s output %q", stdout, mode, want.String())
					}
				})
			}
		}
		for _, mode := range []string{"yaml", "uid"} {
			t.Run(target.kind+"/invalid/"+mode, func(t *testing.T) {
				store := newFakeResourceStore(t)
				before := store.snapshot()
				stdout, _, err := runRoute(t, newTestRenameCommand(store),
					target.kind, "uid:"+target.uid, "--name", "renamed", "-o", mode)
				want := fmt.Sprintf("invalid --output %q for rename %s; accepted values: receipt, none", mode, target.kind)
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("invalid output refusal = %v, want %q", err, want)
				}
				if store.snapshot() != before || store.transactions != 0 || stdout != "" {
					t.Fatal("invalid output changed registry or emitted a result")
				}
			})
		}
	}
}

func TestCLIHelpUnregisterProjectRefs(t *testing.T) {
	for _, refs := range [][]string{
		{"alpha", "beta"},
		{"uid:prj-alpha", "uid:prj-beta"},
		{"alpha", "uid:prj-alpha", "beta", "uid:prj-beta"},
	} {
		for _, action := range []string{"preview", "confirm", "yes"} {
			t.Run(strings.Join(refs, "+")+"/"+action, func(t *testing.T) {
				store := freshStartFixtureStore(t)
				before := store.snapshot()
				var prompts []string
				cmd := newTestDeleteCommand(store, action == "confirm", true, &prompts)
				args := append([]string{"project"}, refs...)
				if action == "preview" {
					args = append(args, "--dry-run")
				} else if action == "yes" {
					args = append(args, "--yes")
				}
				stdout, stderr, err := runRoute(t, &unregisterCommand{delete: cmd}, args...)
				if err != nil {
					t.Fatalf("advertised unregister refs refused: %v (stderr %q)", err, stderr)
				}
				if strings.Count(stdout, "unregister project:") != 1 || !strings.Contains(stdout, "2 projects") {
					t.Fatalf("expected one deduplicated two-Project plan, got %q", stdout)
				}
				if action == "preview" {
					if store.snapshot() != before || store.transactions != 0 || len(prompts) != 0 {
						t.Fatal("preview mutated registry or prompted")
					}
					return
				}
				if action == "confirm" && (len(prompts) != 1 || !strings.Contains(prompts[0], "2 Projects")) {
					t.Fatalf("expected one full-set confirmation, got %q", prompts)
				}
				if store.writes != 1 {
					t.Fatalf("writes = %d, want one atomic unregister", store.writes)
				}
				for _, uid := range []string{"prj-alpha", "prj-beta", "win-alpha-main", "win-beta-main", "agt-alpha-codex"} {
					if freshStartRegistryHasUID(store.registry, uid) {
						t.Errorf("unregister retained selected graph resource %s", uid)
					}
				}
				if _, ok := store.registry.Project("prj-gone"); !ok {
					t.Fatal("unregister removed unrelated Project")
				}
			})
		}
	}
}

func TestCLIHelpUnregisterProjectRefRefusals(t *testing.T) {
	for _, test := range []struct {
		name string
		refs []string
		want string
	}{
		{"missing second", []string{"alpha", "missing"}, "whole target set must resolve"},
		{"missing first", []string{"uid:missing", "uid:prj-beta"}, "whole target set must resolve"},
		{"repeat scope", []string{"--project", "alpha", "--project", "beta"}, "--project accepts at most one occurrence"},
		{"scope mixed with refs", []string{"alpha", "beta", "--project", "alpha"}, "--project accepts at most one occurrence"},
		{"all with refs", []string{"alpha", "beta", "--all"}, "cannot be combined with a selector"},
		{"confirmation required", []string{"alpha", "beta"}, "needs confirmation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := freshStartFixtureStore(t)
			before := store.snapshot()
			cmd := newTestDeleteCommand(store, false, false, nil)
			args := append([]string{"project"}, test.refs...)
			if test.name != "confirmation required" {
				args = append(args, "--yes")
			}
			stdout, _, err := runRoute(t, &unregisterCommand{delete: cmd}, args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("refusal = %v, want %q", err, test.want)
			}
			if store.snapshot() != before || store.transactions != 0 || stdout != "" {
				t.Fatal("refused target set changed registry or emitted a result")
			}
		})
	}
}

func TestCLIHelpDeleteProjectMultipleRefsRemainRefused(t *testing.T) {
	for _, kind := range []string{"project", "projects"} {
		t.Run(kind, func(t *testing.T) {
			store := freshStartFixtureStore(t)
			before := store.snapshot()
			cmd := newTestDeleteCommand(store, false, false, nil)
			_, _, err := runRoute(t, cmd, kind, "uid:prj-alpha", "uid:prj-beta", "--yes")
			if err == nil || !strings.Contains(err.Error(), "--project accepts at most one occurrence") {
				t.Errorf("delete alias cardinality changed: %v", err)
			}
			if store.snapshot() != before || store.transactions != 0 {
				t.Fatal("delete multiple refs mutated registry")
			}
		})
	}
}

func TestCLIHelpUnregisterProjectRefsWithLabels(t *testing.T) {
	store := freshStartFixtureStore(t)
	for _, uid := range []string{"prj-alpha", "prj-beta"} {
		project, _ := store.registry.Project(uid)
		project.Metadata.Labels = map[string]string{"group": "selected"}
	}
	cmd := newTestDeleteCommand(store, false, false, nil)
	stdout, stderr, err := runRoute(t, &unregisterCommand{delete: cmd},
		"project", "alpha", "uid:prj-beta", "--selector", "group=selected", "--yes")
	if err != nil {
		t.Fatalf("label-filtered refs refused: %v (stderr %q)", err, stderr)
	}
	if store.writes != 1 || !strings.Contains(stdout, "2 projects") {
		t.Fatalf("label-filtered unregister did not commit one full plan: writes=%d stdout=%q", store.writes, stdout)
	}
	for _, uid := range []string{"prj-alpha", "prj-beta"} {
		if _, ok := store.registry.Project(uid); ok {
			t.Errorf("selected Project %s retained", uid)
		}
	}
	if _, ok := store.registry.Project("prj-gone"); !ok {
		t.Fatal("label-filtered unregister removed unrelated Project")
	}
}

func TestCLIHelpUnregisterProjectRefsProtectionIsWholeSet(t *testing.T) {
	store := freshStartFixtureStore(t)
	agent, _ := store.registry.Agent("agt-alpha-codex")
	agent.Metadata.Labels = map[string]string{"projmux.io/protected": "true"}
	before := store.snapshot()
	cmd := newTestDeleteCommand(store, false, false, nil)
	_, _, err := runRoute(t, &unregisterCommand{delete: cmd},
		"project", "beta", "alpha", "--yes")
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Errorf("protected descendant refusal = %v", err)
	}
	if store.snapshot() != before || store.transactions != 0 {
		t.Fatal("protected target set partially unregistered")
	}
}

func TestCLIHelpUnregisterDuplicateRefsOneProject(t *testing.T) {
	store := freshStartFixtureStore(t)
	cmd := newTestDeleteCommand(store, false, false, nil)
	stdout, _, err := runRoute(t, &unregisterCommand{delete: cmd},
		"project", "alpha", "uid:prj-alpha", "alpha", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 || !strings.Contains(stdout, "deleting 1 project and") {
		t.Fatalf("duplicate refs did not form one target: writes=%d stdout=%q", store.writes, stdout)
	}
	if _, ok := store.registry.Project("prj-alpha"); ok {
		t.Fatal("duplicate target retained")
	}
	if _, ok := store.registry.Project("prj-beta"); !ok {
		t.Fatal("duplicate refs removed unrelated Project")
	}
}

func TestCLIHelpUnregisterLabelMismatchPreservesWholeSet(t *testing.T) {
	store := freshStartFixtureStore(t)
	project, _ := store.registry.Project("prj-alpha")
	project.Metadata.Labels = map[string]string{"group": "selected"}
	before := store.snapshot()
	cmd := newTestDeleteCommand(store, false, false, nil)
	_, _, err := runRoute(t, &unregisterCommand{delete: cmd},
		"project", "alpha", "beta", "--selector", "group=selected", "--yes")
	if err == nil || !strings.Contains(err.Error(), "whole target set must resolve") {
		t.Errorf("label-excluded ref refusal = %v", err)
	}
	if store.snapshot() != before || store.transactions != 0 {
		t.Fatal("label-excluded ref allowed partial unregister")
	}
}

func TestCLIHelpUnregisterDeclinedConfirmationPreservesWholeSet(t *testing.T) {
	store := freshStartFixtureStore(t)
	before := store.snapshot()
	var prompts []string
	cmd := newTestDeleteCommand(store, true, false, &prompts)
	_, _, err := runRoute(t, &unregisterCommand{delete: cmd}, "project", "alpha", "beta")
	if err == nil || !strings.Contains(err.Error(), "confirmation declined") || len(prompts) != 1 {
		t.Errorf("declined confirmation: err=%v prompts=%q", err, prompts)
	}
	if store.snapshot() != before || store.transactions != 0 {
		t.Fatal("declined confirmation changed registry")
	}
}

func TestCLIHelpUnregisterProtectionRecheckedForWholeSet(t *testing.T) {
	store := freshStartFixtureStore(t)
	cmd := newTestDeleteCommand(store, true, true, nil)
	var atExecution string
	cmd.confirm.ask = func(_ string, _ io.Writer) (bool, error) {
		agent, _ := store.registry.Agent("agt-alpha-codex")
		agent.Metadata.Labels = map[string]string{"projmux.io/protected": "true"}
		atExecution = store.snapshot()
		return true, nil
	}
	_, _, err := runRoute(t, &unregisterCommand{delete: cmd}, "project", "beta", "alpha")
	if err == nil || !strings.Contains(err.Error(), "protected") || atExecution == "" {
		t.Errorf("locked protection refusal = %v, confirmed=%t", err, atExecution != "")
	}
	if store.writes != 0 || store.snapshot() != atExecution {
		t.Fatal("protection added during confirmation allowed partial unregister")
	}
}
