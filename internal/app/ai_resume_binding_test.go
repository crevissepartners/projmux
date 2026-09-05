package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func resumeBindingFixture() coremetadata.Registry {
	return coremetadata.Registry{
		Agents: []coremetadata.Agent{{
			Metadata: coremetadata.ObjectMeta{UID: "agent-b", Name: "builder", Annotations: map[string]string{coremetadata.AnnotationAgentTopic: "Binding conversation"}},
			Spec:     coremetadata.AgentSpec{Provider: aiModeClaude},
			Status:   coremetadata.AgentStatus{PaneRef: "pane-b", SessionRef: &coremetadata.AgentSessionRef{Provider: aiModeClaude, Claude: &coremetadata.ClaudeSessionRef{SessionID: "exact-binding-id"}}},
		}},
		Panes: []coremetadata.Pane{{Metadata: coremetadata.ObjectMeta{UID: "pane-b", Name: "builder-pane", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agent-b"}}}},
	}
}

func TestAIResumeExactBindingProjectsCoherentAgentAndPaneNames(t *testing.T) {
	registry := resumeBindingFixture()
	// A lexically earlier topic without a Pane cannot donate context/name to the
	// bound pair. Among round-trip candidates, lexical UID is the only tie-break.
	earlier := registry.Agents[0]
	earlier.Metadata = coremetadata.ObjectMeta{UID: "agent-a", Name: "earlier", Annotations: map[string]string{coremetadata.AnnotationAgentTopic: "Wrong earlier topic"}}
	earlier.Status.PaneRef = ""
	later := registry.Agents[0]
	later.Metadata = coremetadata.ObjectMeta{UID: "agent-z", Name: "later", Annotations: map[string]string{coremetadata.AnnotationAgentTopic: "Wrong later topic"}}
	later.Status.PaneRef = "pane-z"
	registry.Agents = append(registry.Agents, earlier, later)
	registry.Panes = append(registry.Panes, coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: "pane-z", Name: "later-pane", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agent-z"}}})
	// Same conversation bytes under a different provider are independent.
	foreign := earlier
	foreign.Metadata = coremetadata.ObjectMeta{UID: "agent-0", Name: "foreign"}
	foreign.Spec.Provider = aiModeCodex
	foreign.Status.SessionRef = &coremetadata.AgentSessionRef{Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: "exact-binding-id", SessionID: "codex-alias"}}
	registry.Agents = append(registry.Agents, foreign)
	want := aiResumeExactAgentLabel{Name: "builder", PaneName: "builder-pane", Context: registryview.Context{Value: "Binding conversation", Source: registryview.ContextSourceAgentTopic}}
	for _, order := range [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 3, 0, 2}, {2, 0, 3, 1}, {1, 2, 3, 0}, {2, 1, 0, 3}} {
		reordered := registry.Clone()
		for i, j := range order {
			reordered.Agents[i] = registry.Agents[j]
		}
		slices.Reverse(reordered.Panes)
		labels := aiResumeExactAgentLabels(reordered)
		label := labels[aiResumeExactLabelKey(aiModeClaude, "exact-binding-id")]
		if !reflect.DeepEqual(label, want) {
			t.Fatalf("order %v = %#v, want coherent %#v", order, label, want)
		}
		if labels[aiResumeExactLabelKey(aiModeCodex, "exact-binding-id")].Name != "foreign" || labels[aiResumeExactLabelKey(aiModeCodex, "codex-alias")].Name != "foreign" {
			t.Fatalf("provider/alias binding drift: %#v", labels)
		}
		summary := aisessions.ResumeSummary{Provider: aiModeClaude, ResumeID: "exact-binding-id", Source: aisessions.SourceClaudeTranscript, Label: "Suppressed transcript title"}
		row := aiResumeSummaryRowsWithLabels([]aisessions.ResumeSummary{summary}, labels, time.Time{}, i18n.FallbackLocale, "/work", 0)[1]
		if !strings.HasSuffix(stripANSI(row.Label), "[builder → builder-pane] Binding conversation") {
			t.Fatalf("row = %q", row.Label)
		}
		detail := aiResumeDetailProjection(i18n.FallbackLocale, summary, aisessions.ResumeDetailRef{}, aisessions.ResumeDetail{}, "preview", label)
		for _, name := range []string{want.Name, want.PaneName} {
			if !strings.Contains(row.SearchKey, name) || !strings.Contains(detail, name) {
				t.Fatalf("full binding missing: row=%#v detail=%q", row, detail)
			}
		}
		if strings.Contains(row.SearchKey, "Wrong") || strings.Contains(detail, "Suppressed") {
			t.Fatalf("foreign context/title leaked: %#v %q", row, detail)
		}
	}
}

func TestAIResumeBindingDisplayFallsBackWithoutRoundTripPane(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*coremetadata.Registry)
		want   string
	}{
		{"round trip", func(*coremetadata.Registry) {}, "[builder → builder-pane] Binding conversation"},
		{"missing ref", func(r *coremetadata.Registry) { r.Agents[0].Status.PaneRef = "" }, "[builder] Binding conversation"},
		{"stale ref", func(r *coremetadata.Registry) { r.Agents[0].Status.PaneRef = "pane-gone" }, "[builder] Binding conversation"},
		{"deleted pane", func(r *coremetadata.Registry) { r.Panes = nil }, "[builder] Binding conversation"},
		{"foreign owner", func(r *coremetadata.Registry) { r.Panes[0].Metadata.OwnerRef.UID = "other" }, "[builder] Binding conversation"},
		{"wrong owner kind", func(r *coremetadata.Registry) { r.Panes[0].Metadata.OwnerRef.Kind = coremetadata.KindWindow }, "[builder] Binding conversation"},
		{"no owner", func(r *coremetadata.Registry) { r.Panes[0].Metadata.OwnerRef = nil }, "[builder] Binding conversation"},
		{"deleted agent", func(r *coremetadata.Registry) { r.Agents = nil }, "Untitled · …g-id"},
		{"no session ref", func(r *coremetadata.Registry) { r.Agents[0].Status.SessionRef = nil }, "Untitled · …g-id"},
		{"foreign session provider", func(r *coremetadata.Registry) { r.Agents[0].Status.SessionRef.Provider = aiModeCodex }, "Untitled · …g-id"},
		{"different conversation", func(r *coremetadata.Registry) { r.Agents[0].Status.SessionRef.Claude.SessionID = "other" }, "Untitled · …g-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resumeBindingFixture()
			tc.mutate(&r)
			summary := aisessions.ResumeSummary{Provider: aiModeClaude, ResumeID: "exact-binding-id", Source: aisessions.SourceClaudeTranscript, Label: "Suppressed title"}
			labels := aiResumeExactAgentLabels(r)
			rows := aiResumeSummaryRowsWithLabels([]aisessions.ResumeSummary{summary}, labels, time.Time{}, i18n.FallbackLocale, "/work", 0)
			if len(rows) != 2 || rows[1].Value != aiResumePickerValue(aiModeClaude, summary.ResumeID) || !strings.HasSuffix(stripANSI(rows[1].Label), tc.want) {
				t.Fatalf("rows = %#v want suffix %q", rows, tc.want)
			}
			if !strings.Contains(tc.want, "[") {
				baseline := aiResumeSummaryRowsWithLabels([]aisessions.ResumeSummary{summary}, nil, time.Time{}, i18n.FallbackLocale, "/work", 0)
				if !reflect.DeepEqual(rows, baseline) {
					t.Fatalf("no binding changed baseline: %#v vs %#v", rows, baseline)
				}
			}
		})
	}
}

