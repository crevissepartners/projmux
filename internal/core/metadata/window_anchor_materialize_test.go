package metadata

import (
	"reflect"
	"testing"
)

func TestSetWindowAnchorRejectsForeignOrAbsentPaneWithoutMutation(t *testing.T) {
	fixture := newAnchorSchemaFixture(t)
	mut := testMutator(dirSet{"/src/projmux": true})
	for _, tc := range []struct{ name, window, pane string }{
		{"foreign Pane", fixture.windowUID, fixture.otherShellUID},
		{"absent Pane", fixture.windowUID, "pane-missing"},
		{"absent Window", "window-missing", fixture.shellUID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := fixture.registry.Clone()
			before := reg.Clone()
			if _, err := mut.SetWindowAnchor(&reg, tc.window, tc.pane); err == nil {
				t.Fatal("expected refusal")
			}
			if !reflect.DeepEqual(reg, before) {
				t.Fatal("refused anchor mutation changed Registry")
			}
		})
	}
}
