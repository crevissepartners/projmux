package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCanonicalCommandGraphProjectionMatchesBaseline pins the whole canonical
// projection -- spelling, summary, source edges, output modes, and fields -- to
// one digest, so any change to the public command contract has to be made on
// purpose.
//
// The baseline last moved when the question route summaries added Codex's
// blocking app-server questions to the existing Claude CLI surface.
// Before that, `agent models` gained a Codex app-server
// listing; its summary now names both providers and the default Claude read.
// Before that, `agent sessions backfill` joined the sessions group: one row
// with a json projection that appends past Claude sessions, attributed to
// exactly one Agent by their delivered coordination frames, as estimated history.
// Before that, it moved when `agent sessions list` joined the Agent domain:
// one row with a json projection that lists the Claude conversations an Agent
// has moved through, from the append-only session history and the Registry's
// current `status.sessionRef`.
// Before that, it moved when the `pin project` verbs
// (list|add|remove|toggle|clear|migrate) and the `runtime tag` verbs
// (list|clear|toggle) became catalog children: nine rows, each verb its own
// canonical spelling, with no new source edge.
// Before that, it moved when the output projections were held to what the
// parsers take: `get notifications`, which forwards to the notify queue and
// rejects `-o`, lost the shared catalog it never accepted, and `reconcile
// resources|registry` declare the `json` projection their parsers already
// accepted.
// Before that, it moved when `agent models` joined the Agent domain: one row
// with a json projection that lists the Claude model names projmux suggests
// for --model, a suggestion rather than an allowlist.
// Before that, it moved when `profile list|show|set|delete` joined the graph
// as a noun group of its own: four rows over the named Agent profile files in
// <ConfigDir>/profiles, which store and validate profiles and apply none.
// Before that, it moved when `config locale` and `config agent-questions`
// joined the config domain as the CLI doors onto the central `[ui] locale` and
// agent-question settings: two rows, and the `config` summary names both.
// Before that, it moved when `label project|window|pane|agent` joined the
// graph: four rows that write `metadata.labels` after creation, the field
// `--selector` reads and that `create --label` could previously only set once.
// Before that, it moved when `agent question enable|disable|list|answer`
// joined the Agent domain: four rows that opt a Claude Agent into answering its
// AskUserQuestion prompts from the command line, list them with a json
// projection, and answer one. Before that, it moved when `agent persona
// attach|detach` joined the Agent domain: two rows that give a running or stopped Claude Agent a persona, or
// take it away, and resume it on the same conversation, both with a json
// projection for their dry run. Before that, it moved when `config providers`
// joined the config domain as the CLI door onto the enabled-providers policy,
// adding its row. Before that,
// it moved when the `persona` noun group was added: `persona
// list|show|edit|set|delete` are new rows for the persona files a Claude Agent
// can be created with. Before that, it moved when `create window` gained its
// initial-provider flag: the route's canonical summary stopped promising an
// initial Pane and started naming the two surfaces it can open on, a shell
// Pane or one Agent. Before that, it moved when the private Codex app-server generation routes
// were removed: the five `agent app-server upgrade` rows and the four `agent
// app-server handover` rows lost their rows with the generation pool they
// operated. Before that, it moved when the Project snapshot routes were removed:
// `create snapshot`, `get snapshots`, `delete snapshot`, `restore snapshot`,
// and `prune snapshot` lost their rows, and the `prune` summary stopped naming
// snapshots. Before that, it moved when `prune agent` joined `prune project` as
// the bounded stale Offline/Failed Agent prune, which adds its row and names
// Agents in the `prune` summary. Before that, it moved when the send summary named its
// exit rule: nonzero after printing a failed, refused, expired, or stale
// receipt. The prior move
// described --source as an anchor rather than caller authentication. Before
// that, a move added exact-version Claude target qualification to the
// provider-neutral Agent message routes, and an earlier one added message and
// wait on top of steer's explicit provider-acceptance summary. The earliest
// recorded move added the Project lifecycle verbs: `start|open|stop project`
// and the canonical `unregister project` are new rows, `delete` became a source
// edge of the last of those instead of a canonical owner, `switch` became a
// source of `create project` and `open project` instead of `focus project`, and
// the mutation routes gained the `receipt` projection.
func TestCanonicalCommandGraphProjectionMatchesBaseline(t *testing.T) {
	t.Parallel()

	var baseline strings.Builder
	for _, route := range CanonicalRoutes() {
		fmt.Fprintf(&baseline, "%s\t%s\t%s\t%s\t%s\n",
			route.Spelling, route.Summary, strings.Join(route.Sources, ","),
			outputModesString(route.Outputs), fieldProjectionsString(route.Fields))
	}
	const want = "0d263049b7f53c11ba07ce54a58c10c1291932e975b6c68d4e6f16896ccc4adf"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(baseline.String()))); got != want {
		t.Fatalf("canonical command projection digest = %s, want %s\n%s", got, want, baseline.String())
	}
}