func TestAIResumeBindingNamesPreserveFullSearchAndDetailWithinCellBudget(t *testing.T) {
	for _, tc := range []struct{ name, agent, pane, conversation string }{
		{"short", "builder", "builder-pane", "Conversation"},
		{"max names", strings.Repeat("a", 128), strings.Repeat("p", 128), "Conversation"},
		{"CJK", strings.Repeat("가", 42) + "a", strings.Repeat("面", 42) + "b", strings.Repeat("대화", 50)},
		{"short agent long pane", "a", strings.Repeat("p", 128), "Conversation"},
		{"long agent short pane", strings.Repeat("a", 128), "p", "Conversation"},
		{"agent only", strings.Repeat("a", 128), "", "Conversation"},
		{"ANSI conversation", "builder", "builder-pane", "\x1b[35m" + strings.Repeat("面", 100) + "\x1b[0m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := coremetadata.ValidateName(tc.agent); err != nil {
				t.Fatal(err)
			}
			if tc.pane != "" {
				if err := coremetadata.ValidateName(tc.pane); err != nil {
					t.Fatal(err)
				}
			}
			label := aiResumeExactAgentLabel{Name: tc.agent, PaneName: tc.pane, Context: registryview.Context{Value: tc.conversation, Source: registryview.ContextSourceAgentTopic}}
			trailing := aiResumeBindingConversation(label, tc.conversation)
			if i18n.TerminalCellWidth(trailing) > aiResumeTitleMaxCells || !strings.Contains(trailing, "] ") {
				t.Fatalf("width/grammar drift: %q (%d)", trailing, i18n.TerminalCellWidth(trailing))
			}
			summary := aisessions.ResumeSummary{Provider: aiModeClaude, ResumeID: "exact-id", Source: aisessions.SourceClaudeTranscript}
			row := aiResumeSessionRowWithResolvedLabel(aiResumeSessionMetaFromSummary(summary, ""), label, time.Time{}, i18n.FallbackLocale, "", 0)
			for _, locale := range []i18n.Locale{i18n.FallbackLocale, "ko-KR"} {
				detail := aiResumeDetailProjection(locale, summary, aisessions.ResumeDetailRef{}, aisessions.ResumeDetail{}, "preview", label)
				for _, name := range []string{tc.agent, tc.pane} {
					if name != "" && (!strings.Contains(row.SearchKey, name) || !strings.Contains(detail, name)) {
						t.Fatalf("full name missing: %#v %q", row, detail)
					}
				}
			}
			if tc.name == "max names" && !strings.Contains(trailing, "…") {
				t.Fatalf("missing ellipsis: %q", trailing)
			}
		})
	}
}

