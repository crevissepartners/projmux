// Package aisessions discovers resume-capable AI agent sessions on disk.
package aisessions

import (
	"bufio"
	"encoding/json"
	"errors"
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
		details, ok := scanSessionJSONL(path)
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
		sessions = append(sessions, SessionMeta{
			Agent:        AgentClaude,
			ResumeID:     id,
			Title:        details.title,
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
	sessionsDir = strings.TrimSpace(sessionsDir)
	if sessionsDir == "" {
		return nil
	}
	var sessions []SessionMeta
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
		details, ok := scanSessionJSONL(path)
		if !ok || details.id == "" || cleanCWD(details.cwd) != cwd {
			return nil
		}
		id, err := codex.NormalizeResumeID(details.id)
		if err != nil {
			return nil
		}
		sessions = append(sessions, SessionMeta{
			Agent:        AgentCodex,
			ResumeID:     id,
			Title:        details.title,
			LastModified: info.ModTime(),
			Context: SessionContext{
				CWD:    cwd,
				Branch: details.branch,
			},
			Source: SourceCodexRollout,
		})
		return nil
	})
	return sessions
}

func discoverAntigravity(_ string, _ string) []SessionMeta {
	return nil
}

type sessionDetails struct {
	id     string
	title  string
	cwd    string
	branch string
}

func scanSessionJSONL(path string) (sessionDetails, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionDetails{}, false
	}
	defer f.Close()

	var details sessionDetails
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			continue
		}
		if details.id == "" {
			details.id = firstNestedString(fields, "sessionId", "session_id", "id")
		}
		if details.cwd == "" {
			details.cwd = firstNestedString(fields, "cwd", "current_dir", "currentDir", "project_dir", "projectDir", "project_path", "projectPath", "working_directory", "workingDirectory")
		}
		if details.branch == "" {
			details.branch = firstNestedString(fields, "gitBranch", "git_branch", "branch")
		}
		if details.title == "" {
			details.title = titleFromRecord(fields)
		}
	}
	return details, details.id != "" || details.cwd != "" || details.title != ""
}

func titleFromRecord(fields map[string]any) string {
	recordType := strings.ToLower(stringJSONField(fields, "type"))
	if recordType == "event_msg" {
		if payload, ok := fields["payload"].(map[string]any); ok {
			return cleanTitle(firstNestedString(payload, "message"))
		}
	}
	if recordType == "response_item" {
		if payload, ok := fields["payload"].(map[string]any); ok && strings.EqualFold(stringJSONField(payload, "role"), "user") {
			return cleanTitle(contentText(payload["content"]))
		}
	}
	if recordType == "user" || strings.EqualFold(stringJSONField(fields, "role"), "user") {
		if message, ok := fields["message"].(map[string]any); ok {
			return cleanTitle(contentText(message["content"]))
		}
		return cleanTitle(contentText(fields["content"]))
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
