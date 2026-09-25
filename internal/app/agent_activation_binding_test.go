package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// agentBindingReasonCase breaks exactly one binding fact of a Registry that
// was exactly bound, so the reason it produces is the one under test and not a
// neighbour that happens to be checked first.
type agentBindingReasonCase struct {
	reason agentActivationBindingReason
	// mutate breaks the fact on the Registry of the given Agent and Pane.
	mutate func(registry *coremetadata.Registry, agentUID, paneUID string)
	// expected and observed are the literal two sides the refusal must print.
	expected func(agentUID, paneUID, paneID string) string
	observed func(agentUID, paneUID, paneID string) string
	// agentPresent and panePresent are the presence words of the refusal.
	agentPresent bool
	panePresent  bool
}

func agentBindingTestPane(registry *coremetadata.Registry, paneUID string) *coremetadata.Pane {
	pane, _ := registry.Pane(paneUID)
	return pane
}

func agentBindingTestAgent(registry *coremetadata.Registry, agentUID string) *coremetadata.Agent {
	agent, _ := registry.Agent(agentUID)
	return agent
}

func agentBindingLiteral(value string) func(string, string, string) string {
	return func(string, string, string) string { return value }
}

// agentBindingReasonCases is one case per declared reason;
// TestAgentActivationBindingReasonSetIsClosed holds it to the declarations.
var agentBindingReasonCases = []agentBindingReasonCase{
	{
		reason: agentBindingPaneMissing,
		mutate: func(registry *coremetadata.Registry, _, paneUID string) {
			registry.Panes = slices.DeleteFunc(registry.Panes, func(pane coremetadata.Pane) bool {
				return pane.Metadata.UID == paneUID
			})
		},
		expected:     func(_, paneUID, _ string) string { return "Pane row uid:" + paneUID },
		observed:     agentBindingLiteral("absent"),
		agentPresent: true,
	},
	{
		reason: agentBindingPaneActivationCleared,
		mutate: func(registry *coremetadata.Registry, _, paneUID string) {
			agentBindingTestPane(registry, paneUID).Status.Activation.Generation = ""
		},
		expected: agentBindingLiteral("generation, agentUID, and runtimeID all set"),
		observed: func(agentUID, _, paneID string) string {
			return `generation="" agentUID="` + agentUID + `" runtimeID="` + paneID + `"`
		},
		agentPresent: true, panePresent: true,
	},
	{
		reason: agentBindingRuntimeIDChanged,
		mutate: func(registry *coremetadata.Registry, _, paneUID string) {
			agentBindingTestPane(registry, paneUID).Status.Activation.RuntimeID = "%999"
		},
		expected:     func(_, _, paneID string) string { return paneID },
		observed:     agentBindingLiteral("%999"),
		agentPresent: true, panePresent: true,
	},
	{
		reason: agentBindingActivationAgentChanged,
		mutate: func(registry *coremetadata.Registry, _, paneUID string) {
			agentBindingTestPane(registry, paneUID).Status.Activation.AgentUID = "agent-intruder"
		},
		expected:     func(agentUID, _, _ string) string { return "uid:" + agentUID },
		observed:     agentBindingLiteral("uid:agent-intruder"),
		agentPresent: true, panePresent: true,
	},
	{
		reason: agentBindingActivationGenerationChanged,
		mutate: func(registry *coremetadata.Registry, _, paneUID string) {
			agentBindingTestPane(registry, paneUID).Status.Activation.Generation = "gen-replaced"
		},
		expected:     nil, // the fixture's own generation; filled per fixture
		observed:     agentBindingLiteral("gen-replaced"),
		agentPresent: true, panePresent: true,
	},
	{
		reason: agentBindingAgentMissing,
		mutate: func(registry *coremetadata.Registry, agentUID, _ string) {
			registry.Agents = slices.DeleteFunc(registry.Agents, func(agent coremetadata.Agent) bool {
				return agent.Metadata.UID == agentUID
			})
		},
		expected:    func(agentUID, _, _ string) string { return "Agent row uid:" + agentUID },
		observed:    agentBindingLiteral("absent"),
		panePresent: true,
	},
	{
		reason: agentBindingAgentNotRunning,
		mutate: func(registry *coremetadata.Registry, agentUID, _ string) {
			agentBindingTestAgent(registry, agentUID).Status.Phase = coremetadata.PhaseOffline
		},
		expected:     agentBindingLiteral(string(coremetadata.PhaseRunning)),
		observed:     agentBindingLiteral(string(coremetadata.PhaseOffline)),
		agentPresent: true, panePresent: true,
	},
	{
		reason: agentBindingAgentPaneRefChanged,
		mutate: func(registry *coremetadata.Registry, agentUID, _ string) {
			agentBindingTestAgent(registry, agentUID).Status.PaneRef = "pane-elsewhere"
		},
		expected:     func(_, paneUID, _ string) string { return "uid:" + paneUID },
		observed:     agentBindingLiteral("uid:pane-elsewhere"),
		agentPresent: true, panePresent: true,
	},
}

