package projectlinks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	links "github.com/crevissepartners/projmux/internal/core/projectlinks"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func fixture(t *testing.T) (Service, string, config.Paths) {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
	var uid string
	_, err := storemetadata.NewDefaultStore(paths).Update(func(reg *metadata.Registry) error {
		p, e := (metadata.Mutator{DirExists: func(path string) (bool, error) { info, err := os.Stat(path); return err == nil && info.IsDir(), err }}).RegisterProject(reg, metadata.RegisterProjectOptions{Root: root, Name: "sample", Labels: map[string]string{"z": "last", "a": "first"}})
		uid = p.Project.Metadata.UID
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return Service{Paths: func() (config.Paths, error) { return paths, nil }, RegistryPaths: func() (config.Paths, error) { return paths, nil }}, uid, paths
}
func sampleRules() links.Rules {
	return links.Rules{Jira: []string{"https://jira.example/"}, Repo: []string{"https://github.com/example/"}, URLs: map[string]string{"wiki": "https://wiki.example/", "build": "https://ci.example/"}, Links: []links.Link{{LabelKey: "pr", Template: "{repo}/pull/{value}"}, {LabelKey: "doc", Template: "{wiki}/{project.name}/{project.labels.a}/{value}"}, {LabelKey: "absent", Template: "{wiki}/{project.labels.missing}/{value}"}}}
}
func TestLinkRulesRoundTripDryRunAndDelete(t *testing.T) {
	s, uid, paths := fixture(t)
	ctx := context.Background()
	rules := sampleRules()
	path, _ := links.NewDefaultStore(paths).Path(uid)
	preview, err := s.Write(ctx, "sample", rules, true)
	if err != nil || !preview.DryRun {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote: %v", err)
	}
	saved, err := s.Write(ctx, "uid:"+uid, rules, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, "sample")
	if err != nil || !reflect.DeepEqual(saved, got) {
		t.Fatalf("read %+v %v", got, err)
	}
	preview.DryRun = false
	if !reflect.DeepEqual(preview, got) {
		t.Fatal("dry run/write differ")
	}
	if got.URLs[0].Name != "build" || got.ProjectVariables[2].Placeholder != "{project.labels.a}" || got.Links[0].Example != "https://github.com/example/pull/ABC-123" || got.Links[2].Example != "" {
		t.Fatalf("projection %+v", got)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes[len(bytes)-1:]) != "\n" {
		t.Fatal("store encoding changed")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Delete(ctx, "sample", false)
	var confirmation *Refusal
	if !errors.As(err, &confirmation) || confirmation.Code != CodeConfirmationRequired {
		t.Fatalf("confirmation: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("unconfirmed deletion changed file: %v", err)
	}
	for range 2 {
		empty, err := s.Delete(ctx, "sample", true)
		if err != nil || len(empty.Links) != 0 {
			t.Fatalf("delete %+v %v", empty, err)
		}
		b, _ := json.Marshal(empty)
		if strings.Contains(string(b), "null") {
			t.Fatalf("null list %s", b)
		}
	}
}
func TestLinkRulesRefusalsPreserveFile(t *testing.T) {
	s, uid, paths := fixture(t)
	ctx := context.Background()
	_, err := s.Write(ctx, "uid:"+uid, sampleRules(), false)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := links.NewDefaultStore(paths).Path(uid)
	before, _ := os.ReadFile(path)
	for _, dry := range []bool{false, true} {
		invalid := links.Rules{Links: []links.Link{{LabelKey: "bad", Template: "https://example.com/no-value"}}}
		_, err := s.Write(ctx, "uid:"+uid, invalid, dry)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeInvalid || refusal.Field != "links[0].template" || refusal.Reason != "must contain {value}" {
			t.Fatalf("refusal %v", err)
		}
		huge := links.Rules{}
		for i := range links.MaxLinks {
			huge.Links = append(huge.Links, links.Link{LabelKey: fmt.Sprintf("k%d", i), Template: "https://example.com/" + strings.Repeat("x", 1900) + "/{value}"})
		}
		_, err = s.Write(ctx, "uid:"+uid, huge, dry)
		if !errors.As(err, &refusal) || refusal.Field != "links" || !strings.Contains(refusal.Reason, "the rules encode to") {
			t.Fatalf("size %v", err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("refusal changed file")
		}
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = s.Read(ctx, "uid:"+uid)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnreadable {
		t.Fatalf("read %v", err)
	}
	// Replacement repairs the unreadable file; deletion also needs no load.
	if _, err = s.Write(ctx, "uid:"+uid, sampleRules(), false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delete(ctx, "uid:"+uid, true); err != nil {
		t.Fatal(err)
	}
}
func TestLinkRulesProjectAndCancellation(t *testing.T) {
	s, uid, paths := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, call := range []func() error{func() error { _, e := s.Read(ctx, "uid:"+uid); return e }, func() error { _, e := s.Write(ctx, "uid:"+uid, sampleRules(), false); return e }, func() error { _, e := s.Delete(ctx, "uid:"+uid, true); return e }} {
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel %v", err)
		}
	}
	if _, err := os.Stat(links.NewDefaultStore(paths).Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel wrote %v", err)
	}
	for _, ref := range []string{"missing", "uid:proj-aaaaaaaaaaaaaaaaaaaaaaaaaa", "", "uid:../escape"} {
		_, err := s.Read(context.Background(), ref)
		if err == nil {
			t.Fatalf("accepted %q", ref)
		}
	}
}
func TestLinkRulesProjectionContract(t *testing.T) {
	// Fixed response contract for sorted URLs, examples and Project variables.
	target := links.Project{UID: "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "a b", Labels: map[string]string{"z": "z/z", "a": "a b"}}
	rules := links.Rules{URLs: map[string]string{"z": "https://z.example/", "a": "https://a.example/"}, Links: []links.Link{{LabelKey: "doc", Template: "{a}/{project.name}/{project.labels.z}/{value}"}}}
	got := Project(target, rules)
	want := `{"project":"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa","jira":[],"repo":[],"urls":[{"name":"a","url":"https://a.example/"},{"name":"z","url":"https://z.example/"}],"links":[{"labelKey":"doc","template":"{a}/{project.name}/{project.labels.z}/{value}","example":"https://a.example/a%20b/z%2Fz/ABC-123"}],"exampleValue":"ABC-123","placeholders":["{value}","{a}","{z}","{project.uid}","{project.name}","{project.labels.<key>}"],"projectVariables":[{"placeholder":"{project.uid}","value":"proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"},{"placeholder":"{project.name}","value":"a b"},{"placeholder":"{project.labels.a}","value":"a b"},{"placeholder":"{project.labels.z}","value":"z/z"}]}`
	b, _ := json.Marshal(got)
	var expected Result
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("projection\n%s\nwant\n%s", b, want)
	}
}
func TestLinkRulesDecodeUsesStoreFormat(t *testing.T) {
	for _, raw := range []string{`{"urls":[{"name":"wiki","url":"https://example.com"}]}`, `{"unknown":1}`, `{} {}`, `{"links":[{"labelKey":"a","template":"https://example.com/{value}","extra":true}]}`} {
		_, err := Decode([]byte(raw))
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Field == "" || refusal.Reason == "" {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
	rules, err := Decode([]byte(`{"urls":{"wiki":"https://example.com"}}`))
	if err != nil || rules.URLs["wiki"] != "https://example.com" {
		t.Fatalf("decode %+v %v", rules, err)
	}
}
