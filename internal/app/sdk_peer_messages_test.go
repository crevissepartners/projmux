package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Exercise the public API against the exact process peer wire, using only our
// protocol fixture. CLI and SDK each receive a fresh fixture and receipt store.
func TestSDKPeerMessageProcessSendStatusReplay(t *testing.T) {
	for _, lane := range []string{"sdk", "cli"} {
		t.Run(lane, func(t *testing.T) {
			f := newProcessCodexPeerFixture(t, processCodexSteerFixture("reply({})"))
			paths := config.Paths{ConfigDir: filepath.Join(f.root, "config", "projmux"), StateDir: filepath.Join(f.root, "state", "projmux")}
			disk := store.NewStore(paths.StateDir)
			m := &codexProcessMessages{store: disk, endpoints: map[string]*codexProcessEndpoint{f.endpoint.binding.Agent: f.endpoint}}
			f.endpoint.messages.Store(m)
			f.turn(t, "original", "hold")
			f.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
			sdk, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, HomeDir: f.root, SocketPath: filepath.Join(f.root, "unused.sock")})
			if err != nil {
				t.Fatal(err)
			}
			cmd := newAgentCommand()
			cmd.messagePaths = agentMessagePaths{registryPath: f.path, loadRegistry: f.store.LoadReadOnly}
			cmd.messageStore = disk
			cmd.messageRoute = liveAgentMessageRouteResolver{registryPath: f.path}
			ref := "uid:" + f.endpoint.binding.Agent
			request := projmux.SendMessageRequest{Source: ref, Target: ref, Text: "peer context only", MessageRef: "message-sdk-peer", TTL: time.Minute}
			var first core.MessageReceipt
			for attempt := 0; attempt < 2; attempt++ {
				var got core.MessageReceipt
				if lane == "sdk" {
					got, err = sdk.SendMessage(context.Background(), request)
				} else {
					_, _, err = runRoute(t, cmd, "message", "send", ref, "--source", ref, "--message-ref", request.MessageRef, "--ttl", "1m", "--", request.Text)
					if err == nil {
						got, err = sdk.MessageStatus(context.Background(), request.MessageRef)
					}
				}
				if err != nil || got.Delivery.State != core.MessageDelivered || got.Delivery.Reason != "host-history-injected" || got.Delivery.OutcomeUnknown {
					t.Fatalf("attempt=%d receipt=%+v err=%v", attempt, got, err)
				}
				if attempt == 0 {
					first = got
				} else if got != first {
					t.Fatalf("replay changed receipt: %+v %+v", first, got)
				}
			}
			starts, items := processCodexTurnWrites(t, f)
			if starts != 1 || len(items) != 1 {
				t.Fatalf("peer replay/input boundary: turn starts=%d peer items=%d", starts, len(items))
			}
			if !strings.Contains(items[0]["text"], "untrusted-coordination-only") {
				t.Fatalf("missing peer envelope: %v", items)
			}
			var stdout, stderr bytes.Buffer
			if err := cmd.runMessageStatus([]string{request.MessageRef, "-o", "json"}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(first)
			if strings.TrimSpace(stdout.String()) != string(want) {
				t.Fatalf("CLI/SDK receipt JSON differs: %s %s", stdout.String(), want)
			}
			stored, found, err := disk.Get(request.MessageRef)
			if err != nil || !found || stored.Envelope.Authority != message.PeerAuthority() || stored.Envelope.Operator() {
				t.Fatalf("authority changed: %+v %v", stored, err)
			}
		})
	}
}
