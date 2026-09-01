package codexinstalled

import (
	"reflect"
	"testing"
)

func TestQualificationArtifactSchemaIsStructurallyContentFree(t *testing.T) {
	assertQualificationFields(t, reflect.TypeFor[QualificationArtifact](), map[string]reflect.Type{
		"schema_version": reflect.TypeFor[int](),
		"results":        reflect.TypeFor[[]QualificationResult](),
	})
	assertQualificationFields(t, reflect.TypeFor[QualificationResult](), map[string]reflect.Type{
		"versions":  reflect.TypeFor[VersionTuple](),
		"topology":  reflect.TypeFor[QualificationTopology](),
		"primitive": reflect.TypeFor[QualificationPrimitive](),
		"stage":     reflect.TypeFor[QualificationStage](),
		"class":     reflect.TypeFor[ResultClass](),
		"reason":    reflect.TypeFor[QualificationReason](),
	})
	assertQualificationFields(t, reflect.TypeFor[VersionTuple](), map[string]reflect.Type{
		"cli":        reflect.TypeFor[string](),
		"managed":    reflect.TypeFor[string](),
		"app_server": reflect.TypeFor[string](),
	})

	spec, _ := QualificationSpecFor(PrimitiveThreadList)
	result := newQualificationResult(spec, installedQualificationVersions(), ResultPass, QualificationReason("arbitrary"))
	if err := result.Validate(); err == nil {
		t.Fatal("an open-ended reason passed the closed content-free vocabulary")
	}
	result = newQualificationResult(spec, installedQualificationVersions(), ResultPass, QualificationReasonCompatible)
	result.Stage = QualificationStage("unknown")
	if err := result.Validate(); err == nil {
		t.Fatal("an undeclared terminal stage passed the primitive contract")
	}
	result = newQualificationResult(spec, VersionTuple{
		CLI: "0.152.0", Managed: "0.152.0", AppServer: "/opaque/value",
	}, ResultPass, QualificationReasonCompatible)
	if err := result.Validate(); err == nil {
		t.Fatal("a non-semver value passed the content-free version slots")
	}
}

func TestQualificationSpecsReuseExactlyOneCanonicalOwnerPerPrimitive(t *testing.T) {
	want := map[QualificationPrimitive]QualificationSpec{
		PrimitiveDaemonLifecycle: {
			Primitive: PrimitiveDaemonLifecycle, Topology: QualificationTopologyDirectManaged, Stage: QualificationStageRetire,
			TestName: "TestInstalledHermeticTopologyQualification", SmokeEnv: DefaultSmokeRootEnv,
		},
		PrimitiveThreadList: {
			Primitive: PrimitiveThreadList, Topology: QualificationTopologyDirect, Stage: QualificationStageReady,
			TestName: "TestInstalledIsolatedConversationCatalogSmoke", SmokeEnv: "PROJMUX_CODEX_CATALOG_SMOKE_ROOT",
		},
		PrimitivePreTurnAttach: {
			Primitive: PrimitivePreTurnAttach, Topology: QualificationTopologyDirect, Stage: QualificationStageReady,
			TestName: "TestInstalledIsolatedPreTurnBootstrapSmoke", SmokeEnv: "PROJMUX_CODEX_BROKER_SMOKE_ROOT",
		},
	}
	seenTests := map[string]QualificationPrimitive{}
	for _, got := range QualificationSpecs() {
		if expected, ok := want[got.Primitive]; !ok || got != expected {
			t.Fatalf("qualification spec = %+v", got)
		}
		if previous := seenTests[got.TestName]; previous != "" {
			t.Fatalf("canonical test %q owns both %q and %q", got.TestName, previous, got.Primitive)
		}
		seenTests[got.TestName] = got.Primitive
		delete(want, got.Primitive)
	}
	if len(want) != 0 {
		t.Fatalf("scheduled matrix omitted canonical primitives: %v", want)
	}
}

