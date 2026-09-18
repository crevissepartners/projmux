package app

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	coreusage "github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/web"
)

// The status reads: the notify queue, cached usage, and host load. Each goes to
// the store projmux itself renders from. None of them collects anything:
// usage in particular is read from the snapshot cache, because collection
// calls provider APIs, and a page refreshing on a timer must never do that.

func (b *webBackend) statePaths() (config.Paths, error) {
	if b.paths != nil {
		return b.paths()
	}
	return config.DefaultPathsFromEnv()
}

func (b *webBackend) Notifications(context.Context) (any, error) {
	paths, err := b.statePaths()
	if err != nil {
		return nil, err
	}
	rows, err := notify.NewDefaultStore(paths).List()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []notify.Notification{}
	}
	return map[string]any{"items": rows}, nil
}

func (b *webBackend) AckNotification(_ context.Context, id string) (any, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, " \t\r\n") {
		return nil, web.InvalidRequest("notification id is empty or has whitespace")
	}
	paths, err := b.statePaths()
	if err != nil {
		return nil, err
	}
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if err := notify.NewDefaultStore(paths).Ack(id); err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			return nil, web.NotFound("no notification " + id)
		}
		return nil, err
	}
	// `notify ack` does not tell subscribers the queue moved; the status bar
	// and every other open client would otherwise keep showing the row.
	_ = newNotifyQueueRefreshTransport(paths.StateDir).Publish()
	return map[string]any{"id": id, "acked": true}, nil
}

// webUsage is the cached usage the status bar renders: the HUD cells the bar
// draws, and the rows its popup lists. Both come from the same cache read and
// the same selection the TUI makes; a failed read is reported in Error, the
// way the popup reports it, rather than failing the request.
type webUsage struct {
	HUD         []webUsageCell        `json:"hud"`
	Rows        []webUsageCell        `json:"rows"`
	Unsupported []webUsageUnsupported `json:"unsupported,omitempty"`
	LastSync    time.Time             `json:"lastSync,omitzero"`
	// SyncSource is "last collect", or "cache mtime" when no collection time
	// was recorded.
	SyncSource string `json:"syncSource,omitempty"`
	Error      string `json:"error,omitempty"`
}

// webUsageCell is one usage window.
type webUsageCell struct {
	Model    string    `json:"model"`
	Window   string    `json:"window"`
	Pct      int       `json:"pct"`
	Used     int64     `json:"used,omitempty"`
	Limit    int64     `json:"limit,omitempty"`
	ResetsAt time.Time `json:"resetsAt,omitzero"`
	ResetIn  *int64    `json:"resetInSeconds,omitempty"`
	Updated  time.Time `json:"updatedAt,omitzero"`
	Stale    bool      `json:"stale"`
	Fallback bool      `json:"fallback,omitempty"`
}