func (c agentBindingReasonCase) sides(agentUID, paneUID, paneID, generation string) (string, string) {
	expected := generation
	if c.expected != nil {
		expected = c.expected(agentUID, paneUID, paneID)
	}
	return expected, c.observed(agentUID, paneUID, paneID)
}

// legacyExactAgentActivationBinding is the predicate the reason set replaced,
// kept verbatim with the consumers' own Agent and generation comparison so the
// verdict can be compared case by case.
func legacyExactAgentActivationBinding(registry coremetadata.Registry, paneUID, runtimeID, wantAgentUID, wantGeneration string) bool {
	pane, ok := registry.Pane(paneUID)
	if !ok || strings.TrimSpace(pane.Status.Activation.Generation) == "" ||
		strings.TrimSpace(pane.Status.Activation.AgentUID) == "" ||
		strings.TrimSpace(pane.Status.Activation.RuntimeID) == "" ||
		pane.Status.Activation.RuntimeID != strings.TrimSpace(runtimeID) {
		return false
	}
	agentUID := pane.Status.Activation.AgentUID
	agent, ok := registry.Agent(agentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != paneUID {
		return false
	}
	return agentUID == wantAgentUID && pane.Status.Activation.Generation == wantGeneration
}

// declaredAgentActivationBindingReasons reads every constant of the reason
// type out of the package source, so a reason added anywhere in the package
// without a case fails the sweep instead of shipping untested.
func declaredAgentActivationBindingReasons(t *testing.T) []agentActivationBindingReason {
	t.Helper()
	const typeName = "agentActivationBindingReason"
	fileSet := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parse package source %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatal("no package source parsed")
	}
	var declared []agentActivationBindingReason
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.GenDecl:
				if node.Tok != token.CONST {
					return true
				}
				// An implicit repetition inherits the type of the spec above it.
				inherited := false
				for _, raw := range node.Specs {
					spec := raw.(*ast.ValueSpec)
					typed := inherited && spec.Type == nil && len(spec.Values) == 0
					if ident, ok := spec.Type.(*ast.Ident); ok && ident.Name == typeName {
						typed = true
					}
					inherited = typed
					if !typed {
						continue
					}
					for i := range spec.Names {
						if i >= len(spec.Values) {
							t.Fatalf("%s constant %s has no literal value", typeName, spec.Names[i].Name)
						}
						literal, ok := spec.Values[i].(*ast.BasicLit)
						if !ok || literal.Kind != token.STRING {
							t.Fatalf("%s constant %s is not a string literal", typeName, spec.Names[i].Name)
						}
						declared = append(declared, agentActivationBindingReason(strings.Trim(literal.Value, "\"`")))
					}
				}
				return false
			case *ast.CallExpr:
				// A reason minted by conversion outside the const block would be
				// outside the closed set.
				if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == typeName {
					t.Fatalf("%s: %s converted outside its closed const set", fileSet.Position(node.Pos()), typeName)
				}
			}
			return true
		})
	}
	return declared
}

