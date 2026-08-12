package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

type doctorSessionStateResumeDiagnostic struct {
	Session         string `json:"session"`
	WindowIndex     int    `json:"window_index"`
	PaneIndex       int    `json:"pane_index"`
	Agent           string `json:"agent"`
	Status          string `json:"status"`
	Confidence      string `json:"confidence"`
	ResumeSource    string `json:"resume_source,omitempty"`
	ResumeUpdatedAt string `json:"resume_updated_at,omitempty"`
	Reason          string `json:"reason,omitempty"`
	SnapshotPath    string `json:"snapshot_path,omitempty"`
}

func doctorSessionStateResumeDiagnostics() []doctorSessionStateResumeDiagnostic {
	store, err := sessionstate.NewDefaultStoreFromEnv()
	if err != nil {
		return nil
	}
	return doctorSessionStateResumeDiagnosticsFromStore(store)
}

func doctorSessionStateResumeDiagnosticsFromStore(store sessionstate.Store) []doctorSessionStateResumeDiagnostic {
	entries, err := os.ReadDir(store.Dir)
	if err != nil {
		return nil
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var diagnostics []doctorSessionStateResumeDiagnostic
	for _, entry := range entries {
		if entry == nil || entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		sessionName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		snap, err := store.LoadReadOnly(sessionName)
		if err != nil {
			continue
		}
		path, _ := store.Path(sessionName)
		for _, window := range snap.Windows {
			for _, pane := range window.Panes {
				if pane.Recipe.Kind != sessionstate.RecipeKindAgent {
					continue
				}
				health := sessionStateRecipeResumeHealth(pane.Recipe, snap.SavedAt)
				diagnostics = append(diagnostics, doctorSessionStateResumeDiagnostic{
					Session:         snap.Session,
					WindowIndex:     window.Index,
					PaneIndex:       pane.Index,
					Agent:           strings.TrimSpace(pane.Recipe.Agent),
					Status:          health.Status,
					Confidence:      health.Confidence,
					ResumeSource:    health.Source,
					ResumeUpdatedAt: health.UpdatedAt,
					Reason:          health.Reason,
					SnapshotPath:    path,
				})
			}
		}
	}
	return diagnostics
}