func TestQualificationInstallFailureIsTypedBeforeAnyAmbientCanary(t *testing.T) {
	for _, spec := range QualificationSpecs() {
		result := InstallationFailureQualification(spec)
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		if result.Stage != spec.Stage || result.Class != ResultInfraError || result.Reason != QualificationReasonInstallationFailed {
			t.Fatalf("installation failure for %s = %+v", spec.Primitive, result)
		}
	}
}

func TestQualificationRequiresTheDeclaredVersionInEveryTupleSlot(t *testing.T) {
	spec, _ := QualificationSpecFor(PrimitiveDaemonLifecycle)
	exact := newQualificationResult(spec, installedQualificationVersions(), ResultPass, QualificationReasonCompatible)
	if got := EnforceQualificationVersion(exact, "0.152.0"); got != exact {
		t.Fatalf("exact hosted tuple changed = %+v", got)
	}

	skew := exact
	skew.Versions.Managed = "0.151.0"
	got := EnforceQualificationVersion(skew, "0.152.0")
	if got.Class != ResultFail || got.Reason != QualificationReasonVersionMismatch || got.Versions.Managed != "0.151.0" {
		t.Fatalf("version skew = %+v, want content-free mismatch preserving observations", got)
	}
	invalid := EnforceQualificationVersion(exact, "")
	if invalid.Class != ResultInfraError || invalid.Reason != QualificationReasonDeclaredVersionInvalid {
		t.Fatalf("blank declared version = %+v, want typed infrastructure failure", invalid)
	}
}

func TestQualificationReducerEmitsOneTerminalRecordPerMatrixLeg(t *testing.T) {
	versions := installedQualificationVersions()
	tests := []struct {
		primitive  QualificationPrimitive
		observed   []Result
		succeeded  bool
		wantClass  ResultClass
		wantReason QualificationReason
	}{
		{
			primitive: PrimitiveDaemonLifecycle,
			observed: []Result{
				NewResult(versions, TopologyManaged, StageProvision, ResultPass, "managed-payload-provisioned"),
				NewResult(versions, TopologyDirect, StageReady, ResultPass, "direct-endpoint-ready"),
				NewResult(versions, TopologyDirect, StageClose, ResultPass, "direct-endpoint-closed"),
				NewResult(versions, TopologyManaged, StageRetire, ResultPass, "managed-endpoint-started-reused-retired"),
			},
			succeeded: true, wantClass: ResultPass, wantReason: QualificationReasonCompatible,
		},
		{
			primitive: PrimitiveThreadList,
			observed: []Result{
				NewResult(versions, TopologyDirect, StageReady, ResultPass, "thread-list-compatible"),
			},
			succeeded: true, wantClass: ResultPass, wantReason: QualificationReasonCompatible,
		},
		{
			primitive: PrimitivePreTurnAttach,
			observed: []Result{
				NewResult(versions, TopologyDirect, StageReady, ResultPass, "pre-turn-second-attach-thread-read-compatible"),
			},
			succeeded: true, wantClass: ResultPass, wantReason: QualificationReasonCompatible,
		},
	}
	for _, test := range tests {
		t.Run(string(test.primitive), func(t *testing.T) {
			spec, _ := QualificationSpecFor(test.primitive)
			result := ReduceQualification(spec, test.observed, test.succeeded)
			artifact := QualificationArtifact{SchemaVersion: QualificationSchemaVersion, Results: []QualificationResult{result}}
			if err := artifact.Validate([]QualificationPrimitive{test.primitive}); err != nil {
				t.Fatal(err)
			}
			if len(artifact.Results) != 1 || result.Class != test.wantClass || result.Reason != test.wantReason {
				t.Fatalf("terminal artifact = %+v", artifact)
			}
		})
	}
}

