package mutations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

func TestUnregisterPlanChangeAndAdmission(t *testing.T) {
	for _, change := range []bool{true, false} {
		t.Run(map[bool]string{true: "changed", false: "canceled-after-admission"}[change], func(t *testing.T) {
			s := NewStore(filepath.Join(t.TempDir(), "metadata", "registry.json"))
			result, e := RegisterProject(context.Background(), s, metadata.RegisterProjectOptions{Root: t.TempDir(), Name: "project", DefaultShell: "/bin/sh"})
			if e != nil {
				t.Fatal(e)
			}
			reg, _, e := s.UpdateConvergent(func(*metadata.Registry) error { return nil })
			if e != nil {
				t.Fatal(e)
			}
			resolution := selector.Resolution{Kind: metadata.KindProject, Matches: []selector.Match{{Kind: metadata.KindProject, UID: result.Project.Metadata.UID, Name: "project"}}}
			plan := BuildDeletePlan(reg, metadata.KindProject, resolution)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			commits := 0
			out, e := Unregister(ctx, plan, resolution, UnregisterOptions{Via: DeletionViaSDK, Mutate: func(_ metadata.Kind, _ []string, apply func(*metadata.Registry, metadata.Mutator) error) error {
				cancel()
				_, err := s.Update(func(r *metadata.Registry) error {
					if change {
						r.Windows = nil
						r.Panes = nil
						r.NameReservations = slices.DeleteFunc(r.NameReservations, func(n metadata.NameReservation) bool { return n.Kind != metadata.KindProject })
					}
					if err := apply(r, s.Mutator()); err != nil {
						return err
					}
					commits++
					return nil
				})
				return err
			}})
			if change {
				var f *Failure
				if !errors.As(e, &f) || f.Code != "unregister-plan-changed" || commits != 0 || out.Committed || !strings.Contains(e.Error(), "nothing was deleted") {
					t.Fatalf("%+v %v commits=%d", out, e, commits)
				}
				current, _, err := s.UpdateConvergent(func(*metadata.Registry) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				if len(current.Projects) != 1 || len(current.Windows) != 1 {
					t.Fatal("plan refusal changed Registry")
				}
			} else if e != nil || !out.Committed || commits != 1 {
				t.Fatalf("admitted canceled operation: %+v %v", out, e)
			}
		})
	}
}

func TestDeletionViaVocabularyAndBestEffort(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/design/resource-metadata-model.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, via := range []DeletionVia{DeletionViaCLI, DeletionViaUI, DeletionViaPrune, DeletionViaSDK} {
		if !bytes.Contains(doc, []byte("`"+string(via)+"`")) {
			t.Fatalf("via missing from design: %q", via)
		}
	}
	var warning bytes.Buffer
	RecordDeletion(func() (string, error) { return t.TempDir(), nil }, NewDeletionRecord("unregister-project", "", DeletionViaSDK, DeletionActor{}, nil, nil), &warning)
	if warning.String() != "deletion not recorded: operation-id-unavailable\n" {
		t.Fatal(warning.String())
	}
	if err := RequireProjectLifecyclePlan(metadata.ProjectLifecyclePlan{}, metadata.ProjectLifecycleOperationDeleteProject, metadata.ProjectUIDRemoved, metadata.ProjectDescendantUIDsRemoved, metadata.ProjectStartupWriteDeleteProjectGraph); err == nil {
		t.Fatal("invalid lifecycle accepted")
	}
}
