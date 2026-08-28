package codexappserver

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestBrokerFacingContractRetainsNoProviderContent is the privacy negative
// audit for everything this broker-facing seam adds. The pre-turn snapshot,
// the endpoint authority, and the typed attach refusal are closed field sets
// that carry identity, location, and status only; no prompt, response,
// command, title, or path byte may enter them or their rendered text.
func TestBrokerFacingContractRetainsNoProviderContent(t *testing.T) {
	for _, test := range []struct {
		value  any
		fields []string
	}{
		{value: ThreadSnapshot{}, fields: []string{"ThreadID", "CWD", "RuntimeStatus", "ActiveFlags", "CreatedAt", "UpdatedAt"}},
		{value: EndpointAuthority{}, fields: []string{"Attach", "Refusal", "Lifecycle"}},
		{value: AttachOptions{}, fields: []string{"Timeout", "ExperimentalAPI"}},
		{value: AttachError{}, fields: []string{"Refusal", "Authority", "err"}},
	} {
		valueType := reflect.TypeOf(test.value)
		var got []string
		for i := range valueType.NumField() {
			got = append(got, valueType.Field(i).Name)
		}
		if !reflect.DeepEqual(got, test.fields) {
			t.Fatalf("%s fields = %v, want the closed set %v", valueType.Name(), got, test.fields)
		}
		for _, name := range got {
			for _, forbidden := range []string{"prompt", "message", "text", "content", "command", "output", "token", "name", "title", "transcript", "turn", "item", "diff"} {
				if strings.Contains(strings.ToLower(name), forbidden) {
					t.Fatalf("%s.%s looks like provider content", valueType.Name(), name)
				}
			}
		}
	}

	// The provider's conversation title arrives on the snapshot read and is
	// dropped rather than projected.
	titled := CatalogThread{ID: "thread-1", CWD: "/work/project", Name: "a private conversation title", Branch: "topic/private", RuntimeStatus: "idle"}
	snapshot := newThreadSnapshot(titled)
	for _, dropped := range []string{titled.Name, titled.Branch} {
		for _, rendered := range []string{snapshot.ThreadID, snapshot.CWD, snapshot.RuntimeStatus} {
			if strings.Contains(rendered, dropped) {
				t.Fatalf("snapshot retained %q", dropped)
			}
		}
	}

	// Typed refusals render closed codes only.
	attachErr := &AttachError{Refusal: AttachRefusalVersionSkew, Authority: EndpointAuthority{}, err: errors.New("/home/user/.codex/app-server-control.sock")}
	if text := attachErr.Error(); text != "codex app-server attach refused: version-skew" {
		t.Fatalf("attach refusal text = %q", text)
	}
	for _, code := range []AttachRefusal{
		AttachRefusalNone, AttachRefusalEndpointNotReady, AttachRefusalProtocolMismatch,
		AttachRefusalVersionSkew, AttachRefusalRuntimeVersionUnknown, AttachRefusalOwnershipUnknown,
		AttachRefusalConnectFailed,
	} {
		if strings.ContainsAny(string(code), "/\\ ") {
			t.Fatalf("attach refusal code %q is not a closed content-free code", code)
		}
	}
	for _, err := range []error{ErrResponseAlreadySent, ErrExperimentalRequired} {
		if strings.ContainsAny(err.Error(), "/\\") {
			t.Fatalf("sentinel %q is not content-free", err)
		}
	}
}