func TestAgentActivationBindingReasonSetIsClosed(t *testing.T) {
	t.Parallel()
	declared := declaredAgentActivationBindingReasons(t)
	if len(declared) == 0 {
		t.Fatal("no agentActivationBindingReason constant found in the package source")
	}
	slices.Sort(declared)
	set := slices.Clone(agentActivationBindingReasons)
	slices.Sort(set)
	if !slices.Equal(declared, set) || len(slices.Compact(slices.Clone(set))) != len(set) {
		t.Fatalf("declared reasons %q, agentActivationBindingReasons %q: the slice must declare every constant exactly once", declared, set)
	}
	var covered []agentActivationBindingReason
	for _, test := range agentBindingReasonCases {
		covered = append(covered, test.reason)
	}
	slices.Sort(covered)
	if !slices.Equal(covered, set) {
		t.Fatalf("test cases cover %q, want exactly one case per declared reason %q", covered, set)
	}
	for _, reason := range set {
		if reason == "" || strings.ToLower(string(reason)) != string(reason) || strings.ContainsAny(string(reason), " _") {
			t.Fatalf("reason %q is not a kebab-case name", reason)
		}
	}
}

func TestAgentActivationBindingCheckNamesEveryReasonWithTheLegacyVerdict(t *testing.T) {
	t.Parallel()
	bound := newSessionRefHarness(t, aiModeCodex)
	expect := &agentActivationBindingExpectation{AgentUID: bound.agentUID, Generation: "gen-session-ref"}
	for _, want := range []*agentActivationBindingExpectation{nil, expect} {
		check := checkAgentActivationBinding(*bound.registry, bound.paneUID, " %7 ", want)
		if !check.bound() || check.Reason != "" || check.AgentUID != bound.agentUID ||
			check.Generation != "gen-session-ref" || !check.AgentPresent || !check.PanePresent {
			t.Fatalf("exact binding (expectation %+v) = %+v, want bound with reason \"\"", want, check)
		}
	}
	if !legacyExactAgentActivationBinding(*bound.registry, bound.paneUID, "%7", bound.agentUID, "gen-session-ref") {
		t.Fatal("legacy predicate refuses the bound fixture")
	}

	for _, test := range agentBindingReasonCases {
		t.Run(string(test.reason), func(t *testing.T) {
			t.Parallel()
			h := newSessionRefHarness(t, aiModeCodex)
			test.mutate(h.registry, h.agentUID, h.paneUID)
			check := checkAgentActivationBinding(*h.registry, h.paneUID, "%7",
				&agentActivationBindingExpectation{AgentUID: h.agentUID, Generation: "gen-session-ref"})
			if check.bound() || check.Reason != test.reason {
				t.Fatalf("reason = %q (bound %t), want %q", check.Reason, check.bound(), test.reason)
			}
			wantExpected, wantObserved := test.sides(h.agentUID, h.paneUID, "%7", "gen-session-ref")
			if check.Expected != wantExpected || check.Observed != wantObserved {
				t.Fatalf("sides = (%q, %q), want (%q, %q)", check.Expected, check.Observed, wantExpected, wantObserved)
			}
			if check.AgentUID != h.agentUID || check.AgentPresent != test.agentPresent || check.PanePresent != test.panePresent {
				t.Fatalf("identity/presence = %+v, want Agent %s present=%t Pane present=%t",
					check, h.agentUID, test.agentPresent, test.panePresent)
			}
			if check.Generation != "" {
				t.Fatalf("refused check carries a bound generation: %+v", check)
			}
			if legacyExactAgentActivationBinding(*h.registry, h.paneUID, "%7", h.agentUID, "gen-session-ref") {
				t.Fatal("legacy predicate accepts what the reason check refuses")
			}
		})
	}
}

