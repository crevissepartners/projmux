package metadata

import "testing"

func TestPaneAdoptionRefusesDeletedTransportTombstone(t *testing.T) {
	registry := adoptionFixture()
	match := NewBindingMatcher(RuntimeObservation{}).MatchPane(
		registry, "win-a1", DeletedPaneMirrorPrefix+"pan-deleted")
	if match.Kind != AdoptionRefused || match.UID != "" {
		t.Fatalf("deleted Pane transport tombstone match = %#v, want refused", match)
	}
}

// adoptionFixture is a two-Project registry with a deliberate shape:
//
//   - Project alpha owns Windows win-a1 then win-a2, in that creation order.
//   - win-a1 owns a shell Pane, then an Agent whose managed Pane comes after it
//     in registry insertion order. That mix is what separates "insertion order"
//     from the snapshot projection's shell-then-managed grouping.
//   - Project beta owns win-b1. It exists only to be refused: nothing of
//     Project alpha may ever pair with it, and vice versa.
func adoptionFixture() *Registry {
	reg := NewRegistry()
	owner := func(kind Kind, uid string) *OwnerRef { return &OwnerRef{Kind: kind, UID: uid} }
	reg.Projects = []Project{
		{APIVersion: APIVersion, Kind: KindProject, Metadata: ObjectMeta{UID: "prj-alpha", Name: "alpha"}, Spec: ProjectSpec{Root: "/src/alpha"}},
		{APIVersion: APIVersion, Kind: KindProject, Metadata: ObjectMeta{UID: "prj-beta", Name: "beta"}, Spec: ProjectSpec{Root: "/src/beta"}},
	}
	reg.Windows = []Window{
		{APIVersion: APIVersion, Kind: KindWindow, Metadata: ObjectMeta{UID: "win-a1", Name: "one", OwnerRef: owner(KindProject, "prj-alpha")}},
		{APIVersion: APIVersion, Kind: KindWindow, Metadata: ObjectMeta{UID: "win-a2", Name: "two", OwnerRef: owner(KindProject, "prj-alpha")}},
		{APIVersion: APIVersion, Kind: KindWindow, Metadata: ObjectMeta{UID: "win-b1", Name: "one", OwnerRef: owner(KindProject, "prj-beta")}},
	}
	reg.Agents = []Agent{
		{APIVersion: APIVersion, Kind: KindAgent, Metadata: ObjectMeta{UID: "agt-a1", Name: "codex", OwnerRef: owner(KindWindow, "win-a1")}},
	}
	reg.Panes = []Pane{
		{APIVersion: APIVersion, Kind: KindPane, Metadata: ObjectMeta{UID: "pan-a1-shell", Name: "zsh", OwnerRef: owner(KindWindow, "win-a1")}},
		{APIVersion: APIVersion, Kind: KindPane, Metadata: ObjectMeta{UID: "pan-a1-agent", Name: "codex-pane", OwnerRef: owner(KindAgent, "agt-a1")}},
		{APIVersion: APIVersion, Kind: KindPane, Metadata: ObjectMeta{UID: "pan-a2", Name: "zsh", OwnerRef: owner(KindWindow, "win-a2")}},
		{APIVersion: APIVersion, Kind: KindPane, Metadata: ObjectMeta{UID: "pan-b1", Name: "zsh", OwnerRef: owner(KindWindow, "win-b1")}},
	}
	return &reg
}

