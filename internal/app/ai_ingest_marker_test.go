package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"strings"
	"testing"
	"time"
)

// hookMarkerPane is a stateful tmux Pane behind a hook test's recorded
// transport. Pane set-option writes land in options and a display-message read
// of the Pane expands its format from them, so a second ingest sees exactly what
// the first one left. Reads the wrapped reader already answers keep its answer.
type hookMarkerPane struct {
	pane    string
	options map[string]string
	// answerPane is the #{pane_id} a read reports; empty means pane.
	answerPane string
	readFails  bool
	writeFails bool
	reads      int
}

var hookMarkerFormatField = regexp.MustCompile(`#\{([^}]+)\}`)

func installHookMarkerPane(cmd *aiCommand, pane string) *hookMarkerPane {
	m := &hookMarkerPane{pane: pane, options: map[string]string{}}
	run, read := cmd.runCommand, cmd.readCommand
	cmd.runCommand = func(ctx context.Context, name string, args ...string) error {
		if err := run(ctx, name, args...); err != nil {
			return err
		}
		if name != "tmux" {
			return nil
		}
		a := stripRecordedTmuxRoute(args)
		if len(a) > 0 && a[0] == "set-option" && m.writeFails {
			return errors.New("tmux: server exited unexpectedly")
		}
		switch {
		case len(a) == 6 && a[0] == "set-option" && a[1] == "-p" && a[2] == "-t" && a[3] == m.pane:
			m.options[a[4]] = a[5]
		case len(a) == 6 && a[0] == "set-option" && a[1] == "-p" && a[2] == "-u" && a[3] == "-t" && a[4] == m.pane:
			delete(m.options, a[5])
		}
		return nil
	}
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" {
			m.reads++
		}
		out, err := read(ctx, name, args...)
		if err == nil || name != "tmux" || m.readFails {
			return out, err
		}
		a := stripRecordedTmuxRoute(args)
		if len(a) != 5 || a[0] != "display-message" || a[1] != "-p" || a[2] != "-t" || a[3] != m.pane {
			return out, err
		}
		expanded := hookMarkerFormatField.ReplaceAllStringFunc(a[4], func(field string) string {
			key := field[2 : len(field)-1]
			if key == "pane_id" {
				if m.answerPane != "" {
					return m.answerPane
				}
				return m.pane
			}
			return m.options[key]
		})
		return []byte(expanded + "\n"), nil
	}
	return m
}

// tmuxCalls counts the tmux commands of one ingest: the recorded writes plus
// the reads since readsBefore.
func (m *hookMarkerPane) tmuxCalls(commands []recordedAICommand, readsBefore int) int {
	calls := m.reads - readsBefore
	for _, command := range commands {
		if command.name == "tmux" {
			calls++
		}
	}
	return calls
}

// claudeMarkedPaneFixture is an owned or unbound Claude quiet fixture whose Pane
// already carries the markers of one earlier ingest.
func claudeMarkedPaneFixture(t *testing.T, owned bool) (*claudeQuietHookFixture, *hookMarkerPane) {
	t.Helper()
	f := newClaudeQuietHookFixture(t, owned)
	m := installHookMarkerPane(f.cmd, claudeQuietHookPane)
	if commands, _ := f.ingest(t, "PreToolUse"); len(claudeQuietHookSetOptionCounts(commands)) == 0 {
		t.Fatalf("priming ingest wrote no marker: %q", commands)
	}
	return f, m
}

