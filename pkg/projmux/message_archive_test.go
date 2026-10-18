package projmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func archiveFixture(t *testing.T) (string, *projmux.Client) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "agent-messages")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"messages.json", "history.jsonl", "history.jsonl.1"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "test", "sdk", "consumer", "testdata", "message-archive", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := projmux.NewClient(projmux.Options{StateDir: root, ConfigDir: filepath.Join(root, "absent-config"), SocketPath: filepath.Join(root, "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	return root, c
}

// Check every approved field against the authority on the very same bytes.
// This is a projection comparison, not another implementation of validation.
func assertArchiveParity(t *testing.T, want store.Archive, got core.MessageArchive) {
	t.Helper()
	if (want.Records == nil) != (got.Records == nil) || (want.History == nil) != (got.History == nil) || len(want.Records) != len(got.Records) || len(want.History) != len(got.History) || want.Skipped != got.Skipped {
		t.Fatalf("shape differs: internal %+v public %+v", want, got)
	}
	route := func(r message.Route) core.MessageRoute {
		return core.MessageRoute{AgentUID: r.AgentUID, PaneUID: r.PaneUID, ActivationGeneration: r.ActivationGeneration, Provider: r.Provider, Incarnation: r.Incarnation}
	}
	for i, r := range want.Records {
		e, d := r.Envelope, r.Delivery
		expected := core.MessageArchiveRecord{MessageRef: e.MessageRef, ConversationRef: e.ConversationRef, ReplyTo: e.ReplyTo, Origin: core.MessageOrigin{Kind: e.Origin.Kind, Client: e.Origin.Client}, Source: route(e.Source), Target: route(e.Target), Delivery: core.MessageDelivery{MessageRef: d.MessageRef, ConversationRef: d.ConversationRef, State: core.MessageState(d.State), Reason: d.Reason, OutcomeUnknown: d.OutcomeUnknown, AcceptedAt: d.AcceptedAt, TerminalAt: d.TerminalAt}, Payload: e.Payload, PayloadBytes: len(e.Payload), Operator: e.Operator()}
		if got.Records[i] != expected {
			t.Fatalf("record %d: %+v, want %+v", i, got.Records[i], expected)
		}
	}
	for i, h := range want.History {
		expected := core.MessageHistoryEntry{SchemaVersion: h.SchemaVersion, MessageRef: h.MessageRef, ConversationRef: h.ConversationRef, ReplyTo: h.ReplyTo, State: core.MessageState(h.State), AcceptedAt: h.AcceptedAt, PayloadBytes: h.PayloadBytes, PayloadSHA256: h.PayloadSHA256, Origin: core.MessageOrigin{Kind: h.Origin.Kind, Client: h.Origin.Client}, Source: route(h.Source), Target: route(h.Target), Operator: h.Origin.Operator()}
		if got.History[i] != expected {
			t.Fatalf("history %d: %+v, want %+v", i, got.History[i], expected)
		}
	}
}

func TestMessageArchiveMatchesAuthorityAndOwnsResults(t *testing.T) {
	root, c := archiveFixture(t)
	// Neither a held lock nor a malformed Registry participates in this read.
	for _, path := range []string{filepath.Join(root, "agent-messages", "messages.json.lock"), filepath.Join(root, "registry.json")} {
		if err := os.WriteFile(path, []byte("fixture locked or malformed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := centralTree(t, root)
	want, err := store.ReadArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Records) != 2 || len(want.History) != 4 || want.Skipped != 2 {
		t.Fatalf("fixture not exercised: %+v", want)
	}
	for range 2 {
		got, err := c.ReadMessageArchive(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		assertArchiveParity(t, want, got)
		if got.Records[0].PayloadBytes != 11 || got.Records[0].Operator || !got.Records[1].Operator || !got.History[1].Operator || got.History[2].PayloadSHA256 != "" {
			t.Fatalf("fixture fields: %+v", got)
		}
		got.Records[0].Payload = "caller change"
		got.History[0].Target.AgentUID = "caller change"
		got.Records = append(got.Records, core.MessageArchiveRecord{})
		got.History = append(got.History, core.MessageHistoryEntry{})
	}
	if !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("read changed files, bytes, modes or mtimes")
	}
}

func TestMessageArchiveMissingAndEmptyShapesCreateNothing(t *testing.T) {
	for _, content := range []string{"missing", `{"version":2,"records":null}`, `{"version":2,"records":[]}`} {
		t.Run(content, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "absent")
			if content != "missing" {
				if err := os.MkdirAll(filepath.Join(root, "agent-messages"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "agent-messages", "messages.json"), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := projmux.NewClient(projmux.Options{StateDir: root})
			if err != nil {
				t.Fatal(err)
			}
			before := centralTree(t, parent)
			want, err := store.ReadArchive(root)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.ReadMessageArchive(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertArchiveParity(t, want, got)
			if _, err := c.MessageArchiveFiles(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, centralTree(t, parent)) {
				t.Fatal("created or changed state")
			}
		})
	}
}

func TestMessageArchivePreservesAuthorityErrorsAndLegacy(t *testing.T) {
	// Mutate fixture structure only. Every policy decision remains the reader's.
	tests := []string{"malformed", "oversize", "unknown", "trailing", "version", "duplicate", "invalid-record", "record-cap", "legacy", "bad-legacy", "store-read", "history-read", "rotated-read", "empty"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			root, c := archiveFixture(t)
			path := filepath.Join(root, "agent-messages", "messages.json")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var disk struct {
				Version int            `json:"version"`
				Records []store.Record `json:"records"`
			}
			if err := json.Unmarshal(b, &disk); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "malformed":
				b = []byte("{")
			case "oversize":
				b = []byte(strings.Repeat(" ", 2<<20) + "{}")
			case "unknown":
				b = append(b[:len(b)-2], []byte(`,"unknown":true}`)...)
			case "trailing":
				b = append(b, []byte(" {}")...)
			case "version":
				disk.Version = 99
				b = nil
			case "duplicate":
				disk.Records = append(disk.Records, disk.Records[0])
				b = nil
			case "invalid-record":
				disk.Records[0].Delivery.AcceptedAt = disk.Records[0].Delivery.AcceptedAt.Add(1)
				b = nil
			case "record-cap":
				for len(disk.Records) <= 256 {
					disk.Records = append(disk.Records, disk.Records[0])
				}
				b = nil
			case "legacy", "bad-legacy":
				disk.Version = 1
				disk.Records = disk.Records[:1]
				disk.Records[0].Envelope.Version = 1
				disk.Records[0].Envelope.Source.Incarnation = ""
				disk.Records[0].Envelope.Target.Incarnation = ""
				if name == "bad-legacy" {
					disk.Records[0].Envelope.Version = 2
				}
				b = nil
			case "store-read", "history-read", "rotated-read":
				target := path
				if name == "history-read" {
					target = filepath.Join(root, "agent-messages", "history.jsonl")
				}
				if name == "rotated-read" {
					target = filepath.Join(root, "agent-messages", "history.jsonl.1")
				}
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "empty":
				b = []byte{}
			}
			if b == nil {
				b, err = json.Marshal(disk)
				if err != nil {
					t.Fatal(err)
				}
			}
			if name != "store-read" {
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := centralTree(t, root)
			want, wantErr := store.ReadArchive(root)
			got, gotErr := c.ReadMessageArchive(context.Background())
			if name == "legacy" {
				if wantErr != nil || gotErr != nil {
					t.Fatalf("legacy failed: %v / %v", wantErr, gotErr)
				}
				assertArchiveParity(t, want, got)
				if got.Records[0].Source.Incarnation != "legacy-unqualified-codex" {
					t.Fatal(got)
				}
			} else {
				if wantErr == nil || gotErr == nil || gotErr.Error() != wantErr.Error() || !reflect.DeepEqual(got, core.MessageArchive{}) {
					t.Fatalf("internal %v public %+v %v", wantErr, got, gotErr)
				}
				if errors.Is(wantErr, store.ErrMalformedStore) != errors.Is(gotErr, store.ErrMalformedStore) {
					t.Fatal("sentinel lost")
				}
				var wp, gp *os.PathError
				if errors.As(wantErr, &wp) != errors.As(gotErr, &gp) {
					t.Fatal("PathError lost")
				}
				if wp != nil && (wp.Op != gp.Op || wp.Path != gp.Path || !errors.Is(gotErr, wp.Err)) {
					t.Fatalf("cause changed: %v / %v", wp, gp)
				}
			}
			if !reflect.DeepEqual(before, centralTree(t, root)) {
				t.Fatal("read changed fixture")
			}
		})
	}
}

// A deterministic context observes the two documented cancellation boundaries
// without injecting a second reader or adding hooks to the store authority.
type archiveBoundaryContext struct {
	context.Context
	calls int
	err   error
}

func (c *archiveBoundaryContext) Err() error {
	c.calls++
	if c.calls > 1 {
		return c.err
	}
	return nil
}

func TestMessageArchiveCancellationBoundaries(t *testing.T) {
	root, c := archiveFixture(t)
	before := centralTree(t, root)
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		ctx := &archiveBoundaryContext{Context: context.Background(), err: cause}
		got, err := c.ReadMessageArchive(ctx)
		if !errors.Is(err, cause) || !reflect.DeepEqual(got, core.MessageArchive{}) || ctx.calls != 2 {
			t.Fatalf("archive boundary: %+v %v calls=%d", got, err, ctx.calls)
		}
		ctx = &archiveBoundaryContext{Context: context.Background(), err: cause}
		files, err := c.MessageArchiveFiles(ctx)
		if !errors.Is(err, cause) || files != nil || ctx.calls != 2 {
			t.Fatalf("files boundary: %v %v calls=%d", files, err, ctx.calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := c.ReadMessageArchive(ctx); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, core.MessageArchive{}) {
		t.Fatalf("pre-canceled: %+v %v", got, err)
	}
	if got, err := c.MessageArchiveFiles(ctx); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("pre-canceled paths: %v %v", got, err)
	}
	if !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("canceled read changed fixture")
	}
	path := filepath.Join(root, "agent-messages", "messages.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	boundary := &archiveBoundaryContext{Context: context.Background(), err: context.Canceled}
	if _, err := c.ReadMessageArchive(boundary); !errors.Is(err, store.ErrMalformedStore) || boundary.calls != 1 {
		t.Fatalf("reader error precedence: %v calls=%d", err, boundary.calls)
	}
}

func TestMessageArchiveFilesAndCapturedStateDir(t *testing.T) {
	root, c := archiveFixture(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	paths, err := c.MessageArchiveFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, store.ArchiveFiles(root)) {
		t.Fatal(paths)
	}
	paths[0] = "caller change"
	path := filepath.Join(root, "agent-messages", "messages.json")
	if err := os.WriteFile(path, []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	before := centralTree(t, root)
	paths, err = c.MessageArchiveFiles(context.Background())
	if err != nil || !reflect.DeepEqual(paths, store.ArchiveFiles(root)) {
		t.Fatalf("paths %v %v", paths, err)
	}
	if !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("path projection changed files")
	}
	if _, err := projmux.NewClient(projmux.Options{StateDir: "relative"}); err == nil || err.Error() != "StateDir must be absolute" {
		t.Fatal(err)
	}
	// Default resolution is captured as well as explicit overrides.
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	defaults, err := projmux.NewClient(projmux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	paths, err = defaults.MessageArchiveFiles(context.Background())
	if err != nil || !reflect.DeepEqual(paths, store.ArchiveFiles(filepath.Join(stateHome, "projmux"))) {
		t.Fatalf("captured defaults: %v %v", paths, err)
	}
}