func TestQualificationReducerNeverTurnsUnsupportedOrMissingEvidenceGreen(t *testing.T) {
	versions := installedQualificationVersions()
	lifecycle, _ := QualificationSpecFor(PrimitiveDaemonLifecycle)
	unsupported := ReduceQualification(lifecycle, []Result{
		NewResult(VersionTuple{CLI: versions.CLI}, TopologyManaged, StageProvision, ResultUnsupported, "managed-payload-manifest-missing"),
		NewResult(versions, TopologyDirect, StageReady, ResultPass, "direct-endpoint-ready"),
		NewResult(versions, TopologyDirect, StageClose, ResultPass, "direct-endpoint-closed"),
	}, true)
	if unsupported.Class != ResultUnsupported || unsupported.Reason != QualificationReasonUnsupported {
		t.Fatalf("payload absence = %+v, want typed unsupported", unsupported)
	}
	incompleteUnsupported := ReduceQualification(lifecycle, []Result{
		NewResult(VersionTuple{CLI: versions.CLI}, TopologyManaged, StageProvision, ResultUnsupported, "managed-payload-manifest-missing"),
	}, true)
	if incompleteUnsupported.Class != ResultInfraError || incompleteUnsupported.Reason != QualificationReasonTerminalMissing {
		t.Fatalf("unsupported result without direct evidence = %+v, want typed missing terminal", incompleteUnsupported)
	}

	catalog, _ := QualificationSpecFor(PrimitiveThreadList)
	missing := ReduceQualification(catalog, nil, false)
	if missing.Class != ResultInfraError || missing.Reason != QualificationReasonTerminalMissing {
		t.Fatalf("pre-terminal exit = %+v, want typed infrastructure error", missing)
	}
	partial := ReduceQualification(catalog, []Result{
		NewResult(versions, TopologyDirect, StageReady, ResultPass, "direct-endpoint-ready"),
	}, true)
	if partial.Class != ResultInfraError || partial.Reason != QualificationReasonTerminalMissing {
		t.Fatalf("missing thread/list result = %+v, want typed infrastructure error", partial)
	}
}

func TestQualificationAggregateSynthesizesEveryMissingMatrixChild(t *testing.T) {
	spec, _ := QualificationSpecFor(PrimitiveThreadList)
	child := QualificationArtifact{
		SchemaVersion: QualificationSchemaVersion,
		Results: []QualificationResult{
			newQualificationResult(spec, installedQualificationVersions(), ResultPass, QualificationReasonCompatible),
		},
	}
	encoded, err := child.JSON([]QualificationPrimitive{PrimitiveThreadList})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateQualificationArtifacts(map[QualificationPrimitive][]byte{
		PrimitiveThreadList: encoded,
	})
	if err == nil {
		t.Fatal("an aggregate with missing matrix children passed")
	}
	if len(aggregate.Results) != len(QualificationSpecs()) {
		t.Fatalf("aggregate results = %d", len(aggregate.Results))
	}
	byPrimitive := make(map[QualificationPrimitive]QualificationResult)
	for _, result := range aggregate.Results {
		byPrimitive[result.Primitive] = result
	}
	for _, primitive := range []QualificationPrimitive{PrimitiveDaemonLifecycle, PrimitivePreTurnAttach} {
		result := byPrimitive[primitive]
		if result.Class != ResultInfraError || result.Reason != QualificationReasonChildArtifactMissing {
			t.Fatalf("missing %s child = %+v", primitive, result)
		}
	}
}

func TestQualificationDecoderRejectsUnknownFieldsInsteadOfScanningValues(t *testing.T) {
	encoded := []byte(`{"schema_version":1,"results":[],"extra":"opaque"}`)
	if _, err := DecodeQualificationArtifact(encoded, nil); err == nil {
		t.Fatal("an undeclared artifact field passed structural decoding")
	}
}

func assertQualificationFields(t *testing.T, typ reflect.Type, want map[string]reflect.Type) {
	t.Helper()
	if typ.NumField() != len(want) {
		t.Fatalf("%s fields = %d, want %d", typ, typ.NumField(), len(want))
	}
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		name := field.Tag.Get("json")
		expected, ok := want[name]
		if !ok {
			t.Fatalf("%s has undeclared JSON field %q", typ, name)
		}
		if field.Type != expected {
			t.Fatalf("%s.%s type = %s, want %s", typ, field.Name, field.Type, expected)
		}
	}
}

func installedQualificationVersions() VersionTuple {
	return VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"}
}
