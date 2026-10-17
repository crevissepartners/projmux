package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// PatchStatusbarDefaults replaces only the supplied visibility entries. Unknown
// document fields, unknown leaves and invalid untouched values survive. It is
// an atomic replacement, not a transaction with concurrent writers.
// The caller validates keys and values before calling.
func PatchStatusbarDefaults(ctx context.Context, path string, patch map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(patch) == 0 {
		return nil
	}
	if path == "" {
		return ErrHomeDirRequired
	}
	doc := map[string]json.RawMessage{}
	// #nosec G304 -- path is the captured central configuration path.
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read status bar defaults: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(content, &doc); err != nil {
			return fmt.Errorf("parse status bar defaults: %w", err)
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
	}
	values := map[string]json.RawMessage{}
	if raw, ok := doc["visibility"]; ok {
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("parse status bar visibility: %w", err)
		}
		if values == nil {
			values = map[string]json.RawMessage{}
		}
	}
	for key, value := range patch {
		values[key], _ = json.Marshal(value)
	}
	doc["visibility"], err = json.Marshal(values)
	if err != nil {
		return err
	}
	content, err = json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeFileAtomic(path, append(content, '\n'))
}