// ingestWith runs one Claude event whose cwd and session differ from the
// fixture's defaults where given.
func (f *claudeQuietHookFixture) ingestWith(t *testing.T, event, cwd, session string) []recordedAICommand {
	t.Helper()
	fields := map[string]string{
		"hook_event_name": event,
		"session_id":      claudeQuietHookSession,
		"cwd":             claudeQuietHookCWD,
		"transcript_path": claudeQuietHookTranscript,
	}
	if cwd != "" {
		fields["cwd"] = cwd
	}
	if session != "" {
		fields["session_id"] = session
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	before := len(cmdRecorder(f.cmd).commands)
	if err := f.cmd.ingestClaudeHook(payload, f.explicitPane); err != nil {
		t.Fatalf("ingest claude %s: %v", event, err)
	}
	return cmdRecorder(f.cmd).commands[before:]
}

// TestClaudeHookOnAMarkedPaneWritesNoMarker owns the tool-call path: a Pane
// that already carries every marker of the event gets no set-option at all,
// because each one makes tmux redraw every client and fork the status line's
// jobs.
func TestClaudeHookOnAMarkedPaneWritesNoMarker(t *testing.T) {
	t.Parallel()

	for _, owned := range []bool{true, false} {
		for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolBatch", "ExperimentalEvent"} {
			name := map[bool]string{true: "owned ", false: "unbound "}[owned] + event
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				f, m := claudeMarkedPaneFixture(t, owned)
				before := maps.Clone(m.options)
				// A later hook in a later second must not refresh the resume
				// timestamp of a pointer that did not change.
				f.cmd.now = func() time.Time { return claudeQuietHookClock.Add(time.Minute) }
				readsBefore := m.reads
				commands, _ := f.ingest(t, event)

				if counts := claudeQuietHookSetOptionCounts(commands); len(counts) != 0 {
					t.Errorf("set-option on a marked Pane = %v, want none", counts)
				}
				if len(commands) != 0 {
					t.Errorf("tmux writes = %q, want none", commands)
				}
				if calls := m.tmuxCalls(commands, readsBefore); calls >= 9 {
					t.Errorf("tmux calls = %d, want fewer than the 9 of an unconditional marking", calls)
				}
				if !maps.Equal(m.options, before) {
					t.Errorf("pane options = %v, want unchanged %v", m.options, before)
				}
				if records := claudeQuietHookLogRecords(t, f.cmd); records[len(records)-1].Result != "quiet" {
					t.Errorf("record = %+v, want quiet", records[len(records)-1])
				}
			})
		}
	}
}

// TestClaudeHookWritesOnlyTheMarkersThatChanged pins the per-marker comparison
// and the resume pointer rule: the resume id, source and timestamp are written
// together, and only when the id or the source changes.
func TestClaudeHookWritesOnlyTheMarkersThatChanged(t *testing.T) {
	t.Parallel()

	later := claudeQuietHookClock.Add(time.Minute).Format(time.RFC3339)
	tests := []struct {
		name    string
		cwd     string
		session string
		// prepare edits the Pane after the priming ingest.
		prepare func(options map[string]string)
		want    map[string]int
		// wantState is the final value of these options.
		wantState map[string]string
	}{
		{
			name: "cwd changed", cwd: "/src/other",
			want:      map[string]int{aiPaneContextOption: 1},
			wantState: map[string]string{aiPaneContextOption: "/src/other", aiPaneResumeUpdatedAtOption: "2026-09-14T09:30:00Z"},
		},
		{
			name: "session changed", session: "claude-next-session",
			want: map[string]int{
				aiPaneSessionIDOption: 1, aiPaneResumeIDOption: 1, aiPaneResumeSourceOption: 1, aiPaneResumeUpdatedAtOption: 1,
			},
			wantState: map[string]string{
				aiPaneSessionIDOption: "claude-next-session", aiPaneResumeIDOption: "claude-next-session",
				aiPaneResumeSourceOption: "hook", aiPaneResumeUpdatedAtOption: later,
			},
		},
		{
			name: "resume source changed by another writer",
			prepare: func(options map[string]string) {
				options[aiPaneResumeSourceOption] = "cli"
			},
			want: map[string]int{aiPaneResumeIDOption: 1, aiPaneResumeSourceOption: 1, aiPaneResumeUpdatedAtOption: 1},
			wantState: map[string]string{
				aiPaneResumeSourceOption: "hook", aiPaneResumeUpdatedAtOption: later,
			},
		},
		{
			name: "hook active cleared",
			prepare: func(options map[string]string) {
				delete(options, aiPaneHookActiveOption)
			},
			want:      map[string]int{aiPaneHookActiveOption: 1},
			wantState: map[string]string{aiPaneHookActiveOption: "1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, m := claudeMarkedPaneFixture(t, true)
			if tc.prepare != nil {
				tc.prepare(m.options)
			}
			f.cmd.now = func() time.Time { return claudeQuietHookClock.Add(time.Minute) }
			commands := f.ingestWith(t, "PreToolUse", tc.cwd, tc.session)

			if got := claudeQuietHookSetOptionCounts(commands); !maps.Equal(got, tc.want) {
				t.Errorf("set-option counts = %v, want %v", got, tc.want)
			}
			for option, value := range tc.wantState {
				if m.options[option] != value {
					t.Errorf("%s = %q, want %q", option, m.options[option], value)
				}
			}
		})
	}
}

