package projmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const linkOptionsUID = "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"
const linkOptionsOtherUID = "proj-bbbbbbbbbbbbbbbbbbbbbbbbbb"

func linkOptionsFixture(t *testing.T) (*projmux.Client, string, string, metadata.Registry) {
	t.Helper()
	root := t.TempDir()
	registry := metadata.NewRegistry()
	registry.Projects = []metadata.Project{{APIVersion: metadata.APIVersion, Kind: metadata.KindProject, Metadata: metadata.ObjectMeta{UID: linkOptionsUID, Name: "target"}, Spec: metadata.ProjectSpec{Root: root}}}
	registry.Agents = []metadata.Agent{{APIVersion: metadata.APIVersion, Kind: metadata.KindAgent, Metadata: metadata.ObjectMeta{UID: "agent-aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "dangling", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "win-aaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
	path := storemetadata.PathFor(filepath.Join(root, "state"))
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	writeLinkOptionsRegistry(t, path, registry)
	// Non-private modes exercise read paths without permission repair.
	dir := filepath.Join(root, "config", "project-links")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, linkOptionsOtherUID+".json"), []byte("non-target sentinel"), 0640); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	return c, root, path, registry
}
func writeLinkOptionsRegistry(t *testing.T, path string, registry metadata.Registry) {
	t.Helper()
	b, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0640); err != nil {
		t.Fatal(err)
	}
}
func linkOptionsRules() core.ProjectLinkRules {
	return core.ProjectLinkRules{Repo: []string{"https://github.com/example"}, Links: []core.ProjectLink{{LabelKey: "pr", Template: "{repo}/pull/{value}"}}}
}
func TestSDKLinkRulesDegradedCRUDAndStrictParity(t *testing.T) {
	c, root, registryPath, _ := linkOptionsFixture(t)
	ctx := context.Background()
	options := core.ProjectLinkRulesOptions{AllowDegradedRegistry: true}
	rules := linkOptionsRules()
	type call func() (core.ProjectLinkRulesResult, error)
	pairs := [][2]call{
		{func() (core.ProjectLinkRulesResult, error) { return c.ReadProjectLinkRules(ctx, "target") }, func() (core.ProjectLinkRulesResult, error) {
			return c.ReadProjectLinkRulesWithOptions(ctx, "target", core.ProjectLinkRulesOptions{})
		}},
		{func() (core.ProjectLinkRulesResult, error) {
			return c.WriteProjectLinkRules(ctx, "target", rules, false)
		}, func() (core.ProjectLinkRulesResult, error) {
			return c.WriteProjectLinkRulesWithOptions(ctx, "target", rules, false, core.ProjectLinkRulesOptions{})
		}},
		{func() (core.ProjectLinkRulesResult, error) { return c.DeleteProjectLinkRules(ctx, "target", true) }, func() (core.ProjectLinkRulesResult, error) {
			return c.DeleteProjectLinkRulesWithOptions(ctx, "target", true, core.ProjectLinkRulesOptions{})
		}},
	}
	before := centralTree(t, root)
	for _, pair := range pairs {
		a, ae := pair[0]()
		b, be := pair[1]()
		if !reflect.DeepEqual(a, b) || ae == nil || be == nil || ae.Error() != be.Error() {
			t.Fatalf("strict parity: %+v %v / %+v %v", a, ae, b, be)
		}
		var ar, br *projmux.Refusal
		if !errors.As(ae, &ar) || !errors.As(be, &br) || ar.Code != projmux.CodeRegistryUnavailable || ar.Code != br.Code || ar.Field != br.Field || ar.Reason != br.Reason {
			t.Fatalf("strict refusals: %v / %v", ae, be)
		}
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		if string(aj) != string(bj) {
			t.Fatal("JSON drift")
		}
	}
	if !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("strict calls changed tree")
	}
	preview, err := c.WriteProjectLinkRulesWithOptions(ctx, "target", rules, true, options)
	if err != nil || !preview.DryRun {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	empty, err := c.ReadProjectLinkRulesWithOptions(ctx, "target", options)
	if err != nil || len(empty.Links) != 0 {
		t.Fatalf("missing rules: %+v %v", empty, err)
	}
	if !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("preview/read changed tree")
	}
	saved, err := c.WriteProjectLinkRulesWithOptions(ctx, "uid:"+linkOptionsUID, rules, false, options)
	if err != nil {
		t.Fatal(err)
	}
	readBefore := centralTree(t, root)
	got, err := c.ReadProjectLinkRulesWithOptions(ctx, "target", options)
	if err != nil || !reflect.DeepEqual(saved, got) {
		t.Fatalf("read: %+v %v", got, err)
	}
	if !reflect.DeepEqual(readBefore, centralTree(t, root)) {
		t.Fatal("read changed tree")
	}
	// Only the target rules file and its containing directory may change on success.
	for rel, snapshot := range before {
		if rel == "config/project-links" {
			continue
		}
		if after := centralTree(t, root)[rel]; !reflect.DeepEqual(snapshot, after) {
			t.Fatalf("success changed %s", rel)
		}
	}
	if _, err = c.DeleteProjectLinkRulesWithOptions(ctx, "target", false, options); codeOf(t, err) != projmux.CodeLinkRulesConfirmationRequired {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readBefore, centralTree(t, root)) {
		t.Fatal("confirm refusal changed tree")
	}
	// Stored unreadable files retain read refusal and repair/delete behavior.
	targetPath := filepath.Join(root, "config", "project-links", linkOptionsUID+".json")
	if err = os.WriteFile(targetPath, []byte("broken"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReadProjectLinkRulesWithOptions(ctx, "target", options); codeOf(t, err) != projmux.CodeLinkRulesUnreadable {
		t.Fatal(err)
	}
	if _, err = c.WriteProjectLinkRulesWithOptions(ctx, "target", rules, false, options); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out, err := c.DeleteProjectLinkRulesWithOptions(ctx, "target", true, options)
		if err != nil || len(out.Links) != 0 {
			t.Fatalf("delete: %+v %v", out, err)
		}
	}
	b, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	// Use the actual relative Registry path rather than assuming its layout.
	rel, _ := filepath.Rel(root, registryPath)
	original := before[rel].Bytes
	if string(b) != original {
		t.Fatal("Registry bytes changed")
	}
}

