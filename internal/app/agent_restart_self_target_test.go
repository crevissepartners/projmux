package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// selfTargetPanePID is the process the fixture server reports for the ambient
// Pane: the process a caller must descend from to be inside that Pane.
const selfTargetPanePID = 7001

// selfTargetAmbientPane is the tmux id the self target cases give the fixture
// Agent's managed Pane and the caller's environment.
const selfTargetAmbientPane = "%77"

// selfTargetProbe is the two observations of the self target judgment: the
// caller's parent chain, and the server's answer to the one read of the
// ambient Pane. It starts as a caller outside every Pane, on a server that
// mirrors paneUID at whatever `%N` it is asked about.
type selfTargetProbe struct {
	calls    [][]string
	walks    int
	paneUID  string
	panePID  string
	err      error
	chain    []int
	chainErr error
}

func newSelfTargetProbe(paneUID string) *selfTargetProbe {
	return &selfTargetProbe{paneUID: paneUID, panePID: "7001", chain: []int{90001, 4242}}
}

func (p *selfTargetProbe) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	p.calls = append(p.calls, append([]string{name}, args...))
	if p.err != nil {
		return nil, p.err
	}
	paneID := args[slices.Index(args, "-t")+1]
	return []byte(strings.Join([]string{paneID, p.panePID, p.paneUID}, tmuxRowSep) + "\n"), nil
}

func (p *selfTargetProbe) ancestors() ([]int, error) {
	p.walks++
	return p.chain, p.chainErr
}

// inside makes the caller a descendant of the ambient Pane's process.
func (p *selfTargetProbe) inside() { p.chain = []int{90001, selfTargetPanePID, 4242} }

// restartSelfTargetCommands are the restarting commands that share the self
// target judgment, each as a dry run so a pass changes nothing either.
var restartSelfTargetCommands = []struct {
	name  string
	token string
	args  []string
}{
	{name: "relaunch", token: relaunchReasonSelfTarget, args: []string{"relaunch", "uid:" + personaAttachAgent, "--effort", "max", "--dry-run"}},
	{name: "instructions attach", token: personaReasonSelfTarget, args: []string{"instructions", "attach", "uid:" + personaAttachAgent, "go-reviewer", "--dry-run"}},
	{name: "persona detach", token: personaReasonSelfTarget, args: []string{"persona", "detach", "uid:" + personaAttachAgent, "--dry-run"}},
}

// selfTargetAmbient is how a caller's environment names the fixture Agent's
// managed Pane, and the flags that go with it.
type selfTargetAmbient struct {
	name  string
	flags []string
	// route is the tmux route the one read must go through.
	route   []string
	arrange func(*personaAttachFixture)
}

// selfTargetAmbients are the two ways a caller carries an Agent Pane's id. The
// anchor-only shape is a process that dropped TMUX and TMUX_PANE but kept the
// private producer anchor and names the server with --socket, which is what a
// server started from an Agent's shell does. The other is a process that
// inherited the whole tmux environment.
var selfTargetAmbients = []selfTargetAmbient{
	{name: "anchor variable with --socket", flags: []string{"--socket", "projmux"}, route: []string{"-L", "projmux"},
		arrange: func(f *personaAttachFixture) {
			delete(f.env, "TMUX")
			f.env[runtimeMutationAnchorPaneEnv] = selfTargetAmbientPane
		}},
	{name: "inherited TMUX and TMUX_PANE", route: []string{"-S", testDeleteTarget.Value},
		arrange: func(f *personaAttachFixture) { f.env["TMUX_PANE"] = selfTargetAmbientPane }},
}

// newSelfTargetFixture is the restart fixture with its Agent's managed Pane
// live at selfTargetAmbientPane and the instructions the attach case names.
func newSelfTargetFixture(t *testing.T) *personaAttachFixture {
	t.Helper()
	f := newRelaunchFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	pane, _ := f.store.registry.Pane(personaAttachPane)
	pane.Status.Activation = selfTargetActivation(personaAttachAgent, selfTargetAmbientPane)
	return f
}

// selfTargetActivation is a complete activation of agentUID's managed Pane at
// the tmux id paneID.
func selfTargetActivation(agentUID, paneID string) coremetadata.PaneActivation {
	return coremetadata.PaneActivation{
		Generation: "gen-" + agentUID, AgentUID: agentUID, OperationID: "op-" + agentUID,
		StartedAt: resourceFixtureClock, RuntimeID: paneID,
	}
}

