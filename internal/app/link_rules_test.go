package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	links "github.com/crevissepartners/projmux/internal/core/projectlinks"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func linkRulesFixture(t *testing.T) (*linkRulesCommand, *projmux.Client, string, string) {
	t.Helper()
	home := t.TempDir()
	paths, err := configPaths(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	var uid string
	_, err = storemetadata.NewDefaultStore(paths).Update(func(reg *metadata.Registry) error {
		p, e := (metadata.Mutator{DirExists: func(path string) (bool, error) { info, err := os.Stat(path); return err == nil && info.IsDir(), err }}).RegisterProject(reg, metadata.RegisterProjectOptions{Root: home, Name: "sample", Labels: map[string]string{"team": "a b"}})
		uid = p.Project.Metadata.UID
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &linkRulesCommand{homeDir: func() (string, error) { return home, nil }, lookupEnv: func(string) string { return "" }}
	sdk, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := links.NewDefaultStore(paths).Path(uid)
	return c, sdk, uid, path
}
func runLinkRules(c *linkRulesCommand, args ...string) (string, error) {
	var out, stderr bytes.Buffer
	err := c.Run(args, &out, &stderr)
	return out.String(), err
}

const cliLinkRulesInput = `{"repo":["https://github.com/example"],"urls":{"wiki":"https://wiki.example/"},"links":[{"labelKey":"pr","template":"{repo}/pull/{value}"},{"labelKey":"doc","template":"{wiki}/{project.labels.team}/{value}"}]}`

func TestLinkRulesCLISDKRoundTrip(t *testing.T) {
	c, sdk, uid, path := linkRulesFixture(t)
	c.stdin = strings.NewReader(cliLinkRulesInput)
	raw, err := runLinkRules(c, "set", "sample", "-", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var preview core.ProjectLinkRulesResult
	if err := json.Unmarshal([]byte(raw), &preview); err != nil || !preview.DryRun {
		t.Fatalf("preview %s %v", raw, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote %v", err)
	}
	file := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(file, []byte(cliLinkRulesInput), 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := runLinkRules(c, "set", "uid:"+uid, "--file", file, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	shown, err := runLinkRules(c, "show", "sample", "-o", "json")
	if err != nil || saved != shown {
		t.Fatalf("show %s %v", shown, err)
	}
	got, err := sdk.ReadProjectLinkRules(context.Background(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if string(b)+"\n" != saved {
		t.Fatalf("SDK %s CLI %s", b, saved)
	}
	human, err := runLinkRules(c, "show", "sample")
	if err != nil || !strings.Contains(human, "https://github.com/example/pull/ABC-123") || !strings.Contains(human, "{project.labels.team}") {
		t.Fatalf("text %s %v", human, err)
	}
	for range 2 {
		raw, err := runLinkRules(c, "delete", "sample", "--yes", "-o", "json")
		if err != nil || strings.Contains(raw, "null") {
			t.Fatalf("delete %s %v", raw, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete left file %v", err)
	}
}
func TestLinkRulesCLIRefusals(t *testing.T) {
	c, _, _, path := linkRulesFixture(t)
	c.stdin = strings.NewReader(cliLinkRulesInput)
	if _, err := runLinkRules(c, "set", "sample"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, raw := range []string{`{"links":[{"labelKey":"x","template":"https://example.com/missing"}]}`, `{"urls":[]}`, `{"unknown":1}`, strings.Repeat(" ", links.MaxFileSize+1)} {
		c.stdin = strings.NewReader(raw)
		_, err := runLinkRules(c, "set", "sample", "--dry-run")
		if !IsUsageError(err) || !strings.Contains(err.Error(), "invalid-link-rules") {
			t.Fatalf("invalid %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("refusal wrote")
		}
	}
	_, err := runLinkRules(c, "show", "missing")
	if err == nil {
		t.Fatal("missing accepted")
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = runLinkRules(c, "delete", "sample")
	if !IsUsageError(err) || !strings.Contains(err.Error(), "requires --yes") {
		t.Fatalf("unconfirmed delete: %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != "broken" {
		t.Fatalf("unconfirmed delete changed broken file: %v", readErr)
	}
	_, err = runLinkRules(c, "show", "sample")
	if err == nil || IsUsageError(err) || !strings.Contains(err.Error(), "link-rules-unreadable") {
		t.Fatalf("broken %v", err)
	}
	if _, err := runLinkRules(c, "delete", "sample", "--yes"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{}, {"oops"}, {"show"}, {"show", "sample", "extra"}, {"show", "sample", "-o", "yaml"}, {"delete", "sample", "--dry-run"}} {
		if _, err := runLinkRules(c, args...); !IsUsageError(err) {
			t.Fatalf("args %v: %v", args, err)
		}
	}
}

func TestLinkRulesCLITerminatorPreservesDryRun(t *testing.T) {
	c, _, _, path := linkRulesFixture(t)
	// A flag-shaped filename after -- must never disable dry-run or read stdin.
	c.stdin = strings.NewReader(cliLinkRulesInput)
	_, err := runLinkRules(c, "set", "--dry-run", "--", "sample", "--dry-run=false")
	if err == nil {
		t.Fatal("flag-shaped filename was interpreted as a flag instead of opened")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminator bypassed dry-run: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing rules sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	c.stdin = strings.NewReader(cliLinkRulesInput)
	_, err = runLinkRules(c, "set", "--dry-run", "--", "sample", "--dry-run=false")
	after, readErr := os.ReadFile(path)
	if err == nil || readErr != nil || string(after) != "existing rules sentinel" {
		t.Fatalf("terminator changed existing rules: %v %v", err, readErr)
	}
	// A -- supplied as a string flag's value is not a terminator.
	_, err = runLinkRules(c, "show", "-o", "--", "sample", "--unknown")
	if !IsUsageError(err) || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("flag value consumed as terminator: %v", err)
	}
}

func TestLinkRulesCLIDegradedOptionsParity(t *testing.T) {
	c, sdk, uid, path := linkRulesFixture(t)
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := storemetadata.PathFor(paths.StateDir)
	b, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registry metadata.Registry
	if err = json.Unmarshal(b, &registry); err != nil {
		t.Fatal(err)
	}
	registry.Agents = append(registry.Agents, metadata.Agent{APIVersion: metadata.APIVersion, Kind: metadata.KindAgent, Metadata: metadata.ObjectMeta{UID: "agent-aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "dangling", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "win-aaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
	b, err = json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registryPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"show", "set", "delete"} {
		var strict string
		for _, flag := range []string{"", "--allow-degraded-registry=false"} {
			c.stdin = strings.NewReader(cliLinkRulesInput)
			args := []string{verb, "sample", "-o", "json"}
			if flag != "" {
				args = append(args, flag)
			}
			if verb == "delete" {
				args = append(args, "--yes")
			}
			out, err := runLinkRules(c, args...)
			if err == nil || out != "" {
				t.Fatalf("strict %s: %q %v", verb, out, err)
			}
			if flag == "" {
				strict = err.Error()
			} else if err.Error() != strict {
				t.Fatalf("false drift: %v / %s", err, strict)
			}
		}
	}
	opts := core.ProjectLinkRulesOptions{AllowDegradedRegistry: true}
	c.stdin = strings.NewReader(cliLinkRulesInput)
	preview, err := runLinkRules(c, "set", "sample", "-", "--dry-run", "--allow-degraded-registry", "-o", "json")
	if err != nil || !strings.Contains(preview, `"dryRun":true`) {
		t.Fatalf("preview %s %v", preview, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run wrote")
	}
	c.stdin = strings.NewReader(cliLinkRulesInput)
	saved, err := runLinkRules(c, "set", "--allow-degraded-registry", "sample", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := sdk.ReadProjectLinkRulesWithOptions(context.Background(), "uid:"+uid, opts)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(got)
	if saved != string(expected)+"\n" {
		t.Fatalf("CLI/SDK %s / %s", saved, expected)
	}
	shown, err := runLinkRules(c, "show", "sample", "--allow-degraded-registry=true", "-o", "json")
	if err != nil || shown != saved {
		t.Fatalf("show %s %v", shown, err)
	}
	if _, err = runLinkRules(c, "delete", "sample", "--allow-degraded-registry"); !IsUsageError(err) || !strings.Contains(err.Error(), "requires --yes") {
		t.Fatal(err)
	}
	// The terminator keeps a flag-shaped Project name literal.
	if _, err = runLinkRules(c, "show", "--", "--allow-degraded-registry"); err == nil {
		t.Fatal("terminator interpreted option")
	}
	if _, err = runLinkRules(c, "delete", "sample", "--yes", "--allow-degraded-registry"); err != nil {
		t.Fatal(err)
	}
	registry.Projects = append(registry.Projects, registry.Projects[0])
	duplicate, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registryPath, duplicate, 0600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"show", "set", "delete"} {
		c.stdin = strings.NewReader(cliLinkRulesInput)
		args := []string{verb, "uid:" + uid, "--allow-degraded-registry", "-o", "json"}
		if verb == "delete" {
			args = append(args, "--yes")
		}
		out, err := runLinkRules(c, args...)
		if !IsUsageError(err) || out != "" {
			t.Fatalf("duplicate %s: %q %v", verb, out, err)
		}
	}
	after, err := os.ReadFile(registryPath)
	if err != nil || !bytes.Equal(duplicate, after) {
		t.Fatalf("registry changed: %v", err)
	}
}
