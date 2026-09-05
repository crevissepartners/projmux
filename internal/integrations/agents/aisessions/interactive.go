package aisessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	InteractiveCatalogPageBudget = 3
	InteractiveCatalogTotalPages = 12
	previewReadBytes             = 256 * 1024
	previewExcerptBytes          = 4000
)

// ProviderDiscovery is one provider's bounded row set plus the typed Codex
// source decision. Codex is zero-valued for the other providers.
type ProviderDiscovery struct {
	Sessions      []SessionMeta
	Codex         CodexCatalogOutcome
	Continuation  CodexCatalogContinuation
	MoreNotLoaded bool
}

// DiscoverProviderContext is the provider-isolated interactive discovery
// adapter. It lets the picker launch all providers concurrently and publish a
// failure or row set independently. limit is both the visible result budget and
// the native early-stop budget.
func DiscoverProviderContext(ctx context.Context, provider, cwd string, opts DiscoverOptions, limit int) (ProviderDiscovery, error) {
	cwd = cleanCWD(cwd)
	if cwd == "" {
		return ProviderDiscovery{}, errors.New("discover ai sessions: cwd is empty")
	}
	opts = opts.withDefaults()
	depth := max(opts.Depth, 0)
	var sessions []SessionMeta
	var codexOutcome CodexCatalogOutcome
	var moreNotLoaded bool
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case AgentClaude:
		sessions, moreNotLoaded = discoverClaude(ctx, cwd, opts.ClaudeProjectsDir, depth, limit)
		if opts.retainClaudeMatches {
			return ProviderDiscovery{Sessions: finalizeProviderSessions(sessions, 0, opts.DeferTurns), MoreNotLoaded: moreNotLoaded}, nil
		}
	case AgentCodex:
		codexOutcome = CodexCatalogOutcome{Source: CatalogSourceFallback, Confidence: CatalogConfidenceMedium}
		if opts.OpenCodexCatalog == nil {
			sessions = discoverCodexContext(ctx, cwd, opts.CodexSessionsDir, depth)
		} else {
			var native []SessionMeta
			var continuation CodexCatalogContinuation
			var moreNotLoaded bool
			var err error
			if limit <= 0 {
				native, err = discoverCodexNativeBounded(ctx, cwd, depth, opts.OpenCodexCatalog, InteractiveCatalogPageBudget, 0)
			} else {
				native, continuation, moreNotLoaded, err = discoverCodexNativeInteractive(ctx, cwd, depth, opts.OpenCodexCatalog, limit)
			}
			if err == nil {
				sessions = native
				codexOutcome = CodexCatalogOutcome{Source: CatalogSourceNative, Confidence: CatalogConfidenceHigh}
				result := ProviderDiscovery{Sessions: finalizeProviderSessions(sessions, limit, opts.DeferTurns), Codex: codexOutcome, Continuation: continuation, MoreNotLoaded: moreNotLoaded}
				return result, nil
			}
			sessions = discoverCodexContext(ctx, cwd, opts.CodexSessionsDir, depth)
			reason := codexCatalogFallbackReason(err)
			for i := range sessions {
				sessions[i].Confidence = ConfidenceMedium
				sessions[i].Reason = string(reason)
			}
			codexOutcome.Reason = reason
		}
	case AgentAntigravity:
		sessions = append(sessions, discoverAntigravityCurrentStorage(cwd, opts.AntigravityCacheDir, opts.AntigravityConversationsDir, depth)...)
		sessions = append(sessions, discoverAntigravityHistory(cwd, opts.AntigravityHistoryPath, depth)...)
	default:
		return ProviderDiscovery{}, fmt.Errorf("unsupported session provider %q", provider)
	}
	sessions = finalizeProviderSessions(sessions, limit, opts.DeferTurns)
	return ProviderDiscovery{Sessions: sessions, Codex: codexOutcome, MoreNotLoaded: moreNotLoaded}, nil
}

