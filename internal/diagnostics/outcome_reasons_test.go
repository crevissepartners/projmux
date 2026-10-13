package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type reasonError struct{ reason OutcomeReason }

func (e reasonError) Error() string                { return "private secret /private/input" }
func (e reasonError) OutcomeReason() OutcomeReason { return e.reason }

type reasonExitError struct{ reasonError }

func (reasonExitError) ExitCode() int { return 2 }

// Wrap without formatting the cause; neither classification nor recording may
// read the original error text.
type unreadableReasonError struct{ reasonError }

func (unreadableReasonError) Error() string { panic("error text read") }

type reasonWrapper struct{ error }

func (reasonWrapper) Error() string   { panic("wrapper text read") }
func (e reasonWrapper) Unwrap() error { return e.error }

func TestRecordOutcomeClosedReasonsAndStoreRoundTrip(t *testing.T) {
	var registry CommandOutcomeReasons
	if err := registry.Register(Classify([]string{"agent", "stop"}), []OutcomeReason{"not-ready"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Classify([]string{"agent", "resume"}), []OutcomeReason{"unavailable"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		args          []string
		err           error
		usage         bool
		registry      *CommandOutcomeReasons
		kind, message string
	}{
		{"default", []string{"agent", "stop"}, reasonError{"not-ready"}, false, nil, "runtime", "command failed"},
		{"empty", []string{"agent", "stop"}, reasonError{"not-ready"}, false, &CommandOutcomeReasons{}, "runtime", "command failed"},
		{"registered", []string{"agent", "stop"}, reasonError{"not-ready"}, false, &registry, "runtime", "command failed: not-ready"},
		{"wrapped", []string{"agent", "stop"}, reasonWrapper{unreadableReasonError{reasonError{"not-ready"}}}, false, &registry, "runtime", "command failed: not-ready"},
		{"unknown", []string{"agent", "stop"}, reasonError{"private-secret"}, false, &registry, "runtime", "command failed"},
		{"raw token", []string{"agent", "stop"}, reasonError{"/private/input\nsecret"}, false, &registry, "runtime", "command failed"},
		{"other subcommand", []string{"agent", "resume"}, reasonError{"not-ready"}, false, &registry, "runtime", "command failed"},
		{"other command", []string{"runtime", "stop"}, reasonError{"not-ready"}, false, &registry, "runtime", "command failed"},
		{"unknown argv", []string{"unknown", "stop"}, reasonError{"not-ready"}, false, &registry, "runtime", "command failed"},
		{"usage", []string{"agent", "stop"}, reasonError{"not-ready"}, true, &registry, "usage", "invalid command usage"},
		{"coded exit", []string{"agent", "stop"}, reasonExitError{reasonError{"not-ready"}}, false, &registry, "exit", "command completed with a non-success status"},
		{"wrapped coded exit", []string{"agent", "stop"}, fmt.Errorf("outer: %w", reasonExitError{reasonError{"not-ready"}}), false, &registry, "exit", "command completed with a non-success status"},
		{"untyped", []string{"agent", "stop"}, errors.New("private secret"), false, &registry, "runtime", "command failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal")
			store := NewStore(path)
			var err error
			if tt.name == "default" {
				err = RecordOutcome(store, tt.args, "run", "1.0.0", "tmux", time.Now(), tt.err, tt.usage, false)
			} else {
				err = RecordOutcome(store, tt.args, "run", "1.0.0", "tmux", time.Now(), tt.err, tt.usage, false, tt.registry)
			}
			if err != nil {
				t.Fatal(err)
			}
			events, err := store.Read()
			if err != nil || len(events) != 1 {
				t.Fatalf("read: %v, %v", events, err)
			}
			readOnly, err := store.ReadOnly()
			if err != nil || len(readOnly.Events) != 1 || readOnly.Malformed != 0 {
				t.Fatalf("read only: %v, %v", readOnly, err)
			}
			for _, event := range []Event{events[0], readOnly.Events[0]} {
				if event.Kind != tt.kind || event.Message != tt.message {
					t.Fatalf("event: %#v", event)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"private", "secret", "/input", "outer"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("raw error content recorded: %q", secret)
				}
			}
		})
	}
}

func TestOutcomeReasonRegistrationCopiesAndRejectsWithoutEffects(t *testing.T) {
	var registry CommandOutcomeReasons
	class := Classify([]string{"agent", "stop"})
	tokens := []OutcomeReason{"not-ready"}
	if err := registry.Register(class, tokens); err != nil {
		t.Fatal(err)
	}
	tokens[0] = "changed"
	class.StateChanging = !class.StateChanging
	if reason, ok := outcomeReason(class, reasonError{"not-ready"}, &registry); !ok || reason != "not-ready" {
		t.Fatal("registered set changed")
	}
	invalid := []struct {
		class  CommandClass
		tokens []OutcomeReason
	}{
		{class, []OutcomeReason{"replacement"}},
		{CommandClass{}, []OutcomeReason{"valid"}},
		{CommandClass{Command: "agent", Subcommand: "bad class"}, []OutcomeReason{"valid"}},
		{CommandClass{Command: "/private/path"}, []OutcomeReason{"valid"}},
		{CommandClass{Command: "agent", Subcommand: "resume"}, nil},
		{CommandClass{Command: "agent", Subcommand: "resume"}, []OutcomeReason{"valid", "valid"}},
	}
	for _, token := range []OutcomeReason{"", "Upper", "a/b", "a b", "a\nsecret", "a\u200bsecret", "é", "a-", OutcomeReason(strings.Repeat("a", 97))} {
		invalid = append(invalid, struct {
			class  CommandClass
			tokens []OutcomeReason
		}{Classify([]string{"agent", "resume"}), []OutcomeReason{"valid", token}})
	}
	for _, tt := range invalid {
		if err := registry.Register(tt.class, tt.tokens); err == nil {
			t.Fatalf("accepted invalid registration: %#v", tt)
		}
		if _, ok := outcomeReason(Classify([]string{"agent", "resume"}), reasonError{"valid"}, &registry); ok {
			t.Fatal("partial registration")
		}
		if _, ok := outcomeReason(class, reasonError{"replacement"}, &registry); ok {
			t.Fatal("conflict overwrote registration")
		}
	}
	if _, ok := outcomeReason(class, reasonError{"not-ready"}, &registry); !ok {
		t.Fatal("original registration lost")
	}
	if err := registry.Register(Classify([]string{"agent", "resume"}), []OutcomeReason{"valid"}); err != nil {
		t.Fatal("failed registration reserved class")
	}
	var nilRegistry *CommandOutcomeReasons
	if err := nilRegistry.Register(class, tokens); err == nil {
		t.Fatal("nil accepted")
	}
}

