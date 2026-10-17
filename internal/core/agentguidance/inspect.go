package agentguidance

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Part identifies an assembly input. Enabled is its effective state; an
// empty/missing user file contributes no bytes even when guidance is on.
type Part struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Present bool   `json:"present"`
	Enabled bool   `json:"enabled"`
}

// Inspection describes the current policy, ordered inputs and final body.
// Text is exactly Load's assembly (LoadProcess with process=true).
type Inspection struct {
	Disabled          bool     `json:"disabled"`
	DisabledFragments []string `json:"disabledFragments"`
	Parts             []Part   `json:"parts"`
	Text              string   `json:"text"`
	Digest            string   `json:"digest"`
	MigrationNotice   string   `json:"-"`
	Source            Source   `json:"-"`
}

// Inspect performs first-use migration and observes policy and input names
// under the same lock as CLI updates. Disabled fragments are not read.
func (s Store) Inspect(process bool) (view Inspection, err error) {
	return s.InspectContext(context.Background(), process)
}

// InspectContext preserves caller cancellation until policy lock admission.
// Once admitted, first-use migration finishes with the existing store semantics.
func (s Store) InspectContext(ctx context.Context, process bool) (view Inspection, err error) {
	err = s.withPolicyLockContext(ctx, func() error {
		notice, err := s.migrateLocked()
		if err != nil {
			return err
		}
		view.MigrationNotice = notice
		guidance, err := s.loadCurrent(process)
		if err != nil {
			return err
		}
		settings, _, err := s.LoadSettings()
		if err != nil {
			return err
		}
		view.Disabled = settings.Disabled
		view.DisabledFragments = slices.Clone(settings.DisabledFragments)
		if view.DisabledFragments == nil {
			view.DisabledFragments = []string{}
		}
		view.Text, view.Digest = string(guidance.Text), guidance.Digest
		view.Source = guidance.Source
		view.Parts = []Part{{Source: "default", Name: "built-in", Present: true, Enabled: !view.Disabled}}
		entries, err := os.ReadDir(filepath.Join(s.configDir, FragmentsDirName))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			view.Parts = append(view.Parts, Part{Source: "fragment", Name: entry.Name(), Path: filepath.Join(s.configDir, FragmentsDirName, entry.Name()), Present: true, Enabled: !view.Disabled && !slices.Contains(settings.DisabledFragments, entry.Name())})
		}
		_, err = os.Lstat(s.Path())
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		view.Parts = append(view.Parts, Part{Source: "user", Name: FileName, Path: s.Path(), Present: err == nil, Enabled: !view.Disabled})
		return nil
	})
	return view, err
}

// SetEnabled changes only policy; migration is left to Load/Inspect. An empty
// fragment selects whole guidance. Fragment controls preserve explicit or
// legacy whole off, and whole controls preserve the fragment mask.
func (s Store) SetEnabled(fragment string, enabled bool) error {
	return s.SetEnabledContext(context.Background(), fragment, enabled)
}

// SetEnabledContext preserves caller cancellation until policy lock admission.
func (s Store) SetEnabledContext(ctx context.Context, fragment string, enabled bool) error {
	if fragment != "" {
		if err := ValidateFragmentName(fragment); err != nil {
			return err
		}
	}
	return s.withPolicyLockContext(ctx, func() error {
		settings, explicit, err := s.LoadSettings()
		if err != nil {
			return err
		}
		if fragment == "" {
			settings.Disabled = !enabled
		} else {
			// Creating explicit fragment policy must not turn legacy blank-file off
			// into on. Materialize that implicit switch without migrating any bytes.
			if !explicit {
				root, err := os.OpenRoot(s.configDir)
				if err != nil {
					return err
				}
				user, exists, err := readGuidancePart(root, FileName)
				_ = root.Close()
				if err != nil {
					return err
				}
				if exists && len(bytes.TrimSpace(user)) == 0 {
					settings.Disabled = true
				}
			}
			settings.DisabledFragments = slices.DeleteFunc(settings.DisabledFragments, func(name string) bool { return name == fragment })
			if !enabled {
				settings.DisabledFragments = append(settings.DisabledFragments, fragment)
			}
		}
		return s.saveSettings(settings)
	})
}