func TestCanonicalCommandGraphHasOneOwnerPerSpelling(t *testing.T) {
	t.Parallel()

	owners := map[string][]string{}
	edges := map[string][]string{}
	familyOrders := map[int]string{}
	nodeOrders := map[string]map[int]string{}
	walkRoutes(Routes(), func(path []string, route Route) {
		pathSpelling := strings.Join(path, " ")
		for _, spelling := range route.Canonical {
			edges[spelling] = append(edges[spelling], pathSpelling)
			if spelling == pathSpelling {
				owners[spelling] = append(owners[spelling], pathSpelling)
			}
		}
		if route.CanonicalSummary != "" && !slices.Contains(route.Canonical, pathSpelling) {
			t.Errorf("non-owner route %q declares canonical summary override %q", pathSpelling, route.CanonicalSummary)
		}
		if route.AcceptedOutputs != nil && !slices.Contains(route.Canonical, pathSpelling) {
			t.Errorf("non-owner route %q declares parser output override %v", pathSpelling, route.AcceptedOutputs)
		}
		if len(path) == 1 && route.CanonicalOrder != 0 {
			if prior := familyOrders[route.CanonicalOrder]; prior != "" {
				t.Errorf("canonical family order %d is shared by %q and %q", route.CanonicalOrder, prior, pathSpelling)
			}
			familyOrders[route.CanonicalOrder] = pathSpelling
		}
		if route.CanonicalNodeOrder != 0 {
			family := path[0]
			if nodeOrders[family] == nil {
				nodeOrders[family] = map[int]string{}
			}
			if prior := nodeOrders[family][route.CanonicalNodeOrder]; prior != "" {
				t.Errorf("canonical node order %d in family %q is shared by %q and %q", route.CanonicalNodeOrder, family, prior, pathSpelling)
			}
			nodeOrders[family][route.CanonicalNodeOrder] = pathSpelling
		}
	})

	projected := CanonicalRoutes()
	if len(projected) == 0 {
		t.Fatal("canonical graph projection is empty")
	}
	for _, route := range projected {
		if got := owners[route.Spelling]; len(got) != 1 {
			t.Errorf("canonical spelling %q owners = %v, want exactly one", route.Spelling, got)
		}
		if len(edges[route.Spelling]) == 0 {
			t.Errorf("canonical spelling %q has no executable source edge", route.Spelling)
		}
		if order := canonicalFamilyOrder(strings.Fields(route.Spelling)[0]); order == 0 {
			t.Errorf("canonical spelling %q belongs to an unordered family", route.Spelling)
		}
		delete(owners, route.Spelling)
		delete(edges, route.Spelling)
	}
	for spelling, paths := range owners {
		t.Errorf("unmapped canonical owner %q at %v", spelling, paths)
	}
	for spelling, paths := range edges {
		t.Errorf("orphan canonical edge %q from %v", spelling, paths)
	}
}

func TestCanonicalProjectionHasNoSecondCommandManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRoot(t), "internal", "cli", "canonical.go")
	data, err := os.ReadFile(path) // #nosec G304 -- fixed repository source path
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"[]CanonicalRoute{", "var canonicalRoutes", `Spelling: "`} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("canonical.go contains %q; command facts must live only in the executable graph", forbidden)
		}
	}
}

func outputModesString(modes []OutputMode) string {
	out := make([]string, len(modes))
	for i := range modes {
		out[i] = string(modes[i])
	}
	return strings.Join(out, ",")
}

func fieldProjectionsString(fields []FieldProjection) string {
	out := make([]string, len(fields))
	for i := range fields {
		out[i] = string(fields[i])
	}
	return strings.Join(out, ",")
}
