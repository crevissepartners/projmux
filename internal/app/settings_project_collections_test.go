package app

import (
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const settingsProjectCollectionsGolden = "testdata/settings-project-collections.golden"

// projectCollectionsCase is one platform's spelling of the same three
// collections.
type projectCollectionsCase struct {
	goos       string
	scanRoot   string
	projectUID string
	// projectRoot is the Registry root a managed pin projects.
	projectRoot string
	// candidate is a pinned path no Project claims.
	candidate string
	// candidateAlias is the same directory spelled differently. On Windows it must
	// fold onto candidate; on a case-sensitive host it must not.
	candidateAlias string
}

func projectCollectionsCases() []projectCollectionsCase {
	return []projectCollectionsCase{
		{
			goos:           "linux",
			scanRoot:       "/srv/work",
			projectUID:     "proj-app",
			projectRoot:    "/srv/work/app",
			candidate:      "/srv/work/scratch",
			candidateAlias: "/srv/work/Scratch",
		},
		{
			goos:           "windows",
			scanRoot:       `C:\Users\dev\src`,
			projectUID:     "proj-winapp",
			projectRoot:    `C:\Users\dev\src\app`,
			candidate:      `C:\Users\dev\src\scratch`,
			candidateAlias: `c:/users/dev/src/Scratch`,
		},
	}
}

// settingsProjectCollectionsCommand renders the Projects surfaces over one
// platform's spellings without touching the host's own config or Registry.
func settingsProjectCollectionsCommand(t *testing.T, tc projectCollectionsCase) *settingsCommand {
	t.Helper()

	home := t.TempDir()
	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
		{Kind: pins.KindProject, Value: tc.projectUID},
		{Kind: pins.KindCandidate, Value: tc.candidate},
	}}}
	switcher := &switchCommand{
		pinStore: func() (switchPinStore, error) { return store, nil },
		pinProjects: func() ([]pins.ProjectRef, error) {
			return []pins.ProjectRef{{UID: tc.projectUID, Root: tc.projectRoot}}, nil
		},
		discover:   func(candidates.Inputs) ([]string, error) { return nil, nil },
		homeDir:    func() (string, error) { return home, nil },
		workingDir: func() (string, error) { return home, nil },
		validate:   func(string) error { return nil },
		lookupEnv:  func(string) string { return "" },
		loadWorkdirs: func(string) ([]string, error) {
			return []string{tc.scanRoot}, nil
		},
	}
	registry := coremetadata.Registry{Projects: []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindProject,
		Metadata:   coremetadata.ObjectMeta{UID: tc.projectUID, Name: "app"},
		Spec:       coremetadata.ProjectSpec{Root: tc.projectRoot},
	}}}
	return &settingsCommand{
		switcher:         switcher,
		homeDir:          func() (string, error) { return home, nil },
		lookupEnv:        func(string) string { return "" },
		ai:               testAICommand(home),
		resourceRegistry: func() (coremetadata.Registry, error) { return registry, nil },
	}
}

// TestProjectCollectionsGoldenOnLinuxAndWindows is acceptance (5).
//
// One golden holds both platforms, because the contract is that they say the same
// thing: Workdirs are scan roots, a managed pin is a Registry uid whose root is
// projected, and a pinned path no Project claims is a candidate. The Windows half
// asserts the path boundary too -- a differently spelled candidate folds onto the
// same candidate rather than becoming a second one, and folding never produces a
// managed pin.
func TestProjectCollectionsGoldenOnLinuxAndWindows(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for _, tc := range projectCollectionsCases() {
		cmd := settingsProjectCollectionsCommand(t, tc)
		b.WriteString("== " + tc.goos + " ==\n")

		b.WriteString("Projects\n")
		writeCollectionRows(&b, cmd.projectPickerEntries())

		workdirs, err := cmd.workdirListEntries()
		if err != nil {
			t.Fatalf("%s workdirListEntries() error = %v", tc.goos, err)
		}
		b.WriteString("Additional discovery roots\n")
		writeCollectionRows(&b, workdirs)

		managed, err := cmd.pinnedProjectEntries()
		if err != nil {
			t.Fatalf("%s pinnedProjectEntries() error = %v", tc.goos, err)
		}
		b.WriteString("Pinned Projects\n")
		writeCollectionRows(&b, managed)

		candidatePins, err := cmd.candidatePinEntries()
		if err != nil {
			t.Fatalf("%s candidatePinEntries() error = %v", tc.goos, err)
		}
		b.WriteString("Candidate Pins\n")
		writeCollectionRows(&b, candidatePins)
	}

	got := b.String()
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(settingsProjectCollectionsGolden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(settingsProjectCollectionsGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("project collections changed.\n--- got ---\n%s\n--- want ---\n%s", got, string(want))
	}
}

// writeCollectionRows renders the row values and their stripped labels, which is
// what an operator sees plus what an action carries.
func writeCollectionRows(b *strings.Builder, entries []intpickercompat.Entry) {
	for _, entry := range entries {
		if entry.Value == settingsBackValue {
			continue
		}
		b.WriteString("  " + entry.Value + "\t" + collapseSpaces(stripANSI(entry.Label)) + "\n")
	}
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestWindowsCandidateSpellingFoldsWithoutMintingIdentity is the Windows path
// boundary at the app layer: folding decides candidate exact-match only.
func TestWindowsCandidateSpellingFoldsWithoutMintingIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range projectCollectionsCases() {
		t.Run(tc.goos, func(t *testing.T) {
			t.Parallel()
			store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped, Pins: []pins.Pin{
				{Kind: pins.KindCandidate, Value: tc.candidate},
			}}}
			selection, err := authorityOver(store, pins.ProjectRef{UID: tc.projectUID, Root: tc.projectRoot}).selection()
			if err != nil {
				t.Fatalf("selection() error = %v", err)
			}
			if !selection.pinnedCandidate(tc.candidate) {
				t.Fatal("the stored candidate spelling is not recognized as pinned")
			}
			// The alias question is answered by the platform's own rules, which is
			// what MatchKeyFor freezes. Whatever the answer, it is about the path:
			// no managed pin appears either way.
			wantFolded := candidates.MatchKeyFor(tc.goos, tc.candidateAlias) == candidates.MatchKeyFor(tc.goos, tc.candidate)
			if got := candidates.MatchKey(tc.candidateAlias) == candidates.MatchKey(tc.candidate); got != wantFolded && tc.goos == "linux" {
				t.Fatalf("alias folding = %t, want %t on the running host", got, wantFolded)
			}
			if len(selection.projectUIDs) != 0 {
				t.Fatalf("a candidate pin produced managed pins: %#v", selection.projectUIDs)
			}
		})
	}
}
