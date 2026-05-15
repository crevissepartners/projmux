package psmux

import (
	"fmt"
	"strings"
)

// Command is a native Windows process invocation. Keep it as argv until the
// last integration boundary; generated config rendering must choose an
// explicit shell/process policy instead of reusing POSIX shell quoting.
type Command struct {
	Executable string
	Args       []string
}

// ProjmuxCommand returns a native invocation for projmux.exe and a copied argv.
func ProjmuxCommand(executable string, args ...string) Command {
	return Command{Executable: executable, Args: append([]string(nil), args...)}
}

// PowerShell renders c as one PowerShell-native invocation line suitable for
// generated psmux config entries that must go through PowerShell.
func (c Command) PowerShell() (string, error) {
	return RenderPowerShellCommand(c.Executable, c.Args...)
}

// WindowsCommandLine renders c as a CreateProcess-compatible command line.
// This is for native process APIs that need a single Windows command-line
// string and C-runtime argv parsing. It is not cmd.exe /c quoting; any future
// cmd.exe shell hop needs a separate helper and tests.
func (c Command) WindowsCommandLine() (string, error) {
	return RenderWindowsCommandLine(c.Executable, c.Args...)
}

// RenderPowerShellCommand renders a native command invocation for PowerShell.
// Each argv element is a single-quoted literal, so spaces, backticks, dollar
// signs, ampersands, and other PowerShell metacharacters remain data. Single
// quotes are represented by PowerShell's doubled quote form.
func RenderPowerShellCommand(executable string, args ...string) (string, error) {
	if err := validateCommandArg("executable", executable, false); err != nil {
		return "", err
	}
	parts := make([]string, 0, len(args)+2)
	parts = append(parts, "&", powerShellSingleQuote(executable))
	for i, arg := range args {
		if err := validateCommandArg(fmt.Sprintf("arg %d", i), arg, true); err != nil {
			return "", err
		}
		parts = append(parts, powerShellSingleQuote(arg))
	}
	return strings.Join(parts, " "), nil
}

// RenderWindowsCommandLine renders argv using the Windows command-line quoting
// rules used by CreateProcess/C runtime argv parsing. Shell metacharacters are
// not escaped because this output is not a shell script. Do not pass the
// result through cmd.exe /c or PowerShell.
func RenderWindowsCommandLine(executable string, args ...string) (string, error) {
	if err := validateCommandArg("executable", executable, false); err != nil {
		return "", err
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, windowsCommandLineArg(executable))
	for i, arg := range args {
		if err := validateCommandArg(fmt.Sprintf("arg %d", i), arg, true); err != nil {
			return "", err
		}
		parts = append(parts, windowsCommandLineArg(arg))
	}
	return strings.Join(parts, " "), nil
}

func validateCommandArg(label, value string, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if strings.ContainsRune(value, 0) || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s contains an unsupported control character", label)
	}
	return nil
}

func powerShellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func windowsCommandLineArg(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\"") {
		return value
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range value {
		if r == '\\' {
			backslashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			b.WriteRune(r)
			backslashes = 0
			continue
		}
		if backslashes > 0 {
			b.WriteString(strings.Repeat("\\", backslashes))
			backslashes = 0
		}
		b.WriteRune(r)
	}
	if backslashes > 0 {
		b.WriteString(strings.Repeat("\\", backslashes*2))
	}
	b.WriteByte('"')
	return b.String()
}
