package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
)

// creatorBasisSpellings are the argv spellings that create an Agent: every
// `create agent` spelling, a fan-out, and `create window --provider`. The
// caller appends its own --creator.
var creatorBasisSpellings = []struct {
	name      string
	argv      []string
	wantCount int
}{
	{name: "create agent", wantCount: 1, argv: []string{
		"agent", "--provider", "claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id"}},
	{name: "provider shortcut", wantCount: 1, argv: []string{
		"claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id"}},
	{name: "create-window", wantCount: 1, argv: []string{
		"agent", "--provider", "codex", "--interactive-only", "--project", "uid:prj-alpha",
		"--window", "fresh", "--create-window", "-o", "pane-id"}},
	{name: "fan-out over every Window", wantCount: 2, argv: []string{
		"agent", "--provider", "codex", "--interactive-only", "--project", "uid:prj-alpha", "--all-windows", "-o", "pane-id"}},
	{name: "create window --provider", wantCount: 1, argv: []string{
		"window", "--project", "uid:prj-alpha", "--provider", "claude"}},
}

// assertCreatorRecord checks that an Agent and its managed Pane carry exactly
// the want creator keys, and that the creator Agent is never the Agent itself.
func assertCreatorRecord(t *testing.T, agent coremetadata.Agent, pane coremetadata.Pane, want map[string]string) {
	t.Helper()
	if got := creatorKeysOf(agent.Metadata); !maps.Equal(got, want) {
		t.Fatalf("Agent %s creator keys = %v, want %v", agent.Metadata.UID, got, want)
	}
	if got := creatorKeysOf(pane.Metadata); !maps.Equal(got, want) {
		t.Fatalf("Agent Pane %s creator keys = %v, want %v", pane.Metadata.UID, got, want)
	}
	if agent.Metadata.Annotations[coremetadata.AnnotationCreatorAgent] == agent.Metadata.UID {
		t.Fatalf("Agent %s records itself as its creator", agent.Metadata.UID)
	}
}

// outsideAgentPane makes the fixture's command run from a shell that is no
// Agent's Pane: no ambient tmux Pane at all.
func (fx *creatorFixture) outsideAgentPane() {
	delete(fx.env, "TMUX_PANE")
}

func TestExplicitCreatorIsRecordedOnEverySpellingWhenNoPaneChainIs(t *testing.T) {
	t.Parallel()
	for _, test := range creatorBasisSpellings {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fx := newCreatorFixture(t)
			fx.outsideAgentPane()
			before := fx.agentUIDs()
			argv := append(append([]string{}, test.argv...), "--creator", "uid:"+fx.creatorAgent)
			stdout, stderr, err := runRoute(t, fx.command, argv...)
			if err != nil || stderr != "" || stdout == "" {
				t.Fatalf("create = stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}
			agents, panes := fx.newAgentsSince(t, before)
			if len(agents) != test.wantCount {
				t.Fatalf("new Agents = %d, want %d", len(agents), test.wantCount)
			}
			for i := range agents {
				assertCreatorRecord(t, agents[i], panes[i], coremetadata.ExplicitCreatorAnnotations(fx.creatorAgent))
			}
			if got := creatorQueryCount(fx.tmux); got != 0 {
				t.Fatalf("creator tmux queries = %d, want none without an ambient Pane", got)
			}
		})
	}
}

func TestPaneChainWinsOverADeclaredCreatorOnEverySpelling(t *testing.T) {
	t.Parallel()
	for _, test := range creatorBasisSpellings {
		for _, declaredSelf := range []bool{false, true} {
			name := test.name + "/declaration disagrees"
			if declaredSelf {
				name = test.name + "/declaration agrees"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				fx := newCreatorFixture(t)
				declared := fx.creatorAgent
				if !declaredSelf {
					other := fx.seedAgent(t, "other")
					declared = other.Metadata.UID
				}
				before := fx.agentUIDs()
				argv := append(append([]string{}, test.argv...), "--creator", "uid:"+declared)
				stdout, stderr, err := runRoute(t, fx.command, argv...)
				if err != nil || stdout == "" {
					t.Fatalf("create = stdout=%q stderr=%q err=%v", stdout, stderr, err)
				}
				wantStderr := ""
				if !declaredSelf {
					wantStderr = "creator declaration not recorded: " + creatorDeclinedPaneChain + " (--creator uid:" + declared + ")\n"
				}
				if stderr != wantStderr {
					t.Fatalf("stderr = %q, want %q", stderr, wantStderr)
				}
				agents, panes := fx.newAgentsSince(t, before)
				if len(agents) != test.wantCount {
					t.Fatalf("new Agents = %d, want %d", len(agents), test.wantCount)
				}
				for i := range agents {
					assertCreatorRecord(t, agents[i], panes[i], coremetadata.CreatorAnnotations(fx.creatorAgent, fx.creatorPane))
				}
			})
		}
	}
}

