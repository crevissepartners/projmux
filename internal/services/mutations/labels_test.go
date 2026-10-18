package mutations

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestLabelsLockedExistenceCancellationAndStoreFailure(t *testing.T) {
	for _, mode := range []string{"disappeared", "cancel-before", "cancel-at-lock", "read", "write"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			original := metadata.Registry{Agents: []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent-one", Name: "one"}}}}
			committed := original.Clone()
			updates, mutators := 0, 0
			cause := errors.New("injected store failure")
			s := Store{Mutator: func() metadata.Mutator { mutators++; return metadata.Mutator{} }, Update: func(fn func(*metadata.Registry) error) (metadata.Registry, error) {
				updates++
				working := original.Clone()
				if mode == "read" {
					return metadata.Registry{}, cause
				}
				if mode == "disappeared" {
					working.Agents = nil
				}
				if mode == "cancel-at-lock" {
					cancel()
				}
				if err := fn(&working); err != nil {
					return metadata.Registry{}, err
				}
				if mode == "write" {
					return metadata.Registry{}, cause
				}
				committed = working
				return working, nil
			}}
			result, err := SetLabels(ctx, s, metadata.KindAgent, "agent-one", map[string]string{metadata.LabelProtected: "true"}, nil)
			if err == nil || !reflect.DeepEqual(result, LabelResult{}) || !reflect.DeepEqual(committed, original) {
				t.Fatalf("effects on refusal: %+v %v", result, err)
			}
			var f *Failure
			switch mode {
			case "disappeared":
				if !errors.As(err, &f) || f.Code != "resource-not-found" || mutators != 0 {
					t.Fatal(err)
				}
			case "cancel-before", "cancel-at-lock":
				if !errors.Is(err, context.Canceled) || mutators != 0 {
					t.Fatal(err)
				}
				if mode == "cancel-before" && updates != 0 {
					t.Fatal("opened store before admission")
				}
			default:
				if !errors.As(err, &f) || f.Code != "registry-unavailable" || !errors.Is(err, cause) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLabelsLegacyIdempotenceSnapshotAndDeletePreflight(t *testing.T) {
	for _, value := range []string{"true", "false", "", "legacy"} {
		for _, protect := range []bool{true, false} {
			t.Run(value+map[bool]string{true: "/set", false: "/remove"}[protect], func(t *testing.T) {
				reg := metadata.Registry{Agents: []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent-one", Name: "one", Labels: map[string]string{metadata.LabelProtected: value, "other": "kept"}}}}}
				before := reg.Clone()
				if err := ValidateDeleteTargets(reg, metadata.KindAgent, []string{"agent-one"}); err == nil {
					t.Fatal("legacy presence must protect")
				}
				s := Store{Mutator: func() metadata.Mutator { return metadata.Mutator{Now: func() time.Time { return time.Unix(42, 0) }} }, Update: func(fn func(*metadata.Registry) error) (metadata.Registry, error) {
					working := reg.Clone()
					if err := fn(&working); err != nil {
						return metadata.Registry{}, err
					}
					reg = working
					return reg.Clone(), nil
				}}
				var set map[string]string
				var remove []string
				if protect {
					set = map[string]string{metadata.LabelProtected: "true"}
				} else {
					remove = []string{metadata.LabelProtected}
				}
				got, err := SetLabels(context.Background(), s, metadata.KindAgent, "agent-one", set, remove)
				if err != nil || got.Changed != (!protect || value != "true") {
					t.Fatalf("%+v %v", got, err)
				}
				if !reflect.DeepEqual(got.Registry, reg) {
					t.Fatal("wrong commit projection")
				}
				expected := before.Clone()
				expected.Agents[0].Metadata.Labels = map[string]string{"other": "kept"}
				if protect {
					expected.Agents[0].Metadata.Labels[metadata.LabelProtected] = "true"
				}
				expected.UpdatedAt = reg.UpdatedAt
				if !reflect.DeepEqual(reg, expected) {
					t.Fatal("changed unrelated state")
				}
				again, err := SetLabels(context.Background(), s, metadata.KindAgent, "agent-one", set, remove)
				if err != nil || again.Changed || !reflect.DeepEqual(again.Registry, reg) {
					t.Fatalf("repeat: %+v %v", again, err)
				}
				if err := ValidateDeleteTargets(reg, metadata.KindAgent, []string{"agent-one"}); (err != nil) != protect {
					t.Fatalf("delete preflight: %v", err)
				}
				got.Registry.Agents[0].Metadata.Labels["other"] = "consumer"
				if reg.Agents[0].Metadata.Labels["other"] != "kept" {
					t.Fatal("result aliases store")
				}
			})
		}
	}
}
