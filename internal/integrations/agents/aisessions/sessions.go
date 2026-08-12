// Package aisessions discovers resume-capable AI agent sessions on disk.
//
// Symlink policy: directory traversal (the depth>0 Claude project-dir
// enumeration and the codex sessions walk) follows symlinked directories so
// sessions stored behind a symlink stay discoverable, but every directory is
// recorded in a pathGuard keyed by its resolved real path. Re-entering an
// already-visited real directory is refused, so a symlink cycle (a directory
// linking to one of its ancestors) or two symlink paths aliasing the same real
// directory can never cause an unbounded walk or scan the same file twice.
// Results are additionally deduped by ResumeID (the session identity), so a
// session reached through two paths still appears in the picker exactly once.
package aisessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
)

const (
	AgentClaude      = claude.AgentName
	AgentCodex       = codex.AgentName
	AgentAntigravity = "antigravity"

	SourceClaudeTranscript            = "claude-transcript"
	SourceCodexRollout                = "codex-rollout"
	SourceAntigravityLastConversation = "antigravity-last-conversation"
	SourceAntigravityMetadata         = "antigravity-conversation-metadata"
	SourceAntigravityHistory          = "antigravity-history"

	sessionScanLineLimit  = 100
	sessionTitleLineLimit = 100
	codexScanFileLimit    = 80
	// turnCountLineLimit bounds the deferred full-file turn count so a
	// pathological multi-hundred-MB log cannot stall the background enrich pass.
	// It sits far above the candidate scan window (sessionScanLineLimit): turn
	// counting runs in the background for displayed rows only, so it can read the
	// whole log for an accurate total while the initial render stays fast. Real
	// sessions of hundreds of turns finish well within it.
	turnCountLineLimit = 200000
	// codexScanBudgetMax caps the depth-widened codex scan budget. depth>0
	// widens the per-discovery file budget (more child cwds match), but an
	// unbounded walk would defeat the perf limit, so the budget tops out here.
	codexScanBudgetMax = 400
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

	// Turns is the count of user turns in the session log. Counting turns means
	// scanning the whole log (it defeats the cheap id/cwd/title/branch early-exit
	// of candidate discovery), so it is not paid during discovery; Discover fills
	// it only for the rows the picker displays, in a background second pass (see
	// enrichTurns) that reads the full file for an accurate total regardless of
	// session length. Zero means "unknown" (not yet enriched, or no per-turn
	// data) and renders as a blank cell.
	Turns int

	// sourcePath is the on-disk session log the turn-count enrich pass re-scans.
	// It is internal to discovery (unexported, invisible to the picker contract)
	// and empty for agents with no per-turn log, such as Antigravity.
	sourcePath string
}

// DiscoverOptions controls where session logs are read from. Empty roots use
// the standard agent locations under HomeDir, or the process home directory
// when HomeDir is empty.
type DiscoverOptions struct {
	HomeDir string

	// Depth widens discovery to sessions started in directories up to Depth
	// levels below cwd (path-tree filter on the session's recorded cwd). Zero
	// (the default) keeps the historical exact-cwd behaviour for both agents.
	Depth int

	ClaudeProjectsDir string
	CodexSessionsDir  string
	// AntigravityCacheDir contains the public v1.1.12 cache indexes. Discovery
	// reads only last_conversations.json and conversation_metadata.json.
	AntigravityCacheDir string
	// AntigravityConversationsDir contains the public conversation DB files.
	// Discovery only validates an exact <uuid>.db regular file and reads its
	// mtime; it never opens or queries the SQLite file.
	AntigravityConversationsDir string

	// AntigravityHistoryPath is the Antigravity CLI session index (JSONL). Each
	// line records one conversation's id, workspace cwd, timestamp, and first
	// message; Discover parses it through the same cwd/depth/dedup/sort pipeline
	// as the other agents. Empty resolves to ~/.gemini/antigravity-cli/history.jsonl
	// under HomeDir; a missing or malformed file yields no Antigravity rows.
	AntigravityHistoryPath string

	// DeferTurns returns candidates without their user-turn count. Turn counting
	// is the one expensive field (it reads the whole bounded scan window instead
	// of early-exiting the cheap id/cwd/title/branch pass), so a picker that
	// wants to render immediately sets this to get a fast candidate list and then
	// fills the turn column for the rows it displays with a background EnrichTurns
	// pass. The default (false) counts turns inline for every session, preserving
	// the historical fully-counted result for callers that block on Discover.
	DeferTurns bool
}