func TestSDKLinkRulesDegradedTargetMatrix(t *testing.T) {
	cases := []struct {
		name, ref string
		change    func(*metadata.Registry)
		code      projmux.Code
	}{
		{"missing", "missing", nil, projmux.CodeResourceNotFound},
		{"invalid", "uid:", nil, projmux.CodeSelectorInvalid},
		{"UID same name", "uid:" + linkOptionsUID, func(r *metadata.Registry) { r.Projects = append(r.Projects, r.Projects[0]) }, projmux.CodeSelectorAmbiguous},
		{"UID other name", "uid:" + linkOptionsUID, func(r *metadata.Registry) {
			p := r.Projects[0]
			p.Metadata.Name = "other"
			r.Projects = append(r.Projects, p)
		}, projmux.CodeSelectorAmbiguous},
		{"name duplicate UID", "target", func(r *metadata.Registry) { r.Projects = append(r.Projects, r.Projects[0]) }, projmux.CodeSelectorAmbiguous},
		{"name duplicate other UID", "target", func(r *metadata.Registry) {
			p := r.Projects[0]
			p.Metadata.UID = linkOptionsOtherUID
			r.Projects = append(r.Projects, p)
		}, projmux.CodeSelectorAmbiguous},
		{"name unique but UID duplicate", "target", func(r *metadata.Registry) {
			p := r.Projects[0]
			p.Metadata.Name = "other"
			r.Projects = append(r.Projects, p)
		}, projmux.CodeSelectorAmbiguous},
		{"unique UID shared name", "uid:" + linkOptionsUID, func(r *metadata.Registry) {
			p := r.Projects[0]
			p.Metadata.UID = linkOptionsOtherUID
			r.Projects = append(r.Projects, p)
		}, ""},
		{"invalid target UID", "target", func(r *metadata.Registry) { r.Projects[0].Metadata.UID = "../escape" }, projmux.CodeInvalidLinkRules},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, root, path, r := linkOptionsFixture(t)
			if tc.change != nil {
				tc.change(&r)
				writeLinkOptionsRegistry(t, path, r)
			}
			before := centralTree(t, root)
			opts := core.ProjectLinkRulesOptions{AllowDegradedRegistry: true}
			calls := []func() (core.ProjectLinkRulesResult, error){
				func() (core.ProjectLinkRulesResult, error) {
					return c.ReadProjectLinkRulesWithOptions(context.Background(), tc.ref, opts)
				},
				func() (core.ProjectLinkRulesResult, error) {
					return c.WriteProjectLinkRulesWithOptions(context.Background(), tc.ref, linkOptionsRules(), tc.code == "", opts)
				},
				func() (core.ProjectLinkRulesResult, error) {
					return c.DeleteProjectLinkRulesWithOptions(context.Background(), tc.ref, tc.code != "", opts)
				},
			}
			for i, call := range calls {
				_, err := call()
				want := tc.code
				if want == "" && i == 2 {
					want = projmux.CodeLinkRulesConfirmationRequired
				}
				if want == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if codeOf(t, err) != want {
					t.Fatalf("call %d: %v, want %s", i, err, want)
				}
				if !reflect.DeepEqual(before, centralTree(t, root)) {
					t.Fatalf("call %d changed tree", i)
				}
			}
		})
	}
}

