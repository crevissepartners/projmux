package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// stripRecordedTmuxRoute drops the exact -L/-S route prefix a recorded tmux
// call carries. An assertion names the command that was issued; which server it
// was pinned to is the routing contract's own subject, held by the tests below.
func stripRecordedTmuxRoute(args []string) []string {
	if len(args) >= 2 && (args[0] == "-L" || args[0] == "-S") {
		return args[2:]
	}
	return args
}

// routedAppSocketArgs spells a reflection write the way the delivery issues it
// when the hook inherited no tmux environment: pinned to the app-owned route
// the containment proof accepted, never to the default socket.
func routedAppSocketArgs(args ...string) []string {
	return append([]string{"-L", defaultAppSocket}, args...)
}

// codexHookDeliveryRouteRow renders the containment probe answer of a runtime
// that is app-owned and really does hold paneID as a projmux Pane.
func codexHookDeliveryRouteRow(paneID, paneUID string) []byte {
	return []byte(strings.Join([]string{"1", paneID, paneUID}, tmuxRowSep) + "\n")
}

func codexHookDeliveryTestCommand(t *testing.T, paneID string) *aiCommand {
	t.Helper()
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = codexHookIngestReadCommand(paneID)
	return cmd
}

// A hook launched by the shared app-server inherits no tmux environment at all.
// The reflection must still reach its Pane, and it must do so on a route it
// proved rather than on the default socket an unprefixed call would probe.
func TestCodexHookDeliveryWritesItsPaneWithoutAnyTmuxEnvironment(t *testing.T) {
	const paneID = "%7"
	cmd := codexHookDeliveryTestCommand(t, paneID)
	if got := cmd.env("TMUX"); got != "" {
		t.Fatalf("fixture TMUX = %q, want the app-server's empty environment", got)
	}
	route, err := cmd.codexHookDeliveryRoute(paneID)
	if err != nil {
		t.Fatalf("resolve delivery route without TMUX: %v", err)
	}
	if route.transport.Kind != tmuxSocketName || route.transport.Value != defaultAppSocket {
		t.Fatalf("delivery route = %#v, want the app-owned socket-name route", route.transport)
	}
	if got := route.args("set-option", "-p", "-t", paneID, aiPaneStateOption, "waiting"); !reflect.DeepEqual(
		got, []string{"-L", defaultAppSocket, "set-option", "-p", "-t", paneID, aiPaneStateOption, "waiting"}) {
		t.Fatalf("routed argv = %#v", got)
	}
}

// An inherited receipt already names the client's own server. It is taken
// as-is, and it is taken as an explicit -S route rather than left unprefixed.
func TestCodexHookDeliveryPrefersTheInheritedReceiptOverTheAppRoute(t *testing.T) {
	const paneID = "%7"
	cmd := codexHookDeliveryTestCommand(t, paneID)
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("an inherited receipt must not need a containment probe")
	}
	base := cmd.lookupEnv
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux-1000/projmux,4242,1"
		}
		return base(name)
	}
	route, err := cmd.codexHookDeliveryRoute(paneID)
	if err != nil {
		t.Fatalf("resolve delivery route from inherited receipt: %v", err)
	}
	if route.transport.Kind != tmuxSocketPath || route.transport.Value != "/tmp/tmux-1000/projmux" {
		t.Fatalf("delivery route = %#v, want the inherited socket path", route.transport)
	}
	if route.transport.Source != tmuxInheritedSource {
		t.Fatalf("delivery route source = %q, want the inherited env provenance", route.transport.Source)
	}
}

// The app-owned route is never trusted on its name. Each way the containment
// proof can fail names its own cause, and none of them writes anything.
func TestCodexHookDeliveryRefusesARouteItCannotProve(t *testing.T) {
	const paneID = "%7"
	for _, test := range []struct {
		name       string
		probe      func() ([]byte, error)
		wantReason string
		wantDetail string
	}{
		{
			name:       "runtime is unreachable",
			probe:      func() ([]byte, error) { return []byte("no server running on /tmp/tmux-1000/projmux"), os.ErrNotExist },
			wantReason: codexHookRouteUnavailableReason,
			wantDetail: codexHookCauseNoServer,
		},
		{
			name:       "runtime answers nothing",
			probe:      func() ([]byte, error) { return []byte("\n"), nil },
			wantReason: codexHookRouteUnavailableReason,
			wantDetail: "containment probe returned no single row",
		},
		{
			name: "runtime is not app-owned",
			probe: func() ([]byte, error) {
				return []byte(strings.Join([]string{"", paneID, "pan-alpha"}, tmuxRowSep) + "\n"), nil
			},
			wantReason: codexHookRouteUnavailableReason,
			wantDetail: "server is not app-owned",
		},
		{
			name: "runtime holds a different pane",
			probe: func() ([]byte, error) {
				return codexHookDeliveryRouteRow("%9", "pan-alpha"), nil
			},
			wantReason: codexHookRouteForeignPaneReason,
			wantDetail: paneID,
		},
		{
			name: "pane is not a projmux Pane",
			probe: func() ([]byte, error) {
				return codexHookDeliveryRouteRow(paneID, ""), nil
			},
			wantReason: codexHookRouteForeignPaneReason,
			wantDetail: paneID,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := codexHookDeliveryTestCommand(t, paneID)
			cmd.readCommand = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return test.probe()
			}
			_, err := cmd.codexHookDeliveryRoute(paneID)
			if err == nil {
				t.Fatal("unproven route resolved without a refusal")
			}
			assertCodexHookDeliveryReason(t, err, test.wantReason, test.wantDetail)
			if writes := cmdRecorder(cmd).commands; len(writes) != 0 {
				t.Fatalf("refused route still ran %#v", writes)
			}
		})
	}
}

