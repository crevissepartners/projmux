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

	// Turns is the count of user turns observed while scanning the session log.
	// It is a low-cost signal counted in the same bounded pass that extracts the
	// title (no extra file pass), so it saturates for very long sessions at the
	// scan-line limit. Zero means "unknown" and renders as a blank cell.
	Turns int
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
	depth := max(opts.Depth, 0)
	var sessions []SessionMeta
	sessions = append(sessions, discoverClaude(cwd, opts.ClaudeProjectsDir, depth)...)
	sessions = append(sessions, discoverCodex(cwd, opts.CodexSessionsDir, depth)...)
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
			Turns:  details.turns,
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
			Turns:  details.turns,
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
	turns  int
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
		// Count user turns in the same bounded pass that extracts the title. We
		// deliberately do not early-exit once id/title/branch/cwd are known: a
		// turn count is only meaningful if the scan sees the whole bounded window,
		// so it keeps reading up to sessionScanLineLimit. The cwd-mismatch abort
		// above still short-circuits non-matching sessions on their first cwd
		// record, so the extra reads are paid only for sessions we will display.
		if isUserTurnRecord(fields) {
			details.turns++
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
