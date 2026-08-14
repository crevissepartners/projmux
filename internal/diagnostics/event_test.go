package diagnostics

import (
	"reflect"
	"strings"
	"testing"
)

func TestEventSchemaHasNoGenericOrSensitiveEscapeHatch(t *testing.T) {
	t.Parallel()
	typeOf := reflect.TypeFor[Event]()
	want := []string{"At", "Level", "Component", "Event", "Result", "DurationMS", "RunID", "Version", "MuxBackend", "Command", "Subcommand", "Kind", "Message", "Operation", "Code", "Source", "Transition", "Disposition", "Provider", "Category", "Route", "AIKind", "AIResult", "ResourceResult", "Failure", "WindowCount", "PaneCount", "ShellRecipeCount", "AgentRecipeCount", "StartupRecipeCount", "ItemCount"}
	if typeOf.NumField() != len(want) {
		t.Fatalf("Event fields = %d, want %d", typeOf.NumField(), len(want))
	}
	for i, name := range want {
		if got := typeOf.Field(i).Name; got != name {
			t.Fatalf("Event field %d = %q, want %q", i, got, name)
		}
	}
}

func TestSanitizeMessage(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("한", 700)
	tests := []struct {
		name string
		in   string
		home string
		want string
	}{
		{name: "controls", in: "failed\n\x1b[31m\tthing\x00", want: "failed [31m thing"},
		{name: "home", in: "open /home/alice/private/file", home: "/home/alice", want: "open ~/private/file"},
		{name: "invalid utf8", in: "bad\xffvalue", want: "bad�value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeMessage(tt.in, tt.home); got != tt.want {
				t.Fatalf("SanitizeMessage() = %q, want %q", got, tt.want)
			}
		})
	}
	got := SanitizeMessage(long, "")
	if len([]rune(got)) != maxMessageRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded message runes = %d, suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
}

func TestClassifyAllowlistAndStatePolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want CommandClass
	}{
		{name: "unknown command drops raw", args: []string{"/home/alice/secret", "--token=secret"}, want: CommandClass{}},
		{name: "unknown subcommand drops raw", args: []string{"notify", "secret-body"}, want: CommandClass{Command: "notify"}},
		{name: "status success read only", args: []string{"status", "usage", "/secret/path"}, want: CommandClass{Command: "status", Subcommand: "usage"}},
		{name: "notify list read only", args: []string{"notify", "list", "--json"}, want: CommandClass{Command: "notify", Subcommand: "list"}},
		{name: "notify push changes state", args: []string{"notify", "push", "--text", "private"}, want: CommandClass{Command: "notify", Subcommand: "push", StateChanging: true}},
		{name: "ai ingest automatic success read only", args: []string{"ai", "ingest", "secret-provider"}, want: CommandClass{Command: "ai", Subcommand: "ingest"}},
		{name: "topic get read only", args: []string{"ai", "topic", "get", "--pane", "%1"}, want: CommandClass{Command: "ai", Subcommand: "topic"}},
		{name: "topic set changes state", args: []string{"ai", "topic", "set", "private topic"}, want: CommandClass{Command: "ai", Subcommand: "topic", StateChanging: true}},
		{name: "update check writes cache", args: []string{"update", "check"}, want: CommandClass{Command: "update", Subcommand: "check", StateChanging: true}},
		{name: "diagnostics viewer read only", args: []string{"diagnostics", "log"}, want: CommandClass{Command: "diagnostics", Subcommand: "log"}},
		{name: "diagnostics report explicit local write boundary", args: []string{"diagnostics", "report", "--output", "/secret/path"}, want: CommandClass{Command: "diagnostics", Subcommand: "report"}},
		{name: "terminal preview read only", args: []string{"setup", "terminal", "/secret"}, want: CommandClass{Command: "setup", Subcommand: "terminal"}},
		{name: "terminal apply changes state", args: []string{"setup", "terminal", "--apply", "/secret"}, want: CommandClass{Command: "setup", Subcommand: "terminal", StateChanging: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.args); got != tt.want {
				t.Fatalf("Classify() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestClassifyAutomaticHookAndPollSuccessesAreReadOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want CommandClass
	}{
		{name: "ai ingest", args: []string{"ai", "ingest", "codex-hook"}, want: CommandClass{Command: "ai", Subcommand: "ingest"}},
		{name: "attention arm", args: []string{"attention", "arm", "%1"}, want: CommandClass{Command: "attention", Subcommand: "arm"}},
		{name: "attention clear", args: []string{"attention", "clear", "%1"}, want: CommandClass{Command: "attention", Subcommand: "clear"}},
		{name: "attention window", args: []string{"attention", "window", "@1"}, want: CommandClass{Command: "attention", Subcommand: "window"}},
		{name: "session state autosave", args: []string{"tmux", "autosave-session-state", "--quiet"}, want: CommandClass{Command: "tmux", Subcommand: "autosave-session-state"}},
		{name: "recent window record", args: []string{"window", "record"}, want: CommandClass{Command: "window", Subcommand: "record"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.args); got != tt.want {
				t.Fatalf("Classify(%q) = %#v, want %#v", tt.args, got, tt.want)
			}
		})
	}
}

func TestClassifyCoversEveryTopLevelRule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args     []string
		changing bool
	}{
		{args: []string{"ai", "status", "set"}, changing: true},
		{args: []string{"attention", "list"}},
		{args: []string{"attach", "auto"}, changing: true},
		{args: []string{"current"}, changing: true},
		{args: []string{"diagnostics", "log"}},
		{args: []string{"doctor"}},
		{args: []string{"focus"}, changing: true},
		{args: []string{"hook", "validate"}},
		{args: []string{"key-broker"}, changing: true},
		{args: []string{"kill", "tagged"}, changing: true},
		{args: []string{"notify", "list"}},
		{args: []string{"pin", "list"}},
		{args: []string{"popup-wait-key"}},
		{args: []string{"preview", "select"}, changing: true},
		{args: []string{"prune", "session-state"}},
		{args: []string{"quit"}, changing: true},
		{args: []string{"resources"}},
		{args: []string{"sessions"}, changing: true},
		{args: []string{"session-state", "status"}},
		{args: []string{"session-popup", "preview"}},
		{args: []string{"settings"}, changing: true},
		{args: []string{"setup"}},
		{args: []string{"shell"}, changing: true},
		{args: []string{"status", "usage"}},
		{args: []string{"statusbar", "usage-refresh"}, changing: true},
		{args: []string{"switch"}, changing: true},
		{args: []string{"tag", "list"}},
		{args: []string{"tmux", "print-config"}},
		{args: []string{"update", "status"}},
		{args: []string{"upgrade"}, changing: true},
		{args: []string{"usage"}},
		{args: []string{"version"}},
		{args: []string{"welcome"}},
		{args: []string{"window", "recent"}, changing: true},
	}
	seen := make(map[string]bool, len(tests))
	for _, tt := range tests {
		class := Classify(tt.args)
		if class.Command != tt.args[0] || class.StateChanging != tt.changing {
			t.Errorf("Classify(%q) = %#v, changing want %v", tt.args, class, tt.changing)
		}
		seen[tt.args[0]] = true
	}
	if len(seen) != len(commandRules) {
		t.Fatalf("top-level classification coverage = %d, rules = %d", len(seen), len(commandRules))
	}
	for command := range commandRules {
		if !seen[command] {
			t.Errorf("missing classification audit case for %q", command)
		}
	}
}

func TestClassifyMultiModeCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args     []string
		changing bool
	}{
		{args: []string{"ai", "status", "help"}},
		{args: []string{"ai", "status", "set", "thinking"}, changing: true},
		{args: []string{"ai", "topic", "get"}},
		{args: []string{"ai", "topic", "clear"}, changing: true},
		{args: []string{"doctor", "--json"}},
		{args: []string{"doctor", "--install-missing"}},
		{args: []string{"doctor", "--install-missing=false"}},
		{args: []string{"doctor", "--install-missing", "--dry-run"}},
		{args: []string{"doctor", "--install-missing", "--dry-run=false"}},
		{args: []string{"prune", "session-state"}},
		{args: []string{"prune", "session-state", "delete"}, changing: true},
		{args: []string{"session-state", "restore", "--dry-run"}},
		{args: []string{"setup", "terminal"}},
		{args: []string{"setup", "terminal", "--apply"}, changing: true},
		{args: []string{"setup", "terminal", "--apply=false"}},
		{args: []string{"update", "check"}, changing: true},
		{args: []string{"update", "apply", "--dry-run"}},
		{args: []string{"update", "apply", "--dry-run=false"}, changing: true},
		{args: []string{"upgrade", "--dry-run"}},
		{args: []string{"upgrade", "--dry-run=false"}, changing: true},
		{args: []string{"welcome"}},
		{args: []string{"welcome", "--popup"}, changing: true},
		{args: []string{"welcome", "--popup=false"}},
		{args: []string{"window", "record"}},
		{args: []string{"window", "recent"}, changing: true},
	}
	for _, tt := range tests {
		if got := Classify(tt.args).StateChanging; got != tt.changing {
			t.Errorf("Classify(%q).StateChanging = %v, want %v", tt.args, got, tt.changing)
		}
	}
}

