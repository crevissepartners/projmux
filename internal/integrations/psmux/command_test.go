package psmux

import (
	"strings"
	"testing"
)

func TestRenderPowerShellCommandQuotesWindowsPathAndSensitiveArgs(t *testing.T) {
	t.Parallel()

	got, err := RenderPowerShellCommand(
		`C:\Program Files\projmux weird\projmux.exe`,
		"shell",
		"--config",
		`C:\Users\Ada Lovelace\AppData\Local\projmux\ps mux.conf`,
		"--session",
		"quote'd",
		`double"quote`,
		"backtick`value",
		"dollar$value",
		"amp&pipe|redirect<>semi;paren()brace{}caret^percent%bang!",
		"",
	)
	if err != nil {
		t.Fatalf("RenderPowerShellCommand() error = %v", err)
	}

	want := "& 'C:\\Program Files\\projmux weird\\projmux.exe' 'shell' '--config' 'C:\\Users\\Ada Lovelace\\AppData\\Local\\projmux\\ps mux.conf' '--session' 'quote''d' 'double\"quote' 'backtick`value' 'dollar$value' 'amp&pipe|redirect<>semi;paren()brace{}caret^percent%bang!' ''"
	if got != want {
		t.Fatalf("RenderPowerShellCommand() = %q, want %q", got, want)
	}
}

func TestCommandPowerShellCopiesArgv(t *testing.T) {
	t.Parallel()

	args := []string{"shell", "--config", `C:\Users\Ada Lovelace\psmux.conf`}
	cmd := ProjmuxCommand(`C:\Program Files\projmux\projmux.exe`, args...)
	args[0] = "mutated"

	got, err := cmd.PowerShell()
	if err != nil {
		t.Fatalf("PowerShell() error = %v", err)
	}
	want := "& 'C:\\Program Files\\projmux\\projmux.exe' 'shell' '--config' 'C:\\Users\\Ada Lovelace\\psmux.conf'"
	if got != want {
		t.Fatalf("PowerShell() = %q, want %q", got, want)
	}
}

func TestRenderWindowsCommandLineUsesCreateProcessQuoting(t *testing.T) {
	t.Parallel()

	got, err := RenderWindowsCommandLine(
		`C:\Program Files\projmux\projmux.exe`,
		"shell",
		"--config",
		`C:\Users\Ada Lovelace\AppData\Local\projmux\psmux config\`,
		`quote"d`,
		`backslash\quote"trail\`,
		"backtick`dollar$amp&pipe|redirect<>semi;paren()brace{}caret^percent%bang!",
		"",
	)
	if err != nil {
		t.Fatalf("RenderWindowsCommandLine() error = %v", err)
	}

	want := `"C:\Program Files\projmux\projmux.exe" shell --config "C:\Users\Ada Lovelace\AppData\Local\projmux\psmux config\\" "quote\"d" "backslash\quote\"trail\\" backtick` + "`" + `dollar$amp&pipe|redirect<>semi;paren()brace{}caret^percent%bang! ""`
	if got != want {
		t.Fatalf("RenderWindowsCommandLine() = %q, want %q", got, want)
	}
}

func TestRenderCommandRejectsControlCharacters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func() (string, error)
		want string
	}{
		{
			name: "PowerShell executable newline",
			run:  func() (string, error) { return RenderPowerShellCommand("C:\\bin\\projmux.exe\nwhoami") },
			want: "executable contains an unsupported control character",
		},
		{
			name: "PowerShell arg newline",
			run:  func() (string, error) { return RenderPowerShellCommand("C:\\bin\\projmux.exe", "shell\nwhoami") },
			want: "arg 0 contains an unsupported control character",
		},
		{
			name: "Windows command line nul",
			run:  func() (string, error) { return RenderWindowsCommandLine("C:\\bin\\projmux.exe", "shell\x00whoami") },
			want: "arg 0 contains an unsupported control character",
		},
		{
			name: "empty executable",
			run:  func() (string, error) { return RenderPowerShellCommand(" ") },
			want: "executable is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