// addSelfTargetOtherAgent adds a second Running Agent whose managed Pane is
// live at paneID, in the fixture Agent's Window.
func addSelfTargetOtherAgent(t *testing.T, store *fakeResourceStore, paneID string) {
	t.Helper()
	registry := &store.registry
	registry.Agents = append(registry.Agents, coremetadata.Agent{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{UID: "agt-alpha-claude", Name: "claude", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"}},
		Spec: coremetadata.AgentSpec{Provider: "claude"},
		Status: coremetadata.AgentStatus{Phase: coremetadata.PhaseRunning, PaneRef: "pan-alpha-claude",
			LastTransitionAt: resourceFixtureClock},
	})
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: "pan-alpha-claude", Name: "claude-pane", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agt-alpha-claude"}},
		Spec:   coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent, CWD: "/srv/alpha"},
		Status: coremetadata.PaneStatus{Activation: selfTargetActivation("agt-alpha-claude", paneID)},
	})
	registry.NameReservations = append(registry.NameReservations,
		coremetadata.NameReservation{Scope: "prj-alpha", Kind: coremetadata.KindAgent, Name: "claude", UID: "agt-alpha-claude"},
		coremetadata.NameReservation{Scope: "prj-alpha", Kind: coremetadata.KindPane, Name: "claude-pane", UID: "pan-alpha-claude"},
	)
	*registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("self target fixture is invalid: %v", err)
	}
}

// selfTargetRefusals are the two wordings of the one self target token.
const (
	selfTargetInsideWording     = "owns the Pane this command runs in;"
	selfTargetUnobservedWording = "whether the command runs inside that Pane could not be determined: "
)

// TestRestartOutsideTheAgentPaneIsNotASelfTarget is acceptance 1 and 2: a
// caller whose environment names the Agent's managed Pane, but whose parent
// chain does not hold that Pane's process, is not refused.
func TestRestartOutsideTheAgentPaneIsNotASelfTarget(t *testing.T) {
	for _, command := range restartSelfTargetCommands {
		for _, ambient := range selfTargetAmbients {
			t.Run(command.name+"/"+ambient.name, func(t *testing.T) {
				f := newSelfTargetFixture(t)
				ambient.arrange(f)
				before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
				stdout, stderr, err := runRoute(t, f.command, append(slices.Clone(command.args), ambient.flags...)...)
				if err != nil {
					t.Fatalf("%s from outside the Pane: err=%v stdout=%q stderr=%q, want no refusal", command.name, err, stdout, stderr)
				}
				if stdout == "" {
					t.Fatalf("%s dry run printed nothing", command.name)
				}
				// The Pane's process was read once, on the server the
				// command addresses, and the parent chain was walked once.
				if len(f.self.calls) != 1 || f.self.walks != 1 {
					t.Fatalf("tmux reads=%d walks=%d, want one of each", len(f.self.calls), f.self.walks)
				}
				call := f.self.calls[0]
				if got := call[1 : 1+len(ambient.route)]; !slices.Equal(got, ambient.route) {
					t.Fatalf("self target read %q, want it routed through %q", call, ambient.route)
				}
				if target := call[slices.Index(call, "-t")+1]; target != selfTargetAmbientPane {
					t.Fatalf("self target read %q, want it to ask about %s", call, selfTargetAmbientPane)
				}
				f.assertNothingChanged(t, before, beforeAnnotations)
			})
		}
	}
}

// TestRestartInsideTheAgentPaneIsRefusedAsASelfTarget is acceptance 3: a
// caller that descends from the Pane's process is refused with the command's
// own self target token, whichever variable names the Pane, and nothing is
// changed.
func TestRestartInsideTheAgentPaneIsRefusedAsASelfTarget(t *testing.T) {
	for _, command := range restartSelfTargetCommands {
		for _, ambient := range selfTargetAmbients {
			t.Run(command.name+"/"+ambient.name, func(t *testing.T) {
				f := newSelfTargetFixture(t)
				ambient.arrange(f)
				f.self.inside()
				before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
				stdout, _, err := runRoute(t, f.command, append(slices.Clone(command.args), ambient.flags...)...)
				assertSelfTargetRefusal(t, err, command.token, selfTargetInsideWording)
				if stdout != "" {
					t.Fatalf("refused %s printed %q", command.name, stdout)
				}
				assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
			})
		}
	}
}

