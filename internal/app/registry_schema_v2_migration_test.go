package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestV012MigrationLeavesNoMaterializeRefusals(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../core/metadata/testdata/registry-v012-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source coremetadata.Registry
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	uid := 0
	migrated, ran, report, err := coremetadata.MigrateRegistryWithEnvironment(nil, source, coremetadata.MigrationEnvironment{
		DirectoryExists: func(path string) (bool, error) { return path == "/tmp", nil },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			uid++
			return fmt.Sprintf("%s-materialize-%02d", strings.ToLower(string(kind)), uid), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ran || len(report.Repairs) != 4 || report.InformationLossCount() != 1 {
		t.Fatalf("migration report = %s", report.String())
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("repaired 0.12 Registry is invalid: %v", err)
	}
	for _, project := range migrated.Projects {
		window, ok := migrated.Window(project.Spec.PrimaryWindowRef)
		if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject || window.Metadata.OwnerRef.UID != project.Metadata.UID {
			t.Fatalf("Project %q has invalid primary Window chain", project.Metadata.Name)
		}
		pane, ok := migrated.Pane(window.Spec.AnchorPaneRef)
		if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindWindow || pane.Metadata.OwnerRef.UID != window.Metadata.UID || pane.Spec.Role != coremetadata.PaneRoleShell {
			t.Fatalf("Project %q has invalid primary shell chain", project.Metadata.Name)
		}
		plan := pbtPlan(t, migrated, project)
		fatal, skipped := plan.refusalScope()
		if len(fatal) != 0 || len(skipped) != 0 {
			t.Fatalf("repaired 0.12 materialize refusals: fatal=%v skipped=%v", pbtRender(fatal), pbtRender(skipped))
		}
	}
}
