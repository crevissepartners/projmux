package codexgeneration

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
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
	if inspected != 4 {
		t.Fatalf("inspected %d pure model files, want 4", inspected)
	}
}

func generation(id string, state GenerationState) Generation {
	return Generation{
		Endpoint: metadata.CodexEndpointRef{StateDomainID: "state-domain", EndpointGenerationID: id},
		State:    state, Owner: OwnerProjmuxPrivate, BundleID: "sha256:bundle-" + id,
	}
}

func qualified() QualificationResult {
	return EvaluateQualification(VersionPair{Old: "0.152.0", New: "0.152.1"}, QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true,
		DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true,
		OldStoppedBeforeResume: true, PersistedResumeSnapshot: true,
		SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
	})
}

func TestBoundedPoolRejectsEveryInvalidV1Topology(t *testing.T) {
	tests := []struct {
		name string
		pool Pool
		want TopologyRefusal
	}{
		{name: "two current", pool: Pool{StateDomainID: "state-domain", Generations: []Generation{generation("g1", StateCurrent), generation("g2", StateCurrent)}}, want: RefusalMultipleCurrent},
		{name: "two draining", pool: Pool{StateDomainID: "state-domain", Generations: []Generation{generation("g1", StateDraining), generation("g2", StateHandoverPending)}}, want: RefusalMultipleDraining},
		{name: "no current with live obligation", pool: Pool{StateDomainID: "state-domain", Generations: []Generation{generation("g1", StateDraining)}, Obligations: []AgentObligation{{AgentUID: "agent-1", EndpointGenerationID: "g1", State: ObligationCompletedPersisted}}}, want: RefusalCurrentMissingWithLiveWork},
		{name: "three live slots", pool: Pool{StateDomainID: "state-domain", Generations: []Generation{generation("g1", StateCurrent), generation("g2", StatePreparing), generation("g3", StateRecovering)}}, want: RefusalPoolCapacityExceeded},
		{name: "duplicate generation", pool: Pool{StateDomainID: "state-domain", Generations: []Generation{generation("g1", StateCurrent), generation("g1", StateDraining)}}, want: RefusalGenerationDuplicate},
		{name: "different domain", pool: Pool{StateDomainID: "state-domain", Generations: []Generation{{Endpoint: metadata.CodexEndpointRef{StateDomainID: "other", EndpointGenerationID: "g1"}, State: StateCurrent, Owner: OwnerProjmuxPrivate, BundleID: "bundle"}}}, want: RefusalGenerationDomainMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RefusalOf(test.pool.Validate()); got != test.want {
				t.Fatalf("refusal=%s, want %s", got, test.want)
			}
		})
	}
}

func TestBoundedPoolStateSpaceProperty(t *testing.T) {
	states := []GenerationState{StatePreparing, StateCurrent, StateDraining, StateHandoverPending, StateRetired, StateRecovering, StateBlocked}
	for _, a := range states {
		for _, b := range states {
			for _, c := range states {
				pool := Pool{StateDomainID: "state-domain", Generations: []Generation{
					generation("g1", a), generation("g2", b), generation("g3", c),
				}}
				err := pool.Validate()
				live, current, draining := 0, 0, 0
				for _, state := range []GenerationState{a, b, c} {
					if state != StateRetired {
						live++
					}
					if state == StateCurrent {
						current++
					}
					if state == StateDraining || state == StateHandoverPending {
						draining++
					}
				}
				valid := live <= 2 && current <= 1 && draining <= 1
				if (err == nil) != valid {
					t.Fatalf("states=(%s,%s,%s) err=%v valid=%t", a, b, c, err, valid)
				}
			}
		}
	}
}

func FuzzBoundedPoolNeverAcceptsMoreThanTwoLiveSlots(f *testing.F) {
	f.Add(uint8(0), uint8(1), uint8(4))
	f.Add(uint8(1), uint8(1), uint8(1))
	states := []GenerationState{StatePreparing, StateCurrent, StateDraining, StateHandoverPending, StateRetired, StateRecovering, StateBlocked}
	f.Fuzz(func(t *testing.T, x, y, z uint8) {
		selected := []GenerationState{states[int(x)%len(states)], states[int(y)%len(states)], states[int(z)%len(states)]}
		pool := Pool{StateDomainID: "state-domain"}
		for i, state := range selected {
			pool.Generations = append(pool.Generations, generation(string(rune('a'+i)), state))
		}
		if pool.Validate() == nil {
			live := 0
			for _, state := range selected {
				if state != StateRetired {
					live++
				}
			}
			if live > 2 {
				t.Fatalf("accepted %d live slots", live)
			}
		}
	})
}