// assertSelfTargetRefusal checks a usage refusal that carries token exactly
// once, in wording, and ends with `nothing was changed`.
func assertSelfTargetRefusal(t *testing.T, err error, token, wording string) {
	t.Helper()
	if err == nil || !IsUsageError(err) {
		t.Fatalf("err = %v, want a usage refusal", err)
	}
	text := err.Error()
	if strings.Count(text, "("+token+")") != 1 || !strings.Contains(text, wording) || !strings.HasSuffix(text, "; nothing was changed") {
		t.Fatalf("refusal = %q, want (%s) once, %q, and `nothing was changed`", text, token, wording)
	}
	other := selfTargetInsideWording
	if wording == selfTargetInsideWording {
		other = selfTargetUnobservedWording
	}
	if strings.Contains(text, other) {
		t.Fatalf("refusal = %q, want it not to say %q", text, other)
	}
}

// TestRestartWithAnUnobservedSelfTargetIsRefusedAndSaysSo is acceptance 4:
// when the environment names the Agent's managed Pane and the judgment cannot
// be finished, the restart is refused with the same token and the refusal
// says the judgment could not be made and why.
func TestRestartWithAnUnobservedSelfTargetIsRefusedAndSaysSo(t *testing.T) {
	anchorOnly, inherited := selfTargetAmbients[0], selfTargetAmbients[1]
	for _, test := range []struct {
		name      string
		ambient   selfTargetAmbient
		noFlags   bool
		arrange   func(*personaAttachFixture)
		wantCause string
		wantReads int
		wantWalks int
	}{
		// The anchor alone names no server: outside tmux and without --socket
		// there is nothing to ask.
		{name: "no server to ask", ambient: anchorOnly, noFlags: true,
			wantCause: "no tmux server is named by --socket, --socket-path, or $TMUX"},
		{name: "the read fails", ambient: anchorOnly, wantReads: 1,
			arrange:   func(f *personaAttachFixture) { f.self.err = errors.New("no server running") },
			wantCause: "that Pane could not be read on the tmux server this command addresses"},
		{name: "the Pane has no process id", ambient: inherited, wantReads: 1,
			arrange:   func(f *personaAttachFixture) { f.self.panePID = "" },
			wantCause: "that Pane could not be read on the tmux server this command addresses"},
		{name: "the server mirrors another Pane there", ambient: inherited, wantReads: 1,
			arrange:   func(f *personaAttachFixture) { f.self.paneUID = "pan-elsewhere" },
			wantCause: "the tmux server this command addresses does not hold that Pane"},
		{name: "the parent chain cannot be read", ambient: anchorOnly, wantReads: 1, wantWalks: 1,
			arrange: func(f *personaAttachFixture) {
				f.self.chain, f.self.chainErr = []int{90001}, errors.New("process 90001 vanished")
			},
			wantCause: "this command's parent processes could not be read"},
		{name: "the parent chain is empty", ambient: inherited, wantReads: 1, wantWalks: 1,
			arrange:   func(f *personaAttachFixture) { f.self.chain = nil },
			wantCause: "this command's parent processes could not be read"},
	} {
		for _, command := range restartSelfTargetCommands {
			t.Run(test.name+"/"+command.name, func(t *testing.T) {
				f := newSelfTargetFixture(t)
				test.ambient.arrange(f)
				if test.arrange != nil {
					test.arrange(f)
				}
				args := slices.Clone(command.args)
				if !test.noFlags {
					args = append(args, test.ambient.flags...)
				}
				before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
				stdout, _, err := runRoute(t, f.command, args...)
				assertSelfTargetRefusal(t, err, command.token, selfTargetUnobservedWording+test.wantCause+";")
				if stdout != "" {
					t.Fatalf("refused %s printed %q", command.name, stdout)
				}
				if len(f.self.calls) != test.wantReads || f.self.walks != test.wantWalks {
					t.Fatalf("tmux reads=%d walks=%d, want %d and %d", len(f.self.calls), f.self.walks, test.wantReads, test.wantWalks)
				}
				assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
			})
		}
	}
}

