package mutations

import (
	"context"
	"fmt"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

// LabelResult contains the metadata and snapshot from the same locked commit.
type LabelResult struct {
	Metadata metadata.ObjectMeta
	Registry metadata.Registry
	Changed  bool
}

// SetLabels shares the CLI label transaction with SDK callers. Selection happens
// before admission; exact identity is checked again under the Registry lock.
func SetLabels(ctx context.Context, s Store, kind metadata.Kind, uid string, set map[string]string, remove []string) (LabelResult, error) {
	var result LabelResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if s.Update == nil || s.Mutator == nil {
		return result, Classify(fmt.Errorf("resource registry store is not configured"))
	}
	committed, err := s.Update(func(working *metadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, _, ok := resources.ResourceFor(*working, kind, uid); !ok {
			return &Failure{Code: "resource-not-found", Cause: fmt.Errorf("%s %q disappeared before the mutation ran", strings.ToLower(string(kind)), uid)}
		}
		var err error
		result.Metadata, result.Changed, err = s.Mutator().SetLabels(working, kind, uid, set, remove)
		return err
	})
	if err != nil {
		return LabelResult{}, Classify(err)
	}
	result.Registry = committed.Clone()
	return result, nil
}