// seedAgent creates one more Agent from outside any Pane, with the observation
// seam off, and restores the fixture's ambient Pane afterwards.
func (fx *creatorFixture) seedAgent(t *testing.T, name string) coremetadata.Agent {
	t.Helper()
	pane, hadPane := fx.env["TMUX_PANE"]
	fx.outsideAgentPane()
	stdout, stderr, err := runRoute(t, fx.command,
		"agent", "--provider", "claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "--name", name)
	if err != nil || stderr != "" {
		t.Fatalf("seed Agent %s: stdout=%q stderr=%q err=%v", name, stdout, stderr, err)
	}
	if hadPane {
		fx.env["TMUX_PANE"] = pane
	}
	return agentNamed(t, fx.store, "win-alpha-main", name)
}

func TestInvalidCreatorDeclarationRefusesTheCreateBeforeAnyChange(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		value     string
		argv      []string
		wantError string
	}{
		{name: "no such Agent", value: "uid:agent-missing", wantError: "--creator uid:agent-missing names no Agent in the Registry; nothing was created"},
		{name: "a name, which may be ambiguous", value: "creator", wantError: "--creator must be an exact Agent reference uid:<agent>"},
		{name: "an empty uid", value: "uid:", wantError: "--creator must be an exact Agent reference uid:<agent>"},
		{name: "a Project uid", value: "uid:prj-alpha", wantError: "names no Agent in the Registry"},
		{name: "a Pane uid", value: "uid:<pane>", wantError: "names no Agent in the Registry"},
		{name: "window without an Agent", value: "uid:<agent>", argv: []string{"window", "--project", "uid:prj-alpha"},
			wantError: "--creator applies only to an Agent"},
		{name: "shell window", value: "uid:<agent>", argv: []string{"window", "--project", "uid:prj-alpha", "--provider", "shell"},
			wantError: "--creator applies only to an Agent"},
	} {
		for _, spelling := range creatorBasisSpellings {
			if test.argv != nil && spelling.name != "create agent" {
				continue
			}
			t.Run(test.name+"/"+spelling.name, func(t *testing.T) {
				t.Parallel()
				fx := newCreatorFixture(t)
				value := strings.NewReplacer("<pane>", fx.creatorPane, "<agent>", fx.creatorAgent).Replace(test.value)
				argv := spelling.argv
				if test.argv != nil {
					argv = test.argv
				}
				argv = append(append([]string{}, argv...), "--creator", value)
				registryBefore, runtimeBefore, writes := fx.store.snapshot(), fx.tmux.state(), fx.store.writes
				stdout, stderr, err := runRoute(t, fx.command, argv...)
				if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("create err = %v, want a usage refusal containing %q (stderr=%q)", err, test.wantError, stderr)
				}
				if stdout != "" {
					t.Fatalf("refused create wrote stdout %q", stdout)
				}
				if fx.store.snapshot() != registryBefore || fx.store.writes != writes || fx.tmux.state() != runtimeBefore {
					t.Fatalf("refused create changed the Registry or the runtime")
				}
			})
		}
	}
}

