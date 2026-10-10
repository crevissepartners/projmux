package app

import (
	"context"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"reflect"
	"testing"
)

func TestPickerVirtualPrimaryUsesExistingTerminalWindow(t *testing.T) {
	for _, currentReal := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-real", true: "current-real"}[currentReal], func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			project := virtualPrimaryProjectFixture(t, store)
			mut := store.mutator()
			first, panes, err := mut.AddWindow(&store.registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "terminal"}, "/bin/sh", "op-sibling")
			if err != nil {
				t.Fatal(err)
			}
			session := tmux.addSession("alpha")
			session.windows = nil
			seedOwnedSession(session, project.Metadata.UID, project.Spec.Root)
			live := seedLiveWindow(t, tmux, session, first.Metadata.UID, panes[0].Metadata.UID)
			if _, err := mut.ObserveWindowRuntimeBinding(&store.registry, first.Metadata.UID, session.id, live.id); err != nil {
				t.Fatal(err)
			}
			if _, err := mut.BindLiveProjectSession(&store.registry, project.Metadata.UID, "alpha", tmux.socketPath); err != nil {
				t.Fatal(err)
			}
			second, secondPanes, err := mut.AddWindow(&store.registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "second-terminal"}, "/bin/sh", "op-second-sibling")
			if err != nil {
				t.Fatal(err)
			}
			secondLive := seedLiveWindow(t, tmux, session, second.Metadata.UID, secondPanes[0].Metadata.UID)
			if _, err := mut.ObserveWindowRuntimeBinding(&store.registry, second.Metadata.UID, session.id, secondLive.id); err != nil {
				t.Fatal(err)
			}
			session.current = secondLive.id
			if !currentReal {
				session.current = "@missing"
			}
			before := store.registry.Clone()
			executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true, exists: map[string]bool{"alpha": true}}
			switcher := &switchCommand{managedStopStore: store.store(), tmuxRunner: tmux, lookupEnv: func(string) string { return "" }, sessions: executor, identity: stubSwitchIdentityResolver{name: "alpha"}}
			switcher.materializeVirtualWindow = func(context.Context, string) (virtualWindowShellMaterialization, error) {
				t.Fatal("mixed Project materialized primary")
				return virtualWindowShellMaterialization{}, nil
			}
			for _, sidebar := range []bool{false, true} {
				session.current = secondLive.id
				if !currentReal {
					session.current = "@missing"
				}
				var err error
				if sidebar {
					err = switcher.openProjectTargetPathFromSidebar(context.Background(), switchPlan{Selection: project.Spec.Root, SessionName: "alpha"})
				} else {
					err = switcher.openTarget(context.Background(), project.Spec.Root)
				}
				if err != nil {
					t.Fatal(err)
				}
				want := live.id
				if currentReal {
					want = secondLive.id
				}
				if session.current != want {
					t.Fatalf("sidebar=%v arrival=%s want=%s", sidebar, session.current, want)
				}
			}
			if !reflect.DeepEqual(before, store.registry) || len(session.windows) != 2 {
				t.Fatal("mixed Project changed topology")
			}
			expected := live.id
			if currentReal {
				expected = secondLive.id
			}
			if session.current != expected {
				t.Fatalf("arrival=%s expected=%s", session.current, expected)
			}
		})
	}
}