// TestRestartFromAnotherPaneOrNoPaneObservesNothing is acceptance 5: with no
// ambient Pane, or one that is not the Agent's managed Pane, nothing is a self
// target, and neither the server nor the parent chain is read.
func TestRestartFromAnotherPaneOrNoPaneObservesNothing(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(*testing.T, *personaAttachFixture)
	}{
		{name: "no ambient pane", arrange: func(*testing.T, *personaAttachFixture) {}},
		{name: "another agent's pane", arrange: func(t *testing.T, f *personaAttachFixture) {
			addSelfTargetOtherAgent(t, f.store, "%88")
			f.env["TMUX_PANE"] = "%88"
		}},
		// The anchor variable is read before TMUX_PANE, as it always was.
		{name: "the anchor names another agent's pane", arrange: func(t *testing.T, f *personaAttachFixture) {
			addSelfTargetOtherAgent(t, f.store, "%88")
			f.env[runtimeMutationAnchorPaneEnv], f.env["TMUX_PANE"] = "%88", selfTargetAmbientPane
		}},
		{name: "a pane the registry does not hold", arrange: func(_ *testing.T, f *personaAttachFixture) {
			f.env["TMUX_PANE"] = "%99"
		}},
		{name: "a malformed pane id", arrange: func(_ *testing.T, f *personaAttachFixture) {
			f.env["TMUX_PANE"] = "77"
		}},
	} {
		for _, command := range restartSelfTargetCommands {
			t.Run(test.name+"/"+command.name, func(t *testing.T) {
				f := newSelfTargetFixture(t)
				// A caller inside some Pane: only the ambient Pane decides.
				f.self.inside()
				test.arrange(t, f)
				if _, stderr, err := runRoute(t, f.command, command.args...); err != nil {
					t.Fatalf("%s: err=%v stderr=%q, want no refusal", command.name, err, stderr)
				}
				if len(f.self.calls) != 0 || f.self.walks != 0 {
					t.Fatalf("tmux reads=%d walks=%d, want none", len(f.self.calls), f.self.walks)
				}
			})
		}
	}
}

// TestRestartAnchorConfirmNeedsNoInheritedTmux pins the server confirmation of
// a restart: it reads the Pane on the route it is given, with or without
// $TMUX, and confirms only the exact `%N` mirroring the expected Pane uid.
func TestRestartAnchorConfirmNeedsNoInheritedTmux(t *testing.T) {
	named := tmuxTransport{Kind: tmuxSocketName, Value: "projmux", Source: tmuxSocketNameSource}
	for _, test := range []struct {
		name      string
		route     tmuxTransport
		nilRunner bool
		rewrite   func(*selfTargetProbe)
		answer    string
		wantSkip  string
		wantCalls int
	}{
		{name: "named socket", route: named, wantCalls: 1},
		{name: "socket path", route: testDeleteTarget, wantCalls: 1},
		{name: "no route", wantSkip: creatorSkipServerUnproven},
		{name: "no runner", route: named, nilRunner: true, wantSkip: creatorSkipServerUnproven},
		{name: "query failed", route: named, wantSkip: creatorSkipAnchorQueryFailed, wantCalls: 1,
			rewrite: func(p *selfTargetProbe) { p.err = errors.New("no server") }},
		{name: "other pane uid", route: named, wantSkip: creatorSkipAnchorPaneMismatch, wantCalls: 1,
			rewrite: func(p *selfTargetProbe) { p.paneUID = "pan-other" }},
		{name: "unparsable pid", route: named, wantSkip: creatorSkipAnchorQueryFailed, wantCalls: 1,
			rewrite: func(p *selfTargetProbe) { p.panePID = "x" }},
		{name: "init pid", route: named, wantSkip: creatorSkipAnchorQueryFailed, wantCalls: 1,
			rewrite: func(p *selfTargetProbe) { p.panePID = "1" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := newSelfTargetProbe(personaAttachPane)
			if test.rewrite != nil {
				test.rewrite(probe)
			}
			var runner tmuxCommandRunner = probe
			if test.nilRunner {
				runner = nil
			}
			pid, skip := restartAnchorConfirm(runner, test.route)(context.Background(), selfTargetAmbientPane, personaAttachPane)
			if skip != test.wantSkip || len(probe.calls) != test.wantCalls {
				t.Fatalf("skip=%q calls=%d, want %q/%d", skip, len(probe.calls), test.wantSkip, test.wantCalls)
			}
			if skip == "" && pid != selfTargetPanePID {
				t.Fatalf("pid = %d, want %d", pid, selfTargetPanePID)
			}
		})
	}
}

// A Pane the server no longer has answers the read with blank fields, which
// is not the Pane asked about.
func TestRestartAnchorConfirmRejectsABlankAnswer(t *testing.T) {
	runner := blankSelfTargetRunner{}
	route := tmuxTransport{Kind: tmuxSocketName, Value: "projmux", Source: tmuxSocketNameSource}
	if _, skip := restartAnchorConfirm(runner, route)(context.Background(), selfTargetAmbientPane, personaAttachPane); skip != creatorSkipAnchorPaneMismatch {
		t.Fatalf("skip = %q, want %q", skip, creatorSkipAnchorPaneMismatch)
	}
}

type blankSelfTargetRunner struct{}

func (blankSelfTargetRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(tmuxRowSep + tmuxRowSep + "\n"), nil
}
