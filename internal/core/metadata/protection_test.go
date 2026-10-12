package metadata

import "testing"

func TestAgentProtectionRequiresLabelPresence(t *testing.T) {
	for _, labels := range []map[string]string{nil, {}, {"other/projmux.io/protected": "true"}, {LabelProtected: "true"}, {LabelProtected: "false"}, {LabelProtected: ""}} {
		agent := Agent{Metadata: ObjectMeta{Labels: labels}}
		_, want := labels[LabelProtected]
		if AgentProtected(agent) != want {
			t.Fatalf("labels=%v", labels)
		}
	}
}
