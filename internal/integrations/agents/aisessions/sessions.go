// Package aisessions discovers resume-capable AI agent sessions on disk.
package aisessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
)

const (
	AgentClaude      = claude.AgentName
	AgentCodex       = codex.AgentName
	AgentAntigravity = "antigravity"

	SourceClaudeTranscript = "claude-transcript"
	SourceCodexRollout     = "codex-rollout"

	sessionScanLineLimit  = 100
	sessionTitleLineLimit = 100
	codexScanFileLimit    = 80
)

// SessionContext captures project metadata associated with a resume session.
type SessionContext struct {
	CWD    string
	Branch string
}

// SessionMeta is the Phase 1 picker input contract for a resumable AI session.
type SessionMeta struct {
	Agent        string
	ResumeID     string
	Title        string
	LastModified time.Time
	Context      SessionContext
	Source       string
}

// DiscoverOptions controls where session logs are read from. Empty roots use
// the standard agent locations under HomeDir, or the process home directory
// when HomeDir is empty.
type DiscoverOptions struct {
	HomeDir string

	ClaudeProjectsDir string
	CodexSessionsDir  string

	// AntigravitySessionsDir is reserved for a future supported on-disk format.
	// Phase 0 intentionally returns no Antigravity rows because no stable local
	// session store layout has been confirmed.
	AntigravitySessionsDir string
}

// Discover returns resume sessions for cwd across supported agents, newest
// first. Missing roots, malformed JSONL records, and partial files are skipped.
func Discover(cwd string, opts DiscoverOptions) ([]SessionMeta, error) {
	cwd = cleanCWD(cwd)
	if cwd == "" {
		return nil, errors.New("discover ai sessions: cwd is empty")
	}

	opts = opts.withDefaults()
	var sessions []SessionMeta
	sessions = append(sessions, discoverClaude(cwd, opts.ClaudeProjectsDir)...)
	sessions = append(sessions, discoverCodex(cwd, opts.CodexSessionsDir)...)
	sessions = append(sessions, discoverAntigravity(cwd, opts.AntigravitySessionsDir)...)
	sessions = dedupeByResumeID(sessions)

	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].LastModified.Equal(sessions[j].LastModified) {
			if sessions[i].Agent == sessions[j].Agent {
				return sessions[i].ResumeID < sessions[j].ResumeID
			}
			return sessions[i].Agent < sessions[j].Agent
		}
		return sessions[i].LastModified.After(sessions[j].LastModified)
	})
	return sessions, nil
}

// EncodeClaudeProjectPath returns Claude Code's project directory name for cwd:
// the cleaned slash form with path separators replaced by dashes.
func EncodeClaudeProjectPath(cwd string) string {
	cleaned := filepath.ToSlash(cleanCWD(cwd))
	return strings.ReplaceAll(cleaned, "/", "-")
}

func (opts DiscoverOptions) withDefaults() DiscoverOptions {
	home := strings.TrimSpace(opts.HomeDir)
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = userHome
		}
	}
	if strings.TrimSpace(opts.ClaudeProjectsDir) == "" && home != "" {
		opts.ClaudeProjectsDir = filepath.Join(home, ".claude", "projects")
	}
	if strings.TrimSpace(opts.CodexSessionsDir) == "" && home != "" {
		opts.CodexSessionsDir = filepath.Join(home, ".codex", "sessions")
	}
	return opts
}