// Discover returns resume sessions for cwd across supported agents, newest
// first. Missing roots, malformed JSONL records, and partial files are skipped.
func Discover(cwd string, opts DiscoverOptions) ([]SessionMeta, error) {
	cwd = cleanCWD(cwd)
	if cwd == "" {
		return nil, errors.New("discover ai sessions: cwd is empty")
	}

	opts = opts.withDefaults()
	depth := max(opts.Depth, 0)
	var sessions []SessionMeta
	sessions = append(sessions, discoverClaude(cwd, opts.ClaudeProjectsDir, depth)...)
	sessions = append(sessions, discoverCodex(cwd, opts.CodexSessionsDir, depth)...)
	sessions = append(sessions, discoverAntigravityCurrentStorage(cwd, opts.AntigravityCacheDir, opts.AntigravityConversationsDir, depth)...)
	sessions = append(sessions, discoverAntigravityHistory(cwd, opts.AntigravityHistoryPath, depth)...)
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
	// Candidate discovery above early-exits without counting turns (the cheap
	// pass). Unless the caller defers it, fill the turn count inline now.
	if !opts.DeferTurns {
		enrichTurns(sessions)
	}
	return sessions, nil
}

// EnrichTurns fills SessionMeta.Turns for the given sessions by scanning each
// session's log for user turns, returning the same slice for convenience. A
// picker that discovered candidates with DiscoverOptions.DeferTurns calls this
// on just the rows it displays (a bounded set) from a background goroutine, so
// the expensive turn-count scan never blocks the initial render. It is a no-op
// on sessions with no per-turn log (empty sourcePath, e.g. Antigravity).
func EnrichTurns(sessions []SessionMeta) []SessionMeta {
	enrichTurns(sessions)
	return sessions
}

// enrichTurns re-scans each session's whole log for user turns and records the
// count. Turn counting is the one field that defeats the candidate early-exit
// (it must read the whole file, not just the fast candidate window), so paying
// it only for displayed rows — inline for blocking callers, or in the background
// via EnrichTurns — is what keeps discovery fast. Sessions with no per-turn log
// (empty sourcePath) and unreadable logs are left at Turns 0 (rendered blank).
func enrichTurns(sessions []SessionMeta) {
	for i := range sessions {
		if sessions[i].sourcePath == "" {
			continue
		}
		turns, ok := countUserTurns(sessions[i].sourcePath)
		if !ok {
			continue
		}
		sessions[i].Turns = turns
	}
}

// countUserTurns returns the number of user-turn records in the whole session
// log at path (bounded only by turnCountLineLimit). Unlike the candidate scan it
// extracts nothing else — each line is tested only by isUserTurnRecord — so
// reading the full file stays cheap, and it runs only in the deferred enrich
// pass for displayed rows, never during discovery. ok is false only when the
// file cannot be opened; an empty or all-malformed log yields (0, true).
func countUserTurns(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	return countUserTurnsReader(f)
}

func countUserTurnsReader(r io.Reader) (int, bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	turns := 0
	for lineNo := 0; scanner.Scan(); lineNo++ {
		if lineNo >= turnCountLineLimit {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			continue
		}
		if isUserTurnRecord(fields) {
			turns++
		}
	}
	return turns, true
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
	antigravityRoot := ""
	if historyPath := strings.TrimSpace(opts.AntigravityHistoryPath); historyPath != "" {
		// An explicit history fixture also scopes default cache/DB lookup beside
		// that fixture, keeping callers from accidentally mixing stores.
		antigravityRoot = filepath.Dir(historyPath)
	} else if home != "" {
		antigravityRoot = filepath.Join(home, ".gemini", "antigravity-cli")
		opts.AntigravityHistoryPath = filepath.Join(antigravityRoot, "history.jsonl")
	}
	if strings.TrimSpace(opts.AntigravityCacheDir) == "" && antigravityRoot != "" {
		opts.AntigravityCacheDir = filepath.Join(antigravityRoot, "cache")
	}
	if strings.TrimSpace(opts.AntigravityConversationsDir) == "" && antigravityRoot != "" {
		opts.AntigravityConversationsDir = filepath.Join(antigravityRoot, "conversations")
	}
	return opts
}

