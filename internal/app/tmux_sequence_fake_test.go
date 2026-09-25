package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
)

// answerTmuxReadSequence lets a scripted tmux fake answer a readTmuxSequence
// invocation the way tmux does. The sequence is split at its ";" arguments,
// each command runs through run -- the fake's own per-command logic, with the
// invocation's leading route flags (-S/-L/-f) in front -- and the outputs are
// joined in order, each ending in a newline. A failing command stops the rest
// and returns what printed before it, like tmux. The boundary reads are
// answered here, so a fake never sees them and its recorded calls stay one per
// read.
//
// handled is false for any argv that is not a read sequence; the fake then
// serves it as it always did.
func answerTmuxReadSequence(
	ctx context.Context,
	name string,
	args []string,
	run func(context.Context, string, ...string) ([]byte, error),
) (handled bool, out []byte, err error) {
	if name != "tmux" || !slices.Contains(args, ";") || !slices.Contains(args, routeReadBoundary) {
		return false, nil, nil
	}
	start := 0
	for start < len(args) && strings.HasPrefix(args[start], "-") {
		switch args[start] {
		case "-S", "-L", "-f":
			start += 2
		default:
			start++
		}
	}
	prefix := slices.Clone(args[:min(start, len(args))])
	var buf bytes.Buffer
	for _, part := range splitTmuxSequenceArgs(args[min(start, len(args)):]) {
		if slices.Equal(part, []string{"display-message", "-p", "-F", routeReadBoundary}) {
			buf.WriteString(routeReadBoundary + "\n")
			continue
		}
		got, runErr := run(ctx, name, append(slices.Clone(prefix), part...)...)
		buf.Write(got)
		if len(got) > 0 && !bytes.HasSuffix(got, []byte("\n")) {
			buf.WriteByte('\n')
		}
		if runErr != nil {
			return true, buf.Bytes(), runErr
		}
	}
	return true, buf.Bytes(), nil
}

func splitTmuxSequenceArgs(args []string) [][]string {
	parts := [][]string{}
	current := []string{}
	for _, arg := range args {
		if arg == ";" {
			parts = append(parts, current)
			current = []string{}
			continue
		}
		current = append(current, arg)
	}
	return append(parts, current)
}