func discoverClaude(cwd, projectsDir string) []SessionMeta {
	projectsDir = strings.TrimSpace(projectsDir)
	if projectsDir == "" {
		return nil
	}
	dir := filepath.Join(projectsDir, EncodeClaudeProjectPath(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	sessions := make([]SessionMeta, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		details, ok := scanSessionJSONL(path, sessionScanOptions{
			targetCWD: cwd,
		})
		if !ok {
			continue
		}
		if details.cwd != "" && cleanCWD(details.cwd) != cwd {
			continue
		}
		id := details.id
		if id == "" {
			id = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		id, err = claude.NormalizeResumeID(id)
		if err != nil {
			continue
		}
		title := details.title
		if title == "" {
			title = shortResumeID(id)
		}
		sessions = append(sessions, SessionMeta{
			Agent:        AgentClaude,
			ResumeID:     id,
			Title:        title,
			LastModified: info.ModTime(),
			Context: SessionContext{
				CWD:    cwd,
				Branch: details.branch,
			},
			Source: SourceClaudeTranscript,
		})
	}
	return sessions
}

func discoverCodex(cwd, sessionsDir string) []SessionMeta {
	return discoverCodexWithFileLimit(cwd, sessionsDir, codexScanFileLimit)
}

func discoverCodexWithFileLimit(cwd, sessionsDir string, scanFileLimit int) []SessionMeta {
	sessionsDir = strings.TrimSpace(sessionsDir)
	if sessionsDir == "" {
		return nil
	}

	var candidates []sessionFileCandidate
	_ = filepath.WalkDir(sessionsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		if matched, matchErr := filepath.Match("rollout-*.jsonl", entry.Name()); matchErr != nil || !matched {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		candidates = append(candidates, sessionFileCandidate{
			path:    path,
			modTime: info.ModTime(),
		})
		return nil
	})
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	if scanFileLimit > 0 && len(candidates) > scanFileLimit {
		candidates = candidates[:scanFileLimit]
	}

	sessions := make([]SessionMeta, 0, len(candidates))
	for _, candidate := range candidates {
		details, ok := scanSessionJSONL(candidate.path, sessionScanOptions{
			targetCWD:  cwd,
			requireCWD: true,
		})
		if !ok || details.id == "" || cleanCWD(details.cwd) != cwd {
			continue
		}
		id, err := codex.NormalizeResumeID(details.id)
		if err != nil {
			continue
		}
		title := details.title
		if title == "" {
			title = shortResumeID(id)
		}
		sessions = append(sessions, SessionMeta{
			Agent:        AgentCodex,
			ResumeID:     id,
			Title:        title,
			LastModified: candidate.modTime,
			Context: SessionContext{
				CWD:    cwd,
				Branch: details.branch,
			},
			Source: SourceCodexRollout,
		})
	}
	return sessions
}

func discoverAntigravity(_ string, _ string) []SessionMeta {
	return nil
}

type sessionFileCandidate struct {
	path    string
	modTime time.Time
}

func dedupeByResumeID(sessions []SessionMeta) []SessionMeta {
	if len(sessions) < 2 {
		return sessions
	}
	latest := make(map[string]SessionMeta, len(sessions))
	for _, session := range sessions {
		id := strings.TrimSpace(session.ResumeID)
		if id == "" {
			continue
		}
		current, ok := latest[id]
		if !ok || session.LastModified.After(current.LastModified) {
			latest[id] = session
		}
	}
	deduped := make([]SessionMeta, 0, len(latest))
	for _, session := range latest {
		deduped = append(deduped, session)
	}
	return deduped
}

type sessionDetails struct {
	id     string
	title  string
	cwd    string
	branch string
}

type sessionScanOptions struct {
	targetCWD  string
	requireCWD bool
}

func scanSessionJSONL(path string, opts sessionScanOptions) (sessionDetails, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionDetails{}, false
	}
	defer f.Close()

	return scanSessionJSONLReader(f, opts)
}

func scanSessionJSONLReader(r io.Reader, opts sessionScanOptions) (sessionDetails, bool) {
	var details sessionDetails
	targetCWD := cleanCWD(opts.targetCWD)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			if lineNo >= sessionScanLineLimit {
				break
			}
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			if lineNo >= sessionScanLineLimit {
				break
			}
			continue
		}
		if details.id == "" {
			details.id = firstNestedString(fields, "sessionId", "session_id", "id")
		}
		if details.cwd == "" {
			details.cwd = firstNestedString(fields, "cwd", "current_dir", "currentDir", "project_dir", "projectDir", "project_path", "projectPath", "working_directory", "workingDirectory")
			if targetCWD != "" && details.cwd != "" && cleanCWD(details.cwd) != targetCWD {
				return sessionDetails{}, false
			}
		}
		if details.branch == "" {
			details.branch = firstNestedString(fields, "gitBranch", "git_branch", "branch")
		}
		if details.title == "" && lineNo <= sessionTitleLineLimit {
			details.title = titleFromRecord(fields)
		}
		if details.ready(opts.requireCWD, targetCWD) {
			return details, true
		}
		if lineNo >= sessionScanLineLimit {
			break
		}
	}
	if opts.requireCWD && details.cwd == "" {
		return sessionDetails{}, false
	}
	return details, details.id != "" || details.cwd != "" || details.title != ""
}

func (details sessionDetails) ready(requireCWD bool, targetCWD string) bool {
	if details.id == "" || details.title == "" || details.branch == "" {
		return false
	}
	if targetCWD != "" && details.cwd == "" {
		return false
	}
	return !requireCWD || details.cwd != ""
}

func titleFromRecord(fields map[string]any) string {
	recordType := strings.ToLower(stringJSONField(fields, "type"))
	if recordType == "event_msg" {
		if payload, ok := fields["payload"].(map[string]any); ok {
			return cleanTitleCandidate(firstNestedString(payload, "message"))
		}
	}
	if recordType == "response_item" {
		if payload, ok := fields["payload"].(map[string]any); ok && strings.EqualFold(stringJSONField(payload, "role"), "user") {
			return cleanTitleCandidate(contentText(payload["content"]))
		}
	}
	if recordType == "user" || strings.EqualFold(stringJSONField(fields, "role"), "user") {
		if message, ok := fields["message"].(map[string]any); ok {
			return cleanTitleCandidate(contentText(message["content"]))
		}
		return cleanTitleCandidate(contentText(fields["content"]))
	}
	return ""
}

func contentText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			switch item := item.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				if text := stringJSONField(item, "text"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func firstNestedString(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringJSONField(fields, key); value != "" {
			return value
		}
	}
	for _, raw := range fields {
		nested, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if value := firstNestedString(nested, keys...); value != "" {
			return value
		}
	}
	return ""
}

func stringJSONField(fields map[string]any, key string) string {
	value, ok := fields[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func cleanTitle(title string) string {
	fields := strings.Fields(title)
	if len(fields) == 0 {
		return ""
	}
	title = strings.Join(fields, " ")
	runes := []rune(title)
	if len(runes) > 120 {
		return string(runes[:120])
	}
	return title
}

func cleanTitleCandidate(title string) string {
	title = cleanTitle(title)
	if title == "" || isNoisyTitleCandidate(title) {
		return ""
	}
	return title
}

func isNoisyTitleCandidate(title string) bool {
	title = strings.TrimSpace(title)
	return strings.HasPrefix(title, "<command-") || strings.HasPrefix(title, "# AGENTS.md")
}

func shortResumeID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func cleanCWD(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	cleaned := filepath.Clean(cwd)
	if cleaned == "." {
		return ""
	}
	return cleaned
}
