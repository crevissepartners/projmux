package projmux

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const byUIDProject = "proj-cccccccccccccccccccccccccc"

func byUIDClient(t *testing.T, registry []byte) (*Client, string) {
	t.Helper()
	c, paths := graphFixtureClient(t, registry)
	return c, filepath.Join(paths.ConfigDir, projectlinks.DirName, byUIDProject+projectlinks.FileExt)
}

func useLinkRulesStat(t *testing.T, stat func(string) (core.FileIdentity, bool)) {
	t.Helper()
	previous := statLinkRulesFile
	statLinkRulesFile = stat
	t.Cleanup(func() { statLinkRulesFile = previous })
}

// The rules are read without the Registry, so a missing or malformed one does
// not matter, and a missing file creates nothing.
func TestReadProjectLinkRulesByUIDWithoutRegistry(t *testing.T) {
	for name, registry := range map[string][]byte{"missing": nil, "malformed": []byte("{")} {
		c, path := byUIDClient(t, registry)
		got, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
		if err != nil || got.FileIdentity != nil || !reflect.DeepEqual(got.Rules, core.ProjectLinkRules{}) {
			t.Fatalf("%s Registry, missing file: %+v %v", name, got, err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s Registry: missing read created %v", name, err)
		}
		writeFixtureFile(t, path, `{"jira":["https://jira.example.com"],"repo":["https://example.com/r"],"urls":{"wiki":"https://wiki.example.com"},"links":[{"labelKey":"pr","template":"{repo}/pull/{value}"}]}`)
		got, err = c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
		want := core.ProjectLinkRules{Jira: []string{"https://jira.example.com"}, Repo: []string{"https://example.com/r"}, URLs: map[string]string{"wiki": "https://wiki.example.com"}, Links: []core.ProjectLink{{LabelKey: "pr", Template: "{repo}/pull/{value}"}}}
		if err != nil || !reflect.DeepEqual(got.Rules, want) || got.FileIdentity == nil {
			t.Fatalf("%s Registry: %+v %v", name, got, err)
		}
	}
}

// The identity is the file's own stat, the same while it is unchanged and
// different once it is replaced; a read the stats cannot pin has none.
func TestReadProjectLinkRulesByUIDIdentity(t *testing.T) {
	c, path := byUIDClient(t, nil)
	writeFixtureFile(t, path, `{"links":[]}`)
	first, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
	if err != nil || first.FileIdentity == nil {
		t.Fatalf("%+v %v", first, err)
	}
	if stat, ok := linkRulesFileIdentity(path); !ok || stat != *first.FileIdentity {
		t.Fatalf("identity %+v, want the file's stat %+v", *first.FileIdentity, stat)
	}
	again, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
	if err != nil || again.FileIdentity == nil || *again.FileIdentity != *first.FileIdentity {
		t.Fatalf("unchanged file: %+v %v", again, err)
	}
	replacement := path + ".new"
	writeFixtureFile(t, replacement, `{"jira":["https://jira.example.com"]}`)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	replaced, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
	if err != nil || replaced.FileIdentity == nil || *replaced.FileIdentity == *first.FileIdentity || len(replaced.Rules.Jira) != 1 {
		t.Fatalf("replaced file: %+v %v", replaced, err)
	}

	calls := 0
	useLinkRulesStat(t, func(path string) (core.FileIdentity, bool) {
		calls++
		identity, ok := linkRulesFileIdentity(path)
		identity.Size += int64(calls)
		return identity, ok
	})
	unstable, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
	if err != nil || unstable.FileIdentity != nil || len(unstable.Rules.Jira) != 1 || calls != 2 {
		t.Fatalf("changing stats: %+v %v (%d stats)", unstable, err, calls)
	}
	useLinkRulesStat(t, func(string) (core.FileIdentity, bool) { return core.FileIdentity{}, false })
	unknown, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
	if err != nil || unknown.FileIdentity != nil || len(unknown.Rules.Jira) != 1 {
		t.Fatalf("failed stats: %+v %v", unknown, err)
	}
}

// Files the stored-rules read refuses are link-rules-unreadable refusals that
// keep the read's cause.
func TestReadProjectLinkRulesByUIDUnreadable(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, path string)
		cause error
	}{
		"malformed": {setup: func(t *testing.T, path string) { writeFixtureFile(t, path, "{") }},
		"invalid":   {setup: func(t *testing.T, path string) { writeFixtureFile(t, path, `{"links":[{"labelKey":"","template":"x"}]}`) }},
		"oversized": {setup: func(t *testing.T, path string) {
			writeFixtureFile(t, path, strings.Repeat(" ", projectlinks.MaxFileSize+1))
		}},
		"directory": {setup: func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
		}},
		"escaping symlink": {setup: func(t *testing.T, path string) {
			outside := filepath.Join(t.TempDir(), "rules.json")
			writeFixtureFile(t, outside, `{"links":[]}`)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}},
		"permission": {setup: func(t *testing.T, path string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads unreadable files")
			}
			writeFixtureFile(t, path, `{"links":[]}`)
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
		}, cause: fs.ErrPermission},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, path := byUIDClient(t, nil)
			tc.setup(t, path)
			got, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeLinkRulesUnreadable || errors.Unwrap(err) == nil || !reflect.DeepEqual(got, core.ProjectLinkRulesRead{}) {
				t.Fatalf("%+v %v", got, err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("cause lost: %v", err)
			}
			if !strings.HasPrefix(err.Error(), string(CodeLinkRulesUnreadable)+": read project links") {
				t.Fatalf("message %q", err)
			}
		})
	}
}