func discoverClaude(cwd, projectsDir string, depth int) []SessionMeta {
	projectsDir = strings.TrimSpace(projectsDir)
	if projectsDir == "" {
		return nil
	}
	if depth <= 0 {
		// Exact-cwd: the historical single-directory path (no behaviour change).
		return discoverClaudeDir(filepath.Join(projectsDir, EncodeClaudeProjectPath(cwd)), cwd, 0)
	}

	// depth>0: enumerate candidate project dirs by encoded-cwd prefix. The
	// encoding is lossy ('/' and '-' both become '-'), so this prefix only
	// narrows the on-disk scan; the authoritative child test is withinTree on
	// the cwd recorded inside each file (see discoverClaudeDir), which rejects
	// false-positive siblings such as "repo-other".
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	encoded := EncodeClaudeProjectPath(cwd)
	guard := newPathGuard()
	var sessions []SessionMeta
	for _, entry := range entries {
		// entryIsDir follows symlinks so a symlinked project dir still counts.
		if entry == nil || !entryIsDir(projectsDir, entry) {
			continue
		}
		name := entry.Name()
		if name != encoded && !strings.HasPrefix(name, encoded+"-") {
			continue
		}
		dir := filepath.Join(projectsDir, name)
		// Two project-dir names can symlink to the same real directory; scan the
		// underlying sessions only once so a session is not discovered twice.
		if !guard.visit(dir) {
			continue
		}
		sessions = append(sessions, discoverClaudeDir(dir, cwd, depth)...)
	}
	return sessions
}

// discoverClaudeDir scans one Claude project directory. depth<=0 keeps the
// exact-cwd contract (cwd is the search target and the recorded value); depth>0
// accepts any session whose recorded cwd is within depth levels of cwd and
// records that recorded cwd so the picker can render the relative column.
func discoverClaudeDir(dir, cwd string, depth int) []SessionMeta {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	treeFilter := depth > 0

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
		scanOpts := sessionScanOptions{targetCWD: cwd}
		if treeFilter {
			// No targetCWD short-circuit: we keep non-exact matches and judge
			// them by withinTree below. requireCWD ensures we captured one.
			scanOpts = sessionScanOptions{requireCWD: true}
		}
		details, ok := scanSessionJSONL(path, scanOpts)
		if !ok {
			continue
		}
		recordedCWD := cwd
		if treeFilter {
			if !withinTree(details.cwd, cwd, depth) {
				continue
			}
			recordedCWD = cleanCWD(details.cwd)
		} else if details.cwd != "" && cleanCWD(details.cwd) != cwd {
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
				CWD:    recordedCWD,
				Branch: details.branch,
			},
			Source: SourceClaudeTranscript,
			// Turns is left at 0 here; the candidate scan does not count turns.
			// enrichTurns re-scans sourcePath to fill it for displayed rows.
			sourcePath: path,
		})
	}
	return sessions
}

func discoverCodex(cwd, sessionsDir string, depth int) []SessionMeta {
	return discoverCodexWithFileLimit(cwd, sessionsDir, codexScanBudget(depth), depth)
}

// codexScanBudget returns the codex file-scan budget for a discovery depth. The
// exact-cwd default keeps the historical 80-file limit; depth>0 widens it
// proportionally (more child cwds match, so a fixed budget could drop recent
// matches) and caps it at codexScanBudgetMax so the walk stays bounded.
func codexScanBudget(depth int) int {
	if depth <= 0 {
		return codexScanFileLimit
	}
	budget := codexScanFileLimit * (depth + 1)
	if budget > codexScanBudgetMax {
		return codexScanBudgetMax
	}
	return budget
}

