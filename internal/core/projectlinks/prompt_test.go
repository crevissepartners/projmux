package projectlinks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// renderGoldenRules exercises every rendered part: indexed Jira and Repo
// lists, named URLs stored out of name order, rules on each kind of URL and
// Project variable, a rule whose Project label is missing, and a rule on an
// absolute template.
var renderGoldenRules = Rules{
	Jira: []string{"https://jira.example.com/", "https://jira.partner.example.com"},
	Repo: []string{"https://github.com/example/repo", "https://github.com/example/infra"},
	URLs: map[string]string{"wiki": "https://wiki.example.com/spaces/APP", "docs": "https://docs.example.com"},
	Links: []Link{
		{LabelKey: "jira", Template: "{jira}/browse/{value}"},
		{LabelKey: "partner", Template: "{jira[1]}/browse/{value}"},
		{LabelKey: "pr", Template: "{repo[1]}/pull/{value}"},
		{LabelKey: "page", Template: "{wiki}/{project.labels.team}/{value}"},
		{LabelKey: "branch", Template: "{repo}/tree/{project.name}/{value}"},
		{LabelKey: "owner", Template: "{docs}/{project.labels.owner}/{value}"},
		{LabelKey: "doc", Template: "https://docs.example.com/{value}"},
	},
}

// renderGoldenProject is the Project renderGoldenRules is rendered with. Its
// "bad key" label does not fit the label key grammar, so it is not listed.
var renderGoldenProject = Project{
	UID:    "proj-0123456789abcdefghijklmnop",
	Name:   "my app/x",
	Labels: map[string]string{"team": "core", "env": "prod", "bad key": "x"},
}

// renderGolden is the exact text renderGoldenRules renders. A change to it is a
// change to every Claude Agent's system prompt, so it is pinned byte for byte.
const renderGolden = `# Project label link rules

These are this Project's label link rules. A label is a key and a value, written key=value. A label whose key has a rule below links to that rule's template with its placeholders replaced. Use these rules whenever you turn such a label into a link.

Placeholders: {value} is the label value. {jira[N]} and {repo[N]} are the Nth Jira and Repo URL below, counting from 0; {jira} and {repo} are {jira[0]} and {repo[0]}. {<name>} is the named URL <name>. {project.uid}, {project.name} and {project.labels.<key>} are this Project's UID, name and the value of its label <key>. A URL is substituted with its trailing "/" removed; {value} and every {project.*} value are path-escaped. A rule whose template uses a Project label this Project does not have produces no link.

Jira URLs:
- {jira[0]} (also {jira}): https://jira.example.com/
- {jira[1]}: https://jira.partner.example.com

Repo URLs:
- {repo[0]} (also {repo}): https://github.com/example/repo
- {repo[1]}: https://github.com/example/infra

Named URLs:
- {docs}: https://docs.example.com
- {wiki}: https://wiki.example.com/spaces/APP

Project variables (the values before path-escaping):
- {project.uid}: "proj-0123456789abcdefghijklmnop"
- {project.name}: "my app/x"
- {project.labels.env}: "prod"
- {project.labels.team}: "core"

Rules:
- label key "jira": template {jira}/browse/{value}; example: jira=EXAMPLE-123 links to https://jira.example.com/browse/EXAMPLE-123
- label key "partner": template {jira[1]}/browse/{value}; example: partner=EXAMPLE-123 links to https://jira.partner.example.com/browse/EXAMPLE-123
- label key "pr": template {repo[1]}/pull/{value}; example: pr=EXAMPLE-123 links to https://github.com/example/infra/pull/EXAMPLE-123
- label key "page": template {wiki}/{project.labels.team}/{value}; example: page=EXAMPLE-123 links to https://wiki.example.com/spaces/APP/core/EXAMPLE-123
- label key "branch": template {repo}/tree/{project.name}/{value}; example: branch=EXAMPLE-123 links to https://github.com/example/repo/tree/my%20app%2Fx/EXAMPLE-123
- label key "owner": template {docs}/{project.labels.owner}/{value}; example: none (the template does not produce a link)
- label key "doc": template https://docs.example.com/{value}; example: doc=EXAMPLE-123 links to https://docs.example.com/EXAMPLE-123
`

func TestRenderProjectLinkRulesGolden(t *testing.T) {
	if err := renderGoldenRules.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := string(Render(renderGoldenRules, renderGoldenProject)); got != renderGolden {
		t.Fatalf("Render =\n%s\nwant\n%s", got, renderGolden)
	}
	baseOnly := string(Render(Rules{Jira: []string{"https://jira.example.com"}}, Project{UID: "proj-x", Name: "app"}))
	wantTail := "\n\nJira URLs:\n- {jira[0]} (also {jira}): https://jira.example.com\n\n" +
		"Project variables (the values before path-escaping):\n- {project.uid}: \"proj-x\"\n- {project.name}: \"app\"\n\n" +
		"This Project has no label rules.\n"
	if !strings.HasPrefix(baseOnly, "# Project label link rules\n\n") || !strings.HasSuffix(baseOnly, wantTail) {
		t.Fatalf("Render of a base URL alone =\n%s\nwant it to end with\n%s", baseOnly, wantTail)
	}
}