func TestOutcomeReasonsConcurrentRegistrationAndLookup(t *testing.T) {
	var registry CommandOutcomeReasons
	class := Classify([]string{"agent", "stop"})
	var wg sync.WaitGroup
	successes := make(chan bool, 32)
	for range 32 {
		wg.Go(func() {
			successes <- registry.Register(class, []OutcomeReason{"not-ready"}) == nil
			for range 100 {
				reason, ok := outcomeReason(class, reasonError{"not-ready"}, &registry)
				if !ok || reason != "not-ready" {
					t.Error("concurrent lookup lost published set")
				}
				if _, ok := outcomeReason(Classify([]string{"agent", "resume"}), reasonError{"not-ready"}, &registry); ok {
					t.Error("class leak")
				}
			}
		})
	}
	wg.Wait()
	close(successes)
	count := 0
	for success := range successes {
		if success {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("successful registrations = %d", count)
	}
}

func TestRecordOutcomeRegisteredReasonsPreserveNoWriteAndBestEffort(t *testing.T) {
	var registry CommandOutcomeReasons
	for _, args := range [][]string{{"doctor"}, {"diagnostics", "report"}, {"agent", "stop"}, {"attention", "toggle"}} {
		if err := registry.Register(Classify(args), []OutcomeReason{"not-ready"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"doctor", "--invalid"}, {"diagnostics", "report", "--invalid"}, {"current"}, {"agent", "stop"}} {
		path := filepath.Join(t.TempDir(), "absent", "journal")
		if err := RecordOutcome(NewStore(path), args, "run", "1.0.0", "tmux", time.Now(), reasonError{"not-ready"}, false, args[0] == "agent", &registry); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("no-write path created: %v", err)
		}
	}
	store := NewStore(filepath.Join(t.TempDir(), "journal"))
	if err := RecordOutcome(store, []string{"agent", "stop"}, "run", "1.0.0", "tmux", time.Now(), nil, false, false, &registry); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil || len(events) != 0 {
		t.Fatal("read-only success wrote")
	}
	if err := RecordOutcome(store, []string{"attention", "toggle"}, "run", "1.0.0", "tmux", time.Now(), nil, false, false, &registry); err != nil {
		t.Fatal(err)
	}
	events, err = store.Read()
	if err != nil || len(events) != 1 || events[0].Message != "" || events[0].Result != "success" {
		t.Fatal("success policy changed")
	}
	blocked := NewStore(filepath.Join(t.TempDir(), "file", "journal"))
	if err := os.WriteFile(filepath.Dir(blocked.path), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecordOutcome(blocked, []string{"agent", "stop"}, "run", "1.0.0", "tmux", time.Now(), reasonError{"not-ready"}, false, false, &registry); err == nil {
		t.Fatal("append failure hidden")
	}
}

func TestOutcomeReasonTokenLengthAndSanitizerBoundary(t *testing.T) {
	var registry CommandOutcomeReasons
	token := OutcomeReason(strings.Repeat("a", 96))
	if err := registry.Register(Classify([]string{"agent", "stop"}), []OutcomeReason{token}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "journal"))
	if err := RecordOutcome(store, []string{"agent", "stop"}, "run", "1.0.0", "tmux", time.Now(), reasonError{token}, false, false, &registry); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	if events[0].Message != "command failed: "+string(token) {
		t.Fatal("token was sanitized")
	}
	// Direct messages keep their existing independent sanitizer contract.
	for _, n := range []int{maxMessageRunes, maxMessageRunes + 1} {
		event := events[0]
		event.Message = strings.Repeat("界", n)
		other := NewStore(filepath.Join(t.TempDir(), "journal"))
		if err := other.Append(event); err != nil {
			t.Fatal(err)
		}
		read, err := other.Read()
		if err != nil || len(read) != 1 {
			t.Fatal(err)
		}
		ro, err := other.ReadOnly()
		if err != nil || len(ro.Events) != 1 {
			t.Fatal(err)
		}
		want := event.Message
		if n > maxMessageRunes {
			want = strings.Repeat("界", maxMessageRunes-1) + "…"
		}
		if read[0].Message != want || ro.Events[0].Message != want {
			t.Fatal("reader sanitizer boundary changed")
		}
	}
}