// agentBindingAssertRefusal checks the parts of a refusal an operator acts on:
// what changed, which resources, whether they still exist, and remediation in
// the safe order.
func agentBindingAssertRefusal(t *testing.T, text string, test agentBindingReasonCase, agentUID, paneUID, paneID, generation string) {
	t.Helper()
	expected, observed := test.sides(agentUID, paneUID, paneID, generation)
	presence := func(present bool) string {
		if present {
			return "present"
		}
		return "absent"
	}
	for _, required := range []string{
		string(test.reason),
		"(expected " + expected + ", observed " + observed + ")",
		"Agent uid:" + agentUID,
		"Pane uid:" + paneUID + " " + paneID,
		"the Agent row was " + presence(test.agentPresent),
		"the Pane row was " + presence(test.panePresent),
		"nothing was rolled back",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("refusal %q is missing %q", text, required)
		}
	}
	reread := strings.Index(text, "projmux describe agent uid:"+agentUID)
	rereadPane := strings.Index(text, "projmux describe pane uid:"+paneUID)
	observe := strings.Index(text, "tmux capture-pane -p -t "+paneID)
	remove := strings.Index(text, "projmux delete agent uid:"+agentUID+" --yes")
	if reread < 0 || rereadPane < 0 || observe < 0 || remove < 0 || !(reread < observe && rereadPane < observe && observe < remove) {
		t.Fatalf("refusal steps out of order (re-read %d/%d, observe %d, delete %d): %q", reread, rereadPane, observe, remove, text)
	}
	if !strings.Contains(text, "only when neither read shows activation evidence") || !strings.HasSuffix(text, "--yes`") {
		t.Fatalf("delete is not the last, conditional step: %q", text)
	}
}

func TestCreateAgentActivationBindingRefusalNamesTheChangedFact(t *testing.T) {
	t.Parallel()
	for _, test := range agentBindingReasonCases {
		t.Run(string(test.reason), func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
			var agentUID, paneUID, generation string
			launcher.activationBeforeReturn = func() {
				// The Registry is mutated in place rather than through a store
				// transaction: a broken binding is exactly what Validate would
				// refuse to commit, and a concurrent writer that got there first
				// is what this refusal reports.
				agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
				agentUID, paneUID = agent.Metadata.UID, agent.Status.PaneRef
				generation = agentBindingTestPane(&store.registry, paneUID).Status.Activation.Generation
				test.mutate(&store.registry, agentUID, paneUID)
			}

			stdout, _, err := runRoute(t, create,
				"agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "review", "--", "initial task")
			if err == nil {
				t.Fatal("refused binding returned ordinary success")
			}
			if stdout != "" {
				t.Fatalf("refused binding wrote success output %q", stdout)
			}
			if len(launcher.activationPanes) != 1 || agentUID == "" || paneUID == "" || generation == "" {
				t.Fatalf("activation probes = %q, agent %q pane %q generation %q", launcher.activationPanes, agentUID, paneUID, generation)
			}
			paneID := launcher.activationPanes[0]
			var refusal *agentActivationBindingError
			if !errors.As(err, &refusal) {
				t.Fatalf("error %T %q is not an agentActivationBindingError", err, err)
			}
			if refusal.check.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", refusal.check.Reason, test.reason)
			}
			// The one generator, fed the verdict recomputed from the Registry the
			// recheck read, reproduces the whole create refusal byte for byte.
			want := agentActivationBindingRefusal("create agent", agentBindingStageBeforeRecording, paneUID, paneID,
				checkAgentActivationBinding(store.registry, paneUID, paneID,
					&agentActivationBindingExpectation{AgentUID: agentUID, Generation: generation}))
			if err.Error() != want {
				t.Fatalf("create refusal =\n%q\nwant the generator's\n%q", err.Error(), want)
			}
			if !strings.HasPrefix(err.Error(), "create agent: activation binding changed before recording activation") {
				t.Fatalf("create refusal lost its command prefix or stage: %q", err)
			}
			agentBindingAssertRefusal(t, err.Error(), test, agentUID, paneUID, paneID, generation)
		})
	}
}

