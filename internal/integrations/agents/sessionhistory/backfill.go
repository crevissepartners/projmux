package sessionhistory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The backfill.
//
// The history only knows the conversations an Agent moved through after it
// existed. Backfill recovers a subset of the older ones: a Claude session that
// received a delivered projmux coordination frame names, in that frame, the
// Agent it was delivered to. A session whose delivered frames name exactly one
// target Agent is attributed to it as one `estimated` row.
//
// It is the one reader of provider transcript contents in projmux, run only by
// the explicit, user-run `agent sessions backfill`. Transcripts are opened
// read-only and used for nothing but that attribution: nothing is inferred
// from a folder name, a working directory, or time proximity, and a session
// that never received a message is not recovered.

// ClaudeProjectsDir is where Claude Code keeps its transcripts:
// `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects`. A blank CLAUDE_CONFIG_DIR is
// unset.
func ClaudeProjectsDir(claudeConfigDir, homeDir string) (string, error) {
	base := strings.TrimSpace(claudeConfigDir)
	if base == "" {
		if strings.TrimSpace(homeDir) == "" {
			return "", errors.New("claude projects directory: no home directory")
		}
		base = filepath.Join(homeDir, ".claude")
	}
	return filepath.Join(base, "projects"), nil
}

// coordinationFrameMarker is how a delivered frame starts. It is searched for
// anywhere in the text: Claude shows the frame after a sentence of its own
// ("Another Claude session sent a message:\n{...}").
const coordinationFrameMarker = `{"kind":"projmux-coordination"`

// backfillLockWait bounds how long a backfill queues for the history lock.
// The lock covers only the history read and the one append, never the
// transcript scan or the fsync.
const backfillLockWait = 10 * time.Second

// BackfillOptions are the inputs of one run.
type BackfillOptions struct {
	// ProjectsDir is the Claude projects directory (ClaudeProjectsDir).
	ProjectsDir string
	// StateDir is the projmux state directory holding the history file.
	StateDir string
	// Current is the Registry's current Claude ref of every Agent
	// (RecordFor with SourceCurrent). A session named here is never
	// estimated.
	Current []Record
	// DryRun computes the same report and writes nothing.
	DryRun bool
}

// BackfillReport is the outcome of one run. The JSON keys are a stable
// contract: `agent sessions backfill -o json` prints them.
//
// Every scanned transcript lands in exactly one of attributed,
// alreadyEstimated, skippedObserved, ambiguous, noFrame, and unreadable.
type BackfillReport struct {
	DryRun      bool   `json:"dryRun"`
	ProjectsDir string `json:"projectsDir"`
	HistoryPath string `json:"historyPath"`
	Scanned     int    `json:"scanned"`
	// Attributed transcripts yielded a new estimated row (appended, or would
	// be under DryRun).
	Attributed int `json:"attributed"`
	// AlreadyEstimated sessions already have an estimated row.
	AlreadyEstimated int `json:"alreadyEstimated"`
	// SkippedObserved sessions are already observed, or current in the
	// Registry, for some Agent.
	SkippedObserved int `json:"skippedObserved"`
	// Ambiguous transcripts name two or more distinct target Agents.
	Ambiguous int `json:"ambiguous"`
	// NoFrame transcripts name no target Agent.
	NoFrame int `json:"noFrame"`
	// Unreadable transcripts could not be read, or name one Agent but carry
	// no parseable record timestamp.
	Unreadable int `json:"unreadable"`
	// MalformedLines counts transcript lines that are not JSON objects.
	MalformedLines int `json:"malformedLines"`
	// MalformedFrames counts frame markers that did not decode as one JSON
	// object.
	MalformedFrames int `json:"malformedFrames"`
	// Rows are the attributed rows, in scan order.
	Rows []Record `json:"rows"`
}

// transcriptScan is what one transcript contributes.
type transcriptScan struct {
	sessionID       string
	path            string
	unreadable      bool
	targets         []string
	first, last     *time.Time
	malformedLines  int
	malformedFrames int
}

