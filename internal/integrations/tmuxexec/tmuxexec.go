// Package tmuxexec is the one place projmux builds an external process that
// may be a tmux command client.
//
// A tmux client whose locale is not UTF-8 (no LANG/LC_* at all, or LC_ALL=C)
// rewrites every non-printable or non-ASCII byte of format output to "_". The
// field separators projmux formats use (\x1f and \t) then vanish, and a row
// such as "$0\x1f@0\x1f%0" arrives as "$0_@0_%0". Measured on tmux 3.6: `-u`
// makes such a client emit exactly the bytes a UTF-8 client emits, for the
// separators and for UTF-8 field values, and it changes nothing for a client
// that already runs in a UTF-8 locale. It is a client flag only: it does not
// touch the environment the client hands to a server it starts, so pane shells
// and the server keep the caller's locale.
//
// Command and CommandContext add `-u` to every tmux command client. They leave
// out the clients that take over a terminal (attach-session and a new-session
// that is not detached), since `-u` there decides how the terminal is drawn.
// Every other process name passes through untouched, so a generic runner can
// route all of its commands here.
//
// TestTmuxSpawnSitesAreClosed keeps every process spawn in a tmux-capable
// package (one whose source holds the literal "tmux" or that imports a tmux
// integration package) either behind these functions or on a closed list of
// spawns that never run tmux, and fails a literal "tmux" spawn anywhere else.
// It does not see a package that runs tmux with neither the literal nor such
// an import, for example a program name read from settings.
package tmuxexec

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// utf8ClientFlag is tmux's global flag that makes the client treat its output
// as UTF-8 regardless of LC_ALL, LC_CTYPE, and LANG.
const utf8ClientFlag = "-u"

// Command is exec.Command with Args applied.
func Command(name string, args ...string) *exec.Cmd {
	return exec.Command(name, Args(name, args)...) // #nosec G204 -- argv is the caller's own argv plus the fixed tmux -u flag; no shell.
}

// CommandContext is exec.CommandContext with Args applied.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, Args(name, args)...) // #nosec G204 -- argv is the caller's own argv plus the fixed tmux -u flag; no shell.
}

// Args returns the argv (without the program name) projmux runs for name and
// args. For a tmux command client it is args with the UTF-8 client flag
// after the global flags; for anything else it is args unchanged. The caller's slice is never
// modified, so the caller can keep naming its own args in error messages.
func Args(name string, args []string) []string {
	if !isTmux(name) || hasUTF8ClientFlag(args) || takesOverTerminal(args) {
		return args
	}
	// The flag goes last among the global flags, right before the command, so
	// a leading -L/-S route stays the first argument.
	start, _ := scanGlobalFlags(args)
	out := make([]string, 0, len(args)+1)
	out = append(out, args[:start]...)
	out = append(out, utf8ClientFlag)
	return append(out, args[start:]...)
}

func isTmux(name string) bool {
	return filepath.Base(strings.TrimSpace(name)) == "tmux"
}

// tmuxGlobalValueFlags are the tmux global flags that take a value.
var tmuxGlobalValueFlags = map[byte]bool{'c': true, 'f': true, 'L': true, 'S': true, 'T': true}

// scanGlobalFlags walks the global flags in front of the tmux command. It
// returns the index where the command starts and whether -u is among the
// flags; a flag's value is never read as a flag.
func scanGlobalFlags(args []string) (commandStart int, utf8 bool) {
	i := 0
	for i < len(args) {
		token := args[i]
		if token == "--" {
			return i + 1, utf8
		}
		if len(token) < 2 || token[0] != '-' {
			return i, utf8
		}
		i++
		for j := 1; j < len(token); j++ {
			if token[j] == 'u' {
				utf8 = true
			}
			if tmuxGlobalValueFlags[token[j]] {
				if j == len(token)-1 {
					i++
				}
				break
			}
		}
	}
	return min(i, len(args)), utf8
}

func hasUTF8ClientFlag(args []string) bool {
	_, utf8 := scanGlobalFlags(args)
	return utf8
}

// newSessionValueFlags are the new-session flags that take a value.
var newSessionValueFlags = map[byte]bool{'c': true, 'e': true, 'F': true, 'f': true, 'n': true, 's': true, 't': true, 'x': true, 'y': true}

// takesOverTerminal reports whether the tmux invocation becomes an interactive
// client drawn on the caller's terminal: attach-session, or new-session
// without -d or with -A (which attaches to an existing session even with -d).
// No command at all is a new-session.
func takesOverTerminal(args []string) bool {
	start, _ := scanGlobalFlags(args)
	command := args[start:]
	if len(command) == 0 {
		return true
	}
	switch command[0] {
	case "attach-session", "attach", "a":
		return true
	case "new-session", "new":
	default:
		return false
	}
	detached, attachOrCreate := false, false
	for i := 1; i < len(command); i++ {
		token := command[i]
		if token == "--" || len(token) < 2 || token[0] != '-' {
			break
		}
		for j := 1; j < len(token); j++ {
			switch token[j] {
			case 'd':
				detached = true
			case 'A':
				attachOrCreate = true
			}
			if newSessionValueFlags[token[j]] {
				if j == len(token)-1 {
					i++
				}
				break
			}
		}
	}
	return !detached || attachOrCreate
}