type webUsageUnsupported struct {
	Model  string `json:"model"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

func webUsageCellOf(snap coreusage.Snapshot, window string) webUsageCell {
	cell := webUsageCell{
		Model:    usagecmd.ModelDisplayLabel(snap.Model),
		Window:   window,
		Pct:      int(math.Round(max(snap.Pct, 0))),
		ResetsAt: snap.ResetsAt,
		ResetIn:  snap.ResetInSeconds,
		Updated:  snap.UpdatedAt,
		Stale:    strings.TrimSpace(string(snap.StaleReason)) != "",
		Fallback: usagecmd.FallbackProvenance(snap),
	}
	if snap.Limit > 0 {
		cell.Used, cell.Limit = max(snap.Tokens, 0), snap.Limit
	}
	return cell
}

// Usage reads the usage cache only. Collection calls provider APIs, and a
// page refreshing on a timer must never cause that.
func (b *webBackend) Usage(context.Context) (any, error) {
	command := usagecmd.New(nil)
	out := webUsage{HUD: []webUsageCell{}, Rows: []webUsageCell{}}
	state, unsupported, cachedAt, err := command.CachedState()
	if err != nil {
		out.Error = statusbarUsageErrorSummary(err)
		return out, nil
	}
	cache := statusbarUsageStateFromCache(state, cachedAt)
	out.LastSync, out.SyncSource = cache.LastSync, cache.LastSyncSource
	// The HUD follows the web settings layer's usage visibility; a web.toml
	// that cannot be read is reported and draws no HUD cells.
	c := webSettingsCommand()
	if layer, err := loadWebSettingsLayer(c.homeDir, c.lookupEnv); err != nil {
		out.Error = web.AsError(err).Message
	} else {
		for _, snap := range usagecmd.HUDSnapshotsWithVisibility(state.Snapshots, layer.usageVisible) {
			out.HUD = append(out.HUD, webUsageCellOf(snap, string(snap.Window)))
		}
	}
	for _, snap := range statusbarUsagePopupSnapshots(state.Snapshots) {
		out.Rows = append(out.Rows, webUsageCellOf(snap, usagecmd.SnapshotWindowLabel(snap)))
	}
	for _, provider := range unsupported {
		out.Unsupported = append(out.Unsupported, webUsageUnsupported{
			Model:  provider.Model,
			Label:  statusbarUnsupportedUsageLabel(provider),
			Reason: provider.Reason,
		})
	}
	return out, nil
}

// webStatusbar is which parts of the web status bar are on: the web settings
// layer's value for each part, else the TUI's, read with the functions the
// TUI renders from.
type webStatusbar struct {
	Notifications    bool `json:"notifications"`
	Usage            bool `json:"usage"`
	Project          bool `json:"project"`
	WorkingDirectory bool `json:"workingDirectory"`
	Git              bool `json:"git"`
	Resources        bool `json:"resources"`
	Clock            bool `json:"clock"`
}

func (b *webBackend) Statusbar(context.Context) (any, error) {
	c := webSettingsCommand()
	layer, err := loadWebSettingsLayer(c.homeDir, c.lookupEnv)
	if err != nil {
		return nil, err
	}
	return webStatusbarOf(layer), nil
}

// webStatusbarOf reads the status bar parts through the web settings layer;
// resources is central and read from its own file.
func webStatusbarOf(layer webSettingsLayer) webStatusbar {
	visible := func(key string) bool {
		on, _ := layer.visible(key)
		return on
	}
	return webStatusbar{
		Notifications:    visible("statusbar.notifications"),
		Usage:            visible("statusbar.usage"),
		Project:          visible("statusbar.project"),
		WorkingDirectory: visible("statusbar.working-directory"),
		Git:              visible("statusbar.git"),
		Resources:        loadLiveResourcesMode(layer.homeDir, layer.lookupEnv) == config.LiveResourcesOn,
		Clock:            visible("statusbar.clock"),
	}
}

// webGit is the git segment for one pane's directory.
type webGit struct {
	Cwd    string `json:"cwd"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	gitWorktreeState
}

// gitReader reads git for PaneGit. A variable so a test can replace it.
var gitReader = func() *statusCommand { return newStatusCommand() }

// PaneGit reads the branch and state of the directory a pane works in. The
// directory is the one the Registry records for the pane, never one the
// request names.
func (b *webBackend) PaneGit(ctx context.Context, pane string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	cwd, err := s.paneCwd(pane)
	if err != nil {
		return nil, err
	}
	out := webGit{Cwd: cwd}
	reader := gitReader()
	branch, porcelain, ok := reader.readGitBranch(cwd)
	if !ok {
		return out, nil
	}
	out.Branch = branch
	out.gitWorktreeState = parseGitWorktreeState(porcelain)
	if root := reader.readTrimmed("git", "-C", cwd, "rev-parse", "--show-toplevel"); root != "" {
		out.Repo = filepath.Base(root)
	}
	return out, nil
}

func (s webSnapshot) paneCwd(uid string) (string, error) {
	for _, node := range s.graph.Panes {
		if node.Pane.Metadata.UID != uid {
			continue
		}
		if cwd := strings.TrimSpace(node.Pane.Spec.CWD); cwd != "" {
			return cwd, nil
		}
		for _, agent := range s.graph.Agents {
			if agent.Agent.Metadata.UID == node.AgentUID {
				if cwd := strings.TrimSpace(agent.Agent.Spec.Workspace.CWD); cwd != "" {
					return cwd, nil
				}
			}
		}
		return "", web.NewError(http.StatusConflict, web.CodeNotLive, "pane "+uid+" records no working directory")
	}
	return "", web.NotFound("no pane " + uid)
}

// webSystem is host load as the status bar shows it. A nil value is a
// platform or read that has no answer, not zero.
type webSystem struct {
	Supported     bool `json:"supported"`
	CPUPercent    *int `json:"cpuPercent"`
	MemoryPercent *int `json:"memoryPercent"`
}

func (b *webBackend) System(context.Context) (any, error) {
	if !systemstatus.Supported() {
		return webSystem{}, nil
	}
	paths, err := b.statePaths()
	if err != nil {
		return nil, err
	}
	metrics := (systemstatus.Sampler{CachePath: paths.LiveResourcesSampleFile()}).Sample()
	return webSystem{Supported: true, CPUPercent: metrics.CPUPercent, MemoryPercent: metrics.MemoryPercent}, nil
}

// UploadDir is where images pasted into the web composer are kept, beside
// the rest of projmux state.
func (b *webBackend) UploadDir() (string, error) {
	paths, err := b.statePaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, "web-uploads"), nil
}
