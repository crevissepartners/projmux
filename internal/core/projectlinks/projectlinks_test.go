package projectlinks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func newTestStore(t *testing.T) (Store, string) {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), "config", "projmux")
	return NewStore(configDir), configDir
}

func newProjectUID(t *testing.T) string {
	t.Helper()
	uid, err := coremetadata.NewUID(coremetadata.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

// userinfoPart is the credentials part of a URL, built at runtime so no
// source line holds a credential-shaped URL literal.
func userinfoPart() string {
	return url.UserPassword("user", "pw").String()
}

// withUserinfo returns raw with userinfoPart added.
func withUserinfo(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	parsed.User = url.UserPassword("user", "pw")
	return parsed.String()
}

func sampleRules() Rules {
	return Rules{
		Jira: []string{"https://jira.example.com/"},
		Repo: []string{"https://github.com/example/repo"},
		Links: []Link{
			{LabelKey: "jira", Template: "{jira}/browse/{value}"},
			{LabelKey: "pr", Template: "{repo}/pull/{value}"},
			{LabelKey: "doc", Template: "https://docs.example.com/search?q={value}"},
		},
	}
}

func TestNewDefaultStoreUsesConfigDir(t *testing.T) {
	t.Parallel()
	paths := config.DefaultPaths("/tmp/config-home", "/tmp/state-home")
	store := NewDefaultStore(paths)
	if got, want := store.Dir(), filepath.Join(paths.ConfigDir, "project-links"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestWriteThenLoadRoundTripsAtProjectLinksPath(t *testing.T) {
	t.Parallel()
	store, configDir := newTestStore(t)
	uid := newProjectUID(t)
	want := sampleRules()
	if err := store.Write(uid, want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	path := filepath.Join(configDir, "project-links", uid+".json")
	if got, err := store.Path(uid); err != nil || got != path {
		t.Fatalf("Path() = %q, %v; want %q", got, err, path)
	}
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)
	got, err := store.Load(uid)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("project-links/ holds %d entries, want only the rule file (no temporary files left)", len(entries))
	}
}

func TestWriteEncodesDocumentedJSONFieldNames(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	uid := newProjectUID(t)
	if err := store.Write(uid, Rules{}); err != nil {
		t.Fatalf("Write(empty) error = %v", err)
	}
	path, _ := store.Path(uid)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"jira": []`, `"repo": []`, `"urls": {}`, `"links": []`} {
		if !bytes.Contains(content, []byte(field)) {
			t.Fatalf("file %s does not contain %s", content, field)
		}
	}
	if bytes.Contains(content, []byte("URL\"")) {
		t.Fatalf("file %s holds a legacy jiraURL/repoURL field", content)
	}
	got, err := store.Load(uid)
	if err != nil || !reflect.DeepEqual(got, Rules{}) {
		t.Fatalf("Load(empty) = %+v, %v; want zero Rules, nil", got, err)
	}
}

func TestLoadMissingFileReturnsEmptyRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, store Store)
	}{
		{name: "missing-directory", setup: func(*testing.T, Store) {}},
		{name: "missing-file", setup: func(t *testing.T, store Store) {
			if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newTestStore(t)
			tt.setup(t, store)
			got, err := store.Load(newProjectUID(t))
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, Rules{}) {
				t.Fatalf("Load() = %+v, want zero Rules", got)
			}
		})
	}
}

func TestValidateAcceptsValidRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rules Rules
	}{
		{name: "empty", rules: Rules{}},
		{name: "sample", rules: sampleRules()},
		{name: "http-base-with-path", rules: Rules{Jira: []string{"http://jira.internal:8080/jira"}, Links: []Link{{LabelKey: "jira", Template: "{jira}/browse/{value}"}}}},
		{name: "absolute-template-without-bases", rules: Rules{Links: []Link{{LabelKey: "Tk_2.x-y", Template: "https://t.example.com/{value}#top"}}}},
		{name: "keys-differ-only-in-case", rules: Rules{Links: []Link{
			{LabelKey: "jira", Template: "https://a.example.com/{value}"},
			{LabelKey: "Jira", Template: "https://b.example.com/{value}"},
		}}},
		{name: "at-link-limit", rules: Rules{Links: manyLinks(MaxLinks)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.rules.Validate(); err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func manyLinks(n int) []Link {
	links := make([]Link, n)
	for i := range links {
		links[i] = Link{LabelKey: fmt.Sprintf("k%d", i), Template: "https://x.example.com/{value}"}
	}
	return links
}

func TestWriteRefusesInvalidRulesAndLeavesFileUnchanged(t *testing.T) {
	t.Parallel()
	link := func(key, template string) []Link { return []Link{{LabelKey: key, Template: template}} }
	tests := []struct {
		name  string
		rules Rules
		want  string
	}{
		{name: "relative-jira-url", rules: Rules{Jira: []string{"jira.example.com"}}, want: "jira[0]: must be an absolute http or https URL"},
		{name: "non-http-repo-url", rules: Rules{Repo: []string{"ftp://repo.example.com"}}, want: "repo[0]: must be an absolute http or https URL"},
		{name: "base-url-without-host", rules: Rules{Jira: []string{"https:///browse"}}, want: "jira[0]: must have a host"},
		{name: "base-url-with-userinfo", rules: Rules{Jira: []string{withUserinfo("https://jira.example.com")}}, want: "jira[0]: must not contain userinfo"},
		{name: "base-url-with-query", rules: Rules{Repo: []string{"https://repo.example.com/?tab=1"}}, want: "repo[0]: must not have a query"},
		{name: "base-url-with-fragment", rules: Rules{Repo: []string{"https://repo.example.com/#x"}}, want: "repo[0]: must not have a fragment"},
		{name: "base-url-with-brace", rules: Rules{Repo: []string{"https://repo.example.com/{value}"}}, want: "repo[0]: must not contain"},
		{name: "base-url-too-long", rules: Rules{Jira: []string{"https://j.example.com/" + strings.Repeat("a", MaxURLLength)}}, want: "jira[0]: is"},
		{name: "empty-label-key", rules: Rules{Links: link("", "https://x.example.com/{value}")}, want: "links[0].labelKey: must not be empty"},
		{name: "label-key-with-space", rules: Rules{Links: link(" jira", "https://x.example.com/{value}")}, want: "links[0].labelKey: contains \" \""},
		{name: "label-key-too-long", rules: Rules{Links: link(strings.Repeat("k", MaxLabelKeyLength+1), "https://x.example.com/{value}")}, want: "links[0].labelKey: is"},
		{name: "duplicate-label-key", rules: Rules{Links: []Link{
			{LabelKey: "jira", Template: "https://a.example.com/{value}"},
			{LabelKey: "jira", Template: "https://b.example.com/{value}"},
		}}, want: "links[1].labelKey: duplicates links[0].labelKey"},
		{name: "template-without-value", rules: Rules{Links: link("jira", "https://x.example.com/browse")}, want: "links[0].template: must contain {value}"},
		{name: "template-uses-empty-jira", rules: Rules{Links: link("jira", "{jira}/browse/{value}")}, want: "links[0].template: uses {jira} but jira has no URLs"},
		{name: "template-uses-empty-repo", rules: Rules{Links: link("pr", "{repo}/pull/{value}")}, want: "links[0].template: uses {repo} but repo has no URLs"},
		{name: "template-unknown-placeholder", rules: Rules{Links: link("jira", "https://x.example.com/{project}/{value}")}, want: "links[0].template: has {project}: unknown placeholder"},
		{name: "template-unterminated-placeholder", rules: Rules{Links: link("jira", "https://x.example.com/{value")}, want: "links[0].template: has an unterminated placeholder"},
		{name: "template-stray-close-brace", rules: Rules{Links: link("jira", "https://x.example.com/}{value}")}, want: "links[0].template: has a \"}\" outside a placeholder"},
		{name: "template-relative", rules: Rules{Links: link("jira", "/browse/{value}")}, want: "links[0].template: expanded must be an absolute http or https URL"},
		{name: "template-with-userinfo", rules: Rules{Links: link("jira", "https://"+userinfoPart()+"@x.example.com/{value}")}, want: "links[0].template: expanded must not contain userinfo"},
		{name: "template-too-long", rules: Rules{Links: link("jira", "https://x.example.com/{value}"+strings.Repeat("a", MaxTemplateLength))}, want: "links[0].template: is"},
		{name: "over-link-limit", rules: Rules{Links: manyLinks(MaxLinks + 1)}, want: "links: has 65 rules; the limit is 64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.rules.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, tt.want)
			}
			store, _ := newTestStore(t)
			uid := newProjectUID(t)
			if err := store.Write(uid, sampleRules()); err != nil {
				t.Fatal(err)
			}
			path, _ := store.Path(uid)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = store.Write(uid, tt.rules)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Write() error = %v, want it to contain %q", err, tt.want)
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("Write() error %v is not a *ValidationError", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("rule file changed after a refused Write:\nbefore %s\nafter %s", before, after)
			}
		})
	}
}

func TestWriteRefusesInvalidRulesWithoutCreatingAFile(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	uid := newProjectUID(t)
	if err := store.Write(uid, Rules{Jira: []string{"not a url"}}); err == nil {
		t.Fatal("Write(invalid) error = nil")
	}
	if _, err := os.Stat(store.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(project-links/) error = %v, want not-exist after a refused first Write", err)
	}
}

func TestLoadRefusesCorruptFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty-file", content: "", want: "is not valid project links JSON"},
		{name: "not-json", content: "jira = https://x", want: "is not valid project links JSON"},
		{name: "truncated", content: `{"jira": ["https://j.example.com"], "links": [`, want: "is not valid project links JSON"},
		{name: "wrong-type", content: `{"links": {"jira": "x"}}`, want: "is not valid project links JSON"},
		{name: "unknown-field", content: `{"jira": [], "repo": [], "links": [], "extra": 1}`, want: "is not valid project links JSON"},
		{name: "trailing-data", content: `{"links": []} {"links": []}`, want: "is not valid project links JSON"},
		{name: "invalid-stored-rules", content: `{"links": [{"labelKey": "jira", "template": "{jira}/browse/{value}"}]}`, want: "holds invalid rules: links[0].template: uses {jira} but jira has no URLs"},
		{name: "oversized", content: `{"jira": ["` + strings.Repeat("a", MaxFileSize) + `"]}`, want: "the limit is 65536 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newTestStore(t)
			uid := newProjectUID(t)
			path, _ := store.Path(uid)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := store.Load(uid)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() = %+v, %v; want an error containing %q", got, err, tt.want)
			}
			if !reflect.DeepEqual(got, Rules{}) {
				t.Fatalf("Load() rules = %+v on error, want zero Rules", got)
			}
		})
	}
}

func TestLoadRefusesDirectoryInPlaceOfFile(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	uid := newProjectUID(t)
	path, _ := store.Path(uid)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(uid); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("Load(directory) error = %v, want not a regular file", err)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	rules := sampleRules()
	tests := []struct {
		name   string
		rules  Rules
		key    string
		value  string
		want   string
		wantOK bool
	}{
		{name: "jira-browse", rules: rules, key: "jira", value: "ABC-123", want: "https://jira.example.com/browse/ABC-123", wantOK: true},
		{name: "repo-pull", rules: rules, key: "pr", value: "42", want: "https://github.com/example/repo/pull/42", wantOK: true},
		{name: "value-with-slash-is-escaped", rules: rules, key: "jira", value: "a/b", want: "https://jira.example.com/browse/a%2Fb", wantOK: true},
		{name: "value-with-space-is-escaped", rules: rules, key: "jira", value: "a b", want: "https://jira.example.com/browse/a%20b", wantOK: true},
		{name: "value-with-placeholder-is-not-reexpanded", rules: rules, key: "jira", value: "{jira}", want: "https://jira.example.com/browse/%7Bjira%7D", wantOK: true},
		{name: "value-in-query", rules: rules, key: "doc", value: "x y", want: "https://docs.example.com/search?q=x%20y", wantOK: true},
		{name: "base-trailing-slashes-trimmed", rules: Rules{Jira: []string{"https://j.example.com//"}, Links: []Link{{LabelKey: "jira", Template: "{jira}/browse/{value}"}}}, key: "jira", value: "X-1", want: "https://j.example.com/browse/X-1", wantOK: true},
		{name: "unknown-key", rules: rules, key: "wiki", value: "ABC-123"},
		{name: "key-is-case-sensitive", rules: rules, key: "JIRA", value: "ABC-123"},
		{name: "empty-value", rules: rules, key: "jira", value: ""},
		{name: "empty-rules", rules: Rules{}, key: "jira", value: "ABC-123"},
		{name: "unvalidated-rule-with-empty-base", rules: Rules{Links: []Link{{LabelKey: "jira", Template: "{jira}/browse/{value}"}}}, key: "jira", value: "ABC-123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Resolve(tt.rules, Project{}, tt.key, tt.value)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("Resolve(%q, %q) = %q, %v; want %q, %v", tt.key, tt.value, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestStoreRefusesEscapingProjectUID(t *testing.T) {
	t.Parallel()
	valid := newProjectUID(t)
	uids := []string{
		"",
		".",
		"..",
		"../x",
		"../" + valid,
		"/" + valid,
		"x/" + valid,
		`..\` + valid,
		valid + "/..",
		valid + ".json",
		strings.ToUpper(valid),
		"proj-front",
		"win-" + strings.TrimPrefix(valid, "proj-"),
	}
	ops := []struct {
		name string
		run  func(Store, string) error
	}{
		{name: "Load", run: func(s Store, uid string) error { _, err := s.Load(uid); return err }},
		{name: "Write", run: func(s Store, uid string) error { return s.Write(uid, sampleRules()) }},
		{name: "Delete", run: func(s Store, uid string) error { return s.Delete(uid) }},
		{name: "Path", run: func(s Store, uid string) error { _, err := s.Path(uid); return err }},
	}
	for _, op := range ops {
		for _, uid := range uids {
			t.Run(op.name+"/"+strings.NewReplacer("/", "_", `\`, "_").Replace(uid), func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				configDir := filepath.Join(root, "config", "projmux")
				// A "../x" escape from project-links/ would land in
				// configDir, so it exists and must stay empty.
				if err := os.MkdirAll(configDir, 0o700); err != nil {
					t.Fatal(err)
				}
				store := NewStore(configDir)
				err := op.run(store, uid)
				if err == nil || !strings.Contains(err.Error(), "is not a Project UID") {
					t.Fatalf("%s(%q) error = %v, want a Project UID refusal", op.name, uid, err)
				}
				var created []string
				_ = filepath.WalkDir(root, func(path string, _ os.DirEntry, _ error) error {
					if path != root && path != filepath.Join(root, "config") && path != configDir {
						created = append(created, path)
					}
					return nil
				})
				if len(created) != 0 {
					t.Fatalf("%s(%q) created %v", op.name, uid, created)
				}
			})
		}
	}
}

func TestDeleteRemovesFileAndToleratesMissing(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	uid := newProjectUID(t)
	if err := store.Delete(uid); err != nil {
		t.Fatalf("Delete(missing directory) error = %v, want nil", err)
	}
	if err := store.Write(uid, sampleRules()); err != nil {
		t.Fatal(err)
	}
	other := newProjectUID(t)
	if err := store.Delete(other); err != nil {
		t.Fatalf("Delete(missing file) error = %v, want nil", err)
	}
	if err := store.Delete(uid); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	path, _ := store.Path(uid)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(deleted) error = %v, want not-exist", err)
	}
	if got, err := store.Load(uid); err != nil || !reflect.DeepEqual(got, Rules{}) {
		t.Fatalf("Load(deleted) = %+v, %v; want zero Rules, nil", got, err)
	}
}

// placeholderRules is a rule set with two Jira URLs, two Repo URLs and a named
// URL, the base of the placeholder tests.
func placeholderRules(links ...Link) Rules {
	return Rules{
		Jira:  []string{"https://jira.example.com/", "https://jira.partner.example.com"},
		Repo:  []string{"https://github.com/example/app", "https://github.com/example/infra/"},
		URLs:  map[string]string{"wiki": "https://wiki.example.com/spaces/APP/"},
		Links: links,
	}
}

func TestResolveNewPlaceholdersExpandListsNamedURLsAndProjectVariables(t *testing.T) {
	t.Parallel()
	project := Project{UID: "proj-abc", Name: "my app/x", Labels: map[string]string{"team": "core"}}
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{name: "jira-short-form", template: "{jira}/browse/{value}", want: "https://jira.example.com/browse/AB-1"},
		{name: "jira-index-0-equals-short-form", template: "{jira[0]}/browse/{value}", want: "https://jira.example.com/browse/AB-1"},
		{name: "jira-index-1", template: "{jira[1]}/browse/{value}", want: "https://jira.partner.example.com/browse/AB-1"},
		{name: "repo-index-1", template: "{repo[1]}/pull/{value}", want: "https://github.com/example/infra/pull/AB-1"},
		{name: "named-url", template: "{wiki}/{value}", want: "https://wiki.example.com/spaces/APP/AB-1"},
		{name: "project-name-is-path-escaped", template: "{repo}/tree/{project.name}/{value}", want: "https://github.com/example/app/tree/my%20app%2Fx/AB-1"},
		{name: "project-uid", template: "https://t.example.com/{project.uid}/{value}", want: "https://t.example.com/proj-abc/AB-1"},
		{name: "project-label", template: "{wiki}/{project.labels.team}/{value}", want: "https://wiki.example.com/spaces/APP/core/AB-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rules := placeholderRules(Link{LabelKey: "k", Template: tt.template})
			if err := rules.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			got, ok := Resolve(rules, project, "k", "AB-1")
			if !ok || got != tt.want {
				t.Fatalf("Resolve(%s) = %q, %v; want %q, true", tt.template, got, ok, tt.want)
			}
		})
	}
	short, _ := Resolve(placeholderRules(Link{LabelKey: "k", Template: "{jira}/browse/{value}"}), project, "k", "AB-1")
	indexed, _ := Resolve(placeholderRules(Link{LabelKey: "k", Template: "{jira[0]}/browse/{value}"}), project, "k", "AB-1")
	if short != indexed {
		t.Fatalf("{jira} = %q, {jira[0]} = %q; want the same link", short, indexed)
	}
}

func TestResolveNewPlaceholdersDoNotReexpandInsertedText(t *testing.T) {
	t.Parallel()
	rules := placeholderRules(Link{LabelKey: "k", Template: "{repo}/tree/{project.name}/{value}"})
	project := Project{Name: "{jira}", Labels: map[string]string{}}
	got, ok := Resolve(rules, project, "k", "{wiki}")
	if want := "https://github.com/example/app/tree/%7Bjira%7D/%7Bwiki%7D"; !ok || got != want {
		t.Fatalf("Resolve = %q, %v; want %q", got, ok, want)
	}
}

func TestResolveReturnsFalseForMissingProjectLabelsEmptyValuesAndUnknownKeys(t *testing.T) {
	t.Parallel()
	rules := placeholderRules(
		Link{LabelKey: "team", Template: "{wiki}/{project.labels.team}/{value}"},
		Link{LabelKey: "jira", Template: "{jira}/browse/{value}"},
	)
	if err := rules.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		project Project
		key     string
		value   string
	}{
		{name: "project-label-missing", project: Project{Labels: map[string]string{"other": "x"}}, key: "team", value: "A-1"},
		{name: "project-labels-nil", project: Project{}, key: "team", value: "A-1"},
		{name: "project-label-empty", project: Project{Labels: map[string]string{"team": ""}}, key: "team", value: "A-1"},
		{name: "empty-value", project: Project{Labels: map[string]string{"team": "core"}}, key: "jira", value: ""},
		{name: "key-with-no-rule", project: Project{Labels: map[string]string{"team": "core"}}, key: "wiki", value: "A-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, ok := Resolve(rules, tt.project, tt.key, tt.value); ok || got != "" {
				t.Fatalf("Resolve(%q, %q) = %q, %v; want \"\", false", tt.key, tt.value, got, ok)
			}
		})
	}
	// A comma is part of the one value: one link, never two.
	got, ok := Resolve(rules, Project{}, "jira", "a,b")
	if want := "https://jira.example.com/browse/a%2Cb"; !ok || got != want {
		t.Fatalf("Resolve(jira, a,b) = %q, %v; want the one link %q", got, ok, want)
	}
}

func TestProjectOfTakesOnlyUIDNameAndLabels(t *testing.T) {
	t.Parallel()
	registryProject := coremetadata.Project{
		Metadata: coremetadata.ObjectMeta{UID: "proj-x", Name: "app", Labels: map[string]string{"team": "core"},
			Annotations: map[string]string{"secret": "s"}},
		Spec: coremetadata.ProjectSpec{Root: "/srv/app"},
	}
	got := ProjectOf(registryProject)
	if want := (Project{UID: "proj-x", Name: "app", Labels: map[string]string{"team": "core"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectOf = %+v, want %+v", got, want)
	}
	got.Labels["team"] = "changed"
	if registryProject.Metadata.Labels["team"] != "core" {
		t.Fatal("ProjectOf shares the Registry Project's labels map")
	}
}

// writeRawRules stores content as uid's rule file without validating it.
func writeRawRules(t *testing.T, store Store, uid string, content []byte) {
	t.Helper()
	path, _ := store.Path(uid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func manyURLs(n int) []string {
	urls := make([]string, n)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://u%d.example.com", i)
	}
	return urls
}

func manyNamedURLs(n int) map[string]string {
	urls := make(map[string]string, n)
	for i := range n {
		urls[fmt.Sprintf("u%d", i)] = fmt.Sprintf("https://u%d.example.com", i)
	}
	return urls
}

func TestWriteAndLoadRefuseInvalidURLListsNamesAndPlaceholders(t *testing.T) {
	t.Parallel()
	link := func(template string) []Link { return []Link{{LabelKey: "k", Template: template}} }
	two := []string{"https://a.example.com", "https://b.example.com"}
	named := func(name string) map[string]string { return map[string]string{name: "https://w.example.com"} }
	tests := []struct {
		name   string
		rules  Rules
		field  string
		reason string
	}{
		{name: "jira-index-out-of-range", rules: Rules{Jira: two, Links: link("{jira[2]}/{value}")}, field: "links[0].template", reason: "uses {jira[2]} but jira has 2 URLs"},
		{name: "repo-index-out-of-range", rules: Rules{Repo: two[:1], Links: link("{repo[1]}/{value}")}, field: "links[0].template", reason: "uses {repo[1]} but repo has 1 URL"},
		{name: "huge-index-is-out-of-range", rules: Rules{Jira: two, Links: link("{jira[99999999999999999999999]}/{value}")}, field: "links[0].template", reason: "but jira has 2 URLs"},
		{name: "jira-index-leading-zero", rules: Rules{Jira: two, Links: link("{jira[01]}/{value}")}, field: "links[0].template", reason: "has {jira[01]}: index has a leading zero"},
		{name: "jira-index-not-decimal", rules: Rules{Jira: two, Links: link("{jira[x]}/{value}")}, field: "links[0].template", reason: "has {jira[x]}: malformed index"},
		{name: "jira-short-form-with-no-urls", rules: Rules{Links: link("{jira}/{value}")}, field: "links[0].template", reason: "uses {jira} but jira has no URLs"},
		{name: "undefined-name", rules: Rules{URLs: named("wiki"), Links: link("{docs}/{value}")}, field: "links[0].template", reason: "uses {docs} but urls has no \"docs\""},
		{name: "reserved-name-value", rules: Rules{URLs: named("value")}, field: "urls.value", reason: "is reserved"},
		{name: "reserved-name-jira", rules: Rules{URLs: named("jira")}, field: "urls.jira", reason: "is reserved"},
		{name: "reserved-name-repo", rules: Rules{URLs: named("repo")}, field: "urls.repo", reason: "is reserved"},
		{name: "reserved-name-project", rules: Rules{URLs: named("project")}, field: "urls.project", reason: "is reserved"},
		{name: "malformed-name-uppercase", rules: Rules{URLs: named("Wiki")}, field: "urls.Wiki", reason: "must be a lowercase ASCII letter"},
		{name: "malformed-name-leading-digit", rules: Rules{URLs: named("1x")}, field: "urls.1x", reason: "must be a lowercase ASCII letter"},
		{name: "malformed-name-too-long", rules: Rules{URLs: named("w" + strings.Repeat("a", MaxURLNameLength))}, field: "urls.w" + strings.Repeat("a", MaxURLNameLength), reason: "must be a lowercase ASCII letter"},
		{name: "unknown-project-field", rules: Rules{Links: link("https://x.example.com/{project.owner}/{value}")}, field: "links[0].template", reason: "has {project.owner}: unknown Project field"},
		{name: "project-root-is-not-exposed", rules: Rules{Links: link("https://x.example.com/{project.root}/{value}")}, field: "links[0].template", reason: "has {project.root}: unknown Project field"},
		{name: "malformed-project-label-key", rules: Rules{Links: link("https://x.example.com/{project.labels.a b}/{value}")}, field: "links[0].template", reason: "Project label key \"a b\" contains \" \""},
		{name: "over-limit-jira", rules: Rules{Jira: manyURLs(MaxJiraURLs + 1)}, field: "jira", reason: "has 17 URLs; the limit is 16"},
		{name: "over-limit-repo", rules: Rules{Repo: manyURLs(MaxRepoURLs + 1)}, field: "repo", reason: "has 17 URLs; the limit is 16"},
		{name: "over-limit-urls", rules: Rules{URLs: manyNamedURLs(MaxNamedURLs + 1)}, field: "urls", reason: "has 33 URLs; the limit is 32"},
		{name: "invalid-url-inside-jira", rules: Rules{Jira: []string{"https://a.example.com", "https://b.example.com", "https://c.example.com", "ftp://d.example.com"}}, field: "jira[3]", reason: "must be an absolute http or https URL"},
		{name: "empty-url-inside-repo", rules: Rules{Repo: []string{""}}, field: "repo[0]", reason: "must not be empty"},
		{name: "invalid-url-inside-urls", rules: Rules{URLs: map[string]string{"wiki": "https://w.example.com/?q=1"}}, field: "urls.wiki", reason: "must not have a query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newTestStore(t)
			uid := newProjectUID(t)
			if err := store.Write(uid, sampleRules()); err != nil {
				t.Fatal(err)
			}
			path, _ := store.Path(uid)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = store.Write(uid, tt.rules)
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Field != tt.field || !strings.Contains(validation.Reason, tt.reason) {
				t.Fatalf("Write() error = %v, want a *ValidationError on %s containing %q", err, tt.field, tt.reason)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("rule file changed after a refused Write:\nbefore %s\nafter %s", before, after)
			}

			raw, err := json.Marshal(tt.rules)
			if err != nil {
				t.Fatal(err)
			}
			writeRawRules(t, store, uid, raw)
			got, err := store.Load(uid)
			if err == nil || !errors.As(err, &validation) || validation.Field != tt.field {
				t.Fatalf("Load() = %+v, %v; want the %s refusal", got, err, tt.field)
			}
		})
	}
}

func TestWriteRefusesRemovingAURLATemplateUses(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	uid := newProjectUID(t)
	rules := placeholderRules(Link{LabelKey: "k", Template: "{jira[1]}/browse/{value}"})
	if err := store.Write(uid, rules); err != nil {
		t.Fatal(err)
	}
	rules.Jira = rules.Jira[:1]
	var validation *ValidationError
	if err := store.Write(uid, rules); !errors.As(err, &validation) || validation.Field != "links[0].template" {
		t.Fatalf("Write() without the used URL error = %v, want a links[0].template refusal", err)
	}
}

func TestLoadRefusesThePreListJiraURLAndRepoURLFields(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		content string
		field   string
	}{
		{name: "both-old-fields", content: `{"jiraURL": "https://j.example.com", "repoURL": "", "links": []}`, field: "jiraURL"},
		{name: "repoURL-only", content: `{"repoURL": "https://github.com/example/repo", "links": []}`, field: "repoURL"},
		{name: "jiraURL-with-jira", content: `{"jira": ["https://j.example.com"], "jiraURL": "https://j.example.com", "links": []}`, field: "jiraURL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newTestStore(t)
			uid := newProjectUID(t)
			writeRawRules(t, store, uid, []byte(tt.content))
			got, err := store.Load(uid)
			if err == nil || !strings.Contains(err.Error(), "is not valid project links JSON") || !strings.Contains(err.Error(), `unknown field "`+tt.field+`"`) {
				t.Fatalf("Load(%s) = %+v, %v; want an unknown field %q error", tt.content, got, err, tt.field)
			}
			if !reflect.DeepEqual(got, Rules{}) {
				t.Fatalf("Load(%s) rules = %+v on error, want zero Rules", tt.content, got)
			}
		})
	}
}

func TestLoadNormalizesEmptyListsAndURLsToNil(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	uid := newProjectUID(t)
	writeRawRules(t, store, uid, []byte(`{"jira": [], "repo": [], "urls": {}, "links": []}`))
	if got, err := store.Load(uid); err != nil || !reflect.DeepEqual(got, Rules{}) {
		t.Fatalf("Load(empty) = %+v, %v; want zero Rules", got, err)
	}
}