func TestPlaceholdersListsEveryUsablePlaceholderInOrder(t *testing.T) {
	got := Placeholders(renderGoldenRules)
	want := []string{
		"{value}",
		"{jira}", "{jira[0]}", "{jira[1]}",
		"{repo}", "{repo[0]}", "{repo[1]}",
		"{docs}", "{wiki}",
		"{project.uid}", "{project.name}", "{project.labels.<key>}",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Placeholders = %q, want %q", got, want)
	}
	if got, want := Placeholders(Rules{}), []string{"{value}", "{project.uid}", "{project.name}", "{project.labels.<key>}"}; !slices.Equal(got, want) {
		t.Fatalf("Placeholders(no URLs) = %q, want %q", got, want)
	}
}

func TestRenderProjectLinkRulesDigestIsDeterministic(t *testing.T) {
	first, second := Render(renderGoldenRules, renderGoldenProject), Render(renderGoldenRules, renderGoldenProject)
	if !bytes.Equal(first, second) || Digest(first) != Digest(second) {
		t.Fatal("rendering the same rules twice differs")
	}
	sum := sha256.Sum256([]byte(renderGolden))
	if got, want := Digest(first), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("Digest = %q, want sha256 hex %q", got, want)
	}
	if err := ValidateDigest(Digest(first)); err != nil {
		t.Fatal(err)
	}
	changed := renderGoldenRules
	changed.Links = append([]Link{}, renderGoldenRules.Links[:2]...)
	if Digest(Render(changed, renderGoldenProject)) == Digest(first) {
		t.Fatal("removing a rule kept the digest")
	}
	renamed := renderGoldenProject
	renamed.Name = "renamed"
	if Digest(Render(renderGoldenRules, renamed)) == Digest(first) {
		t.Fatal("renaming the Project kept the digest")
	}
	relabeled := renderGoldenProject
	relabeled.Labels = map[string]string{"team": "platform"}
	if Digest(Render(renderGoldenRules, relabeled)) == Digest(first) {
		t.Fatal("changing the Project's labels kept the digest")
	}
}

func TestRenderEmptyProjectLinkRulesRendersNothing(t *testing.T) {
	for name, rules := range map[string]Rules{
		"zero":        {},
		"empty links": {Links: []Link{}},
		"empty lists": {Jira: []string{}, Repo: []string{}, URLs: map[string]string{}},
	} {
		if got := Render(rules, renderGoldenProject); len(got) != 0 {
			t.Fatalf("%s: Render = %q, want nothing", name, got)
		}
	}
}

func TestProjectLinkRulesSnapshotIsContentAddressedAndPrivate(t *testing.T) {
	store := NewSnapshotStore(t.TempDir())
	rendered := Render(renderGoldenRules, renderGoldenProject)
	snapshot, err := store.WriteSnapshot(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Digest != Digest(rendered) || filepath.Base(snapshot.Path) != snapshot.Digest+".md" {
		t.Fatalf("snapshot = %+v, want <digest>.md", snapshot)
	}
	assertMode(t, snapshot.Path, 0o600)
	assertMode(t, filepath.Dir(snapshot.Path), 0o700)
	if content, err := os.ReadFile(snapshot.Path); err != nil || !bytes.Equal(content, rendered) {
		t.Fatalf("snapshot content = %q, %v", content, err)
	}
	again, err := store.WriteSnapshot(rendered)
	if err != nil || again != snapshot {
		t.Fatalf("rewriting the same rendering = %+v, %v; want %+v", again, err, snapshot)
	}
	if path, err := store.RecordedSnapshotPath(snapshot.Digest); err != nil || path != snapshot.Path {
		t.Fatalf("RecordedSnapshotPath = %q, %v", path, err)
	}
	if err := os.WriteFile(snapshot.Path, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordedSnapshotPath(snapshot.Digest); err == nil {
		t.Fatal("a snapshot whose bytes no longer match its digest was accepted")
	}
	if _, err := store.RecordedSnapshotPath("not-a-digest"); err == nil {
		t.Fatal("a malformed digest was accepted")
	}
	if _, err := store.WriteSnapshot(nil); err == nil {
		t.Fatal("an empty rendering was written")
	}
}

func TestProjectLinkRulesCompositeIsPersonaSeparatorThenRules(t *testing.T) {
	store := NewSnapshotStore(t.TempDir())
	rendered := Render(renderGoldenRules, renderGoldenProject)
	snapshot, err := store.WriteSnapshot(rendered)
	if err != nil {
		t.Fatal(err)
	}
	personaContent := []byte("You review Go code.")
	composite, err := store.WriteComposite(personaContent, snapshot.Digest)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte{}, personaContent...), CompositeSeparator...), rendered...)
	content, err := os.ReadFile(composite.Path)
	if err != nil || !bytes.Equal(content, want) {
		t.Fatalf("composite content = %q, %v; want %q", content, err, want)
	}
	if composite.Digest != Digest(want) || filepath.Base(composite.Path) != "composite-"+composite.Digest+".md" {
		t.Fatalf("composite = %+v, want composite-<digest>.md", composite)
	}
	assertMode(t, composite.Path, 0o600)
	if _, err := store.WriteComposite(personaContent, Digest([]byte("missing"))); err == nil {
		t.Fatal("a composite over a missing rules snapshot was written")
	}
}