// TestClaudeHookWritesEveryMarkerWhenItCannotTrustTheRead keeps the old
// behavior wherever the read gives no answer about this Pane.
func TestClaudeHookWritesEveryMarkerWhenItCannotTrustTheRead(t *testing.T) {
	t.Parallel()

	allMarkers := map[string]int{
		aiPaneHookActiveOption: 1, aiPaneManagedOption: 1, aiPaneAgentOption: 1, aiPaneContextOption: 1,
		aiPaneSessionIDOption: 1, aiPaneResumeIDOption: 1, aiPaneResumeSourceOption: 1,
		aiPaneResumeUpdatedAtOption: 1, aiPaneTranscriptPathOption: 1,
	}
	for name, spoil := range map[string]func(*hookMarkerPane){
		"read fails":       func(m *hookMarkerPane) { m.readFails = true },
		"answer for other": func(m *hookMarkerPane) { m.answerPane = "%8" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, m := claudeMarkedPaneFixture(t, true)
			spoil(m)
			commands, _ := f.ingest(t, "PreToolUse")
			if got := claudeQuietHookSetOptionCounts(commands); !maps.Equal(got, allMarkers) {
				t.Errorf("set-option counts = %v, want every marker once %v", got, allMarkers)
			}
		})
	}
}

// TestClaudeHookMarkerWriteFailureStillReportsError keeps the record honest: a
// marker that has to be written and does not land still turns the record into
// an error, whether or not the read answered.
func TestClaudeHookMarkerWriteFailureStillReportsError(t *testing.T) {
	t.Parallel()

	for name, readFails := range map[string]bool{"changed marker": false, "read fails": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, m := claudeMarkedPaneFixture(t, true)
			m.readFails = readFails
			m.writeFails = true
			f.ingestWith(t, "PreToolUse", "/src/other", "")
			records := claudeQuietHookLogRecords(t, f.cmd)
			got := records[len(records)-1]
			if got.Result != "error" || string(got.Reason) != aiPaneWriteReasonMarkerUnavailable {
				t.Errorf("record = %+v, want error %q", got, aiPaneWriteReasonMarkerUnavailable)
			}
		})
	}
}

// TestAntigravityHookOnAMarkedPaneWritesNoMarker is the Antigravity quiet path
// of the same rule; its conversation id also fills the thread marker.
func TestAntigravityHookOnAMarkedPaneWritesNoMarker(t *testing.T) {
	t.Parallel()

	for _, owned := range []bool{true, false} {
		for _, event := range []string{"PostToolUse", "PostInvocation", "ExperimentalEvent"} {
			name := map[bool]string{true: "owned ", false: "unbound "}[owned] + event
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				f := newAntigravityQuietHookFixture(t, owned)
				m := installHookMarkerPane(f.cmd, antigravityQuietHookPane)
				if commands, _ := f.ingest(t, event, nil); len(claudeQuietHookSetOptionCounts(commands)) == 0 {
					t.Fatalf("priming ingest wrote no marker: %q", commands)
				}
				readsBefore := m.reads
				commands, _ := f.ingest(t, event, nil)
				if len(commands) != 0 {
					t.Errorf("tmux writes on a marked Pane = %q, want none", commands)
				}
				if calls := m.tmuxCalls(commands, readsBefore); calls >= 10 {
					t.Errorf("tmux calls = %d, want fewer than the 10 of an unconditional marking", calls)
				}
			})
		}
	}
}

// TestCodexQuietHookOnAMarkedPaneWritesNoMarker is the Codex quiet path of the
// same rule.
func TestCodexQuietHookOnAMarkedPaneWritesNoMarker(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	m := installHookMarkerPane(cmd, "%7")
	payload := codexHookPayload{EventName: "PostToolUse", SessionID: "codex-session", CWD: "/repo/projmux"}
	cmd.quietCodexHook("%7", payload, "catalog quiet event")
	primed := len(cmdRecorder(cmd).commands)
	if primed == 0 {
		t.Fatal("priming quiet hook wrote no marker")
	}
	readsBefore := m.reads
	cmd.quietCodexHook("%7", payload, "catalog quiet event")
	commands := cmdRecorder(cmd).commands[primed:]
	if len(commands) != 0 {
		t.Errorf("tmux writes on a marked Pane = %q, want none", commands)
	}
	if calls := m.tmuxCalls(commands, readsBefore); calls >= primed {
		t.Errorf("tmux calls = %d, want fewer than the %d writes of an unconditional marking", calls, primed)
	}
	if !strings.Contains(strings.Join(stripRecordedTmuxRoute(cmdRecorder(cmd).commands[0].args), " "), aiPaneHookActiveOption) {
		t.Errorf("first priming write = %q, want the hook-active marker", cmdRecorder(cmd).commands[0].args)
	}
}