func TestIdentityAndVersionTokensRejectNonCanonicalWhitespace(t *testing.T) {
	for _, token := range []string{" generation", "generation ", "generation\n"} {
		if validIdentityToken(token) {
			t.Fatalf("identity token %q validated", token)
		}
	}
	for _, version := range []string{" 0.152.0", "0.152.0 ", "0.152.0\n"} {
		if validVersionToken(version) {
			t.Fatalf("version token %q validated", version)
		}
		result := qualified()
		result.Versions.Old = version
		if _, err := result.JSON(); err == nil {
			t.Fatalf("qualification receipt normalized version %q", version)
		}
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
			got := ApplyAuthorized(test.endpoint, stored, test.ref, func() {
				writes.Provider++
				writes.Registry++
				writes.Tmux++
			})
			if got != test.want || writes.Provider != test.wantWrites || writes.Registry != test.wantWrites || writes.Tmux != test.wantWrites {
				t.Fatalf("decision=%s writes=%+v, want %s/%d for each mutation surface", got, writes, test.want, test.wantWrites)
			}
		})
	}
}

func TestOldStopBarrierKeepsSuccessorResumeAtZeroUntilSafe(t *testing.T) {
	old := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "old"}
	newEndpoint := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "new"}
	writes := 0
	if got := ApplySuccessorResume(old, newEndpoint, false, true, func() { writes++ }); got != ResumeOwnerStillLive || writes != 0 {
		t.Fatalf("live-old decision=%s writes=%d", got, writes)
	}
	if got := ApplySuccessorResume(old, newEndpoint, true, true, func() { writes++ }); got != ResumeAllowed || writes != 1 {
		t.Fatalf("stopped-old decision=%s writes=%d", got, writes)
	}
}

func TestReadOnlyUpgradePlanIsRepeatableContentFreeAndNamesExactBlockers(t *testing.T) {
	pool := Pool{StateDomainID: "state-domain", Generations: []Generation{
		generation("old", StateDraining),
		{Endpoint: metadata.CodexEndpointRef{StateDomainID: "state-domain", EndpointGenerationID: "current"}, State: StateCurrent, Owner: OwnerUnmanaged, BundleID: "sha256:current"},
	}, Obligations: []AgentObligation{{AgentUID: "agent-exact", EndpointGenerationID: "old", State: ObligationNoTurn}}}
	q := qualified()
	first := PlanUpgrade(pool, "target", &q)
	second := PlanUpgrade(pool, "target", &q)
	if got := pool.Generations[0].State; got != StateDraining {
		t.Fatalf("plan mutated its input generation to %s", got)
	}
	if !reflect.DeepEqual(first, second) || first.Decision != PlanBlocked || first.Mutations != (MutationCount{}) {
		t.Fatalf("repeated plan drift/mutation: first=%+v second=%+v", first, second)
	}
	encoded, err := first.JSON()
	if err != nil {
		t.Fatal(err)
	}
	text := first.Text()
	wantJSON := `{
  "modelVersion": 1,
  "stateDomainID": "state-domain",
  "currentGeneration": "current",
  "targetGeneration": "target",
  "decision": "blocked",
  "blockers": [
    {
      "code": "agent-obligation",
      "agentUID": "agent-exact",
      "endpointGenerationID": "old",
      "reason": "no-turn"
    },
    {
      "code": "pool-full",
      "endpointGenerationID": "old",
      "reason": "bounded current-plus-draining pool has no free slot"
    },
    {
      "code": "unmanaged-lifecycle",
      "endpointGenerationID": "current",
      "reason": "exact-current unmanaged endpoint is attach-only; operator-owned stop is required"
    }
  ],
  "mutations": {
    "registry": 0,
    "provider": 0,
    "tmux": 0,
    "process": 0,
    "currentPointer": 0
  }
}`
	wantText := `codex app-server upgrade plan: blocked
state-domain: state-domain
current-generation: current
target-generation: target
blocker: code=agent-obligation agent=agent-exact generation=old reason=no-turn
blocker: code=pool-full agent=none generation=old reason=bounded current-plus-draining pool has no free slot
blocker: code=unmanaged-lifecycle agent=none generation=current reason=exact-current unmanaged endpoint is attach-only; operator-owned stop is required
mutations: registry=0 provider=0 tmux=0 process=0 current-pointer=0`
	if string(encoded) != wantJSON {
		t.Fatalf("plan JSON golden drift:\n--- got ---\n%s\n--- want ---\n%s", encoded, wantJSON)
	}
	if text != wantText {
		t.Fatalf("plan text golden drift:\n--- got ---\n%s\n--- want ---\n%s", text, wantText)
	}
	for _, exact := range []string{"agent-exact", "old", "current"} {
		if strings.Count(string(encoded), exact) == 0 || strings.Count(text, exact) == 0 {
			t.Fatalf("plan output misses exact blocker identity %q", exact)
		}
	}
	for _, secret := range []string{"prompt body", "secret-token", "/tmp/private.sock"} {
		if strings.Contains(string(encoded)+text, secret) {
			t.Fatalf("plan leaked %q", secret)
		}
	}
}

