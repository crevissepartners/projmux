package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// cliPlanOwner returns the text after owner= on the CLI plan line of uid, or
// "" when that line prints no owner.
func cliPlanOwner(t *testing.T, plan, uid string) string {
	t.Helper()
	for _, line := range strings.Split(plan, "\n") {
		if strings.HasPrefix(line, " ") || !strings.Contains(line, " uid="+uid) {
			continue
		}
		_, owner, _ := strings.Cut(line, " owner=")
		return owner
	}
	t.Fatalf("CLI plan has no target line for %s:\n%s", uid, plan)
	return ""
}

// assertDeletePlanOwnerJSON checks that every target carries an owner key with
// want and that no cascade entry carries one.
func assertDeletePlanOwnerJSON(t *testing.T, plan core.DeletePlan, want string) {
	t.Helper()
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Targets []map[string]json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Targets) != 1 {
		t.Fatalf("targets = %s", b)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := wire.Targets[0]["owner"]
	if !ok || !bytes.Equal(owner, wantJSON) {
		t.Fatalf("target owner JSON = %s", b)
	}
	var descendants []map[string]json.RawMessage
	if err := json.Unmarshal(wire.Targets[0]["descendants"], &descendants); err != nil {
		t.Fatal(err)
	}
	for _, d := range descendants {
		if _, ok := d["owner"]; ok {
			t.Fatalf("cascade entry has an owner: %s", b)
		}
	}
}

func TestDeleteTargetOwnerMatchesCLIPlan(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind core.Kind
		uid  string
	}{
		{"window", core.KindWindow, "win-alpha-main"},
		{"pane", core.KindPane, "pan-alpha-log"},
		{"agent-owned-pane", core.KindPane, "pan-alpha-codex"},
		{"agent", core.KindAgent, "agt-alpha-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			_, _, client := renameState(t, store.registry)
			stdout, stderr, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), strings.ToLower(string(tc.kind)), "uid:"+tc.uid, "--dry-run")
			if err != nil {
				t.Fatalf("CLI dry run: %v %s", err, stderr)
			}
			want := cliPlanOwner(t, stdout, tc.uid)
			if want == "" {
				t.Fatalf("CLI plan printed no owner:\n%s", stdout)
			}
			got, err := client.PreflightDelete(context.Background(), core.DeleteRequest{Kind: tc.kind, Ref: "uid:" + tc.uid, DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Plan.Targets) != 1 || got.Plan.Targets[0].UID != tc.uid || got.Plan.Targets[0].Owner != want {
				t.Fatalf("SDK targets = %+v, want owner %q", got.Plan.Targets, want)
			}
			assertDeletePlanOwnerJSON(t, got.Plan, want)
		})
	}
}

func TestUnregisterProjectTargetOwnerIsEmpty(t *testing.T) {
	store := newFakeResourceStore(t)
	_, _, client := renameState(t, store.registry)
	var stdout, stderr bytes.Buffer
	if err := (&unregisterCommand{delete: newTestDeleteCommand(store, false, false, nil)}).Run([]string{"project", "uid:prj-alpha", "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("CLI dry run: %v %s", err, stderr.String())
	}
	if owner := cliPlanOwner(t, stdout.String(), "prj-alpha"); owner != "" {
		t.Fatalf("CLI Project owner = %q", owner)
	}
	got, err := client.UnregisterProject(context.Background(), core.UnregisterRequest{Ref: "uid:prj-alpha", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Plan.Targets) != 1 || got.Plan.Targets[0].Owner != "" || len(got.Plan.Targets[0].Descendants) == 0 {
		t.Fatalf("SDK targets = %+v", got.Plan.Targets)
	}
	assertDeletePlanOwnerJSON(t, got.Plan, "")
}
