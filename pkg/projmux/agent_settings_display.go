package projmux

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ReadAgentDisplaySettingsFromSnapshot resolves, for display, the settings a
// plain `agent relaunch` would resolve for the Agent rows of snapshot, a
// Registry snapshot the caller holds. It reads no Registry, contacts no tmux
// server, process host or provider, and admits nothing.
//
// An empty AgentUIDs selects every Agent row; otherwise only rows with one of
// those bare UIDs (duplicates in the request are ignored). Results follow the
// snapshot's row order, one per selected row, so duplicate UID rows each get
// their own result from their own annotations. A requested UID with no row
// follows them, in UID order, with Unavailable resource-not-found. An empty or
// "uid:"-prefixed UID is a CodeSelectorInvalid refusal.
//
// A row is resolved from its UID, owner Window, provider (its declared one,
// else its conversation's), annotations and PaneRef only: the host is
// "process" when the Pane its PaneRef names is a process Pane, else "tmux",
// and its Project is the Project that owns that Window. Nothing else in the
// snapshot is read, so other rows and fields cannot change the result, and
// unselected rows and unknown UIDs plan and resolve nothing. The request is
// built with the code ReadAgentSettings and the CLI relaunch use; a Window
// whose Project is not in the snapshot only omits the Project's prompt parts.
//
// Unlike ReadAgentSettings, a missing Window or Project and instructions that
// cannot be read now do not make a row Unavailable; Cacheable is false instead
// for the latter. Unavailable is set only when the recorded profile cannot be
// resolved (the profile's own token, else relaunch-no-conversation), and for
// unknown UIDs. Cacheable is set as AgentSettingsResolution describes. Planning
// shares the agent guidance per provider and host and the label link rules per
// provider and Project among the selected rows, and writes what every launch
// plan writes: content-addressed snapshots and the guidance's one-time
// first-use migration. Their result depends on the inputs SettingsInputPaths
// names, the profiles and personas the rows' annotations name, and the
// captured state directory those snapshots are written to.
//
// snapshot is only read. Results own their values; concurrent calls are
// supported.
func (c *Client) ReadAgentDisplaySettingsFromSnapshot(ctx context.Context, snapshot core.RegistrySnapshot, request core.ReadAgentSettingsRequest) (core.ReadAgentSettingsResult, error) {
	if err := ctx.Err(); err != nil {
		return core.ReadAgentSettingsResult{}, err
	}
	for _, uid := range request.AgentUIDs {
		if strings.TrimSpace(uid) == "" || strings.HasPrefix(uid, "uid:") {
			return core.ReadAgentSettingsResult{}, &Refusal{Code: CodeSelectorInvalid, message: "ReadAgentSettingsRequest.AgentUIDs must hold bare, non-empty Agent UIDs"}
		}
	}
	if c.pathsErr != nil {
		return core.ReadAgentSettingsResult{}, resourceRefusal(c.pathsErr)
	}
	var wanted map[string]bool
	if len(request.AgentUIDs) > 0 {
		wanted = map[string]bool{}
		for _, uid := range request.AgentUIDs {
			wanted[uid] = false
		}
	}
	paths := c.paths
	reader := displaySettingsReader{snapshot: newDisplaySnapshotIndex(snapshot), paths: func() (config.Paths, error) { return paths, nil }, planner: newMemoPromptPlanner(newAgentSettingsPlanner(paths))}
	out := core.ReadAgentSettingsResult{Agents: []core.AgentSettingsResolution{}}
	for i := range snapshot.Agents {
		agent := &snapshot.Agents[i]
		if wanted != nil {
			if _, ok := wanted[agent.Metadata.UID]; !ok {
				continue
			}
			wanted[agent.Metadata.UID] = true
		}
		if err := ctx.Err(); err != nil {
			return core.ReadAgentSettingsResult{}, err
		}
		out.Agents = append(out.Agents, reader.resolve(agent))
	}
	var missing []string
	for uid, found := range wanted {
		if !found {
			missing = append(missing, uid)
		}
	}
	slices.Sort(missing)
	for _, uid := range missing {
		out.Agents = append(out.Agents, core.AgentSettingsResolution{AgentUID: uid, Unavailable: string(CodeResourceNotFound)})
	}
	if err := ctx.Err(); err != nil {
		return core.ReadAgentSettingsResult{}, err
	}
	return out, nil
}