// Backfill scans the top-level Claude transcripts
// `<ProjectsDir>/<project>/<session>.jsonl` and appends one estimated row per
// session attributable to exactly one Agent. It holds the history file's
// exclusive lock across the read, the check, and the append, so concurrent
// runs never add the same session twice and a second run adds nothing. The
// lock is released before the fsync, which Backfill still waits for. A
// missing projects directory scans nothing.
func Backfill(opts BackfillOptions) (BackfillReport, error) {
	report := BackfillReport{DryRun: opts.DryRun, ProjectsDir: opts.ProjectsDir, Rows: []Record{}}
	if strings.TrimSpace(opts.StateDir) == "" {
		return report, errors.New("agent session history: no state directory")
	}
	report.HistoryPath = Path(opts.StateDir)
	transcripts, err := listTranscripts(opts.ProjectsDir)
	if err != nil {
		return report, err
	}
	var candidates []Record
	for _, path := range transcripts {
		scan := scanTranscript(path)
		report.Scanned++
		report.MalformedLines += scan.malformedLines
		report.MalformedFrames += scan.malformedFrames
		switch {
		case scan.unreadable:
			report.Unreadable++
		case len(scan.targets) == 0:
			report.NoFrame++
		case len(scan.targets) > 1:
			report.Ambiguous++
		case scan.first == nil:
			report.Unreadable++
		default:
			last := *scan.last
			candidates = append(candidates, Record{
				AgentUID: scan.targets[0], Provider: ProviderClaude,
				SessionID: scan.sessionID, TranscriptPath: scan.path,
				ObservedAt: scan.first.UTC(), Source: SourceEstimated, LastRecordAt: &last,
			}.normalized())
		}
	}

	if opts.DryRun {
		read, err := Read(opts.StateDir, "")
		if err != nil {
			return report, err
		}
		classify(&report, candidates, read.Records, opts.Current)
		return report, nil
	}
	// Nothing to add creates nothing.
	if len(candidates) == 0 {
		return report, nil
	}
	file, err := openLocked(opts.StateDir, os.O_RDWR, backfillLockWait)
	if err != nil {
		return report, err
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return report, fmt.Errorf("agent session history: rewind: %w", err)
	}
	read, err := readRecords(file, "")
	if err != nil {
		return report, err
	}
	classify(&report, candidates, read.Records, opts.Current)
	if len(report.Rows) == 0 {
		return report, nil
	}
	var framed []byte
	for _, row := range report.Rows {
		line, err := frame(row)
		if err != nil {
			return report, err
		}
		framed = append(framed, line...)
	}
	if _, err := file.Write(framed); err != nil {
		return report, fmt.Errorf("agent session history: append: %w", err)
	}
	if err := unlockAndSync(file); err != nil {
		return report, err
	}
	return report, nil
}

// classify applies the precedence to the candidates: a session observed or
// current for any Agent is skipped, one already estimated (in the file or
// earlier in this run) is not added again, and the rest are attributed.
func classify(report *BackfillReport, candidates, history, current []Record) {
	observed := map[string]bool{}
	estimated := map[string]bool{}
	for _, record := range history {
		if record.Provider != ProviderClaude {
			continue
		}
		switch record.Source {
		case SourceObserved, SourceCurrent:
			observed[record.SessionID] = true
		case SourceEstimated:
			estimated[record.SessionID] = true
		}
	}
	for _, record := range current {
		if record.Provider == ProviderClaude {
			observed[record.SessionID] = true
		}
	}
	for _, candidate := range candidates {
		switch {
		case observed[candidate.SessionID]:
			report.SkippedObserved++
		case estimated[candidate.SessionID]:
			report.AlreadyEstimated++
		default:
			estimated[candidate.SessionID] = true
			report.Attributed++
			report.Rows = append(report.Rows, candidate)
		}
	}
}

