package aisessions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	SourceCodexAppServer = "codex-app-server"

	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"

	ReasonAppServerUnavailable = "app-server-unavailable"
	ReasonAppServerUnsupported = "app-server-unsupported"
	ReasonAppServerTimeout     = "app-server-timeout"
	ReasonAppServerProtocol    = "app-server-protocol"
	ReasonMalformedPagination  = "malformed-pagination"

	codexCatalogMaxPages = 100
)

type CatalogSource string
type CatalogConfidence string
type CatalogReason string

const (
	CatalogSourceNative     CatalogSource     = SourceCodexAppServer
	CatalogSourceFallback   CatalogSource     = SourceCodexRollout
	CatalogConfidenceHigh   CatalogConfidence = ConfidenceHigh
	CatalogConfidenceMedium CatalogConfidence = ConfidenceMedium
	CatalogReasonNone       CatalogReason     = ""
)

// CodexCatalogOutcome is the closed invocation-level source decision. It is
// populated even when the selected source yields zero rows, so diagnostics and
// tests do not have to infer authority from row presence.
type CodexCatalogOutcome struct {
	Source     CatalogSource
	Confidence CatalogConfidence
	Reason     CatalogReason
}

var errMalformedCatalogPagination = errors.New("codex catalog malformed pagination")

// CodexCatalog is one initialized native catalog connection. One Codex provider
// discovery opens at most one connection and either consumes its bounded pages
// or discards the native result before one rollout fallback.
type CodexCatalog interface {
	List(context.Context, codexappserver.CatalogQuery) (codexappserver.CatalogPage, error)
	Read(context.Context, string) (codexappserver.CatalogThread, error)
	Close() error
}

type OpenCodexCatalog func(context.Context) (CodexCatalog, error)

// CodexCatalogContinuation owns the exact native catalog connection and
// cursor left by the interactive initial page budget. Continue consumes one
// page at a time so the picker can publish every incremental row set and stop
// immediately when its own visible-row or time budget is reached.
type CodexCatalogContinuation interface {
	Continue(context.Context) (CodexContinuationResult, error)
	Close() error
}

// CodexContinuationResult is cumulative metadata from the one native
// invocation. HasMore reports only cursor presence; Reason classifies a
// terminal continuation fault without changing the already-selected native
// authority or starting rollout fallback.
type CodexContinuationResult struct {
	Sessions []SessionMeta
	HasMore  bool
	Reason   CatalogReason
}

type defaultCodexCatalog struct{ client *codexappserver.Client }

// NewCodexCatalog wraps an already initialized, generation-routed client. The
// caller retains responsibility for selecting the exact endpoint; Close on the
// returned catalog closes that client.
func NewCodexCatalog(client *codexappserver.Client) CodexCatalog {
	if client == nil {
		return nil
	}
	return &defaultCodexCatalog{client: client}
}

func (c *defaultCodexCatalog) List(ctx context.Context, query codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	return c.client.ListCatalogThreads(ctx, query)
}

func (c *defaultCodexCatalog) Read(ctx context.Context, threadID string) (codexappserver.CatalogThread, error) {
	return c.client.ReadCatalogThread(ctx, threadID)
}

func (c *defaultCodexCatalog) ReadPreview(ctx context.Context, threadID string) (codexappserver.CatalogPreview, error) {
	return c.client.ReadCatalogPreview(ctx, threadID)
}

func (c *defaultCodexCatalog) Close() error { return c.client.Close() }

// NewDefaultCodexCatalogOpener creates the production native-user-action
// opener. Read-only health commands do not use this seam and therefore never
// start the shared daemon.
func NewDefaultCodexCatalogOpener(projmuxVersion string) OpenCodexCatalog {
	return func(ctx context.Context) (CodexCatalog, error) {
		health, err := codexappserver.EnsureDefaultProxyReady(ctx, codexappserver.TriggerNativeUserAction, projmuxVersion, true)
		if err != nil {
			return nil, err
		}
		if health.Source != codexappserver.SourceAppServer || health.Availability != codexappserver.AvailabilityAvailable {
			return nil, catalogHealthError{availability: health.Availability}
		}
		client, err := codexappserver.OpenDefaultProxy(ctx, codexappserver.DefaultProbeTimeout, projmuxVersion)
		if err != nil {
			return nil, err
		}
		return &defaultCodexCatalog{client: client}, nil
	}
}

