package projectlinks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// renderGoldenRules exercises every rendered part: both base URLs, a rule on
// each base, and a rule on an absolute template.
var renderGoldenRules = Rules{
	JiraURL: "https://jira.example.com/",
	RepoURL: "https://github.com/example/repo",
	Links: []Link{
		{LabelKey: "jira", Template: "{jira}/browse/{value}"},
		{LabelKey: "pr", Template: "{repo}/pull/{value}"},
		{LabelKey: "doc", Template: "https://docs.example.com/{value}"},
	},
}

// renderGolden is the exact text renderGoldenRules renders. A change to it is a
// change to every Claude Agent's system prompt, so it is pinned byte for byte.
const renderGolden = `# Project label link rules

These are this Project's label link rules. A label is a key and a value, written key=value. A label whose key has a rule below links to that rule's template, with {value} replaced by the label value (path-escaped), {jira} by the Jira URL and {repo} by the Repo URL. Use these rules whenever you turn such a label into a link.

Jira URL: https://jira.example.com/
Repo URL: https://github.com/example/repo

Rules:
- label key "jira": template {jira}/browse/{value}; example: jira=EXAMPLE-123 links to https://jira.example.com/browse/EXAMPLE-123
- label key "pr": template {repo}/pull/{value}; example: pr=EXAMPLE-123 links to https://github.com/example/repo/pull/EXAMPLE-123
- label key "doc": template https://docs.example.com/{value}; example: doc=EXAMPLE-123 links to https://docs.example.com/EXAMPLE-123
`

func TestRenderProjectLinkRulesGolden(t *testing.T) {
	if got := string(Render(renderGoldenRules)); got != renderGolden {
		t.Fatalf("Render =\n%s\nwant\n%s", got, renderGolden)
	}
	baseOnly := string(Render(Rules{JiraURL: "https://jira.example.com"}))
	want := "# Project label link rules\n\n" +
		"These are this Project's label link rules. A label is a key and a value, written key=value. " +
		"A label whose key has a rule below links to that rule's template, with {value} replaced by the label value (path-escaped), " +
		"{jira} by the Jira URL and {repo} by the Repo URL. Use these rules whenever you turn such a label into a link.\n\n" +
		"Jira URL: https://jira.example.com\n\nThis Project has no label rules.\n"
	if baseOnly != want {
		t.Fatalf("Render of a base URL alone =\n%s\nwant\n%s", baseOnly, want)
	}
}

func TestRenderProjectLinkRulesDigestIsDeterministic(t *testing.T) {
	first, second := Render(renderGoldenRules), Render(renderGoldenRules)
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
	if Digest(Render(changed)) == Digest(first) {
		t.Fatal("removing a rule kept the digest")
	}
}

func TestRenderEmptyProjectLinkRulesRendersNothing(t *testing.T) {
	for name, rules := range map[string]Rules{
		"zero":        {},
		"empty links": {Links: []Link{}},
	} {
		if got := Render(rules); len(got) != 0 {
			t.Fatalf("%s: Render = %q, want nothing", name, got)
		}
	}
}

func TestProjectLinkRulesSnapshotIsContentAddressedAndPrivate(t *testing.T) {
	store := NewSnapshotStore(t.TempDir())
	rendered := Render(renderGoldenRules)
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
	rendered := Render(renderGoldenRules)
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