func TestReadProjectLinkRulesByUIDRefusalsAndCancellation(t *testing.T) {
	c, path := byUIDClient(t, nil)
	var refusal *Refusal
	for _, uid := range []string{"", "uid:" + byUIDProject, "project-01", "../" + byUIDProject, " " + byUIDProject} {
		got, err := c.ReadProjectLinkRulesByUID(context.Background(), uid)
		if !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid || !reflect.DeepEqual(got, core.ProjectLinkRulesRead{}) {
			t.Fatalf("%q: %+v %v", uid, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ReadProjectLinkRulesByUID(ctx, byUIDProject); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled on entry: %v", err)
	}

	// Cancellation during the read is reported after it, unless the read failed.
	for content, unreadable := range map[string]bool{`{"links":[]}`: false, "{": true} {
		writeFixtureFile(t, path, content)
		ctx, cancel := context.WithCancel(context.Background())
		useLinkRulesStat(t, func(path string) (core.FileIdentity, bool) {
			cancel()
			return linkRulesFileIdentity(path)
		})
		got, err := c.ReadProjectLinkRulesByUID(ctx, byUIDProject)
		if unreadable && (!errors.As(err, &refusal) || refusal.Code != CodeLinkRulesUnreadable) {
			t.Fatalf("read failure did not take precedence: %v", err)
		}
		if !unreadable && !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled during read: %+v %v", got, err)
		}
		cancel()
	}
}

// The rules ReadProjectLinkRulesByUID returns are the ones the selector read
// returns for the same Project, and they belong to the caller.
func TestReadProjectLinkRulesByUIDMatchesStoreAndIsOwned(t *testing.T) {
	c, path := byUIDClient(t, nil)
	writeFixtureFile(t, path, `{"jira":["https://jira.example.com"],"urls":{"wiki":"https://wiki.example.com"},"links":[{"labelKey":"pr","template":"{jira}/{value}"}]}`)
	paths, err := c.instructions.Paths()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := projectlinks.NewDefaultStore(paths).Load(byUIDProject)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Rules.Jira, stored.Jira) || !reflect.DeepEqual(got.Rules.URLs, stored.URLs) || len(got.Rules.Links) != 1 || got.Rules.Links[0].Template != stored.Links[0].Template {
		t.Fatalf("%+v, want %+v", got.Rules, stored)
	}
	got.Rules.Jira[0], got.Rules.URLs["wiki"] = "changed", "changed"
	*got.FileIdentity = core.FileIdentity{}
	again, err := c.ReadProjectLinkRulesByUID(context.Background(), byUIDProject)
	if err != nil || again.Rules.Jira[0] != "https://jira.example.com" || again.Rules.URLs["wiki"] != "https://wiki.example.com" || *again.FileIdentity == (core.FileIdentity{}) {
		t.Fatalf("results share memory: %+v %v", again, err)
	}
}