type catalogHealthError struct{ availability codexappserver.Availability }

func (e catalogHealthError) Error() string { return "Codex catalog " + string(e.availability) }

type codexNativeCatalogState struct {
	catalog     CodexCatalog
	query       codexappserver.CatalogQuery
	seenCursors map[string]struct{}
	byID        map[string]SessionMeta
	closeOnce   sync.Once
	closeErr    error
}

func openCodexNativeCatalogState(ctx context.Context, cwd string, depth int, open OpenCodexCatalog) (*codexNativeCatalogState, error) {
	catalog, err := open(ctx)
	if err != nil {
		return nil, err
	}
	state := &codexNativeCatalogState{
		catalog: catalog, seenCursors: make(map[string]struct{}), byID: make(map[string]SessionMeta),
	}
	if depth <= 0 {
		state.query.CWD = cwd
	}
	return state, nil
}

func (s *codexNativeCatalogState) close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.catalog.Close() })
	return s.closeErr
}

// readPage validates a page transactionally: no rows from a malformed page
// are admitted into the cumulative native result.
func (s *codexNativeCatalogState) readPage(ctx context.Context, cwd string, depth int) (bool, error) {
	page, err := s.catalog.List(ctx, s.query)
	if err != nil {
		return false, err
	}
	candidates := make(map[string]SessionMeta)
	for _, thread := range page.Threads {
		if !withinTree(thread.CWD, cwd, depth) {
			continue
		}
		id, err := codex.NormalizeResumeID(thread.ID)
		if err != nil {
			return false, fmt.Errorf("%w: invalid native thread id", codexappserver.ErrProtocol)
		}
		title := strings.TrimSpace(thread.Name)
		if title == "" {
			title = shortResumeID(id)
		}
		provenance := TitleProvenanceNone
		if !titleIsResumeID(title, id) {
			provenance = TitleExplicitProvider
		}
		candidate := SessionMeta{
			Agent: AgentCodex, ResumeID: id, Title: title, TitleProvenance: provenance,
			LastModified: thread.RecencyAt,
			UpdatedAt:    thread.UpdatedAt,
			Context:      SessionContext{CWD: cleanCWD(thread.CWD), Branch: thread.Branch},
			Source:       SourceCodexAppServer, Confidence: ConfidenceHigh,
			RuntimeStatus: thread.RuntimeStatus,
		}
		if current, ok := candidates[id]; !ok || candidate.LastModified.After(current.LastModified) {
			candidates[id] = candidate
		}
	}
	hasMore := page.NextCursor != nil
	if hasMore {
		next := *page.NextCursor
		if strings.TrimSpace(next) == "" || len(page.Threads) == 0 {
			return false, errMalformedCatalogPagination
		}
		if _, repeated := s.seenCursors[next]; repeated {
			return false, errMalformedCatalogPagination
		}
		s.seenCursors[next] = struct{}{}
		s.query.Cursor = &next
	}
	for id, candidate := range candidates {
		if current, ok := s.byID[id]; !ok || candidate.LastModified.After(current.LastModified) {
			s.byID[id] = candidate
		}
	}
	return hasMore, nil
}

func (s *codexNativeCatalogState) sessions() []SessionMeta {
	sessions := make([]SessionMeta, 0, len(s.byID))
	for _, session := range s.byID {
		sessions = append(sessions, session)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].LastModified.Equal(sessions[j].LastModified) {
			return sessions[i].ResumeID < sessions[j].ResumeID
		}
		return sessions[i].LastModified.After(sessions[j].LastModified)
	})
	return sessions
}

type codexCatalogContinuation struct {
	state  *codexNativeCatalogState
	cwd    string
	depth  int
	closed atomic.Bool
}