func TestClassifyDirectHelpIsReadOnly(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"attach", "help"},
		{"sessions", "--help"},
		{"settings", "-h"},
		{"shell", "-help"},
		{"switch", "--help"},
		{"switch", "--h"},
		{"upgrade", "--help"},
		{"upgrade", "--h"},
		{"current", "-help"},
		// The flag package returns flag.ErrHelp for these regardless of value,
		// and the CLI help boundary answers them with exit 0, so none of them
		// may be scored as a state change.
		{"switch", "--help=true"},
		{"switch", "--help=false"},
		{"upgrade", "-help=true"},
		{"current", "--h=false"},
		{"sessions", "-h="},
	}
	for _, args := range tests {
		class := Classify(args)
		if class.Command != args[0] || class.StateChanging {
			t.Errorf("Classify(%q) = %#v, want known read-only help", args, class)
		}
	}

	// A later help-looking token can be a flag value. Only direct top-level
	// help is excluded, so this remains conservatively mutation-capable.
	if got := Classify([]string{"upgrade", "--ref", "--help"}); !got.StateChanging {
		t.Fatalf("Classify(upgrade --ref --help) = %#v, want conservative changing classification", got)
	}

	// Near-miss spellings are ordinary flags, not help, so they stay
	// conservatively mutation-capable.
	for _, args := range [][]string{
		{"upgrade", "--helper"},
		{"upgrade", "--section=help"},
		{"upgrade", "-h5"},
	} {
		if got := Classify(args); !got.StateChanging {
			t.Errorf("Classify(%q) = %#v, want changing: this is not a help spelling", args, got)
		}
	}

	// Nested help of a state-changing sub-route is read-only too. The CLI help
	// boundary answers these with exit 0 and runs no handler, so scoring them as
	// mutations would record a phantom state change.
	for _, args := range [][]string{
		{"ai", "settings", "--help"},
		{"ai", "split", "-h"},
		{"ai", "topic", "set", "--help"},
		{"ai", "status", "set", "--help"},
		{"ai", "integrate", "codex", "--help"},
		{"prune", "session-state", "delete", "--help"},
		{"session-state", "restore", "--help"},
		{"update", "check", "--help"},
		{"update", "apply", "--help=true"},
		{"tmux", "apply", "--h"},
		{"statusbar", "click", "-help"},
		{"window", "recent", "--help"},
		{"notify", "push", "--help"},
		{"hook", "trust", "--help"},
		{"pin", "add", "--help"},
	} {
		if got := Classify(args); got.StateChanging {
			t.Errorf("Classify(%q) = %#v, want read-only nested help", args, got)
		}
	}

	// The scan must not walk past a flag into its value, and a later bare `help`
	// word can be a real positional value rather than a help request.
	for _, args := range [][]string{
		// `--help` here can only be the value of `--ref`.
		{"upgrade", "--ref", "--help"},
		// `pin add help` pins a directory literally named "help".
		{"pin", "add", "help"},
		{"pin", "remove", "help"},
		// Payload after `--` is never help.
		{"ai", "split", "--", "--help"},
	} {
		if got := Classify(args); !got.StateChanging {
			t.Errorf("Classify(%q) = %#v, want conservative changing classification", args, got)
		}
	}
}