func discoverCodexWithFileLimit(cwd, sessionsDir string, scanFileLimit, depth int) []SessionMeta {
	sessionsDir = strings.TrimSpace(sessionsDir)
	if sessionsDir == "" {
		return nil
	}
	treeFilter := depth > 0

	var candidates []sessionFileCandidate
	walkCodexSessionFiles(sessionsDir, newPathGuard(), func(path string, modTime time.Time) {
		candidates = append(candidates, sessionFileCandidate{
			path:    path,
			modTime: modTime,
		})
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
		scanOpts := sessionScanOptions{targetCWD: cwd, requireCWD: true}
		if treeFilter {
			// Keep non-exact matches; judge them by withinTree below.
			scanOpts = sessionScanOptions{requireCWD: true}
		}
		details, ok := scanSessionJSONL(candidate.path, scanOpts)
		if !ok || details.id == "" || !withinTree(details.cwd, cwd, depth) {
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
		recordedCWD := cwd
		if treeFilter {
			recordedCWD = cleanCWD(details.cwd)
		}
		sessions = append(sessions, SessionMeta{
			Agent:        AgentCodex,
			ResumeID:     id,
			Title:        title,
			LastModified: candidate.modTime,
			Context: SessionContext{
				CWD:    recordedCWD,
				Branch: details.branch,
			},
			Source: SourceCodexRollout,
			// Turns is left at 0 here; enrichTurns fills it from sourcePath.
			sourcePath: candidate.path,
		})
	}
	return sessions
}

// walkCodexSessionFiles walks root for codex rollout files, descending into
// subdirectories and following symlinked directories. guard makes following
// safe: it records the resolved real path of every directory entered, so a
// symlink cycle (a directory linking to an ancestor) or two symlink paths
// aliasing the same real directory resolve to an already-visited location and
// are skipped. The walk is therefore always bounded regardless of symlinks.
func walkCodexSessionFiles(dir string, guard *pathGuard, visit func(path string, modTime time.Time)) {
	// A directory whose real path was already walked is a cycle or alias; stop.
	if !guard.visit(dir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if entryIsDir(dir, entry) {
			walkCodexSessionFiles(full, guard, visit)
			continue
		}
		if matched, matchErr := filepath.Match("rollout-*.jsonl", entry.Name()); matchErr != nil || !matched {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			// Follow the link to the real file for its mtime, and skip it if the
			// same real file was already collected through another path.
			info, statErr := os.Stat(full)
			if statErr != nil || info.IsDir() || !guard.visit(full) {
				continue
			}
			visit(full, info.ModTime())
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		visit(full, info.ModTime())
	}
}

// pathGuard bounds a symlink-following directory walk. It records the resolved
// real path of every directory (and symlinked file) already visited so a
// symlink cycle or alias resolves to an already-seen real location and is
// refused, making an unbounded walk or a double scan impossible independent of
// the depth cap.
type pathGuard struct {
	visited map[string]struct{}
}

func newPathGuard() *pathGuard {
	return &pathGuard{visited: make(map[string]struct{})}
}

// visit reports whether path's resolved real location is newly seen. A false
// return means path aliases an already-visited real location and must be
// skipped to avoid re-walking it.
func (g *pathGuard) visit(path string) bool {
	real := resolveRealPath(path)
	if _, ok := g.visited[real]; ok {
		return false
	}
	g.visited[real] = struct{}{}
	return true
}

// resolveRealPath returns path with all symlinks resolved. A broken or cyclic
// symlink cannot be resolved; the cleaned literal path is used instead, which
// still dedupes exact repeats without following a dangling link.
func resolveRealPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// entryIsDir reports whether entry under parent is a directory, following one
// level of symlink so a symlinked directory counts as a directory. Broken
// symlinks report false.
func entryIsDir(parent string, entry fs.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&fs.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.IsDir()
}

// withinTree reports whether recordedCWD is cwd itself or a descendant up to
// depth levels below it. The decision is purely on the cleaned paths: Rel must
// resolve without a ".." prefix (so parents/siblings are excluded) and the
// number of path segments must not exceed depth. depth 0 therefore matches only
// the exact cwd, identical to the historical equality check.
func withinTree(recordedCWD, cwd string, depth int) bool {
	if depth < 0 {
		depth = 0
	}
	recorded := cleanCWD(recordedCWD)
	base := cleanCWD(cwd)
	if recorded == "" || base == "" {
		return false
	}
	if recorded == base {
		return true
	}
	if depth == 0 {
		return false
	}
	rel, err := filepath.Rel(base, recorded)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	return len(strings.Split(rel, "/")) <= depth
}

// antigravityHistoryRecord is one line of the Antigravity CLI session index
// (~/.gemini/antigravity-cli/history.jsonl). Only the fields Discover surfaces
// are decoded; unknown fields are ignored.
type antigravityHistoryRecord struct {
	ConversationID string `json:"conversationId"`
	Workspace      string `json:"workspace"`
	Timestamp      int64  `json:"timestamp"`
	Display        string `json:"display"`
}

// discoverAntigravity reads the Antigravity history index and returns the
// sessions whose recorded workspace is within depth levels of cwd. It runs
// through the same cwd/depth filter the other agents use (the shared Discover
// caller then dedupes, sorts, and caps). A missing file, malformed lines, and
// records whose conversationId is not a valid resume id are skipped silently so
// a broken index degrades to no Antigravity rows rather than an error.
func discoverAntigravityHistory(cwd, historyPath string, depth int) []SessionMeta {
	historyPath = strings.TrimSpace(historyPath)
	if historyPath == "" {
		return nil
	}
	f, err := os.Open(historyPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var sessions []SessionMeta
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record antigravityHistoryRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		id, err := antigravity.NormalizeResumeID(record.ConversationID)
		if err != nil {
			continue
		}
		if !withinTree(record.Workspace, cwd, depth) {
			continue
		}
		// Mirror the other agents: at exact-cwd depth the recorded cwd is the
		// search target; depth>0 records the session's own workspace so the picker
		// can render the relative column.
		recordedCWD := cwd
		if depth > 0 {
			recordedCWD = cleanCWD(record.Workspace)
		}
		title := cleanTitleCandidate(record.Display)
		if title == "" {
			title = shortResumeID(id)
		}
		sessions = append(sessions, SessionMeta{
			Agent:        AgentAntigravity,
			ResumeID:     id,
			Title:        title,
			LastModified: time.UnixMilli(record.Timestamp),
			Context: SessionContext{
				CWD: recordedCWD,
			},
			Source: SourceAntigravityHistory,
			// history.jsonl carries no per-turn data; Antigravity turn count is
			// unknown (0) and renders as a blank cell.
			Turns: 0,
		})
	}
	return sessions
}

// discoverAntigravityCurrentStorage reads only Antigravity CLI v1.1.12's
// public cache indexes and the filename/mtime boundary of conversation DBs.
// The cache is a latest-conversation floor, not a complete history. Missing or
// malformed indexes and stale rows degrade independently without failing the
// rest of discovery.
func discoverAntigravityCurrentStorage(cwd, cacheDir, conversationsDir string, depth int) []SessionMeta {
	cacheDir = strings.TrimSpace(cacheDir)
	conversationsDir = strings.TrimSpace(conversationsDir)
	if cacheDir == "" || conversationsDir == "" {
		return nil
	}

	var sessions []SessionMeta
	sessions = append(sessions, discoverAntigravityLastConversations(
		cwd,
		filepath.Join(cacheDir, "last_conversations.json"),
		conversationsDir,
		depth,
	)...)
	sessions = append(sessions, discoverAntigravityConversationMetadata(
		cwd,
		filepath.Join(cacheDir, "conversation_metadata.json"),
		conversationsDir,
		depth,
	)...)
	return sessions
}

func discoverAntigravityLastConversations(cwd, cachePath, conversationsDir string, depth int) []SessionMeta {
	// #nosec G304 -- cachePath is the caller-selected read-only Antigravity cache root joined with the fixed upstream last_conversations.json filename; it is never used for writes or DB content.
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil
	}
	var records map[string]json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil || records == nil {
		return nil
	}

	var sessions []SessionMeta
	for workspaceValue, rawID := range records {
		workspace, ok := antigravityWorkspacePath(workspaceValue)
		if !ok || !withinTree(workspace, cwd, depth) {
			continue
		}
		var candidateID string
		if err := json.Unmarshal(rawID, &candidateID); err != nil {
			continue
		}
		id, modTime, ok := antigravityConversationDB(conversationsDir, candidateID)
		if !ok {
			continue
		}
		sessions = append(sessions, antigravityCacheSession(cwd, workspace, id, "", modTime, depth, SourceAntigravityLastConversation))
	}
	return sessions
}

type antigravityConversationMetadataIndex struct {
	Conversations map[string]json.RawMessage `json:"conversations"`
}

func discoverAntigravityConversationMetadata(cwd, cachePath, conversationsDir string, depth int) []SessionMeta {
	// #nosec G304 -- cachePath is the caller-selected read-only Antigravity cache root joined with the fixed upstream conversation_metadata.json filename; it is never used for writes or DB content.
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil
	}
	var index antigravityConversationMetadataIndex
	if err := json.Unmarshal(data, &index); err != nil || index.Conversations == nil {
		return nil
	}

	var sessions []SessionMeta
	for candidateID, rawRecord := range index.Conversations {
		var record map[string]any
		if err := json.Unmarshal(rawRecord, &record); err != nil {
			continue
		}
		rawSummary := stringJSONField(record, "summary")
		if rawSummary == "" {
			// The metadata lane requires all three upstream associations: UUID,
			// workspace URI/path, and summary. The last-conversation lane is the
			// short-UUID floor when metadata has no summary.
			continue
		}
		workspace, ok := matchingAntigravityMetadataWorkspace(record, cwd, depth)
		if !ok {
			continue
		}
		id, modTime, ok := antigravityConversationDB(conversationsDir, candidateID)
		if !ok {
			continue
		}
		// Summary supplies only a safe title candidate. It never supplies cwd or
		// identity; a present but noisy summary falls back to the short UUID.
		title := cleanTitleCandidate(rawSummary)
		sessions = append(sessions, antigravityCacheSession(cwd, workspace, id, title, modTime, depth, SourceAntigravityMetadata))
	}
	return sessions
}

func matchingAntigravityMetadataWorkspace(record map[string]any, cwd string, depth int) (string, bool) {
	// Accept the documented singular forms and observed URI/path list variants.
	// Unknown fields remain ignored. A workspace-less metadata row is unusable,
	// even when it has a summary, because project ownership cannot be inferred.
	keys := []string{
		"workspace", "workspace_uri", "workspaceUri", "workspace_path", "workspacePath",
		"workspace_uris", "workspaceUris", "WorkspaceURIs",
		"workspace_paths", "workspacePaths", "WorkspacePaths",
	}
	for _, key := range keys {
		switch value := record[key].(type) {
		case string:
			if workspace, ok := antigravityWorkspacePath(value); ok && withinTree(workspace, cwd, depth) {
				return workspace, true
			}
		case []any:
			for _, item := range value {
				workspaceValue, ok := item.(string)
				if !ok {
					continue
				}
				if workspace, ok := antigravityWorkspacePath(workspaceValue); ok && withinTree(workspace, cwd, depth) {
					return workspace, true
				}
			}
		}
	}
	return "", false
}

func antigravityCacheSession(cwd, workspace, id, title string, modTime time.Time, depth int, source string) SessionMeta {
	recordedCWD := cwd
	if depth > 0 {
		recordedCWD = cleanCWD(workspace)
	}
	if title == "" {
		title = shortResumeID(id)
	}
	return SessionMeta{
		Agent:        AgentAntigravity,
		ResumeID:     id,
		Title:        title,
		LastModified: modTime,
		Context:      SessionContext{CWD: recordedCWD},
		Source:       source,
		Turns:        0,
	}
}

// antigravityConversationDB validates exactly <normalized-uuid>.db. UUID
// validation prevents traversal and sidecar suffixes, while Lstat rejects a DB
// symlink (including one escaping the store) and accepts only regular files.
// The file is never opened; its mtime is the cache candidate's recency.
func antigravityConversationDB(conversationsDir, candidateID string) (string, time.Time, bool) {
	id, err := antigravity.NormalizeResumeID(candidateID)
	if err != nil {
		return "", time.Time{}, false
	}
	path := filepath.Join(conversationsDir, id+".db")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", time.Time{}, false
	}
	return id, info.ModTime(), true
}