// TestWindowAdoptionMatchesByProjectScopeThenOrdinal pins the whole matching
// rule for Windows, one row per decision the rule is allowed to make.
//
// Every "refuse" row is a case where a content heuristic would have produced an
// answer and the structural rule declines to. Nothing here matches on
// window_name -- and the fixture makes that falsifiable: `one` is the name of a
// Window in both Projects.
func TestWindowAdoptionMatchesByProjectScopeThenOrdinal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// prepare runs before the decision under test, so a row can express
		// "after the pass already claimed something".
		prepare     func(*BindingMatcher, *Registry)
		runtime     RuntimeObservation
		projectUID  string
		observedUID string
		want        AdoptionMatch
	}{
		{
			name:       "a blank live window adopts the first unbound Window of its Project",
			projectUID: "prj-alpha",
			want:       AdoptionMatch{Kind: AdoptionAdopt, UID: "win-a1"},
		},
		{
			name:        "a live window still carrying a uid the Project owns is rebound, not adopted",
			projectUID:  "prj-alpha",
			observedUID: "win-a2",
			want:        AdoptionMatch{Kind: AdoptionRebind, UID: "win-a2"},
		},
		{
			name:        "surrounding whitespace on the observed option is not a uid",
			projectUID:  "prj-alpha",
			observedUID: "  win-a2  ",
			want:        AdoptionMatch{Kind: AdoptionRebind, UID: "win-a2"},
		},
		{
			// Refuse case: the session resolves to no Project. Pushing the live
			// window into some other Project is the one mistake no later pass
			// can undo.
			name:       "an unresolved session adopts nothing",
			projectUID: "",
			want:       AdoptionMatch{Kind: AdoptionRefused},
		},
		{
			// Refuse-to-adopt case: a foreign uid is evidence of "not ours", not
			// a blank. It is never paired with an existing registry Window --
			// that would be re-identification off a failed lookup -- and the
			// unbound win-a1 sitting right there proves the refusal is real.
			name:        "a uid the registry does not know is never adopted",
			projectUID:  "prj-alpha",
			observedUID: "win-from-another-machine",
			want:        AdoptionMatch{Kind: AdoptionForeign},
		},
		{
			// Refuse case, cross-project half: a uid that belongs to another
			// Project is not this Project's to rebind, and the blank-adoption
			// path must not be reached for it either.
			name:        "a uid owned by a different Project is refused",
			projectUID:  "prj-alpha",
			observedUID: "win-b1",
			want:        AdoptionMatch{Kind: AdoptionRefused},
		},
		{
			// Refuse case: the candidate is already the binding of a different
			// live tmux window. Skipped rather than stolen -- and with the
			// second candidate also bound, nothing is left.
			name:       "a candidate already bound to another live window is never stolen",
			runtime:    RuntimeObservation{Windows: map[string]bool{"win-a1": true, "win-a2": true}},
			projectUID: "prj-alpha",
			want:       AdoptionMatch{Kind: AdoptionUnmatched},
		},
		{
			name:       "adoption skips a bound candidate and takes the next one in creation order",
			runtime:    RuntimeObservation{Windows: map[string]bool{"win-a1": true}},
			projectUID: "prj-alpha",
			want:       AdoptionMatch{Kind: AdoptionAdopt, UID: "win-a2"},
		},
		{
			name: "a Window this pass already adopted is not adopted twice",
			prepare: func(b *BindingMatcher, reg *Registry) {
				b.MatchWindow(reg, "prj-alpha", "")
			},
			projectUID: "prj-alpha",
			want:       AdoptionMatch{Kind: AdoptionAdopt, UID: "win-a2"},
		},
		{
			// Refuse case: two live tmux windows carrying one uid is ambiguous.
			// Rebinding both would point one registry Window at two runtimes.
			name: "a uid two live windows both carry is refused the second time",
			prepare: func(b *BindingMatcher, reg *Registry) {
				b.MatchWindow(reg, "prj-alpha", "win-a1")
			},
			projectUID:  "prj-alpha",
			observedUID: "win-a1",
			want:        AdoptionMatch{Kind: AdoptionRefused},
		},
		{
			name: "a Project whose Windows are all claimed reports unmatched, so the import path creates",
			prepare: func(b *BindingMatcher, reg *Registry) {
				b.MatchWindow(reg, "prj-alpha", "")
				b.MatchWindow(reg, "prj-alpha", "")
			},
			projectUID: "prj-alpha",
			want:       AdoptionMatch{Kind: AdoptionUnmatched},
		},
		{
			// Cross-project isolation, the other direction: Project beta's walk
			// never sees Project alpha's Windows, whatever alpha's walk did.
			name: "another Project's walk never reaches into this Project",
			prepare: func(b *BindingMatcher, reg *Registry) {
				b.MatchWindow(reg, "prj-alpha", "")
				b.MatchWindow(reg, "prj-alpha", "")
				b.MatchWindow(reg, "prj-alpha", "")
			},
			projectUID: "prj-beta",
			want:       AdoptionMatch{Kind: AdoptionAdopt, UID: "win-b1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := adoptionFixture()
			binder := NewBindingMatcher(tt.runtime)
			if tt.prepare != nil {
				tt.prepare(binder, reg)
			}
			if got := binder.MatchWindow(reg, tt.projectUID, tt.observedUID); got != tt.want {
				t.Fatalf("MatchWindow = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestPaneAdoptionMatchesInsideOneWindowByOrdinal pins the Pane half.
//
// The candidate order is registry insertion order across both owners a Window
// has -- itself for shell Panes, its Agents for managed ones -- because that is
// the order the import created them in, one per observed tmux pane.
func TestPaneAdoptionMatchesInsideOneWindowByOrdinal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prepare     func(*BindingMatcher, *Registry)
		runtime     RuntimeObservation
		windowUID   string
		observedUID string
		want        AdoptionMatch
	}{
		{
			name:      "the first blank pane takes the Window's own first Pane",
			windowUID: "win-a1",
			want:      AdoptionMatch{Kind: AdoptionAdopt, UID: "pan-a1-shell"},
		},
		{
			name: "the second blank pane takes the managed Pane that follows it in insertion order",
			prepare: func(b *BindingMatcher, reg *Registry) {
				b.MatchPane(reg, "win-a1", "")
			},
			windowUID: "win-a1",
			want:      AdoptionMatch{Kind: AdoptionAdopt, UID: "pan-a1-agent"},
		},
		{
			name:        "a pane still carrying a uid its Window owns is rebound",
			windowUID:   "win-a1",
			observedUID: "pan-a1-agent",
			want:        AdoptionMatch{Kind: AdoptionRebind, UID: "pan-a1-agent"},
		},
		{
			// Refuse case: the Pane belongs to a sibling Window. Ordinal
			// alignment is scoped to one Window, never to the Project.
			name:        "a Pane of a sibling Window is refused",
			windowUID:   "win-a1",
			observedUID: "pan-a2",
			want:        AdoptionMatch{Kind: AdoptionRefused},
		},
		{
			name:        "a Pane of another Project is refused",
			windowUID:   "win-a1",
			observedUID: "pan-b1",
			want:        AdoptionMatch{Kind: AdoptionRefused},
		},
		{
			name:        "a uid the registry does not know is never adopted",
			windowUID:   "win-a1",
			observedUID: "pan-nowhere",
			want:        AdoptionMatch{Kind: AdoptionForeign},
		},
		{
			name:      "a Window that was not matched has no scope, so nothing pairs",
			windowUID: "",
			want:      AdoptionMatch{Kind: AdoptionRefused},
		},
		{
			name:      "a Pane already bound to a live tmux pane is never stolen",
			runtime:   RuntimeObservation{Panes: map[string]bool{"pan-a1-shell": true}},
			windowUID: "win-a1",
			want:      AdoptionMatch{Kind: AdoptionAdopt, UID: "pan-a1-agent"},
		},
		{
			name:      "a Window with no Pane left reports unmatched, so the import path creates one",
			runtime:   RuntimeObservation{Panes: map[string]bool{"pan-a1-shell": true, "pan-a1-agent": true}},
			windowUID: "win-a1",
			want:      AdoptionMatch{Kind: AdoptionUnmatched},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := adoptionFixture()
			binder := NewBindingMatcher(tt.runtime)
			if tt.prepare != nil {
				tt.prepare(binder, reg)
			}
			if got := binder.MatchPane(reg, tt.windowUID, tt.observedUID); got != tt.want {
				t.Fatalf("MatchPane = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestAdoptionNeverCrossesProjectsForOneBlankLiveWindow is the negative case
// stated on its own, because it is the failure adoption must never produce.
//
// Project alpha's Windows are all bound elsewhere. A blank live window of
// Project alpha therefore has no candidate -- and Project beta's unbound Window
// sitting right there, with the same name, must not become one.
func TestAdoptionNeverCrossesProjectsForOneBlankLiveWindow(t *testing.T) {
	t.Parallel()

	reg := adoptionFixture()
	binder := NewBindingMatcher(RuntimeObservation{
		Windows: map[string]bool{"win-a1": true, "win-a2": true},
	})
	got := binder.MatchWindow(reg, "prj-alpha", "")
	if got.Kind != AdoptionUnmatched {
		t.Fatalf("MatchWindow = %+v, want unmatched rather than a cross-project adoption", got)
	}
	// And the other Project's Window is still free, so nothing was consumed on
	// its behalf either.
	if beta := binder.MatchWindow(reg, "prj-beta", ""); beta.UID != "win-b1" {
		t.Fatalf("Project beta's Window = %+v, want win-b1 still available", beta)
	}
}

// TestClaimKeepsAFreshlyCreatedObjectOutOfTheCandidateSet pins why the import
// path reports its mints to the matcher.
//
// Without it, a Window created for the third live tmux window of a session is
// still an unclaimed candidate when the fourth is matched, and the fourth
// adopts the Window that was just created for the third -- two live tmux
// windows pointing at one registry Window.
func TestClaimKeepsAFreshlyCreatedObjectOutOfTheCandidateSet(t *testing.T) {
	t.Parallel()

	reg := adoptionFixture()
	binder := NewBindingMatcher(RuntimeObservation{})
	if got := binder.MatchWindow(reg, "prj-alpha", ""); got.UID != "win-a1" {
		t.Fatalf("first match = %+v", got)
	}
	binder.Claim("win-a2")
	if got := binder.MatchWindow(reg, "prj-alpha", ""); got.Kind != AdoptionUnmatched {
		t.Fatalf("a claimed Window was still adoptable: %+v", got)
	}
}

// TestPaneCandidateOrderIsRegistryInsertionOrder guards the one ordering
// decision the Pane rule depends on. snapshotPanesOf groups shell Panes ahead
// of managed ones; borrowing that grouping here would shear the alignment for
// every Window that mixes the two.
func TestPaneCandidateOrderIsRegistryInsertionOrder(t *testing.T) {
	t.Parallel()

	reg := adoptionFixture()
	// Insert a second shell Pane after the managed one, so the two orders
	// actually disagree.
	reg.Panes = append(reg.Panes, Pane{
		APIVersion: APIVersion, Kind: KindPane,
		Metadata: ObjectMeta{UID: "pan-a1-late", Name: "logs", OwnerRef: &OwnerRef{Kind: KindWindow, UID: "win-a1"}},
	})
	want := []string{"pan-a1-shell", "pan-a1-agent", "pan-a1-late"}
	got := reg.paneUIDsInWindowOrder("win-a1")
	if !equalStrings(got, want) {
		t.Fatalf("pane candidate order = %v, want %v", got, want)
	}
}