func TestOperatorSeamRecordsTheClientAndObservesNoPaneChain(t *testing.T) {
	t.Parallel()
	for _, test := range creatorBasisSpellings {
		for _, declare := range []bool{false, true} {
			name := test.name
			if declare {
				name += "/with a declaration"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				fx := newCreatorFixture(t)
				if err := fx.command.recordOperatorCreator("client-x"); err != nil {
					t.Fatalf("recordOperatorCreator: %v", err)
				}
				argv := append([]string{}, test.argv...)
				wantStderr := ""
				if declare {
					argv = append(argv, "--creator", "uid:"+fx.creatorAgent)
					wantStderr = "creator declaration not recorded: " + creatorDeclinedOperator + " (--creator uid:" + fx.creatorAgent + ")\n"
				}
				before := fx.agentUIDs()
				stdout, stderr, err := runRoute(t, fx.command, argv...)
				if err != nil || stdout == "" || stderr != wantStderr {
					t.Fatalf("create = stdout=%q stderr=%q err=%v, want stderr %q", stdout, stderr, err, wantStderr)
				}
				agents, panes := fx.newAgentsSince(t, before)
				if len(agents) != test.wantCount {
					t.Fatalf("new Agents = %d, want %d", len(agents), test.wantCount)
				}
				for i := range agents {
					assertCreatorRecord(t, agents[i], panes[i], coremetadata.OperatorCreatorAnnotations("client-x"))
				}
				if got := creatorQueryCount(fx.tmux); got != 0 {
					t.Fatalf("operator create ran %d pane-chain queries, want none", got)
				}
			})
		}
	}
}

func TestOperatorSeamRefusesANameOutsideTheClientRule(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "Web", "2x", "a b", strings.Repeat("a", operatorclient.MaxBytes+1)} {
		command := &createCommand{}
		err := command.recordOperatorCreator(name)
		if !errors.Is(err, operatorclient.ErrInvalid) || command.operatorCreatorClient != "" {
			t.Fatalf("recordOperatorCreator(%q) = %v, client %q; want the %s refusal and no seam",
				name, err, command.operatorCreatorClient, operatorclient.ReasonInvalid)
		}
	}
	if !operatorclient.Valid(uiOperatorClient) {
		t.Fatalf("the UI operator client %q breaks the client name rule", uiOperatorClient)
	}
}

// uiProducerCreatesAgent classifies every canonical create producer. A new
// producer fails the test below until it is classified here, so a UI path that
// creates an Agent cannot join without the creator record being checked.
var uiProducerCreatesAgent = map[canonicalCreateProducer]bool{
	canonicalProducerPaneMenu:       true,
	canonicalProducerSavedDefault:   true,
	canonicalProducerProviderPicker: true,
	canonicalProducerResumePicker:   true,
	canonicalProducerDirectProvider: true,
	canonicalProducerDirectShell:    false,
}

func TestEveryUIProducerThatCreatesAnAgentRecordsTheUIOperator(t *testing.T) {
	t.Parallel()
	for _, producer := range canonicalCreateProducers {
		createsAgent, classified := uiProducerCreatesAgent[producer]
		if !classified {
			t.Fatalf("canonical create producer %q is not classified in uiProducerCreatesAgent", producer)
		}
		if !createsAgent {
			continue
		}
		for _, window := range []bool{false, true} {
			name := string(producer) + "/split"
			if window {
				name = string(producer) + "/new Window"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				fx := newCreatorFixture(t)
				withPopupOrigin(fx.command, fx.tmux, func(key string) string { return fx.env[key] })
				before := fx.agentUIDs()
				answer := agentPaneIntent{producer: producer, provider: aiModeClaude, placement: "right"}
				var stdout, stderr bytes.Buffer
				var err error
				if window {
					_, err = fx.command.createWindowFromIntent(windowCreateIntent{anchorPaneID: fx.creatorID, answer: answer}, &stdout, &stderr)
				} else {
					answer.anchorPaneID = fx.creatorID
					_, err = fx.command.createFromIntent(answer, &stdout, &stderr)
				}
				if err != nil {
					t.Fatalf("intent create: %v (stderr=%q)", err, stderr.String())
				}
				agents, panes := fx.newAgentsSince(t, before)
				if len(agents) != 1 {
					t.Fatalf("intent created %d Agents, want 1", len(agents))
				}
				assertCreatorRecord(t, agents[0], panes[0], coremetadata.OperatorCreatorAnnotations(uiOperatorClient))
				if got := creatorQueryCount(fx.tmux); got != 0 {
					t.Fatalf("intent create ran %d pane-chain queries, want none", got)
				}
			})
		}
	}
}

