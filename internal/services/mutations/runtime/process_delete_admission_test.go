package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

func TestProcessDeleteStopAdmissionExactPane(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      m.Kind
		protected bool
		negative  string
		starting  bool
	}{
		{"pane-unprotected", m.KindPane, false, "", false},
		{"pane-protected-agent-retained", m.KindPane, true, "", false},
		{"agent-unprotected", m.KindAgent, false, "", false},
		{"agent-protected", m.KindAgent, true, "protected", false},
		{"other-pane", m.KindPane, false, "other-pane", false},
		{"signature-mismatch", m.KindPane, false, "signature", false},
		{"generation-mismatch", m.KindPane, false, "generation", false},
		{"last-virtual-pane-protected-cascade", m.KindPane, true, "protected-cascade", false},
		{"starting-pane-reservation", m.KindPane, false, "", true},
		{"starting-authority-mismatch", m.KindPane, false, "generation", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, _, target := deleteProcessFixture(t)
			if tc.starting {
				pane, _ := reg.Pane(target.Pane)
				pane.Status.Activation = m.PaneActivation{}
			}
			// Keep a sibling to distinguish a leaf Pane from a final virtual
			// Pane, whose plan also removes its Window and Agent.
			siblingUID := ""
			if tc.negative != "protected-cascade" {
				sibling, err := store.DefaultMutator().AddPane(&reg, target.Window, m.BootstrapPane{Name: "retained-shell"}, "", "retain-sibling")
				if err != nil {
					t.Fatal(err)
				}
				siblingUID = sibling.Metadata.UID
			}
			agent, ok := reg.Agent(target.Agent)
			if !ok {
				t.Fatal("FIXTURE: missing Agent")
			}
			if tc.protected {
				agent.Metadata.Labels = map[string]string{m.LabelProtected: "true"}
			}
			uid := target.Agent
			if tc.kind == m.KindPane {
				uid = target.Pane
			}
			if tc.negative == "other-pane" {
				uid = siblingUID
			}
			plan := mutations.BuildDeletePlan(reg, tc.kind, selector.Resolution{Kind: tc.kind, Matches: []selector.Match{{Kind: tc.kind, UID: uid}}})
			plan.ExactUID = true
			if len(plan.Targets) != 1 {
				t.Fatalf("FIXTURE: targets=%d", len(plan.Targets))
			}
			if tc.kind == m.KindPane && tc.negative != "protected-cascade" {
				if plan.Cascades() != 0 {
					t.Fatalf("FIXTURE: leaf Pane cascades=%d", plan.Cascades())
				}
				for _, d := range plan.Targets[0].Descendants {
					if d.Kind == m.KindAgent {
						t.Fatal("FIXTURE: Pane removes Agent")
					}
				}
			}
			if tc.negative == "protected-cascade" {
				removesAgent := false
				for _, d := range plan.Targets[0].Descendants {
					removesAgent = removesAgent || (d.Kind == m.KindAgent && d.UID == target.Agent)
				}
				if !removesAgent {
					t.Fatal("final virtual Pane plan must remove its Agent")
				}
			}
			protection := mutations.ValidateDeleteProtection(reg, plan)
			if tc.negative == "protected" || tc.negative == "protected-cascade" {
				var f *mutations.Failure
				if !errors.As(protection, &f) || f.Code != mutations.CodeAgentProtected {
					t.Fatalf("FIXTURE protection: %v", protection)
				}
			} else if protection != nil {
				t.Fatalf("FIXTURE unexpected protection: %v", protection)
			}
			path := filepath.Join(t.TempDir(), "metadata", "registry.json")
			st := store.NewStore(path)
			if _, err := st.Update(func(dst *m.Registry) error { *dst = reg.Clone(); return nil }); err != nil {
				t.Fatalf("FIXTURE persist: %v", err)
			}
			if err := st.WithAdmissionBarrier(func(current m.Registry) error {
				if _, _, ok := current.CurrentProcessActivation(target.Binding); ok == tc.starting {
					return errors.New("FIXTURE activation does not match starting state")
				}
				return current.Validate()
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("FIXTURE read: %v", err)
			}
			admission := ProcessDeleteAdmission{Kind: tc.kind, UIDs: []string{uid}, Signature: plan.Signature()}
			binding := processHostBinding(target.Binding)
			if tc.negative == "signature" {
				admission.Signature += "-wrong"
			}
			if tc.negative == "generation" {
				binding.Generation += "-wrong"
			}
			applied := 0
			err = AdmitProcessDeleteStop(path, "delete-stop", mustProcessDeleteAdmission(admission), processhost.Authority{Binding: binding}, func() error { applied++; return nil })
			var failure *mutations.Failure
			protected := errors.As(err, &failure) && failure.Code == mutations.CodeAgentProtected
			t.Logf("OBSERVE kind=%s protectedAgent=%t targets=%d descendants=%d protectionPreflight=%v apply=%d stale=%t protectedRefusal=%t error=%v", tc.kind, tc.protected, len(plan.Targets), len(plan.Targets[0].Descendants), protection, applied, errors.Is(err, processhost.ErrStale), protected, err)
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("FIXTURE read after: %v", readErr)
			}
			if string(before) != string(after) {
				t.Error("admission changed Registry bytes")
			}
			current, loadErr := st.Load()
			if loadErr != nil {
				t.Fatalf("FIXTURE load after: %v", loadErr)
			}
			if _, ok := current.Agent(target.Agent); !ok {
				t.Error("admission removed Agent")
			}
			switch tc.negative {
			case "":
				if err != nil || applied != 1 {
					t.Errorf("exact %s must admit apply once (Pane retains Agent); apply=%d err=%v", tc.kind, applied, err)
				}
			case "protected", "protected-cascade":
				if !protected || applied != 0 || errors.Is(err, processhost.ErrStale) {
					t.Errorf("protected Agent control: apply=%d err=%v", applied, err)
				}
			default:
				if !errors.Is(err, processhost.ErrStale) || applied != 0 || protected {
					t.Errorf("negative %s control: apply=%d err=%v", tc.negative, applied, err)
				}
			}
		})
	}
}
