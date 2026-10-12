package mutations

import (
	"encoding/json"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	DeletionRecordsFile         = "deletion-records.jsonl"
	DeletionRecordSchemaVersion = 1
)

// DeletionVia is the closed vocabulary of routes that reach an explicit
// deletion.
type DeletionVia string

const (
	DeletionViaCLI   DeletionVia = "cli"
	DeletionViaUI    DeletionVia = "ui"
	DeletionViaPrune DeletionVia = "prune"
	DeletionViaSDK   DeletionVia = "sdk"
)

// The closed operation vocabulary. `delete-project` is the deprecated alias of
// `unregister-project`; the record keeps the spelling the operator used.
const (
	DeletionOperationDeleteWindow      = "delete-window"
	DeletionOperationDeletePane        = "delete-pane"
	DeletionOperationDeleteAgent       = "delete-agent"
	DeletionOperationUnregisterProject = "unregister-project"
	DeletionOperationDeleteProject     = "delete-project"
	DeletionOperationPruneAgent        = "prune-agent"
	DeletionOperationPruneProject      = "prune-project"
)

// DeletionAffectedDeleted is the only affected action today.
const DeletionAffectedDeleted = "deleted"

// Reason tokens, printed as `deletion not recorded: <token>`. They are a
// closed, stable vocabulary.
const (
	DeletionSkipStateDirUnavailable  = "state-dir-unavailable"
	DeletionSkipAppendFailed         = "append-failed"
	DeletionSkipOperationIDMissing   = "operation-id-unavailable"
	DeletionNotRecordedDiagnosticFmt = "deletion not recorded: %s\n"
)

// DeletionRecord is one line of deletion-records.jsonl.
type DeletionRecord struct {
	SchemaVersion int                `json:"schemaVersion"`
	At            time.Time          `json:"at"`
	Operation     string             `json:"operation"`
	OperationID   string             `json:"operationID"`
	Via           DeletionVia        `json:"via"`
	Actor         DeletionActor      `json:"actor"`
	Targets       []DeletionTarget   `json:"targets"`
	Affected      []DeletionAffected `json:"affected"`
}

// DeletionActor is the pane-chain judgment of who ran the deletion. Every key
// is always present. Basis is `pane-chain` when the judgment succeeded, the
// creation skip token that stopped it otherwise, and empty when there was no
// ambient Pane to judge or the route never judges (UI).
type DeletionActor struct {
	AgentUID string `json:"agentUID"`
	PaneUID  string `json:"paneUID"`
	Basis    string `json:"basis"`
}

// DeletionTarget is one resource the operator named.
type DeletionTarget struct {
	Kind string `json:"kind"`
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// DeletionAffected is one resource the committed cascade removed.
type DeletionAffected struct {
	Kind   string `json:"kind"`
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

func NewDeletionRecord(operation, operationID string, via DeletionVia, actor DeletionActor,
	targets []DeletionTarget, affected []DeletionAffected,
) DeletionRecord {
	if targets == nil {
		targets = []DeletionTarget{}
	}
	if affected == nil {
		affected = []DeletionAffected{}
	}
	return DeletionRecord{
		SchemaVersion: DeletionRecordSchemaVersion,
		At:            time.Now().UTC(),
		Operation:     operation,
		OperationID:   operationID,
		Via:           via,
		Actor:         actor,
		Targets:       targets,
		Affected:      affected,
	}
}

func DeletionTargetsOf(plan DeletePlan) []DeletionTarget {
	targets := make([]DeletionTarget, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		targets = append(targets, DeletionTarget{Kind: string(plan.Kind), UID: target.Match.UID, Name: target.Match.Name})
	}
	return targets
}

// DeletionAffectedBetween is the actual cascade result: every resource of
// either root kind (Project, ControlSession) and every Window, Agent, and Pane
// present before the commit and absent after it, in kind order and then by
// uid.
func DeletionAffectedBetween(before, after coremetadata.Registry) []DeletionAffected {
	var out []DeletionAffected
	diff := func(kind coremetadata.Kind, was, is []coremetadata.ObjectMeta) {
		present := make(map[string]bool, len(is))
		for _, meta := range is {
			present[meta.UID] = true
		}
		var removed []DeletionAffected
		for _, meta := range was {
			if !present[meta.UID] {
				removed = append(removed, DeletionAffected{Kind: string(kind), UID: meta.UID, Name: meta.Name, Action: DeletionAffectedDeleted})
			}
		}
		slices.SortFunc(removed, func(a, b DeletionAffected) int { return strings.Compare(a.UID, b.UID) })
		out = append(out, removed...)
	}
	projects := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Projects))
		for _, project := range registry.Projects {
			metas = append(metas, project.Metadata)
		}
		return metas
	}
	controls := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.ControlSessions))
		for _, control := range registry.ControlSessions {
			metas = append(metas, control.Metadata)
		}
		return metas
	}
	windows := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Windows))
		for _, window := range registry.Windows {
			metas = append(metas, window.Metadata)
		}
		return metas
	}
	agents := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Agents))
		for _, agent := range registry.Agents {
			metas = append(metas, agent.Metadata)
		}
		return metas
	}
	panes := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Panes))
		for _, pane := range registry.Panes {
			metas = append(metas, pane.Metadata)
		}
		return metas
	}
	diff(coremetadata.KindProject, projects(before), projects(after))
	diff(coremetadata.KindControlSession, controls(before), controls(after))
	diff(coremetadata.KindWindow, windows(before), windows(after))
	diff(coremetadata.KindAgent, agents(before), agents(after))
	diff(coremetadata.KindPane, panes(before), panes(after))
	return out
}

type DeletionRecordLog struct {
	Path string
}

// append returns the not-recorded token, or "" once the line is durable.
func (l DeletionRecordLog) Append(record DeletionRecord) string {
	if strings.TrimSpace(l.Path) == "" {
		return DeletionSkipStateDirUnavailable
	}
	body, err := json.Marshal(record)
	if err != nil {
		return DeletionSkipAppendFailed
	}
	// The leading delimiter repairs a previous process's unterminated partial
	// tail before placing this complete row; both delimiters and the body are
	// one O_APPEND write.
	framed := make([]byte, 0, len(body)+2)
	framed = append(framed, '\n')
	framed = append(framed, body...)
	framed = append(framed, '\n')
	if err := localstate.EnsurePrivateDir(filepath.Dir(l.Path)); err != nil {
		return DeletionSkipStateDirUnavailable
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.OpenFile(l.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, localstate.PrivateFileMode)
	if err != nil {
		return DeletionSkipAppendFailed
	}
	defer file.Close()
	if _, err := file.Write(framed); err != nil {
		return DeletionSkipAppendFailed
	}
	if err := file.Sync(); err != nil {
		return DeletionSkipAppendFailed
	}
	return ""
}

// RecordDeletion is best effort after commit, preserving CLI warning tokens.
func RecordDeletion(stateDir func() (string, error), record DeletionRecord, stderr io.Writer) {
	if stateDir == nil {
		return
	}
	report := func(token string) {
		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, DeletionNotRecordedDiagnosticFmt, token)
		}
	}
	if strings.TrimSpace(record.OperationID) == "" {
		report(DeletionSkipOperationIDMissing)
		return
	}
	dir, err := stateDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		report(DeletionSkipStateDirUnavailable)
		return
	}
	if token := (DeletionRecordLog{Path: filepath.Join(dir, DeletionRecordsFile)}).Append(record); token != "" {
		report(token)
	}
}