func TestAIResumeBindingSnapshotIsInvocationLocalAndAddsNoReadsOrWrites(t *testing.T) {
	r := resumeBindingFixture()
	before, _ := json.Marshal(r)
	var loads, previews, reads, writes atomic.Int32
	cmd := testAICommand(t.TempDir())
	cmd.loadRegistry = func() (coremetadata.Registry, error) { loads.Add(1); return r.Clone(), nil }
	cmd.updateRegistry = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) { writes.Add(1); return r, nil }
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		reads.Add(1)
		return nil, fmt.Errorf("unexpected command read")
	}
	cmd.readFile = func(string) ([]byte, error) { reads.Add(1); return nil, fmt.Errorf("unexpected file read") }
	cmd.writeFile = func(string, []byte, os.FileMode) error { writes.Add(1); return fmt.Errorf("unexpected write") }
	cmd.readResumePreview = func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		previews.Add(1)
		return aisessions.Preview{User: "preview"}, nil
	}
	summary := aisessions.ResumeSummary{Provider: aiModeClaude, ResumeID: "exact-binding-id", Source: aisessions.SourceClaudeTranscript, Label: "Suppressed title", Branch: "main"}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	controller.startOnce.Do(func() {})
	controller.summaries = []aisessions.ResumeSummary{summary, {Provider: aiModeClaude, ResumeID: "unbound-id", Source: aisessions.SourceClaudeTranscript}}
	controller.detailRefs[aiResumeExactLabelKey(summary.Provider, summary.ResumeID)] = aisessions.ResumeDetailRef{Provider: summary.Provider, ResumeID: summary.ResumeID, Source: summary.Source, RuntimeStatus: "idle"}
	rows := controller.entries(controller.summaries)
	footer, more := controller.footer()
	value := rows[1].Value
	controller.focus(value)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.SelectionDetail != nil && strings.Contains(update.SelectionDetail.TextByValue[value], "Turns:") && !strings.Contains(update.SelectionDetail.TextByValue[value], "Loading preview") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detail did not settle")
		}
		time.Sleep(time.Millisecond)
	}
	controller.focus(aiResumeNewValue)
	controller.focus(value)
	if got := controller.entries(controller.summaries); !reflect.DeepEqual(got, rows) {
		t.Fatalf("focus mutated frozen rows: %#v", got)
	}
	gotFooter, gotMore := controller.footer()
	if gotFooter != footer || gotMore != more {
		t.Fatal("binding focus mutated footer")
	}
	after, _ := json.Marshal(r)
	if string(after) != string(before) || loads.Load() != 1 || previews.Load() != 1 || reads.Load() != 0 || writes.Load() != 0 || len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("audit loads=%d previews=%d extra reads=%d writes=%d commands=%v", loads.Load(), previews.Load(), reads.Load(), writes.Load(), cmdRecorder(cmd).commands)
	}
	// A rename or cascade is visible only on the next invocation.
	r.Agents = nil
	r.Panes = nil
	if got := controller.entries(controller.summaries); !reflect.DeepEqual(got, rows) {
		t.Fatal("open invocation reloaded deleted binding")
	}
	next := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer next.close()
	fallback := next.entries(controller.summaries)
	if loads.Load() != 2 || strings.Contains(fallback[1].Label, "builder") || fallback[1].Value != value || len(fallback) != len(rows) {
		t.Fatalf("next-open fallback = %#v loads=%d", fallback, loads.Load())
	}
}

