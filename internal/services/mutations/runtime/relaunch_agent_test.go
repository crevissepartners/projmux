package runtime

import (
	"errors"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestAgentRestartRunFailureEvidenceAndNoReplay(t *testing.T) {
	for _, tc := range []struct {
		name                                               string
		running, confirm, busy, stopFailure, resumeFailure bool
		live                                               AgentRestartLiveness
		want                                               []string
		wantErr                                            bool
	}{
		{name: "busy", running: true, busy: true, wantErr: true},
		{name: "confirmed", running: true, busy: true, confirm: true, want: []string{"transport", "preflight", "stop", "resume", "pane"}},
		{name: "alive", running: true, stopFailure: true, live: AgentRestartAlive, want: []string{"transport", "preflight", "stop", "observe", "stop-failed"}, wantErr: true},
		{name: "unknown", running: true, stopFailure: true, live: AgentRestartUnknown, want: []string{"transport", "preflight", "stop", "observe", "stop-failed"}, wantErr: true},
		{name: "closed", running: true, stopFailure: true, live: AgentRestartClosed, want: []string{"transport", "preflight", "stop", "observe", "stop-failed", "resume", "pane"}},
		{name: "resume-failed", running: true, resumeFailure: true, want: []string{"transport", "preflight", "stop", "resume", "resume-failed"}, wantErr: true},
		{name: "offline", want: []string{"preflight", "resume", "pane"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("fixture failure")
			var calls []string
			p := AgentRestartPlan{Running: tc.running, PaneUID: "old", PaneName: "named", Interaction: metadata.InteractionIdle}
			if tc.busy {
				p.Interaction = metadata.InteractionUnknown
			}
			_, err := p.Run(AgentRestartExecution{Confirm: tc.confirm, Tokens: AgentRestartTokens{Busy: "busy"}, Refuse: func(string, string) error { return cause },
				ValidateTransport: func() error { calls = append(calls, "transport"); return nil }, BeforeStop: func() error { calls = append(calls, "preflight"); return nil },
				Stop: func() error {
					calls = append(calls, "stop")
					if tc.stopFailure {
						return cause
					}
					return nil
				},
				Observe: func() (AgentRestartLiveness, error) {
					calls = append(calls, "observe")
					return tc.live, errors.New("observe")
				},
				StopFailed: func(err error, live AgentRestartLiveness, _ error) error {
					calls = append(calls, "stop-failed")
					if !errors.Is(err, cause) {
						t.Fatal(err)
					}
					if live == AgentRestartClosed {
						return nil
					}
					return err
				},
				Resume: func(stopped StoppedAgentPane) error {
					calls = append(calls, "resume")
					if stopped.UID != "old" || stopped.Name != "named" {
						t.Fatal(stopped)
					}
					if tc.resumeFailure {
						return cause
					}
					return nil
				},
				ResumeFailed: func(err error) error { calls = append(calls, "resume-failed"); return err }, NewPane: func() string { calls = append(calls, "pane"); return "new" },
			})
			if (err != nil) != tc.wantErr || tc.wantErr && !errors.Is(err, cause) || !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("calls=%v want=%v err=%v", calls, tc.want, err)
			}
		})
	}
}

func TestAgentRelaunchDispositionPreservesModelAndLayerSemantics(t *testing.T) {
	p := AgentRestartPlan{Running: true}
	for _, tc := range []struct {
		q            AgentSettingsRequest
		reasons      []string
		running, dry bool
		want         string
	}{
		{q: AgentSettingsRequest{Effort: "high"}, running: true, want: "unchanged"},
		{q: AgentSettingsRequest{Model: "same"}, running: true, want: "restarted"},
		{q: AgentSettingsRequest{Model: "same"}, running: true, dry: true, want: "would-restart"},
		{q: AgentSettingsRequest{}, running: false, dry: true, want: "would-resume"},
		{q: AgentSettingsRequest{}, running: false, want: "resumed"},
		{reasons: []string{"profile-changed"}, running: true, dry: true, want: "would-restart"},
	} {
		p.Running = tc.running
		p.Settings.Resolution.Reasons = tc.reasons
		if got := p.RelaunchDisposition(tc.q, tc.dry); got != tc.want {
			t.Fatalf("%+v got %s", tc, got)
		}
	}
}

func TestAgentRestartCodexPreflightBeforeSelfAndNoSnapshots(t *testing.T) {
	cause := errors.New("invalid stored route")
	var calls []string
	p := AgentRestartPlan{Provider: "codex", Running: true}
	err := p.Plan(AgentSettingsRequest{}, AgentRestartPlanning{
		Refuse: func(_, s string) error { return errors.New(s) }, Tokens: AgentRestartTokens{NoConversation: "no-conversation"},
		Predict: func(string) (AgentResumePlan, error) { calls = append(calls, "predict"); return AgentResumePlan{}, nil },
		ResolveSettings: func(AgentResumePlan, AgentSettingsRequest) (AgentSettingsLaunch, error) {
			calls = append(calls, "settings")
			return AgentSettingsLaunch{Snapshot: func() error { t.Fatal("snapshot before execution"); return nil }}, nil
		},
		PreflightCodex: func(AgentResumePlan, AgentSettingsLaunch) error { calls = append(calls, "native"); return cause },
		SelfTarget:     func() error { t.Fatal("self observation after preflight refusal"); return nil },
	})
	if !errors.Is(err, cause) || !reflect.DeepEqual(calls, []string{"predict", "settings", "native"}) {
		t.Fatalf("%v %v", calls, err)
	}
}
