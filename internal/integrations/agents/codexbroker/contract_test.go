package codexbroker

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestBrokerRetainsNoProviderContent is the privacy negative audit for this
// package. Everything the broker keeps or renders must be a closed token, an
// epoch, or a counter: provider payloads pass through Event and Mutation and
// must not survive anywhere behind them.
func TestBrokerRetainsNoProviderContent(t *testing.T) {
	for _, test := range []struct {
		value  any
		fields []string
	}{
		{value: Fence{}, fields: []string{"Connection", "Binding"}},
		{value: ApprovalLease{}, fields: []string{"Fence", "ThreadID", "RawRequestID"}},
		{value: WriteRecord{}, fields: []string{"Fence", "Method", "Outcome", "Attempts"}},
		{value: Mutation{}, fields: []string{"Method", "Params", "Result"}},
		{value: Event{}, fields: []string{"Fence", "Origin", "Sequence", "Method", "Params", "Snapshot", "Lease"}},
		{value: Config{}, fields: []string{"Endpoint", "Opener", "Clock", "Jitter", "Backlog"}},
		{value: Diagnostics{}, fields: []string{
			"Endpoint", "ConnectionEpoch", "OpenAttempts", "Connects", "Disconnects", "Bindings",
			"ReleasedBindings", "RevokedBindings", "BufferedEvents", "DeliveredEvents",
			"ThreadlessEvents", "UnboundEvents", "StaleEvents", "Applied", "Refused",
			"Indeterminate", "Resends",
		}},
	} {
		valueType := reflect.TypeOf(test.value)
		var got []string
		for i := range valueType.NumField() {
			got = append(got, valueType.Field(i).Name)
		}
		if !reflect.DeepEqual(got, test.fields) {
			t.Fatalf("%s fields = %v, want the closed set %v", valueType.Name(), got, test.fields)
		}
	}

	// The two types the broker retains, as opposed to forwards, may not even
	// be shaped like provider content.
	for _, retained := range []any{Diagnostics{}, WriteRecord{}} {
		valueType := reflect.TypeOf(retained)
		for i := range valueType.NumField() {
			name := strings.ToLower(valueType.Field(i).Name)
			for _, forbidden := range []string{
				"prompt", "message", "text", "content", "command", "output",
				"token", "title", "transcript", "turn", "item", "diff", "param",
			} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("%s.%s looks like provider content", valueType.Name(), valueType.Field(i).Name)
				}
			}
		}
	}

	// Every closed code renders as a bare token, with no path, no whitespace,
	// and nothing a caller could mistake for a location.
	var codes []string
	for _, code := range []Refusal{
		RefusalNone, RefusalBrokerClosed, RefusalEndpointUnknown, RefusalThreadRequired,
		RefusalBindingExists, RefusalBindingClosed, RefusalControlNotOpen,
		RefusalStaleConnectionEpoch, RefusalStaleBindingEpoch, RefusalResyncRequired,
		RefusalSnapshotUnavailable, RefusalLeaseIdentityMismatch,
		RefusalResponseAlreadyAnswered, RefusalDisconnectBoundary,
	} {
		codes = append(codes, string(code))
	}
	for _, code := range []MutationOutcome{MutationApplied, MutationRefused, MutationIndeterminate} {
		codes = append(codes, string(code))
	}
	for _, code := range []EventOrigin{EventOriginSnapshot, EventOriginLive} {
		codes = append(codes, string(code))
	}
	codes = append(codes, string(DefaultEndpointKey))
	for _, code := range codes {
		if code == "" || strings.ContainsAny(code, "/\\ \t") {
			t.Fatalf("code %q is not a closed content-free token", code)
		}
	}

	// A typed refusal renders its code alone even when it wraps a cause that
	// carries a machine-local path.
	cause := errors.New("/home/user/.codex/app-server-control.sock")
	refusal := refuse(RefusalStaleConnectionEpoch, cause)
	if text := refusal.Error(); text != "codex broker refused: stale-connection-epoch" {
		t.Fatalf("refusal text = %q", text)
	}
	if !errors.Is(refusal, cause) || RefusalOf(refusal) != RefusalStaleConnectionEpoch {
		t.Fatalf("refusal classification failed for %v", refusal)
	}
	if got := RefusalOf(errors.New("unclassified")); got != RefusalNone {
		t.Fatalf("RefusalOf(unclassified) = %s", got)
	}

	// End to end: a prompt-bearing mutation and a payload-bearing event must
	// leave no byte of either in what the broker keeps.
	const secret = "a private prompt body"
	endpoint := newFakeEndpoint()
	broker, _, _ := newTestBroker(t, 8, endpoint)
	binding, fence := boundBinding(t, broker, "thread-one")
	if _, err := binding.Submit(t.Context(), fence, Mutation{
		Method: "turn/start", Params: map[string]string{"text": secret},
	}); err != nil {
		t.Fatal(err)
	}
	endpoint.push(codexappserver.Notification{
		Method: "item/updated",
		Params: json.RawMessage(`{"threadId":"thread-one","text":` + strconv.Quote(secret) + `}`),
	})
	waitUntil(t, "the payload-bearing event to be delivered", func() bool {
		return broker.Diagnostics().DeliveredEvents == 2
	})
	retained := fmt.Sprintf("%+v %+v", broker.Diagnostics(), broker.WriteLedger())
	if strings.Contains(retained, secret) {
		t.Fatalf("retained broker state kept provider content: %s", retained)
	}
}

// TestBrokerImportsNoRegistryTmuxOrCLIPackage is the package-boundary audit.
// The broker is a dark protocol component: its only in-repo dependency may be
// the app-server client it consumes. A reverse dependency on the Registry, on
// tmux, or on the CLI would make the endpoint layer answer to its own adapters.
func TestBrokerImportsNoRegistryTmuxOrCLIPackage(t *testing.T) {
	const allowed = "github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	inspected := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		file, err := parser.ParseFile(fileSet, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		inspected++
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			// A first segment without a dot is a standard library package.
			if !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
				continue
			}
			if path != allowed {
				t.Fatalf("%s imports %q; the broker may depend only on %q", entry.Name(), path, allowed)
			}
		}
	}
	if inspected < 8 {
		t.Fatalf("inspected %d files, expected the whole package", inspected)
	}
}