func antigravityWorkspacePath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		if !strings.EqualFold(parsed.Scheme, "file") || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) {
			return "", false
		}
		unescaped, err := url.PathUnescape(parsed.EscapedPath())
		if err != nil {
			return "", false
		}
		value = unescaped
	}
	cleaned := cleanCWD(value)
	if cleaned == "" || !filepath.IsAbs(cleaned) {
		return "", false
	}
	return cleaned, true
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
		if !ok || preferSessionCandidate(session, current) {
			latest[id] = session
		}
	}
	deduped := make([]SessionMeta, 0, len(latest))
	for _, session := range latest {
		deduped = append(deduped, session)
	}
	return deduped
}

func preferSessionCandidate(candidate, current SessionMeta) bool {
	// Source priority is an Antigravity contract. Preserve the historical
	// newest-only behavior across providers (including a coincidental UUID
	// collision with Claude/Codex) and for every non-Antigravity provider.
	if candidate.Agent != AgentAntigravity || current.Agent != AgentAntigravity {
		return candidate.LastModified.After(current.LastModified)
	}
	candidatePriority := sessionSourcePriority(candidate.Source)
	currentPriority := sessionSourcePriority(current.Source)
	return candidatePriority > currentPriority ||
		(candidatePriority == currentPriority && candidate.LastModified.After(current.LastModified))
}