func (c *codexCatalogContinuation) Continue(ctx context.Context) (CodexContinuationResult, error) {
	if c.closed.Load() {
		return CodexContinuationResult{}, errors.New("codex catalog continuation is closed")
	}
	hasMore, err := c.state.readPage(ctx, c.cwd, c.depth)
	result := CodexContinuationResult{Sessions: c.state.sessions(), HasMore: hasMore}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			result.Reason = codexCatalogFallbackReason(err)
		}
		c.closed.Store(true)
		_ = c.state.close()
		return result, err
	}
	if !hasMore {
		c.closed.Store(true)
		_ = c.state.close()
	}
	return result, nil
}

func (c *codexCatalogContinuation) Close() error {
	c.closed.Store(true)
	return c.state.close()
}

func discoverCodexNativeInteractive(ctx context.Context, cwd string, depth int, open OpenCodexCatalog, rowBudget int) ([]SessionMeta, CodexCatalogContinuation, bool, error) {
	state, err := openCodexNativeCatalogState(ctx, cwd, depth, open)
	if err != nil {
		return nil, nil, false, err
	}
	hasMore := false
	for range InteractiveCatalogPageBudget {
		hasMore, err = state.readPage(ctx, cwd, depth)
		if err != nil {
			_ = state.close()
			return nil, nil, false, err
		}
		if !hasMore || rowBudget > 0 && len(state.byID) >= rowBudget {
			break
		}
	}
	sessions := state.sessions()
	if !hasMore {
		_ = state.close()
		return sessions, nil, false, nil
	}
	if rowBudget > 0 && len(sessions) >= rowBudget {
		_ = state.close()
		return sessions, nil, true, nil
	}
	return sessions, &codexCatalogContinuation{state: state, cwd: cwd, depth: depth}, false, nil
}

// discoverCodexNativeBounded is the interactive catalog path. pageBudget is a
// hard call budget and rowBudget stops pagination once enough in-tree rows are
// available for the picker. A zero row budget retains the complete catalog
// behavior used by non-interactive callers.
func discoverCodexNativeBounded(ctx context.Context, cwd string, depth int, open OpenCodexCatalog, pageBudget, rowBudget int) ([]SessionMeta, error) {
	state, err := openCodexNativeCatalogState(ctx, cwd, depth, open)
	if err != nil {
		return nil, err
	}
	defer state.close()
	if pageBudget <= 0 || pageBudget > codexCatalogMaxPages {
		pageBudget = codexCatalogMaxPages
	}
	for pageNo := range pageBudget {
		hasMore, err := state.readPage(ctx, cwd, depth)
		if err != nil {
			return nil, err
		}
		if rowBudget > 0 && len(state.byID) >= rowBudget {
			break
		}
		if !hasMore {
			break
		}
		if pageNo == pageBudget-1 {
			if rowBudget > 0 {
				break
			}
			return nil, errMalformedCatalogPagination
		}
	}

	return state.sessions(), nil
}

func codexCatalogFallbackReason(err error) CatalogReason {
	var healthErr catalogHealthError
	switch {
	case errors.Is(err, errMalformedCatalogPagination):
		return CatalogReason(ReasonMalformedPagination)
	case errors.As(err, &healthErr):
		switch healthErr.availability {
		case codexappserver.AvailabilityUnsupported:
			return CatalogReason(ReasonAppServerUnsupported)
		case codexappserver.AvailabilityTimeout:
			return CatalogReason(ReasonAppServerTimeout)
		case codexappserver.AvailabilityProtocolError:
			return CatalogReason(ReasonAppServerProtocol)
		default:
			return CatalogReason(ReasonAppServerUnavailable)
		}
	case errors.Is(err, codexappserver.ErrUnsupported):
		return CatalogReason(ReasonAppServerUnsupported)
	case errors.Is(err, context.DeadlineExceeded):
		return CatalogReason(ReasonAppServerTimeout)
	case errors.Is(err, codexappserver.ErrProtocol):
		return CatalogReason(ReasonAppServerProtocol)
	default:
		return CatalogReason(ReasonAppServerUnavailable)
	}
}
