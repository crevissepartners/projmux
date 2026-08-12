package diagnostics

import (
	"reflect"
	"strings"
	"testing"
)

func TestEventSchemaHasNoGenericOrSensitiveEscapeHatch(t *testing.T) {
	t.Parallel()
	typeOf := reflect.TypeFor[Event]()
	want := []string{"At", "Level", "Component", "Event", "Result", "DurationMS", "RunID", "Version", "MuxBackend", "Command", "Subcommand", "Kind", "Message"}
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
		{name: "ai ingest changes state", args: []string{"ai", "ingest", "secret-provider"}, want: CommandClass{Command: "ai", Subcommand: "ingest", StateChanging: true}},
		{name: "topic get read only", args: []string{"ai", "topic", "get", "--pane", "%1"}, want: CommandClass{Command: "ai", Subcommand: "topic"}},
		{name: "topic set changes state", args: []string{"ai", "topic", "set", "private topic"}, want: CommandClass{Command: "ai", Subcommand: "topic", StateChanging: true}},
		{name: "update check writes cache", args: []string{"update", "check"}, want: CommandClass{Command: "update", Subcommand: "check", StateChanging: true}},
		{name: "diagnostics viewer read only", args: []string{"diagnostics", "log"}, want: CommandClass{Command: "diagnostics", Subcommand: "log"}},
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
		{args: []string{"init", "ghostty"}},
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
		{args: []string{"doctor", "--install-missing"}, changing: true},
		{args: []string{"prune", "session-state"}},
		{args: []string{"prune", "session-state", "delete"}, changing: true},
		{args: []string{"setup", "terminal"}},
		{args: []string{"setup", "terminal", "--apply"}, changing: true},
		{args: []string{"update", "check"}, changing: true},
		{args: []string{"welcome"}},
		{args: []string{"welcome", "--popup"}, changing: true},
		{args: []string{"window", "record"}, changing: true},
		{args: []string{"window", "recent"}, changing: true},
	}
	for _, tt := range tests {
		if got := Classify(tt.args).StateChanging; got != tt.changing {
			t.Errorf("Classify(%q).StateChanging = %v, want %v", tt.args, got, tt.changing)
		}
	}
}

func TestMuxBackendDoesNotCopyUnknownEnvironmentValue(t *testing.T) {
	t.Parallel()
	if got := MuxBackend(func(string) string { return "secret-backend" }, "linux"); got != "tmux" {
		t.Fatalf("MuxBackend() = %q, want tmux", got)
	}
}
