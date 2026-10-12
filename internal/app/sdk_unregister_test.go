package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func unregisterCLI(t *testing.T, state string, refs []string, dry, confirm bool) (string, string, error) {
	t.Helper()
	s := store.NewStore(store.PathFor(state))
	c := newDeleteCommand()
	c.store = &resourceStore{load: s.LoadReadOnly, update: s.Update, mutator: store.DefaultMutator, stateDir: func() (string, error) { return state, nil }}
	c.lookupEnv = func(string) string { return "" }
	c.activeTarget = func() (activeTargetObserver, bool) { return activeTargetObserver{}, false }
	args := append([]string{"project"}, refs...)
	if dry {
		args = append(args, "--dry-run")
	}
	if confirm {
		args = append(args, "--yes")
	}
	var out, stderr bytes.Buffer
	err := (&unregisterCommand{delete: c}).Run(args, &out, &stderr)
	return out.String(), stderr.String(), err
}
func normalizeDeletionRows(raw string) string {
	return regexp.MustCompile(`("(?:at|operationID|via)"\s*:\s*)"[^"]*"`).ReplaceAllString(raw, `${1}"NORMALIZED"`)
}
func TestUnregisterProjectSDKCLIParity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	reg, _ := sdkResourceFixture(t)
	// The fixture covers a Project with Window/Pane/Agent descendants, plus
	// multiple retained Projects. A separate zero-window Project is also valid.
	empty := metadata.Project{APIVersion: metadata.APIVersion, Kind: metadata.KindProject, Metadata: metadata.ObjectMeta{UID: "prj-empty", Name: "empty"}, Spec: metadata.ProjectSpec{Root: t.TempDir()}}
	for i := range reg.Projects {
		reg.Projects[i].Spec.Root = t.TempDir()
	}
	reg.Projects = append(reg.Projects, empty)
	reg.NameReservations = append(reg.NameReservations, metadata.NameReservation{Scope: "", Kind: metadata.KindProject, Name: "empty", UID: "prj-empty"})
	sdkState, _, c := renameState(t, reg)
	cliState, _, _ := renameState(t, reg)
	t.Setenv("TMUX", "/poison,999,1")
	t.Setenv("TMUX_PANE", "%999")
	for _, project := range reg.Projects {
		ref := "uid:" + project.Metadata.UID
		refs := []string{ref}
		flags := resourceQueryFlags{kind: metadata.KindProject, projects: repeatedFlag(refs)}
		resolution, err := flags.resolve(selector.VerbDelete, false, reg)
		if err != nil {
			t.Fatal(err)
		}
		plan := buildDeletePlan(reg, metadata.KindProject, resolution)
		preview, err := c.UnregisterProject(context.Background(), core.UnregisterRequest{Refs: refs, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if sdkJSON(t, preview.Plan) != sdkJSON(t, mutations.PreviewDelete(mutations.DeletePlan(plan))) {
			t.Fatal("preview differs from CLI plan")
		}
		expected := bytes.Buffer{}
		if err := writeDeletePlan(&expected, "unregister project", plan, windowLiveDeletePlan{}, paneLiveDeletePlan{}, tmuxTransport{}, true, false); err != nil {
			t.Fatal(err)
		}
		dry, _, err := unregisterCLI(t, cliState, refs, true, false)
		if err != nil || dry != expected.String() {
			t.Fatalf("dry run: %q %v", dry, err)
		}
		got, err := c.UnregisterProject(context.Background(), core.UnregisterRequest{Refs: refs, Confirm: true})
		if err != nil || !got.Committed || got.OperationID == "" || got.Notice != "" {
			t.Fatalf("SDK: %+v %v", got, err)
		}
		human, warnings, err := unregisterCLI(t, cliState, refs, false, true)
		if err != nil || warnings != "" {
			t.Fatalf("CLI: %v %q", err, warnings)
		}
		sr, cr := projectRegistry(t, sdkState), projectRegistry(t, cliState)
		if normalizeProjectJSON(t, projectRegistryBytes(t, sdkState), sr) != normalizeProjectJSON(t, projectRegistryBytes(t, cliState), cr) {
			t.Fatal("Registry bytes differ")
		}
		for _, state := range []string{sdkState, cliState} {
			if _, err := os.Stat(project.Spec.Root); err != nil {
				t.Fatalf("external root removed %s: %v", state, err)
			}
		}
		sdkRows, e := os.ReadFile(filepath.Join(sdkState, deletionRecordsFile))
		if e != nil {
			t.Fatal(e)
		}
		cliRows, e := os.ReadFile(filepath.Join(cliState, deletionRecordsFile))
		if e != nil {
			t.Fatal(e)
		}
		if normalizeDeletionRows(string(sdkRows)) != normalizeDeletionRows(string(cliRows)) {
			t.Fatalf("records differ:\n%s\n%s", sdkRows, cliRows)
		}
		expectedReceipt := projectUnregisterReceipt(plan, false)
		if sdkJSON(t, got.Receipt) != sdkJSON(t, expectedReceipt) {
			t.Fatal("receipt bytes differ")
		}
		if !strings.Contains(human, cli.OperationReceipt(expectedReceipt).HumanLine()) {
			t.Fatalf("CLI receipt absent: %q", human)
		}
		var row mutations.DeletionRecord
		rows := bytes.Split(bytes.TrimSpace(sdkRows), []byte("\n"))
		if err := json.Unmarshal(rows[len(rows)-1], &row); err != nil {
			t.Fatal(err)
		}
		if got.OperationID != row.OperationID || sdkJSON(t, got.Plan.Affected) != sdkJSON(t, row.Affected) || row.Via != mutations.DeletionViaSDK || row.Actor != (mutations.DeletionActor{}) {
			t.Fatalf("record/result mismatch %+v %+v", got, row)
		}
	}
}
func TestUnregisterProjectSDKNoWriteAndRefusals(t *testing.T) {
	reg, _ := sdkResourceFixture(t)
	state, _, c := renameState(t, reg)
	before := projectRegistryBytes(t, state)
	info, err := os.Stat(store.PathFor(state))
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []core.UnregisterRequest{
		{Refs: []string{"alpha"}, DryRun: true},
		{Refs: []string{"alpha"}},
		{Refs: nil, Confirm: true},
		{Refs: []string{"alpha", "beta"}, Confirm: true},
		{Refs: []string{"missing"}, Confirm: true},
		{Refs: []string{"uid:"}, Confirm: true},
	} {
		got, err := c.UnregisterProject(context.Background(), req)
		var r *projmux.Refusal
		switch {
		case req.DryRun:
			if err != nil {
				t.Fatal(err)
			}
		case len(req.Refs) == 1 && req.Refs[0] == "alpha":
			if !errors.As(err, &r) || r.Code != projmux.CodeConfirmationRequired || len(got.Plan.Targets) != 1 {
				t.Fatalf("%+v %v", got, err)
			}
		default:
			if !errors.As(err, &r) {
				t.Fatalf("untyped error %v", err)
			}
			_, _, cliErr := unregisterCLI(t, state, req.Refs, false, true)
			if cliErr == nil {
				t.Fatal("CLI accepted refused SDK request")
			}
			if len(req.Refs) == 1 && req.Refs[0] == "missing" && (r.Code != projmux.CodeResourceNotFound || r.Error() != cliErr.Error()) {
				t.Fatalf("selection differs: %v %v", r, cliErr)
			}
			if len(req.Refs) != 1 && (!strings.Contains(r.Error(), "UnregisterRequest.Refs") || r.Code != projmux.CodeSelectorInvalid) {
				t.Fatal(r)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.UnregisterProject(ctx, core.UnregisterRequest{Refs: []string{"alpha"}, Confirm: true}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	next, e := os.Stat(store.PathFor(state))
	if e != nil {
		t.Fatal(e)
	}
	if before != projectRegistryBytes(t, state) || !next.ModTime().Equal(info.ModTime()) {
		t.Fatal("refusal/dry run wrote Registry")
	}
	if _, err := os.Stat(filepath.Join(state, deletionRecordsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record written: %v", err)
	}
}