func TestSDKLinkRulesDegradedAdmissionNoEffects(t *testing.T) {
	for _, broken := range []string{"none", "malformed", "unsupported", "lost", "read failure"} {
		t.Run(broken, func(t *testing.T) {
			c, root, path, _ := linkOptionsFixture(t)
			switch broken {
			case "malformed":
				if err := os.WriteFile(path, []byte("{"), 0640); err != nil {
					t.Fatal(err)
				}
			case "unsupported":
				if err := os.WriteFile(path, []byte(`{"schemaVersion":999}`), 0640); err != nil {
					t.Fatal(err)
				}
			case "lost":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "registry.initialized"), []byte("1"), 0600); err != nil {
					t.Fatal(err)
				}
			case "read failure":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0750); err != nil {
					t.Fatal(err)
				}
			}
			opts := core.ProjectLinkRulesOptions{AllowDegradedRegistry: true}
			before := centralTree(t, root)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			calls := []func() error{
				func() error { _, e := c.ReadProjectLinkRulesWithOptions(ctx, "target", opts); return e },
				func() error {
					_, e := c.WriteProjectLinkRulesWithOptions(ctx, "target", linkOptionsRules(), false, opts)
					return e
				},
				func() error { _, e := c.DeleteProjectLinkRulesWithOptions(ctx, "target", true, opts); return e },
			}
			for _, call := range calls {
				if err := call(); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			if broken == "none" {
				invalid := core.ProjectLinkRules{Links: []core.ProjectLink{{LabelKey: "x", Template: "missing"}}}
				if _, err := c.WriteProjectLinkRulesWithOptions(context.Background(), "target", invalid, false, opts); codeOf(t, err) != projmux.CodeInvalidLinkRules {
					t.Fatal(err)
				}
			} else {
				for _, call := range []func() error{
					func() error {
						_, e := c.ReadProjectLinkRulesWithOptions(context.Background(), "target", opts)
						return e
					},
					func() error {
						_, e := c.WriteProjectLinkRulesWithOptions(context.Background(), "target", linkOptionsRules(), false, opts)
						return e
					},
					func() error {
						_, e := c.DeleteProjectLinkRulesWithOptions(context.Background(), "target", true, opts)
						return e
					},
				} {
					if err := call(); codeOf(t, err) != projmux.CodeRegistryUnavailable {
						t.Fatal(err)
					}
				}
			}
			if !reflect.DeepEqual(before, centralTree(t, root)) {
				t.Fatal("pre-admission failure changed tree")
			}
		})
	}
}
