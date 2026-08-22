package codexappserver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const DefaultCatalogPageSize uint32 = 100

// CatalogQuery is the content-free thread/list surface used by the resume
// catalog. CWD is exact when non-empty; a caller implementing tree-depth
// filtering leaves it empty and applies that filter to the returned metadata.
type CatalogQuery struct {
	Cursor *string
	CWD    string
}

// CatalogThread is the safe subset of a native thread. It deliberately omits
// preview and turns so prompt/transcript content cannot reach picker metadata.
type CatalogThread struct {
	ID            string
	CWD           string
	Name          string
	Branch        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RecencyAt     time.Time
	RuntimeStatus string
	ActiveFlags   []string
}

type CatalogPage struct {
	Threads    []CatalogThread
	NextCursor *string
}

// ListCatalogThreads requests one exact opaque-cursor page. The filter is
// intentional: non-archived top-level interactive/app-server conversations,
// ordered by provider recency newest-first. Sub-agent source kinds are not
// promoted into the user Resume Picker.
func (c *Client) ListCatalogThreads(ctx context.Context, query CatalogQuery) (CatalogPage, error) {
	params := threadListParams{
		Cursor: query.Cursor, Limit: DefaultCatalogPageSize,
		SortKey: "recency_at", SortDirection: "desc",
		SourceKinds: []string{"cli", "vscode", "appServer"},
		Archived:    false,
	}
	if cwd := strings.TrimSpace(query.CWD); cwd != "" {
		params.CWD = filepath.Clean(cwd)
	}
	var result threadListResult
	if err := c.Request(ctx, methodThreadList, params, &result); err != nil {
		return CatalogPage{}, err
	}
	page := CatalogPage{NextCursor: cloneStringPointer(result.NextCursor), Threads: make([]CatalogThread, 0, len(result.Data))}
	for _, raw := range result.Data {
		thread, err := normalizeCatalogThread(raw)
		if err != nil {
			return CatalogPage{}, err
		}
		page.Threads = append(page.Threads, thread)
	}
	return page, nil
}

// ReadDefaultCatalogThread performs one bounded probe-only validation of an
// exact candidate. Session State autosave can call this from a quiet background
// path, so it must never start, restart, or otherwise own the shared daemon.
// It never substitutes a returned id or loads turns.
func ReadDefaultCatalogThread(ctx context.Context, projmuxVersion, threadID string) (CatalogThread, error) {
	readCtx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()
	client, err := OpenDefaultProxy(readCtx, DefaultProbeTimeout, projmuxVersion)
	if err != nil {
		return CatalogThread{}, err
	}
	defer client.Close()
	return client.ReadCatalogThread(readCtx, threadID)
}

// ReadCatalogThread validates one exact bound thread without loading turns or
// subscribing to it. The provider must echo the requested identity.
func (c *Client) ReadCatalogThread(ctx context.Context, threadID string) (CatalogThread, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return CatalogThread{}, fmt.Errorf("%w: thread/read requires thread id", ErrProtocol)
	}
	var result threadReadResult
	if err := c.Request(ctx, methodThreadRead, threadReadParams{ThreadID: threadID, IncludeTurns: false}, &result); err != nil {
		return CatalogThread{}, err
	}
	thread, err := normalizeCatalogThread(result.Thread)
	if err != nil {
		return CatalogThread{}, err
	}
	if thread.ID != threadID {
		return CatalogThread{}, fmt.Errorf("%w: thread/read returned a different thread", ErrProtocol)
	}
	return thread, nil
}

func normalizeCatalogThread(raw wireCatalogThread) (CatalogThread, error) {
	id := strings.TrimSpace(raw.ID)
	cwd := strings.TrimSpace(raw.CWD)
	status := strings.TrimSpace(raw.Status.Type)
	if id == "" || cwd == "" || !filepath.IsAbs(cwd) || raw.CreatedAt <= 0 || raw.UpdatedAt <= 0 {
		return CatalogThread{}, fmt.Errorf("%w: malformed thread metadata", ErrProtocol)
	}
	switch status {
	case "notLoaded", "idle", "systemError":
		if len(raw.Status.ActiveFlags) != 0 {
			return CatalogThread{}, fmt.Errorf("%w: inactive thread returned active flags", ErrProtocol)
		}
	case "active":
		for _, flag := range raw.Status.ActiveFlags {
			if flag != "waitingOnApproval" && flag != "waitingOnUserInput" {
				return CatalogThread{}, fmt.Errorf("%w: unknown active thread flag", ErrProtocol)
			}
		}
	default:
		return CatalogThread{}, fmt.Errorf("%w: unknown thread status", ErrProtocol)
	}
	recency := raw.UpdatedAt
	if raw.RecencyAt != nil {
		if *raw.RecencyAt <= 0 {
			return CatalogThread{}, fmt.Errorf("%w: malformed thread recency", ErrProtocol)
		}
		recency = *raw.RecencyAt
	}
	thread := CatalogThread{
		ID: id, CWD: filepath.Clean(cwd), CreatedAt: time.Unix(raw.CreatedAt, 0).UTC(),
		UpdatedAt: time.Unix(raw.UpdatedAt, 0).UTC(), RecencyAt: time.Unix(recency, 0).UTC(),
		RuntimeStatus: status, ActiveFlags: append([]string(nil), raw.Status.ActiveFlags...),
	}
	if raw.Name != nil {
		thread.Name = strings.TrimSpace(*raw.Name)
	}
	if raw.GitInfo != nil && raw.GitInfo.Branch != nil {
		thread.Branch = strings.TrimSpace(*raw.GitInfo.Branch)
	}
	return thread, nil
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