// The reflection's own failure surface: an attributed hook that cannot write
// records why, and the reason is never the process exit status the discarding
// runner used to hand back.
func TestCodexHookDeliveryFailureReasonsAreConcreteAndClosed(t *testing.T) {
	const paneID = "%7"
	cmd := codexHookDeliveryTestCommand(t, paneID)
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	err := cmd.applyCodexHookSemanticDelivery(paneID, coremetadata.InteractionResponseComplete, config.AISemanticStateOnly, attentionNotifyInput{})
	if err == nil {
		t.Fatal("delivery on an unroutable hook returned no error")
	}
	assertCodexHookDeliveryReason(t, err, codexHookRouteUnavailableReason, "")
	for _, token := range []string{
		codexHookRouteUnavailableReason,
		codexHookRouteForeignPaneReason,
		codexHookWriteRejectedReason,
		codexHookInheritedRoute,
		codexHookAppOwnedRoute,
		codexHookCauseNoServer,
		codexHookCauseNoPane,
		codexHookCauseDenied,
		codexHookCauseUnclassified,
	} {
		if strings.TrimSpace(token) == "" {
			t.Fatal("a delivery vocabulary token is empty")
		}
		assertNoLeakedTransportDetail(t, "vocabulary token", token)
	}
}

// A write that fails after the route was proven is explained by re-reading the
// route, not by the write's exit status. The classifier is a pure read, so no
// pane option is written twice on the way to the explanation.
func TestCodexHookDeliveryExplainsARejectedWriteByRereadingItsRoute(t *testing.T) {
	const paneID = "%7"
	for _, test := range []struct {
		name       string
		stillThere bool
		wantDetail string
	}{
		{name: "pane vanished", stillThere: false, wantDetail: "pane is gone from this runtime"},
		{name: "pane survived", stillThere: true, wantDetail: "runtime still holds this pane and rejected the option write"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := codexHookDeliveryTestCommand(t, paneID)
			probes := 0
			cmd.readCommand = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				probes++
				if probes > 1 && !test.stillThere {
					return codexHookDeliveryRouteRow("%9", "pan-alpha"), nil
				}
				return codexHookDeliveryRouteRow(paneID, "pan-alpha"), nil
			}
			writes := 0
			cmd.runCommand = func(_ context.Context, name string, args ...string) error {
				cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
				writes++
				return errors.New("exit status 1")
			}
			err := cmd.applyCodexHookSemanticDelivery(paneID, coremetadata.InteractionResponseComplete, config.AISemanticStateOnly, attentionNotifyInput{})
			if err == nil {
				t.Fatal("rejected write returned no error")
			}
			assertCodexHookDeliveryReason(t, err, codexHookWriteRejectedReason, test.wantDetail)
			if !strings.Contains(err.Error(), aiPaneStateOption) {
				t.Fatalf("rejected write reason %q does not name the option", err)
			}
			if writes != 1 {
				t.Fatalf("rejected write repeated the option write %d times", writes)
			}
		})
	}
}

func assertCodexHookDeliveryReason(t *testing.T, err error, wantReason, wantDetail string) {
	t.Helper()
	var delivery *codexHookDeliveryError
	if !errors.As(err, &delivery) {
		t.Fatalf("error %v is not a codex hook delivery refusal", err)
	}
	if delivery.Reason != wantReason {
		t.Fatalf("delivery reason = %q, want %q", delivery.Reason, wantReason)
	}
	if wantDetail != "" && !strings.Contains(delivery.Detail, wantDetail) {
		t.Fatalf("delivery detail = %q, want it to name %q", delivery.Detail, wantDetail)
	}
	assertNoLeakedTransportDetail(t, "delivery reason", err.Error())
}

// forbiddenDeliveryDetail is what a durable record must never carry: the socket
// the route resolved to, the shape that names it, tmux's own words, and the
// process exit status this whole layer exists to stop recording.
var forbiddenDeliveryDetail = []string{"/tmp/", "-S/", "-L/", "-S ", "-L ", "exit status", "no server running"}

func assertNoLeakedTransportDetail(t *testing.T, what, value string) {
	t.Helper()
	for _, forbidden := range forbiddenDeliveryDetail {
		if strings.Contains(value, forbidden) {
			t.Fatalf("%s carries %q, which the change boundary keeps out of durable records: %s", what, forbidden, value)
		}
	}
}

// The record is the surface, not the error value. This injects a transport
// failure whose tmux output names its socket, drives the real ingest route, and
// then reads back the whole log file: a reason that leaks is caught here even
// if every unit assertion above still passes.
func TestCodexHookDeliveryLeavesNoTransportDetailInTheIngestLog(t *testing.T) {
	const paneID = "%7"
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.stdin = bytes.NewBufferString(
		`{"hook_event_name":"Stop","thread_id":"codex-session","turn_id":"turn-leak","cwd":"/repo/projmux"}`)
	base := codexHookIngestReadCommand(paneID)
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && slices.Contains(args, codexHookDeliveryRouteFormat) {
			// Exactly what a real tmux prints when the route has no server, and
			// exactly the string that must not survive into the record.
			return []byte("no server running on /tmp/tmux-1000/default"), os.ErrNotExist
		}
		return base(ctx, name, args...)
	}
	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("an unroutable Stop was ingested without an error")
	}
	path, err := cmd.aiIngestLogPath()
	if err != nil {
		t.Fatalf("resolve ingest log path: %v", err)
	}
	recorded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ingest log: %v", err)
	}
	if !strings.Contains(string(recorded), codexHookRouteUnavailableReason) {
		t.Fatalf("ingest log did not record the refusal at all: %s", recorded)
	}
	assertNoLeakedTransportDetail(t, "ingest log", string(recorded))
}
