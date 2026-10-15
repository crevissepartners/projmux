package projmux

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"github.com/crevissepartners/projmux/internal/services/approvals"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSDKUsagePathsIgnoreCLIEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	now := time.Now().UTC().Truncate(time.Second)
	state := filepath.Join(root, ".local", "state", "projmux")
	defaultDir := filepath.Join(state, "usage")
	explicitDir, envDir := filepath.Join(root, "explicit"), filepath.Join(root, "cli-only")
	for i, dir := range []string{defaultDir, explicitDir, envDir} {
		store := usage.NewStore(dir)
		if err := store.SaveState(usage.State{Snapshots: []usage.Snapshot{{Model: "claude", Window: usage.WindowWeekly, Pct: float64((i + 1) * 10), UpdatedAt: now}}}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendHistory([]usage.MetricPoint{{Provider: "claude", Name: "usage.percent", Window: "weekly", Value: float64((i + 1) * 10), ObservedAt: now}}, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, envDir := range []string{"", envDir, "relative-cli-path"} {
		t.Setenv("PROJMUX_USAGE_STATE_DIR", envDir)
		for _, tc := range []struct {
			options Options
			want    float64
		}{
			{Options{}, 10},
			{Options{StateDir: state}, 10},
			{Options{UsageStateDir: explicitDir}, 20},
			{Options{StateDir: state, UsageStateDir: explicitDir}, 20},
		} {
			c, err := NewClient(tc.options)
			if err != nil {
				t.Fatal(err)
			}
			cache, err := c.GetUsage(context.Background())
			if err != nil || len(cache.Snapshots) != 1 || float64(cache.Snapshots[0].Pct) != tc.want {
				t.Fatalf("options=%+v env=%q cache=%+v err=%v", tc.options, envDir, cache, err)
			}
			history, err := c.GetUsageHistory(context.Background(), core.UsageHistoryFilter{Now: now})
			if err != nil || len(history) != 1 || history[0].Value != tc.want {
				t.Fatalf("options=%+v env=%q history=%+v err=%v", tc.options, envDir, history, err)
			}
		}
		if got := os.Getenv("PROJMUX_USAGE_STATE_DIR"); got != envDir {
			t.Fatalf("client changed CLI env to %q", got)
		}
	}
	if _, err := NewClient(Options{UsageStateDir: "relative"}); err == nil || !strings.Contains(err.Error(), "UsageStateDir") {
		t.Fatal("relative UsageStateDir accepted", err)
	}
}

func TestSDKApprovalRefusalCodesDeclared(t *testing.T) {
	// Derive the declared public Code values from source, so an unannounced wire
	// token cannot silently become a new SDK contract.
	declared := map[Code]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				v := spec.(*ast.ValueSpec)
				typ, ok := v.Type.(*ast.Ident)
				if !ok || typ.Name != "Code" {
					continue
				}
				for _, value := range v.Values {
					if lit, ok := value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						declared[Code(s)] = true
					}
				}
			}
		}
	}
	cases := []struct {
		err  error
		want Code
	}{
		{&agentcontrol.Refusal{Code: "stale-epoch"}, CodeControlStaleEpoch},
		{&agentcontrol.Refusal{Code: "stale-binding"}, CodeControlStaleBinding},
		{&agentcontrol.Refusal{Code: "unavailable"}, CodeControlUnavailable},
		{&agentcontrol.Refusal{Code: "invalid-frame"}, CodeControlInvalidFrame},
		{&agentcontrol.Refusal{Code: "ambiguous-request"}, CodeControlAmbiguousRequest},
		{&agentcontrol.Refusal{Code: "unsafe-decision"}, CodeControlUnsafeDecision},
		{&agentcontrol.Refusal{Code: "future-private-code"}, CodePermissionControlUnavailable},
		{&agentcontrol.BindingError{Reason: "missing"}, CodePermissionControlUnavailable},
	}
	for _, code := range []Code{CodePermissionNotFound, CodePermissionNotPending, CodePermissionExpired, CodePermissionAnsweringOff, CodePermissionProviderUnsupported, CodePermissionDecisionUnavailable, CodeOperatorClientInvalid} {
		cases = append(cases, struct {
			err  error
			want Code
		}{&approvals.Failure{Code: string(code), Message: "refused"}, code})
	}
	for _, tc := range cases {
		got := approvalRefusal(fmt.Errorf("recovery: %w", tc.err))
		var r *Refusal
		wantMessage := fmt.Errorf("recovery: %w", tc.err).Error()
		if !errors.As(got, &r) || r.Code != tc.want || !declared[r.Code] || got.Error() != wantMessage {
			t.Fatalf("%v -> %#v, expected declared %q", tc.err, got, tc.want)
		}
	}
	if got := approvalRefusal(context.Canceled); got != context.Canceled {
		t.Fatal("cancellation mapped", got)
	}
}

func TestSDKResourceNonRegistryErrorsRemainOrdinary(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("runtime reader unavailable")} {
		if got := resourceRefusal(err); got != err {
			t.Fatalf("%v changed to %v", err, got)
		}
	}
}

func TestSelectorInputErrorPreservesResolutionAndCause(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		&selector.SelectorError{Op: "resolve agent", Detail: `--agent "missing" matched nothing`, Want: selector.CardinalityExactOne, Got: 0},
		&selector.SelectorError{Op: "resolve window", Detail: `--window "main" matched too many`, Want: selector.CardinalityExactOne, Got: 2},
	} {
		if got := selectorInputError(err, "core.Ref.Value"); got != err {
			t.Fatalf("changed non-parser error: %v", got)
		}
	}
	_, err := selector.ParseLabel("--selector")
	got := selectorInputError(err, "ResourceSelector.Labels")
	if err.Error() != `parse selector: --selector "--selector" must be key=value` || got.Error() != `parse selector: ResourceSelector.Labels "--selector" must be key=value` {
		t.Fatalf("original=%v mapped=%v", err, got)
	}
}
