package projectlinks

import (
	"bytes"
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
		JiraURL: "https://jira.example.com/",
		RepoURL: "https://github.com/example/repo",
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
	for _, field := range []string{`"jiraURL": ""`, `"repoURL": ""`, `"links": []`} {
		if !bytes.Contains(content, []byte(field)) {
			t.Fatalf("file %s does not contain %s", content, field)
		}
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
		{name: "http-base-with-path", rules: Rules{JiraURL: "http://jira.internal:8080/jira", Links: []Link{{LabelKey: "jira", Template: "{jira}/browse/{value}"}}}},
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
		{name: "relative-jira-url", rules: Rules{JiraURL: "jira.example.com"}, want: "jiraURL: must be an absolute http or https URL"},
		{name: "non-http-repo-url", rules: Rules{RepoURL: "ftp://repo.example.com"}, want: "repoURL: must be an absolute http or https URL"},
		{name: "base-url-without-host", rules: Rules{JiraURL: "https:///browse"}, want: "jiraURL: must have a host"},
		{name: "base-url-with-userinfo", rules: Rules{JiraURL: withUserinfo("https://jira.example.com")}, want: "jiraURL: must not contain userinfo"},
		{name: "base-url-with-query", rules: Rules{RepoURL: "https://repo.example.com/?tab=1"}, want: "repoURL: must not have a query"},
		{name: "base-url-with-fragment", rules: Rules{RepoURL: "https://repo.example.com/#x"}, want: "repoURL: must not have a fragment"},
		{name: "base-url-with-brace", rules: Rules{RepoURL: "https://repo.example.com/{value}"}, want: "repoURL: must not contain"},
		{name: "base-url-too-long", rules: Rules{JiraURL: "https://j.example.com/" + strings.Repeat("a", MaxURLLength)}, want: "jiraURL: is"},
		{name: "empty-label-key", rules: Rules{Links: link("", "https://x.example.com/{value}")}, want: "links[0].labelKey: must not be empty"},
		{name: "label-key-with-space", rules: Rules{Links: link(" jira", "https://x.example.com/{value}")}, want: "links[0].labelKey: contains \" \""},
		{name: "label-key-too-long", rules: Rules{Links: link(strings.Repeat("k", MaxLabelKeyLength+1), "https://x.example.com/{value}")}, want: "links[0].labelKey: is"},
		{name: "duplicate-label-key", rules: Rules{Links: []Link{
			{LabelKey: "jira", Template: "https://a.example.com/{value}"},
			{LabelKey: "jira", Template: "https://b.example.com/{value}"},
		}}, want: "links[1].labelKey: duplicates links[0].labelKey"},
		{name: "template-without-value", rules: Rules{Links: link("jira", "https://x.example.com/browse")}, want: "links[0].template: must contain {value}"},
		{name: "template-uses-empty-jira", rules: Rules{Links: link("jira", "{jira}/browse/{value}")}, want: "links[0].template: uses {jira} but jiraURL is empty"},
		{name: "template-uses-empty-repo", rules: Rules{Links: link("pr", "{repo}/pull/{value}")}, want: "links[0].template: uses {repo} but repoURL is empty"},
		{name: "template-unknown-placeholder", rules: Rules{Links: link("jira", "https://x.example.com/{project}/{value}")}, want: "links[0].template: has unknown placeholder {project}"},
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
	if err := store.Write(uid, Rules{JiraURL: "not a url"}); err == nil {
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
		{name: "truncated", content: `{"jiraURL": "https://j.example.com", "links": [`, want: "is not valid project links JSON"},
		{name: "wrong-type", content: `{"links": {"jira": "x"}}`, want: "is not valid project links JSON"},
		{name: "unknown-field", content: `{"jiraURL": "", "repoURL": "", "links": [], "extra": 1}`, want: "is not valid project links JSON"},
		{name: "trailing-data", content: `{"links": []} {"links": []}`, want: "is not valid project links JSON"},
		{name: "invalid-stored-rules", content: `{"links": [{"labelKey": "jira", "template": "{jira}/browse/{value}"}]}`, want: "holds invalid rules: links[0].template: uses {jira} but jiraURL is empty"},
		{name: "oversized", content: `{"jiraURL": "` + strings.Repeat("a", MaxFileSize) + `"}`, want: "the limit is 65536 bytes"},
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
		{name: "base-trailing-slashes-trimmed", rules: Rules{JiraURL: "https://j.example.com//", Links: []Link{{LabelKey: "jira", Template: "{jira}/browse/{value}"}}}, key: "jira", value: "X-1", want: "https://j.example.com/browse/X-1", wantOK: true},
		{name: "unknown-key", rules: rules, key: "wiki", value: "ABC-123"},
		{name: "key-is-case-sensitive", rules: rules, key: "JIRA", value: "ABC-123"},
		{name: "empty-value", rules: rules, key: "jira", value: ""},
		{name: "empty-rules", rules: Rules{}, key: "jira", value: "ABC-123"},
		{name: "unvalidated-rule-with-empty-base", rules: Rules{Links: []Link{{LabelKey: "jira", Template: "{jira}/browse/{value}"}}}, key: "jira", value: "ABC-123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Resolve(tt.rules, tt.key, tt.value)
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