func finalizeProviderSessions(sessions []SessionMeta, limit int, deferTurns bool) []SessionMeta {
	sessions = dedupeByResumeID(sessions)
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].LastModified.Equal(sessions[j].LastModified) {
			return sessions[i].ResumeID < sessions[j].ResumeID
		}
		return sessions[i].LastModified.After(sessions[j].LastModified)
	})
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	if !deferTurns {
		enrichTurns(sessions)
	}
	return sessions
}

type catalogPreviewReader interface {
	ReadPreview(context.Context, string) (codexappserver.CatalogPreview, error)
}

// Preview is invocation-local content for one exact SessionMeta. It never
// mutates the session metadata and reads a bounded tail for local transcripts.
type Preview struct {
	User      string
	Assistant string
}

func ReadPreview(ctx context.Context, session SessionMeta, open OpenCodexCatalog) (Preview, error) {
	if strings.TrimSpace(session.ResumeID) == "" {
		return Preview{}, errors.New("preview session id is empty")
	}
	if session.Agent == AgentCodex && session.Source == SourceCodexAppServer {
		if open == nil {
			return Preview{}, errors.New("native preview unavailable")
		}
		catalog, err := open(ctx)
		if err != nil {
			return Preview{}, err
		}
		defer catalog.Close()
		reader, ok := catalog.(catalogPreviewReader)
		if !ok {
			return Preview{}, errors.New("native preview unsupported")
		}
		projection, err := reader.ReadPreview(ctx, session.ResumeID)
		if err != nil {
			return Preview{}, err
		}
		if projection.ThreadID != session.ResumeID {
			return Preview{}, errors.New("native preview identity mismatch")
		}
		return Preview{User: projection.User, Assistant: projection.Assistant}, nil
	}
	if session.sourcePath == "" {
		return Preview{}, errors.New("preview unavailable")
	}
	return readLocalPreview(ctx, session)
}

func readLocalPreview(ctx context.Context, session SessionMeta) (Preview, error) {
	f, err := os.Open(session.sourcePath)
	if err != nil {
		return Preview{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Preview{}, err
	}
	start := max(info.Size()-previewReadBytes, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return Preview{}, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	preview := Preview{}
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return Preview{}, ctx.Err()
		default:
		}
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		role, text := previewRecord(record, session)
		text = boundedExcerpt(text)
		switch role {
		case "user":
			preview.User = text
		case "assistant":
			preview.Assistant = text
		}
	}
	if err := scanner.Err(); err != nil {
		return Preview{}, err
	}
	if preview.User == "" && preview.Assistant == "" {
		return Preview{}, errors.New("preview unavailable")
	}
	return preview, nil
}

func previewRecord(record map[string]any, session SessionMeta) (string, string) {
	if session.Agent == AgentAntigravity {
		if stringJSONField(record, "conversationId") != session.ResumeID {
			return "", ""
		}
		return "user", stringJSONField(record, "display")
	}
	typ := strings.ToLower(stringJSONField(record, "type"))
	if typ == "event_msg" {
		payload, _ := record["payload"].(map[string]any)
		switch strings.ToLower(stringJSONField(payload, "type")) {
		case "user_message":
			return "user", firstNestedString(payload, "message")
		case "agent_message":
			return "assistant", firstNestedString(payload, "message")
		}
	}
	if typ == "user" || typ == "assistant" {
		message, _ := record["message"].(map[string]any)
		return typ, contentText(message["content"])
	}
	return "", ""
}

func boundedExcerpt(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= previewExcerptBytes {
		return value
	}
	cut := previewExcerptBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func FormatPreview(preview Preview) string {
	var out strings.Builder
	if preview.User != "" {
		fmt.Fprintf(&out, "User\n%s", preview.User)
	}
	if preview.Assistant != "" {
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		fmt.Fprintf(&out, "Assistant\n%s", preview.Assistant)
	}
	return out.String()
}