func TestReadOnlyUpgradePlanNeverRendersInvalidIdentitiesOrQualificationFields(t *testing.T) {
	q := qualified()
	hostile := "provider-content/private/path\nsecret"
	pool := Pool{StateDomainID: hostile, Generations: []Generation{{
		Endpoint: metadata.CodexEndpointRef{StateDomainID: hostile, EndpointGenerationID: hostile},
		State:    StateCurrent, Owner: OwnerUnmanaged, BundleID: hostile,
	}}, Obligations: []AgentObligation{{AgentUID: hostile, EndpointGenerationID: hostile, State: ObligationNoTurn}}}
	plan := PlanUpgrade(pool, hostile, &q)
	raw, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if plan.StateDomainID != "" || plan.CurrentGeneration != "" || plan.TargetGeneration != "" ||
		strings.Contains(string(raw), hostile) || strings.Contains(plan.Text(), hostile) {
		t.Fatalf("invalid identity reached plan output: json=%s text=%s", raw, plan.Text())
	}

	validPool := Pool{StateDomainID: "state-domain", Generations: []Generation{generation("current", StateCurrent)}}
	q.Reason = QualificationReason(hostile)
	plan = PlanUpgrade(validPool, "target", &q)
	raw, err = plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), hostile) || strings.Contains(plan.Text(), hostile) ||
		len(plan.Blockers) != 1 || plan.Blockers[0].Reason != "qualification-invalid" {
		t.Fatalf("invalid qualification reached plan output: json=%s text=%s", raw, plan.Text())
	}
}

func TestQualificationDecisionTableClosesPhase2OnEitherSharedStateFailure(t *testing.T) {
	base := qualified()
	if gate := GateQualification(base); !gate.Phase2Ready || gate.Lane != FollowupGenerationPool {
		t.Fatalf("qualified gate=%+v", gate)
	}
	tests := []struct {
		name   string
		mutate func(*QualificationEvidence)
		reason QualificationReason
	}{
		{name: "distinct threads no", mutate: func(e *QualificationEvidence) { e.DistinctThreadCreateTurn = false }, reason: ReasonDistinctThreadFailed},
		{name: "same thread no", mutate: func(e *QualificationEvidence) { e.PersistedResumeSnapshot = false }, reason: ReasonSameThreadOwnershipFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := base.Evidence
			test.mutate(&evidence)
			result := EvaluateQualification(base.Versions, evidence)
			gate := GateQualification(result)
			if result.Verdict != VerdictNo || result.Reason != test.reason || gate.Phase2Ready || gate.Lane != FollowupSingleEndpointJournaledHandover || gate.Blocker == "" {
				t.Fatalf("result=%+v gate=%+v", result, gate)
			}
			raw, err := result.JSON()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeQualificationResult(raw)
			if err != nil || !reflect.DeepEqual(decoded, result) {
				t.Fatalf("receipt round trip=%+v err=%v", decoded, err)
			}
		})
	}
}

func TestQualificationReceiptRetainsOnlyContentFreeAllowlistedFields(t *testing.T) {
	resultType := reflect.TypeFor[QualificationResult]()
	var fields []string
	for i := range resultType.NumField() {
		fields = append(fields, resultType.Field(i).Name)
	}
	if want := RetainedQualificationFields(); !reflect.DeepEqual(fields, want) {
		t.Fatalf("QualificationResult fields=%v want=%v", fields, want)
	}
	evidenceType := reflect.TypeFor[QualificationEvidence]()
	for i := range evidenceType.NumField() {
		name := strings.ToLower(evidenceType.Field(i).Name)
		for _, forbidden := range []string{"prompt", "message", "content", "secret", "token", "path", "socket", "transcript", "output"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("QualificationEvidence.%s can retain content", evidenceType.Field(i).Name)
			}
		}
	}
	raw, err := json.Marshal(qualified())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatalf("receipt unexpectedly retained a secret-like value: %s", raw)
	}
}