func TestClassifyPreviewOnlyIntegrationIntents(t *testing.T) {
	t.Parallel()
	providers := []string{"codex", "claude", "antigravity", "tmux-bell"}
	for _, provider := range providers {
		args := []string{"ai", "integrate", provider, "--remove", "--dry-run"}
		if got := Classify(args); got.StateChanging {
			t.Errorf("Classify(%q) = %#v, want read-only dry-run", args, got)
		}
	}
	if got := Classify([]string{"ai", "integrate", "codex", "--dry-run=false"}); !got.StateChanging {
		t.Fatalf("Classify(ai integrate codex --dry-run=false) = %#v, want changing", got)
	}
	if got := Classify([]string{"ai", "integrate", "unknown", "--dry-run"}); !got.StateChanging {
		t.Fatalf("Classify(ai integrate unknown --dry-run) = %#v, want conservative changing", got)
	}
}

func TestMuxBackendIsTmux(t *testing.T) {
	t.Parallel()
	if got := MuxBackend(); got != "tmux" {
		t.Fatalf("MuxBackend() = %q, want tmux", got)
	}
}

func TestLifecycleSchemaRejectsMismatchedOutcomeCodes(t *testing.T) {
	t.Parallel()
	base := Event{
		At:         "2026-08-13T00:00:00Z",
		Level:      "error",
		Component:  "runtime",
		Event:      "lifecycle.outcome",
		Result:     "error",
		RunID:      "safe-run",
		Version:    "0.10.0",
		MuxBackend: "tmux",
		Kind:       "runtime",
		Operation:  string(OperationSessionCreate),
		Code:       string(CodeSessionCreateFailed),
	}
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "failure code on success", mutate: func(e *Event) { e.Level, e.Result, e.Kind = "info", "success", "" }},
		{name: "skipped code on error", mutate: func(e *Event) { e.Operation, e.Code = string(OperationTmuxApply), string(CodeTmuxApplyReloadSkipped) }},
		{name: "code outside session operation domain", mutate: func(e *Event) { e.Code = string(CodeTmuxApplyFailed) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := base
			tt.mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) = nil, want closed-schema rejection", event)
			}
		})
	}
}

func TestLifecycleSchemaCompositeTransitionMatrix(t *testing.T) {
	t.Parallel()
	allowed := []struct {
		outer Operation
		code  Code
	}{
		{OperationSessionCreate, CodeSessionCreateFailed},
		{OperationSessionCreate, CodeSessionAttachFailed},
		{OperationSessionCreate, CodeSessionSwitchFailed},
		{OperationSessionAttach, CodeSessionAttachFailed},
		{OperationSessionAttach, CodeSessionKillFailed},
		{OperationSessionSwitch, CodeSessionSwitchFailed},
		{OperationSessionSwitch, CodeSessionKillFailed},
		{OperationSessionKill, CodeSessionKillFailed},
		{OperationSessionKill, CodeSessionCreateFailed},
		{OperationSessionKill, CodeSessionAttachFailed},
		{OperationSessionKill, CodeSessionSwitchFailed},
	}
	rejected := []struct {
		outer Operation
		code  Code
	}{
		{OperationSessionAttach, CodeSessionCreateFailed},
		{OperationSessionAttach, CodeSessionSwitchFailed},
		{OperationSessionSwitch, CodeSessionCreateFailed},
		{OperationSessionSwitch, CodeSessionAttachFailed},
	}
	for _, tt := range append(allowed, rejected...) {
		t.Run(string(tt.outer)+"/"+string(tt.code), func(t *testing.T) {
			event := Event{
				At:         "2026-08-13T00:00:00Z",
				Level:      "error",
				Component:  "runtime",
				Event:      "lifecycle.outcome",
				Result:     "error",
				RunID:      "closed-composite",
				Version:    "0.10.0",
				MuxBackend: "tmux",
				Kind:       "runtime",
				Operation:  string(tt.outer),
				Code:       string(tt.code),
			}
			_, err := sanitizeEvent(event, "")
			wantAllowed := false
			for _, pair := range allowed {
				wantAllowed = wantAllowed || pair == tt
			}
			if wantAllowed && err != nil {
				t.Errorf("sanitizeEvent() error = %v, want allowed observed transition", err)
			}
			if !wantAllowed && err == nil {
				t.Error("sanitizeEvent() = nil, want rejection for unobserved transition")
			}
		})
	}
}
