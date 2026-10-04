package metadata

import (
	"reflect"
	"testing"
)

func TestProcessPaneCurrentActivationSeparatesHistory(t *testing.T) {
	reg := processSchemaFixture(t)
	pane := reg.Panes[1]
	binding := pane.Status.Activation.Process.Binding
	before := reg.Clone()
	activation, provider, ok := reg.CurrentProcessActivation(binding)
	if !ok || provider != "codex" || activation != *pane.Status.Activation.Process {
		t.Fatalf("current activation: %+v %q %v", activation, provider, ok)
	}
	activation.Binding.HostInstanceID = "changed-copy"
	if !reflect.DeepEqual(reg, before) {
		t.Fatal("read-only activation aliases Registry")
	}
	retired := pane.Status.ProcessSession.History.Binding
	if _, _, ok := reg.CurrentProcessActivation(retired); ok {
		t.Fatal("retired generation became current authority")
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("historical session validation changed: %v", err)
	}
}

func TestProcessPaneCurrentActivationRejectsBrokenOwnership(t *testing.T) {
	base := processSchemaFixture(t)
	binding := base.Panes[1].Status.Activation.Process.Binding
	cases := map[string]func(*Registry){
		"tmux":               func(r *Registry) { r.Panes[1].Spec.Runtime.Kind = RuntimeTmux },
		"missing activation": func(r *Registry) { r.Panes[1].Status.Activation.Process = nil },
		"generation":         func(r *Registry) { r.Panes[1].Status.Activation.Generation = "replacement" },
		"operation":          func(r *Registry) { r.Panes[1].Status.Activation.OperationID = "replacement" },
		"pane owner kind":    func(r *Registry) { r.Panes[1].Metadata.OwnerRef.Kind = KindWindow },
		"agent owner kind":   func(r *Registry) { r.Agents[0].Metadata.OwnerRef.Kind = KindProject },
		"pane backref":       func(r *Registry) { r.Agents[0].Status.PaneRef = "replacement" },
		"provider":           func(r *Registry) { r.Agents[0].Spec.Provider = "unknown" },
		"host birth":         func(r *Registry) { r.Panes[1].Status.Activation.Process.HostProcess.Start = "" },
		"child birth":        func(r *Registry) { r.Panes[1].Status.Activation.Process.Child.PID = 0 },
		"foreign project":    func(r *Registry) { r.Windows[0].Metadata.OwnerRef.UID = "foreign" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			reg := base.Clone()
			change(&reg)
			before := reg.Clone()
			if _, _, ok := reg.CurrentProcessActivation(binding); ok {
				t.Fatal("broken current binding accepted")
			}
			if !reflect.DeepEqual(reg, before) {
				t.Fatal("failed read mutated Registry")
			}
		})
	}
}
