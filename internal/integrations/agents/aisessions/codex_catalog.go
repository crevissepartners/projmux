package aisessions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

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

type Discovery struct {
	Sessions []SessionMeta
	Codex    CodexCatalogOutcome
}

var errMalformedCatalogPagination = errors.New("codex catalog malformed pagination")

// CodexCatalog is one initialized native catalog connection. One Discover
// invocation opens at most one connection and either consumes all of its pages
// or discards the native result before one rollout fallback.
type CodexCatalog interface {
	List(context.Context, codexappserver.CatalogQuery) (codexappserver.CatalogPage, error)
	Read(context.Context, string) (codexappserver.CatalogThread, error)
	Close() error
}

type OpenCodexCatalog func(context.Context) (CodexCatalog, error)

type defaultCodexCatalog struct{ client *codexappserver.Client }

func (c *defaultCodexCatalog) List(ctx context.Context, query codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	return c.client.ListCatalogThreads(ctx, query)
}

func (c *defaultCodexCatalog) Read(ctx context.Context, threadID string) (codexappserver.CatalogThread, error) {
	return c.client.ReadCatalogThread(ctx, threadID)
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

func discoverCodexNative(ctx context.Context, cwd string, depth int, open OpenCodexCatalog) ([]SessionMeta, error) {
	catalog, err := open(ctx)
	if err != nil {
		return nil, err
	}
	defer catalog.Close()

	query := codexappserver.CatalogQuery{}
	if depth <= 0 {
		query.CWD = cwd
	}
	seenCursors := make(map[string]struct{})
	byID := make(map[string]SessionMeta)
	for pageNo := range codexCatalogMaxPages {
		page, err := catalog.List(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, thread := range page.Threads {
			if !withinTree(thread.CWD, cwd, depth) {
				continue
			}
			id, err := codex.NormalizeResumeID(thread.ID)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid native thread id", codexappserver.ErrProtocol)
			}
			title := strings.TrimSpace(thread.Name)
			if title == "" {
				title = shortResumeID(id)
			}
			candidate := SessionMeta{
				Agent: AgentCodex, ResumeID: id, Title: title,
				LastModified: thread.RecencyAt,
				Context:      SessionContext{CWD: cleanCWD(thread.CWD), Branch: thread.Branch},
				Source:       SourceCodexAppServer, Confidence: ConfidenceHigh,
				RuntimeStatus: thread.RuntimeStatus,
			}
			if current, ok := byID[id]; !ok || candidate.LastModified.After(current.LastModified) {
				byID[id] = candidate
			}
		}
		if page.NextCursor == nil {
			break
		}
		next := *page.NextCursor
		if strings.TrimSpace(next) == "" || len(page.Threads) == 0 {
			return nil, errMalformedCatalogPagination
		}
		if _, repeated := seenCursors[next]; repeated {
			return nil, errMalformedCatalogPagination
		}
		seenCursors[next] = struct{}{}
		query.Cursor = &next
		if pageNo == codexCatalogMaxPages-1 {
			return nil, errMalformedCatalogPagination
		}
	}

	sessions := make([]SessionMeta, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].LastModified.Equal(sessions[j].LastModified) {
			return sessions[i].ResumeID < sessions[j].ResumeID
		}
		return sessions[i].LastModified.After(sessions[j].LastModified)
	})
	return sessions, nil
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
