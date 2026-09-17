package codexgeneration

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestPhase0ModelImportsNoMutationAdapter(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	const allowed = "github.com/crevissepartners/projmux/internal/core/metadata"
	fileSet := token.NewFileSet()
	inspected := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
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
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") && path != allowed {
				t.Fatalf("%s imports mutation-capable dependency %q", entry.Name(), path)
			}
		}
	}
	// The package keeps four pure owners: the state vocabulary, the authority
	// decision, the lifecycle projection, and the consumer projection. consumer.go
	// centralizes endpoint/fence actionability for the notification, sidebar,
	// statusbar, and reply consumers, while this import census still kills any
	// mutation-capable dependency added to any owner.
	if inspected != 4 {
		t.Fatalf("inspected %d pure model files, want 4", inspected)
	}
}

func TestCompositeAuthorityRejectsSameNumberCrossGenerationAndLegacyWithZeroWrites(t *testing.T) {
	durable := &metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "old"}
	stored := &metadata.CodexAuthorityRef{StateDomainID: "domain", EndpointGenerationID: "old", BrokerRuntimeID: "runtime-old", ConnectionEpoch: 1, BindingEpoch: 1}
	tests := []struct {
		name       string
		ref        *metadata.CodexAuthorityRef
		endpoint   *metadata.CodexEndpointRef
		want       AuthorityDecision
		wantWrites int
	}{
		{name: "exact", ref: stored, endpoint: durable, want: AuthorityAllowed, wantWrites: 1},
		{name: "same epochs other generation", ref: &metadata.CodexAuthorityRef{StateDomainID: "domain", EndpointGenerationID: "new", BrokerRuntimeID: "runtime-new", ConnectionEpoch: 1, BindingEpoch: 1}, endpoint: durable, want: AuthorityEndpointMismatch},
		{name: "same generation restarted broker", ref: &metadata.CodexAuthorityRef{StateDomainID: "domain", EndpointGenerationID: "old", BrokerRuntimeID: "runtime-new", ConnectionEpoch: 1, BindingEpoch: 1}, endpoint: durable, want: AuthorityBrokerRuntimeMismatch},
		{name: "legacy activation", ref: nil, endpoint: durable, want: AuthorityLegacyUnavailable},
		{name: "legacy durable ref", ref: stored, endpoint: nil, want: AuthorityLegacyUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writes := struct{ Provider, Registry, Tmux int }{}
			got := DecideAuthority(test.endpoint, stored, test.ref)
			if got == AuthorityAllowed {
				writes.Provider++
				writes.Registry++
				writes.Tmux++
			}
			if got != test.want || writes.Provider != test.wantWrites || writes.Registry != test.wantWrites || writes.Tmux != test.wantWrites {
				t.Fatalf("decision=%s writes=%+v, want %s/%d for each mutation surface", got, writes, test.want, test.wantWrites)
			}
		})
	}
}