// listTranscripts returns the regular `<project>/<name>.jsonl` files directly
// under projectsDir, in path order. Symlinks, deeper files (a session's
// `subagents/` transcripts), and other names are not transcripts here.
func listTranscripts(projectsDir string) ([]string, error) {
	if strings.TrimSpace(projectsDir) == "" {
		return nil, nil
	}
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("claude projects directory: %w", err)
	}
	var paths []string
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		dir := filepath.Join(projectsDir, project.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("claude project directory: %w", err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".jsonl") || name == ".jsonl" {
				continue
			}
			path := filepath.Join(dir, name)
			if abs, err := filepath.Abs(path); err == nil {
				path = abs
			}
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// transcriptRecord is the part of a transcript line Backfill looks at. Every
// field stays raw so an unexpected type in one of them never makes the line
// malformed.
type transcriptRecord struct {
	Type        json.RawMessage `json:"type"`
	IsSidechain json.RawMessage `json:"isSidechain"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
}

// scanTranscript reads one transcript read-only.
func scanTranscript(path string) transcriptScan {
	scan := transcriptScan{sessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), path: path}
	// #nosec G304 -- the path is a transcript listed under the Claude projects directory.
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		scan.unreadable = true
		return scan
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		scan.unreadable = true
		return scan
	}
	seen := map[string]bool{}
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			scan.scanLine(trimmed, seen)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				scan.unreadable = true
			}
			return scan
		}
	}
}

func (scan *transcriptScan) scanLine(line []byte, seen map[string]bool) {
	var record transcriptRecord
	if err := json.Unmarshal(line, &record); err != nil {
		scan.malformedLines++
		return
	}
	var stamp string
	if json.Unmarshal(record.Timestamp, &stamp) == nil {
		if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			if scan.first == nil {
				scan.first = &at
			}
			scan.last = &at
		}
	}
	var kind string
	if json.Unmarshal(record.Type, &kind) != nil || kind != "user" {
		return
	}
	var sidechain bool
	if json.Unmarshal(record.IsSidechain, &sidechain) == nil && sidechain {
		return
	}
	for _, text := range userTexts(record.Message) {
		targets, malformed := frameTargets(text)
		scan.malformedFrames += malformed
		for _, uid := range targets {
			if !seen[uid] {
				seen[uid] = true
				scan.targets = append(scan.targets, uid)
			}
		}
	}
}

// userTexts is the text of a user record's message: its string content, or
// the text of its `text` blocks. tool_result and every other block type are
// not text a frame was delivered in.
func userTexts(message json.RawMessage) []string {
	var body struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &body) != nil {
		return nil
	}
	var text string
	if json.Unmarshal(body.Content, &text) == nil {
		return []string{text}
	}
	var blocks []json.RawMessage
	if json.Unmarshal(body.Content, &blocks) != nil {
		return nil
	}
	var texts []string
	for _, raw := range blocks {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &block) == nil && block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return texts
}

// deliveredFrame is the part of a coordination frame Backfill reads.
type deliveredFrame struct {
	Kind   string `json:"kind"`
	Target struct {
		AgentUID string `json:"agentUID"`
	} `json:"target"`
}

// frameTargets decodes one JSON value at each frame marker in text and
// returns the target Agent uid of every frame, with the number of markers
// that did not decode as a frame object.
func frameTargets(text string) ([]string, int) {
	var targets []string
	malformed := 0
	for rest := text; ; {
		at := strings.Index(rest, coordinationFrameMarker)
		if at < 0 {
			return targets, malformed
		}
		rest = rest[at:]
		decoder := json.NewDecoder(strings.NewReader(rest))
		var raw json.RawMessage
		var decoded deliveredFrame
		if decoder.Decode(&raw) != nil || json.Unmarshal(raw, &decoded) != nil {
			malformed++
			rest = rest[len(coordinationFrameMarker):]
			continue
		}
		rest = rest[decoder.InputOffset():]
		if uid := strings.TrimSpace(decoded.Target.AgentUID); decoded.Kind == "projmux-coordination" && uid != "" {
			targets = append(targets, uid)
		}
	}
}