// Exercise the public native runner on an actual PTY. The helper process owns
// only a synthetic Registry snapshot; no tmux socket or provider is opened.
func TestAIResumeBindingNativeFramesLocaleAndSizeGolden(t *testing.T) {
	if locale := os.Getenv("_PROJMUX_BINDING_NATIVE_FIXTURE"); locale != "" {
		cmd := testAICommand(t.TempDir())
		cmd.loadRegistry = func() (coremetadata.Registry, error) {
			registry := resumeBindingFixture()
			if shape := os.Getenv("_PROJMUX_BINDING_NATIVE_NAMES"); shape != "" {
				registry.Agents[0].Metadata.Name, registry.Panes[0].Metadata.Name = resumeBindingLongNames(shape)
			}
			return registry, nil
		}
		controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
		defer controller.close()
		controller.locale = i18n.Locale(locale)
		summary := aisessions.ResumeSummary{Provider: aiModeClaude, ResumeID: "exact-binding-id", Source: aisessions.SourceClaudeTranscript, Branch: "main"}
		rows := controller.entries([]aisessions.ResumeSummary{summary})
		items := make([]intpicker.Item, len(rows))
		for i, row := range rows {
			items[i] = intpicker.Item{Label: row.Label, Title: row.Label, Value: row.Value, SearchText: row.SearchKey}
		}
		binding := controller.labels[aiResumeExactLabelKey(summary.Provider, summary.ResumeID)]
		detail := aiResumeDetailProjection(controller.locale, summary, aisessions.ResumeDetailRef{Source: summary.Source}, aisessions.ResumeDetail{}, "Fixture preview", binding)
		result, err := (intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout}).Run(intpicker.Options{UI: "ai-resume-picker", Locale: controller.locale, Title: "AI Resume", Prompt: "AI Resume > ", Items: items, InitialIndex: 1, InitialIndexSet: true, SelectionDetail: &intpicker.SelectionDetail{TextByValue: map[string]string{rows[1].Value: detail}}})
		if err != nil || !result.Closed {
			t.Fatalf("native close = %#v, %v", result, err)
		}
		return
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("native PTY fixture requires python3")
	}
	var golden strings.Builder
	for _, locale := range []string{"en-US", "ko-KR"} {
		for _, size := range [][2]int{{80, 24}, {120, 40}} {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			command := exec.CommandContext(ctx, "python3", "-c", resumeBindingPTYDriver, os.Args[0], locale, fmt.Sprint(size[0]), fmt.Sprint(size[1]))
			output, err := command.CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("native %s %v: %v\n%s", locale, size, err, output)
			}
			frame := regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`).ReplaceAllString(string(output), "")
			lines := strings.Split(strings.TrimRight(strings.ReplaceAll(frame, "\r", ""), "\n"), "\n")
			if len(lines) != size[1] {
				t.Fatalf("native %s %v got %d lines:\n%s", locale, size, len(lines), frame)
			}
			fmt.Fprintf(&golden, "[%s %dx%d]\n", locale, size[0], size[1])
			for i, line := range lines {
				if i18n.TerminalCellWidth(line) != size[0] {
					t.Fatalf("frame row %d width=%d: %q", i, i18n.TerminalCellWidth(line), line)
				}
				fmt.Fprintln(&golden, strings.TrimRight(line, " "))
			}
			if strings.Count(frame, "[builder → builder-pane]") != 2 {
				t.Fatalf("native row/detail binding missing %s %v:\n%s", locale, size, frame)
			}
		}
	}
	path := filepath.Join("testdata", "ai-resume-binding-native.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(golden.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if golden.String() != string(want) {
		t.Fatalf("native binding golden mismatch:\n%s", golden.String())
	}
}

func resumeBindingLongNames(shape string) (string, string) {
	if shape == "cjk" {
		var agent, pane strings.Builder
		for i := range 42 {
			agent.WriteRune(rune('가' + i))
			pane.WriteRune(rune('面' + i))
		}
		return agent.String() + "-a", pane.String() + "-p"
	}
	return strings.Repeat("abcdefghijklmnopqrstuvwxyz", 5)[:128], strings.Repeat("ZYXWVUTSRQPONMLKJIHGFEDCBA", 5)[:128]
}

func TestAIResumeBindingLongNamesAreRecoverableByNativeDetailScroll(t *testing.T) {
	for _, shape := range []string{"max", "cjk"} {
		for _, locale := range []i18n.Locale{i18n.FallbackLocale, "ko-KR"} {
			t.Run(shape+"/"+string(locale), func(t *testing.T) {
				agent, pane := resumeBindingLongNames(shape)
				for _, name := range []string{agent, pane} {
					if err := coremetadata.ValidateName(name); err != nil {
						t.Fatal(err)
					}
				}
				binding := aiResumeExactAgentLabel{Name: agent, PaneName: pane}
				lines := aiResumeBindingDetailOverflow(locale, binding)
				if len(lines) < 4 {
					t.Fatalf("long names need continuation lines: %#v", lines)
				}
				// Reconstruct complete names from the scrollable fields, independently
				// of the canonical unwrapped line that the viewport clips.
				var recovered [2]string
				field := -1
				for _, line := range lines {
					if i18n.TerminalCellWidth(line) > 64 {
						t.Fatalf("overflow row exceeds its native budget: %q", line)
					}
					if _, value, ok := strings.Cut(line, ": "); ok {
						field++
						recovered[field] = value
					} else {
						recovered[field] += strings.TrimSpace(line)
					}
				}
				if recovered != [2]string{agent, pane} {
					t.Fatalf("scrollable names = %q, %q", recovered[0], recovered[1])
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, "python3", "-c", resumeBindingPTYDriver, os.Args[0], string(locale), "80", "24", shape)
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("native scroll: %v\n%s", err, output)
				}
				visible := regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`).ReplaceAllString(string(output), "")
				for _, line := range lines {
					if !strings.Contains(visible, line) {
						t.Fatalf("native Shift-Down never exposed complete continuation %q:\n%s", line, visible)
					}
				}
			})
		}
	}
}