// displaySnapshotIndex finds the first Pane, Window and Project row with a UID
// without reading any other field of the snapshot.
type displaySnapshotIndex struct {
	snapshot                core.RegistrySnapshot
	panes, windows, project map[string]int
}

func newDisplaySnapshotIndex(snapshot core.RegistrySnapshot) displaySnapshotIndex {
	index := displaySnapshotIndex{snapshot: snapshot, panes: map[string]int{}, windows: map[string]int{}, project: map[string]int{}}
	first := func(rows map[string]int, uid string, i int) {
		if _, ok := rows[uid]; !ok {
			rows[uid] = i
		}
	}
	for i := range snapshot.Panes {
		first(index.panes, snapshot.Panes[i].Metadata.UID, i)
	}
	for i := range snapshot.Windows {
		first(index.windows, snapshot.Windows[i].Metadata.UID, i)
	}
	for i := range snapshot.Projects {
		first(index.project, snapshot.Projects[i].Metadata.UID, i)
	}
	return index
}

type displaySettingsReader struct {
	snapshot displaySnapshotIndex
	paths    func() (config.Paths, error)
	planner  runtime.RelaunchPromptPlanner
}

// resolve is one row's display settings. Only the fields the ReadAgentDisplaySettingsFromSnapshot
// contract names are copied into the internal values the planner and
// resolver read.
func (r displaySettingsReader) resolve(agent *core.Agent) core.AgentSettingsResolution {
	stored := metadata.Agent{Spec: metadata.AgentSpec{Provider: agent.Spec.Provider}}
	if agent.Status.SessionRef != nil {
		stored.Status.SessionRef = &metadata.AgentSessionRef{Provider: agent.Status.SessionRef.Provider}
	}
	out := core.AgentSettingsResolution{AgentUID: agent.Metadata.UID, Provider: runtime.RelaunchAgentProvider(stored), Host: "tmux"}
	if i, ok := r.snapshot.panes[agent.Status.PaneRef]; ok && agent.Status.PaneRef != "" && r.snapshot.snapshot.Panes[i].Spec.Runtime.EffectiveKind() == core.RuntimeProcess {
		out.Host = "process"
	}
	if out.Provider != "claude" && out.Provider != "codex" {
		out.Cacheable = true
		return out
	}
	projectUID := ""
	var project metadata.Project
	if owner := agent.Metadata.OwnerRef; owner != nil {
		if i, ok := r.snapshot.windows[owner.UID]; ok {
			if root := r.snapshot.snapshot.Windows[i].Metadata.OwnerRef; root != nil && root.Kind == core.KindProject {
				projectUID = root.UID
			}
		}
	}
	if i, ok := r.snapshot.project[projectUID]; ok && projectUID != "" {
		row := r.snapshot.snapshot.Projects[i].Metadata
		project.Metadata = metadata.ObjectMeta{UID: row.UID, Name: row.Name, Labels: maps.Clone(row.Labels)}
	}
	annotations := maps.Clone(agent.Metadata.Annotations)
	target := runtime.RelaunchSettingsTarget{Provider: out.Provider, Host: out.Host, Project: project, Annotations: annotations}
	request, guidance, links := runtime.RelaunchSettingsRequest(runtime.AgentSettingsRequest{Source: metadata.SettingSourceRelaunch}, target, r.planner)
	request.ProjectUID = projectUID
	launch, err := runtime.ResolveAgentSettingsWithPaths(r.paths, out.Provider, annotations, request)
	if err != nil {
		out.Unavailable = agentSettingsUnavailableReason(err)
		return out
	}
	out.Layered = launch.Layered
	out.Current, out.New = publicAgentLaunchSettings(launch.Resolution.Current), publicAgentLaunchSettings(launch.Resolution.New)
	out.Reasons = append([]string{}, launch.Resolution.Reasons...)
	out.Cacheable = launch.InstructionsErr == nil && guidance.Unavailable == nil && links.Unavailable == nil && links.Project.Unavailable == nil
	return out
}
