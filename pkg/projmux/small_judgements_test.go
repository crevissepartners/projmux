package projmux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestIsOperatorMessageOriginEqualsInternal(t *testing.T) {
	long := strings.Repeat("a", 33)
	var operators int
	for _, tc := range []core.MessageOrigin{
		{Kind: "operator", Client: "popup"}, {Kind: "operator", Client: "cli"}, {Kind: "operator", Client: "a1-b2"}, {Kind: "operator", Client: strings.Repeat("a", 32)},
		{Kind: "operator", Client: ""}, {Kind: "operator", Client: "Popup"}, {Kind: "operator", Client: " popup"},
		{Kind: "operator", Client: "popup "}, {Kind: "operator", Client: "1popup"}, {Kind: "operator", Client: "-x"},
		{Kind: "operator", Client: "pop up"}, {Kind: "operator", Client: long},
		{Kind: "Operator", Client: "popup"}, {Kind: " operator", Client: "popup"}, {Kind: "operator ", Client: "popup"},
		{Kind: "agent", Client: "popup"}, {Kind: "agent", Client: ""}, {Kind: "", Client: "popup"}, {},
	} {
		got, want := projmux.IsOperatorMessageOrigin(tc), message.IsOperatorOrigin(tc.Kind, tc.Client)
		if got != want {
			t.Fatalf("%+v: public %v internal %v", tc, got, want)
		}
		if got {
			operators++
		}
	}
	if operators != 4 {
		t.Fatalf("representative table has %d operator origins, want 4", operators)
	}
}

func TestMessagePayloadSHA256EqualsInternal(t *testing.T) {
	for _, payload := range []string{"", "hello", "안녕 🌿", "line one\nline two\r\n", "\x00\xff not utf-8", strings.Repeat("x", 100000)} {
		got := projmux.MessagePayloadSHA256(payload)
		if want := message.PayloadSHA256(payload); got != want || len(got) != 64 || got != strings.ToLower(got) {
			t.Fatalf("%q: public %q internal %q", payload, got, want)
		}
	}
	if got := projmux.MessagePayloadSHA256(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty payload digest %q", got)
	}
}