func TestAwaitAgentActivationBindingRefusalUsesTheSameGenerator(t *testing.T) {
	t.Parallel()
	for _, test := range agentBindingReasonCases {
		t.Run(string(test.reason), func(t *testing.T) {
			t.Parallel()
			h := newSessionRefHarness(t, aiModeCodex)
			agent, _ := h.registry.Agent(h.agentUID)
			agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
			runner := &activationAuthorityRunner{paneUID: h.paneUID}
			now := sessionRefObservedAt
			h.cmd.now = func() time.Time { return now }
			sleeps := 0
			h.cmd.sleep = func(d time.Duration) {
				now = now.Add(d)
				sleeps++
				if sleeps == 1 {
					// Break the binding after the loop has read it bound once.
					test.mutate(h.registry, h.agentUID, h.paneUID)
				}
			}

			acknowledged, source, err := h.cmd.AwaitAgentActivation(context.Background(), runner, "%7",
				agentActivationStartupDeadline, agentActivationAcknowledgementDeadline)
			if acknowledged || source != string(coremetadata.InteractionSourceProviderHook) || err == nil {
				t.Fatalf("Await = (%t, %q, %v), want a refused binding", acknowledged, source, err)
			}
			if sleeps != 1 {
				t.Fatalf("refusal came after %d sleeps, want the first read after the change", sleeps)
			}
			var refusal *agentActivationBindingError
			if !errors.As(err, &refusal) || refusal.check.Reason != test.reason || refusal.stage != agentBindingStageWhileAwaiting {
				t.Fatalf("error = %#v, want reason %q while awaiting", err, test.reason)
			}
			want := agentActivationBindingRefusal("", agentBindingStageWhileAwaiting, h.paneUID, "%7",
				checkAgentActivationBinding(*h.registry, h.paneUID, "%7",
					&agentActivationBindingExpectation{AgentUID: h.agentUID, Generation: "gen-session-ref"}))
			if err.Error() != want {
				t.Fatalf("Await refusal =\n%q\nwant the generator's\n%q", err.Error(), want)
			}
			agentBindingAssertRefusal(t, err.Error(), test, h.agentUID, h.paneUID, "%7", "gen-session-ref")
		})
	}
}

func TestAwaitAgentActivationInitialBindingRefusalUsesTheSameGenerator(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		mutate   func(registry *coremetadata.Registry, agentUID, paneUID string)
		reason   agentActivationBindingReason
		agentUID func(h *sessionRefHarness) string
	}{
		{
			name:     "agent row left Running",
			mutate:   agentBindingReasonCases[slices.IndexFunc(agentBindingReasonCases, func(c agentBindingReasonCase) bool { return c.reason == agentBindingAgentNotRunning })].mutate,
			reason:   agentBindingAgentNotRunning,
			agentUID: func(h *sessionRefHarness) string { return h.agentUID },
		},
		{
			// With no expectation and no Pane row there is no Agent to name, so
			// the refusal says so and routes every step through the Pane.
			name:     "pane row gone",
			mutate:   agentBindingReasonCases[0].mutate,
			reason:   agentBindingPaneMissing,
			agentUID: func(*sessionRefHarness) string { return "" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newSessionRefHarness(t, aiModeCodex)
			test.mutate(h.registry, h.agentUID, h.paneUID)
			h.cmd.sleep = func(time.Duration) { t.Fatal("initial refusal waited") }

			_, _, err := h.cmd.AwaitAgentActivation(context.Background(), &activationAuthorityRunner{paneUID: h.paneUID}, "%7",
				agentActivationStartupDeadline, agentActivationAcknowledgementDeadline)
			var refusal *agentActivationBindingError
			if !errors.As(err, &refusal) || refusal.check.Reason != test.reason || refusal.stage != agentBindingStageBeforeAwaiting {
				t.Fatalf("error = %#v, want reason %q before awaiting", err, test.reason)
			}
			check := checkAgentActivationBinding(*h.registry, h.paneUID, "%7", nil)
			if check.AgentUID != test.agentUID(h) {
				t.Fatalf("check names Agent %q, want %q", check.AgentUID, test.agentUID(h))
			}
			if want := agentActivationBindingRefusal("", agentBindingStageBeforeAwaiting, h.paneUID, "%7", check); err.Error() != want {
				t.Fatalf("initial refusal =\n%q\nwant the generator's\n%q", err.Error(), want)
			}
			if test.agentUID(h) == "" {
				for _, required := range []string{"Agent (none bound)", "projmux describe pane uid:" + h.paneUID, "projmux delete pane uid:" + h.paneUID + " --yes"} {
					if !strings.Contains(err.Error(), required) {
						t.Fatalf("refusal %q is missing %q", err, required)
					}
				}
				if strings.Contains(err.Error(), "delete agent") {
					t.Fatalf("refusal suggests deleting an unnamed Agent: %q", err)
				}
			}
		})
	}
}