// sessionSourcePriority keeps source guarantees separate from recency. Live
// hook/session-id metadata is authoritative, a DB-validated current-storage
// cache UUID is next, and legacy history is only the final fallback.
func sessionSourcePriority(source string) int {
	switch strings.TrimSpace(source) {
	case "hook", "session-id":
		return 4
	case SourceAntigravityLastConversation:
		return 3
	case SourceAntigravityMetadata:
		return 2
	case SourceAntigravityHistory:
		return 1
	default:
		return 0
	}
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

// ready reports whether the cheap candidate fields (id, title, branch, and a
// cwd consistent with the scan's constraints) are all captured, so a non
// turn-counting scan can stop before the line limit. This is the early-exit
// that keeps candidate discovery fast.
func (details sessionDetails) ready(requireCWD bool, targetCWD string) bool {
	if details.id == "" || details.title == "" || details.branch == "" {
		return false
	}
	if targetCWD != "" && details.cwd == "" {
		return false
	}
	return !requireCWD || details.cwd != ""
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
			// Candidate discovery: stop as soon as the cheap fields are known.
			// This is the #477 early-exit; the turn count is a separate deferred
			// full-file pass (see countUserTurns), so nothing defeats it here.
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

// isUserTurnRecord reports whether a session-log record is a user turn (a human
// prompt), counting toward SessionMeta.Turns. It matches codex user_message /
// user-role response items and Claude user records, but excludes tool-result
// carrier records (Claude replays tool output as role "user"), so the count
// tracks conversational turns rather than raw record lines.
func isUserTurnRecord(fields map[string]any) bool {
	switch strings.ToLower(stringJSONField(fields, "type")) {
	case "event_msg":
		// codex: payload.type == "user_message" is the human prompt event.
		if payload, ok := fields["payload"].(map[string]any); ok {
			return strings.EqualFold(stringJSONField(payload, "type"), "user_message")
		}
		return false
	case "response_item":
		// codex: a response item with role "user" carrying real text.
		if payload, ok := fields["payload"].(map[string]any); ok && strings.EqualFold(stringJSONField(payload, "role"), "user") {
			return hasUserText(payload["content"])
		}
		return false
	case "user":
		// claude: a user record whose message content is real user text.
		if message, ok := fields["message"].(map[string]any); ok {
			return hasUserText(message["content"])
		}
		return hasUserText(fields["content"])
	default:
		if strings.EqualFold(stringJSONField(fields, "role"), "user") {
			return hasUserText(fields["content"])
		}
		return false
	}
}

// hasUserText reports whether content holds real user-authored text rather than
// a tool-result / tool-use carrier. A bare string counts; an array counts only
// if it holds a string or a non-tool block with text, so Claude's tool-result
// user records (content is an array of tool_result blocks) are not miscounted.
func hasUserText(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		for _, item := range v {
			switch item := item.(type) {
			case string:
				if strings.TrimSpace(item) != "" {
					return true
				}
			case map[string]any:
				switch strings.ToLower(stringJSONField(item, "type")) {
				case "tool_result", "tool_use":
					continue
				case "text":
					return true
				default:
					if stringJSONField(item, "text") != "" {
						return true
					}
				}
			}
		}
	}
	return false
}

func titleFromRecord(fields map[string]any) string {
	recordType := strings.ToLower(stringJSONField(fields, "type"))
	if recordType == "event_msg" {
		if payload, ok := fields["payload"].(map[string]any); ok {
			// codex: only user_message events carry the human prompt; skip
			// agent_message and other event types so the title is the actual
			// prompt. Untyped payloads keep the legacy behavior.
			if payloadType := stringJSONField(payload, "type"); payloadType != "" && !strings.EqualFold(payloadType, "user_message") {
				return ""
			}
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
	title = unwrapContextWrappers(title)
	title = cleanTitle(title)
	if title == "" || isNoisyTitleCandidate(title) {
		return ""
	}
	return title
}

// contextWrapperTags are the XML wrapper tags agents inject around context
// (instructions, environment info) as synthetic user turns. Codex writes them
// as the first user records of a rollout, which otherwise leak into resume
// titles as raw "<environment_context>..." text.
var contextWrapperTags = map[string]bool{
	"user_instructions":   true,
	"environment_context": true,
}

// unwrapContextWrappers strips agent-injected XML context wrapper blocks from
// a title candidate. Removal is deliberately conservative so user-typed text
// is never mangled:
//   - only LEADING `<tag>...</tag>` blocks are removed, and only when the
//     matching close tag is present;
//   - the tag must be a known wrapper (contextWrapperTags) or snake_case
//     (agent wrappers use snake_case names; HTML tags never do);
//   - `<` anywhere past the leading block is never touched, so prompts like
//     "why is a < b wrong?" or "<div>x</div> is broken" stay verbatim.
//
// If nothing but wrappers remains, the empty result makes the scan move on to
// the next user candidate.
func unwrapContextWrappers(text string) string {
	for {
		trimmed := strings.TrimSpace(text)
		rest, ok := stripLeadingWrapperBlock(trimmed)
		if !ok {
			return trimmed
		}
		text = rest
	}
}

// stripLeadingWrapperBlock removes one leading `<tag>...</tag>` wrapper block
// and reports whether it did. It only fires when text starts with a wrapper
// tag (see isContextWrapperTag) and the matching close tag exists.
func stripLeadingWrapperBlock(text string) (string, bool) {
	if !strings.HasPrefix(text, "<") {
		return "", false
	}
	end := strings.IndexByte(text, '>')
	if end < 0 {
		return "", false
	}
	name, _, _ := strings.Cut(text[1:end], " ")
	if !isContextWrapperTag(name) {
		return "", false
	}
	closing := "</" + name + ">"
	_, after, ok := strings.Cut(text, closing)
	if !ok {
		return "", false
	}
	return after, true
}

// isContextWrapperTag reports whether tag names an agent context wrapper:
// either a known wrapper tag, or a lowercase snake_case name (must contain
// "_"), which HTML tags and user-typed inline `<` expressions never match.
func isContextWrapperTag(tag string) bool {
	if contextWrapperTags[tag] {
		return true
	}
	if !strings.Contains(tag, "_") {
		return false
	}
	if tag == "" || tag[0] < 'a' || tag[0] > 'z' {
		return false
	}
	for _, r := range tag {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// noisyTitleCandidatePrefixes are known agent-injected records that should not
// become resume titles. Keep this explicit: broad tag-shape matching would
// discard user-authored HTML and custom-element prompts.
var noisyTitleCandidatePrefixes = []string{
	"<command-",
	"<local-command-",
	"<task-notification",
	"<system-reminder",
	"# AGENTS.md",
}

func isNoisyTitleCandidate(title string) bool {
	title = strings.TrimSpace(title)
	for _, prefix := range noisyTitleCandidatePrefixes {
		if strings.HasPrefix(title, prefix) {
			return true
		}
	}
	return false
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