// A history entry carries the digest the rule produces, so the public function
// agrees with the value the public archive read returns for the same payload.
func TestMessagePayloadSHA256EqualsArchiveEntry(t *testing.T) {
	root, c := archiveFixture(t)
	const payload = "안녕 🌿"
	path := filepath.Join(root, "agent-messages", "history.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := projmux.MessagePayloadSHA256(payload)
	if err = os.WriteFile(path, []byte(strings.Replace(string(b), `"fixture-digest"`, fmt.Sprintf("%q", digest), 1)), 0600); err != nil {
		t.Fatal(err)
	}
	archive, err := c.ReadMessageArchive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var matched int
	for _, h := range archive.History {
		for _, r := range archive.Records {
			if h.MessageRef == r.MessageRef && h.PayloadSHA256 != "" {
				matched++
				if r.Payload != payload || h.PayloadSHA256 != projmux.MessagePayloadSHA256(r.Payload) {
					t.Fatalf("history digest %q, payload %q", h.PayloadSHA256, r.Payload)
				}
			}
		}
	}
	if matched != 1 {
		t.Fatalf("fixture paired %d history entries with a payload, want 1", matched)
	}
}

func TestMatchesProcessWaitEqualsInternal(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	binding := core.ProcessBinding{HostInstanceID: "host", ProjectUID: "proj", WindowUID: "win", AgentUID: "agent", PaneUID: "pane", Generation: "gen", OperationID: "op"}
	code := func(n int) *int { return &n }
	wait := func(edit func(*core.TerminationEvidence)) *core.TerminationEvidence {
		r := &core.TerminationEvidence{Source: core.TerminationSourceSupervisor, Classification: "normal", ObservedAt: at, PaneUID: "pane", AgentUID: "agent", Generation: "gen", OperationID: "op", ExitCode: code(0)}
		if edit != nil {
			edit(r)
		}
		return r
	}
	var matches, refusals int
	for _, tc := range []struct {
		name    string
		binding core.ProcessBinding
		receipt *core.TerminationEvidence
	}{
		{"nil receipt", binding, nil},
		{"zero binding and receipt", core.ProcessBinding{}, &core.TerminationEvidence{}},
		{"exact normal", binding, wait(nil)},
		{"control action source", binding, wait(func(r *core.TerminationEvidence) { r.Source = core.TerminationSourceControlAction })},
		{"reconcile source", binding, wait(func(r *core.TerminationEvidence) { r.Source = "reconcile" })},
		{"empty source", binding, wait(func(r *core.TerminationEvidence) { r.Source = "" })},
		{"zero observed at", binding, wait(func(r *core.TerminationEvidence) { r.ObservedAt = time.Time{} })},
		{"pane differs", binding, wait(func(r *core.TerminationEvidence) { r.PaneUID = "other" })},
		{"agent differs", binding, wait(func(r *core.TerminationEvidence) { r.AgentUID = "other" })},
		{"generation differs", binding, wait(func(r *core.TerminationEvidence) { r.Generation = "other" })},
		{"operation differs", binding, wait(func(r *core.TerminationEvidence) { r.OperationID = "other" })},
		{"host, project and window are not compared", core.ProcessBinding{HostInstanceID: "h2", ProjectUID: "p2", WindowUID: "w2", AgentUID: "agent", PaneUID: "pane", Generation: "gen", OperationID: "op"}, wait(nil)},
		{"no exit code and no signal", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode = nil })},
		{"signal without exit code, abnormal", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Signal, r.Classification = nil, "TERM", "abnormal" })},
		{"signal without exit code, normal", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Signal = nil, "TERM" })},
		{"signal without exit code, wrong class", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Signal, r.Classification = nil, "TERM", "unknown" })},
		{"hup killed", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Signal, r.Classification = nil, "HUP", "killed" })},
		{"hup killed spaced lowercase", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Signal, r.Classification = nil, " hup ", "killed" })},
		{"killed with exit code", binding, wait(func(r *core.TerminationEvidence) { r.Classification = "killed" })},
		{"killed with other signal", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Signal, r.Classification = nil, "TERM", "killed" })},
		{"killed from control action", binding, wait(func(r *core.TerminationEvidence) {
			r.Source, r.ExitCode, r.Signal, r.Classification = core.TerminationSourceControlAction, nil, "HUP", "killed"
		})},
		{"exit code -1", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Classification = code(-1), "abnormal" })},
		{"exit code 256", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Classification = code(256), "abnormal" })},
		{"exit code 255", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Classification = code(255), "abnormal" })},
		{"exit code 1 abnormal", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode, r.Classification = code(1), "abnormal" })},
		{"exit code 1 declared normal", binding, wait(func(r *core.TerminationEvidence) { r.ExitCode = code(1) })},
		{"exit code 0 declared abnormal", binding, wait(func(r *core.TerminationEvidence) { r.Classification = "abnormal" })},
		{"exit code and signal together", binding, wait(func(r *core.TerminationEvidence) { r.Signal = "TERM" })},
		{"intentional", binding, wait(func(r *core.TerminationEvidence) { r.Classification = "intentional" })},
		{"interrupted", binding, wait(func(r *core.TerminationEvidence) { r.Classification = "interrupted" })},
		{"unknown class", binding, wait(func(r *core.TerminationEvidence) { r.Classification = "unknown" })},
		{"unlisted class", binding, wait(func(r *core.TerminationEvidence) { r.Classification = "other" })},
	} {
		var internalReceipt *metadata.TerminationEvidence
		if tc.receipt != nil {
			internalReceipt = new(metadata.TerminationEvidence)
			b, err := json.Marshal(tc.receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, internalReceipt); err != nil {
				t.Fatal(err)
			}
		}
		var internalBinding metadata.ProcessBinding
		b, err := json.Marshal(tc.binding)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &internalBinding); err != nil {
			t.Fatal(err)
		}
		got, want := projmux.MatchesProcessWait(tc.binding, tc.receipt), metadata.MatchesProcessWait(internalBinding, internalReceipt)
		if got != want {
			t.Fatalf("%s: public %v internal %v", tc.name, got, want)
		}
		if got {
			matches++
		} else {
			refusals++
		}
	}
	if matches < 6 || refusals < 15 {
		t.Fatalf("representative table has %d matches and %d refusals", matches, refusals)
	}
}
