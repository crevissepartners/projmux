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
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func sdkResourceFixture(t *testing.T) (metadata.Registry, *projmux.Client) {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "")
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", state)
	reg := getFixtureRegistry(t)
	// Add an offline Agent to cover CLI workspace and activation defaults.
	reg.Agents = []metadata.Agent{{APIVersion: metadata.APIVersion, Kind: metadata.KindAgent, Metadata: metadata.ObjectMeta{UID: "agt-alpha", Name: "reader", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "win-alpha-main"}, Labels: map[string]string{"role": "reader"}}, Spec: metadata.AgentSpec{Provider: "codex"}, Status: metadata.AgentStatus{Phase: metadata.PhaseOffline}}}
	reg.NameReservations = append(reg.NameReservations, metadata.NameReservation{Scope: "prj-alpha", Kind: metadata.KindAgent, Name: "reader", UID: "agt-alpha"})
	path := store.PathFor(filepath.Join(state, "projmux"))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{StateDir: filepath.Join(state, "projmux")})
	if err != nil {
		t.Fatal(err)
	}
	return reg, c
}
func sdkJSON(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	if err := writeJSON(&b, v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
func TestResourceSDKCLIParity(t *testing.T) {
	_, c := sdkResourceFixture(t)
	ctx := context.Background()
	for _, kind := range []core.Kind{core.KindProject, core.KindWindow, core.KindPane, core.KindAgent} {
		token := strings.ToLower(string(kind)) + "s"
		for _, scope := range []struct {
			sdk projmux.ResourceSelector
			cli []string
		}{{}, {sdk: projmux.ResourceSelector{Project: "alpha"}, cli: []string{"--project", "alpha"}}, {sdk: projmux.ResourceSelector{Windows: []string{"main"}}, cli: []string{"--window", "main"}}, {sdk: projmux.ResourceSelector{Labels: []string{"role=shell"}}, cli: []string{"--selector", "role=shell"}}, {sdk: projmux.ResourceSelector{Project: "alpha", Windows: []string{"main"}, Panes: []string{"zsh"}, Labels: []string{"role=shell"}}, cli: []string{"--project", "alpha", "--window", "main", "--pane", "zsh", "--selector", "role=shell"}}, {sdk: projmux.ResourceSelector{Labels: []string{"empty=yes"}}, cli: []string{"--selector", "empty=yes"}}} {
			if kind == core.KindProject && len(scope.sdk.Windows) > 0 || kind != core.KindPane && len(scope.sdk.Panes) > 0 {
				continue
			}
			args := append([]string{token, "-o", "json"}, scope.cli...)
			var out, stderr bytes.Buffer
			cmd := newGetCommand()
			cmd.reads = runtimeResourceReadLookup(newRuntimeDiagnosticsReader(nil))
			cliErr := cmd.Run(args, &out, &stderr)
			list, sdkErr := c.ListResources(ctx, kind, scope.sdk)
			if cliErr != nil || sdkErr != nil {
				if cliErr == nil || sdkErr == nil || cliErr.Error() != sdkErr.Error() {
					t.Fatalf("%s scoped errors CLI=%v SDK=%v", kind, cliErr, sdkErr)
				}
				continue
			}
			if got := sdkJSON(t, list); got != out.String() {
				t.Fatalf("%s list bytes differ:\n%s\n%s", kind, out.String(), got)
			}
		}
	}
	for _, ref := range []core.Ref{{Kind: core.KindProject, Value: "uid:prj-alpha"}, {Kind: core.KindWindow, Value: "uid:win-alpha-main"}, {Kind: core.KindPane, Value: "uid:pan-alpha-zsh"}, {Kind: core.KindAgent, Value: "uid:agt-alpha"}} {
		cmd := newDescribeCommand()
		cmd.reads = runtimeResourceReadLookup(newRuntimeDiagnosticsReader(nil))
		var out, stderr bytes.Buffer
		err := cmd.Run([]string{strings.ToLower(string(ref.Kind)), ref.Value, "-o", "json"}, &out, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.GetResource(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if got := sdkJSON(t, r); got != out.String() {
			t.Fatalf("%s describe bytes differ:\n%s\n%s", ref.Kind, out.String(), got)
		}
	}
}
func TestResourceSDKRefusalParity(t *testing.T) {
	_, c := sdkResourceFixture(t)
	for _, test := range []struct {
		ref  core.Ref
		code projmux.Code
	}{{core.Ref{Kind: core.KindPane, Value: "uid:missing"}, projmux.CodeResourceNotFound}, {core.Ref{Kind: core.KindWindow, Value: "main"}, projmux.CodeSelectorAmbiguous}, {core.Ref{Kind: core.KindPane, Value: "uid:"}, projmux.CodeSelectorInvalid}} {
		cmd := newDescribeCommand()
		var out, stderr bytes.Buffer
		cliErr := cmd.Run([]string{strings.ToLower(string(test.ref.Kind)), test.ref.Value, "-o", "json"}, &out, &stderr)
		_, err := c.GetResource(context.Background(), test.ref)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != test.code || cliErr == nil || sdkSelectorMessageMismatch(refusal, cliErr, "--"+strings.ToLower(string(test.ref.Kind)), "core.Ref.Value") || out.Len() != 0 || !IsUsageError(cliErr) {
			t.Fatalf("CLI=%v SDK=%v expected %s", cliErr, err, test.code)
		}
	}
	_, err := c.ListResources(context.Background(), core.KindPane, projmux.ResourceSelector{Labels: []string{"bad key"}})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid {
		t.Fatal(err)
	}
}
func TestResourceSDKDeepCopy(t *testing.T) {
	_, c := sdkResourceFixture(t)
	ctx := context.Background()
	before, err := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	want := sdkJSON(t, before)
	a := before.(core.Agent)
	a.Metadata.Labels["role"] = "changed"
	a.Metadata.OwnerRef.UID = "changed"
	a.Spec.Workspace.CWD = "changed"
	next, err := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "reader"})
	if err != nil || sdkJSON(t, next) != want {
		t.Fatal("returned resource changed next read", err)
	}
	other, err := projmux.NewClient(projmux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	next, err = other.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "reader"})
	if err != nil || sdkJSON(t, next) != want {
		t.Fatal("returned resource changed other client", err)
	}
	list, err := c.ListResources(ctx, core.KindAgent, projmux.ResourceSelector{})
	if err != nil {
		t.Fatal(err)
	}
	view := list.Items[0].(core.AgentView)
	view.Metadata.Labels["role"] = "list changed"
	next, err = c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "reader"})
	if err != nil || sdkJSON(t, next) != want {
		t.Fatal("list changed subsequent read", err)
	}
}

func TestResourceSDKMissingRegistryParity(t *testing.T) {
	_, c := sdkResourceFixture(t)
	path := store.PathFor(filepath.Join(os.Getenv("XDG_STATE_HOME"), "projmux"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A first-use registry is empty and the read creates nothing.
	list, err := c.ListResources(context.Background(), core.KindProject, projmux.ResourceSelector{})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("missing first-use Registry: %+v %v", list, err)
	}
	_, err = c.GetResource(context.Background(), core.Ref{Kind: core.KindProject, Value: "missing"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound {
		t.Fatal(err)
	}
	// A completed-write marker turns later disappearance into state loss.
	marker := filepath.Join(filepath.Dir(path), "registry.initialized")
	if err = os.WriteFile(marker, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newGetCommand()
	var out, stderr bytes.Buffer
	cliErr := cmd.Run([]string{"projects", "-o", "json"}, &out, &stderr)
	_, err = c.ListResources(context.Background(), core.KindProject, projmux.ResourceSelector{})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRegistryUnavailable || cliErr == nil || cliErr.Error() != refusal.Error() || IsUsageError(cliErr) {
		t.Fatalf("lost Registry CLI=%v SDK=%v", cliErr, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read recreated Registry", err)
	}
}