func TestCreatorRecordNeverNamesTheCreatedAgent(t *testing.T) {
	t.Parallel()
	for _, record := range []creatorRecord{
		{basis: coremetadata.CreatorBasisPaneChain, agentUID: "agent-self", paneUID: "pane-other"},
		{basis: coremetadata.CreatorBasisExplicit, agentUID: "agent-self"},
	} {
		working := coremetadata.NewRegistry()
		working.Agents = append(working.Agents, coremetadata.Agent{Metadata: coremetadata.ObjectMeta{
			UID: "agent-self", Annotations: record.withAnnotations(map[string]string{"keep": "1"}),
		}})
		working.Panes = append(working.Panes, coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: "pane-self"}})
		got := record.forAgent(&working, "agent-self")
		if got.basis != "" || got.annotations() != nil {
			t.Fatalf("forAgent(%s) kept record %+v naming the created Agent", record.basis, got)
		}
		stored, _ := working.Agent("agent-self")
		if keys := creatorKeysOf(stored.Metadata); len(keys) != 0 || stored.Metadata.Annotations["keep"] != "1" {
			t.Fatalf("stored Agent annotations = %v, want the creator keys removed and the rest kept", stored.Metadata.Annotations)
		}
		pane, _ := working.Pane("pane-self")
		if keys := creatorKeysOf(got.annotatePane(&working, *pane).Metadata); len(keys) != 0 {
			t.Fatalf("Pane of a self-named record carries creator keys %v", keys)
		}
		if other := record.forAgent(&working, "agent-other"); other.basis != record.basis {
			t.Fatalf("forAgent on another Agent dropped the record: %+v", other)
		}
	}
}

func TestDecideCreatorPriorityTable(t *testing.T) {
	t.Parallel()
	fx := newCreatorFixture(t)
	// Seeding runs a create, which binds the route the pane-chain query uses.
	other := fx.seedAgent(t, "other").Metadata.UID
	for _, test := range []struct {
		name         string
		ambient      bool
		operator     string
		declared     string
		wantBasis    string
		wantAgent    string
		wantClient   string
		wantDeclined string
	}{
		{name: "nothing", wantBasis: ""},
		{name: "pane chain", ambient: true, wantBasis: coremetadata.CreatorBasisPaneChain, wantAgent: fx.creatorAgent},
		{name: "declaration alone", declared: other, wantBasis: coremetadata.CreatorBasisExplicit, wantAgent: other},
		{name: "pane chain over a declaration", ambient: true, declared: other,
			wantBasis: coremetadata.CreatorBasisPaneChain, wantAgent: fx.creatorAgent, wantDeclined: creatorDeclinedPaneChain},
		{name: "pane chain agreeing with a declaration", ambient: true, declared: fx.creatorAgent,
			wantBasis: coremetadata.CreatorBasisPaneChain, wantAgent: fx.creatorAgent},
		{name: "operator over a pane chain", ambient: true, operator: "client-x",
			wantBasis: coremetadata.CreatorBasisOperator, wantClient: "client-x"},
		{name: "operator over a declaration", operator: "client-x", declared: other,
			wantBasis: coremetadata.CreatorBasisOperator, wantClient: "client-x", wantDeclined: creatorDeclinedOperator},
	} {
		command := *fx.command
		command.operatorCreatorClient = test.operator
		env := maps.Clone(fx.env)
		if !test.ambient {
			delete(env, "TMUX_PANE")
		}
		command.lookupEnv = func(key string) string { return env[key] }
		working := fx.store.registry.Clone()
		got, err := command.decideCreator(context.Background(), "create agent", &working, test.declared)
		if err != nil {
			t.Fatalf("%s: decideCreator: %v", test.name, err)
		}
		if got.basis != test.wantBasis || got.agentUID != test.wantAgent || got.client != test.wantClient || got.declined != test.wantDeclined {
			t.Fatalf("%s: decideCreator = %+v, want basis %q agent %q client %q declined %q",
				test.name, got, test.wantBasis, test.wantAgent, test.wantClient, test.wantDeclined)
		}
	}
}

// withUICreator is want plus the creator record every UI Agent create writes,
// for tests that assert a UI Agent's exact annotation set.
func withUICreator(want map[string]string) map[string]string {
	out := maps.Clone(want)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, coremetadata.OperatorCreatorAnnotations(uiOperatorClient))
	return out
}
