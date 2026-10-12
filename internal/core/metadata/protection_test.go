package metadata

import (
	"reflect"
	"strings"
	"testing"
)

func TestAgentProtectionRequiresLabelPresence(t *testing.T) {
	for _, labels := range []map[string]string{nil, {}, {"other/projmux.io/protected": "true"}, {LabelProtected: "true"}, {LabelProtected: "false"}, {LabelProtected: ""}} {
		agent := Agent{Metadata: ObjectMeta{Labels: labels}}
		_, want := labels[LabelProtected]
		if AgentProtected(agent) != want {
			t.Fatalf("labels=%v", labels)
		}
	}
}

func TestAgentProtectionLabelWrites(t *testing.T) {
	for _, value := range []string{"true", "false", "", "TRUE"} {
		t.Run(value, func(t *testing.T) {
			mutator, reg, uids := labelFixture(t)
			before := reg.Clone()
			_, _, err := mutator.SetLabels(reg, KindAgent, uids[KindAgent], map[string]string{LabelProtected: value}, nil)
			if value == "true" {
				if err != nil {
					t.Fatal(err)
				}
				a, _ := reg.Agent(uids[KindAgent])
				if !AgentProtected(*a) {
					t.Fatal("true did not protect")
				}
				if _, _, err := mutator.SetLabels(reg, KindAgent, uids[KindAgent], nil, []string{LabelProtected}); err != nil {
					t.Fatal(err)
				}
				if AgentProtected(*a) {
					t.Fatal("removal retained protection")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "projmux.io/protected-") {
					t.Fatalf("invalid write %v", err)
				}
				if !reflect.DeepEqual(*reg, before) {
					t.Fatal("invalid write mutated Registry")
				}
			}
		})
	}
}

func TestAgentCreationProtectionLabels(t *testing.T) {
	for _, value := range []string{"true", "false", ""} {
		t.Run(value, func(t *testing.T) {
			mutator, reg, uids := labelFixture(t)
			before := reg.Clone()
			agent, err := mutator.CreateAgent(reg, uids[KindWindow], CreateAgentOptions{Provider: "codex", Labels: map[string]string{LabelProtected: value}, OperationID: "op-protect"})
			if value == "true" {
				if err != nil || !AgentProtected(agent) {
					t.Fatalf("valid creation agent=%v err=%v", agent, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "projmux.io/protected-") {
					t.Fatalf("invalid create %v", err)
				}
				if !reflect.DeepEqual(*reg, before) {
					t.Fatal("invalid creation reserved or wrote resources")
				}
			}
		})
	}
}
