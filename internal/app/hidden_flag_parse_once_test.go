package app

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/cli"
)

// TestHiddenRouteFlagParseFailurePrintsReasonOnce runs hidden routes' parsers
// with an unknown flag and adds what the entrypoint prints for the returned
// error: the reason reaches stderr exactly once, the usage at most once, and
// the verdict keeps the hidden routes' historical exit 1 (not the usage exit
// 2), because tmux config, provider hooks and supervisors consume it.
func TestHiddenRouteFlagParseFailurePrintsReasonOnce(t *testing.T) {
	t.Parallel()
	const reason = "flag provided but not defined: -zz"
	tests := []struct {
		route string
		run   func(args []string, stderr io.Writer) error
	}{
		{route: "internal preview", run: func(args []string, stderr io.Writer) error {
			return (&previewCommand{}).Run(args, io.Discard, stderr)
		}},
		{route: "internal status usage", run: func(args []string, stderr io.Writer) error {
			return (&usagecmd.Command{}).RunStatus(args, io.Discard, stderr)
		}},
		{route: "internal status notify", run: func(args []string, stderr io.Writer) error {
			return (&statusCommand{}).runNotify(args, io.Discard, stderr)
		}},
		{route: "internal agent-hook ingest bell", run: (&aiCommand{}).runIngestBell},
		{route: "internal focus", run: func(args []string, stderr io.Writer) error {
			return newFocusTestCommand(&focusFakeRunner{}, nil, nil).Run(args, io.Discard, stderr)
		}},
		{route: "internal tmux autosave-session-state", run: (&tmuxCommand{}).runAutosaveSessionState},
	}
	for _, tt := range tests {
		t.Run(tt.route, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			err := tt.run([]string{"--zz"}, &stderr)
			if err == nil || err.Error() != reason {
				t.Fatalf("err = %v, want %q", err, reason)
			}
			want := cli.Failure{ExitCode: 1, Kind: cli.FailureRuntime, Reported: true}
			if got := cli.ClassifyFailure(err, IsUsageError(err)); got != want {
				t.Fatalf("verdict = %+v, want %+v", got, want)
			}
			total := stderr.String() + entrypointFailureLine(err)
			if got := strings.Count(total, reason); got != 1 {
				t.Fatalf("total stderr = %q, want %q exactly once, got %d", total, reason, got)
			}
			if got := strings.Count(total, "Usage of "); got > 1 {
				t.Fatalf("total stderr = %q, want the usage at most once, got %d", total, got)
			}
		})
	}
}