const resumeBindingPTYDriver = `
import os, pty, fcntl, termios, struct, subprocess, sys, select, time
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', int(sys.argv[4]), int(sys.argv[3]), 0, 0))
env = os.environ.copy()
for key in ('TMUX', 'TMUX_PANE'):
    env.pop(key, None)
env['_PROJMUX_BINDING_NATIVE_FIXTURE'] = sys.argv[2]
env['PROJMUX_NATIVE_TTY_FALLBACK'] = '0'
if len(sys.argv) > 5:
    env['_PROJMUX_BINDING_NATIVE_NAMES'] = sys.argv[5]
process = subprocess.Popen([sys.argv[1], '-test.run=^TestAIResumeBindingNativeFramesLocaleAndSizeGolden$', '-test.timeout=10s'], stdin=slave, stdout=slave, stderr=slave, env=env, close_fds=True)
os.close(slave)
buffer = b''
start, end = b'\x1b[?2026h', b'\x1b[?2026l'
deadline = time.monotonic()+12
try:
    while end not in buffer and time.monotonic() < deadline:
        if select.select([master], [], [], 0.1)[0]:
            buffer += os.read(master, 65536)
    if start not in buffer or end not in buffer:
        raise RuntimeError('native frame not completed: '+repr(buffer))
    frame = buffer.split(start,1)[1].split(end,1)[0]
    if len(sys.argv) > 5:
        for step in range(12):
            os.write(master, b'\x1b[1;2B')
            update = b''
            while end not in update and time.monotonic() < deadline:
                if select.select([master], [], [], 0.1)[0]:
                    update += os.read(master, 65536)
            if end not in update:
                raise RuntimeError('native detail scroll did not render')
            frame += b'\n' + update.split(start,1)[1].split(end,1)[0]
    os.write(master, b'\x03')
    process.wait(timeout=3)
    if process.returncode:
        raise RuntimeError('native helper failed: '+str(process.returncode))
    sys.stdout.buffer.write(frame)
finally:
    if process.poll() is None:
        process.kill()
        process.wait()
    os.close(master)
`
