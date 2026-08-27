package resourcegraph

import (
	"errors"
	"slices"
	"testing"
)

// TestResolveTransportPicksExactlyOneServer covers the routing decision, whose
// only forbidden outcome is an unprefixed tmux call.
func TestResolveTransportPicksExactlyOneServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		request    TransportRequest
		wantKind   TransportKind
		wantValue  string
		wantSource TransportSource
		wantArgs   []string
		wantErr    bool
	}{
		{
			name:     "explicit socket name",
			request:  TransportRequest{SocketName: " projmux "},
			wantKind: TransportSocketName, wantValue: "projmux",
			wantSource: TransportSourceSocketName, wantArgs: []string{"-L", "projmux"},
		},
		{
			name:     "explicit absolute socket path is cleaned",
			request:  TransportRequest{SocketPath: "/tmp/pmx/../pmx/tmux-1000/pmx"},
			wantKind: TransportSocketPath, wantValue: "/tmp/pmx/tmux-1000/pmx",
			wantSource: TransportSourceSocketPath, wantArgs: []string{"-S", "/tmp/pmx/tmux-1000/pmx"},
		},
		{
			name:    "relative socket path is refused rather than resolved",
			request: TransportRequest{SocketPath: "tmux-1000/pmx"},
			wantErr: true,
		},
		{
			name:    "both socket flags is a usage conflict",
			request: TransportRequest{SocketName: "projmux", SocketPath: "/tmp/pmx"},
			wantErr: true,
		},
		{
			name:     "explicit flags outrank an inherited server",
			request:  TransportRequest{SocketName: "probe", InheritedTMUX: "/tmp/tmux-1000/projmux,8084,6"},
			wantKind: TransportSocketName, wantValue: "probe",
			wantSource: TransportSourceSocketName, wantArgs: []string{"-L", "probe"},
		},
		{
			name:     "blank socket name does not outrank inherited transport",
			request:  TransportRequest{SocketName: " \t", InheritedTMUX: "/tmp/tmux-1000/projmux,8084,6"},
			wantKind: TransportSocketPath, wantValue: "/tmp/tmux-1000/projmux",
			wantSource: TransportSourceInheritedEnv, wantArgs: []string{"-S", "/tmp/tmux-1000/projmux"},
		},
		{
			name:     "blank socket path does not conflict with explicit name",
			request:  TransportRequest{SocketName: "probe", SocketPath: " \n"},
			wantKind: TransportSocketName, wantValue: "probe",
			wantSource: TransportSourceSocketName, wantArgs: []string{"-L", "probe"},
		},
		{
			name:     "blank socket name does not conflict with explicit path",
			request:  TransportRequest{SocketName: " ", SocketPath: "/tmp/probe"},
			wantKind: TransportSocketPath, wantValue: "/tmp/probe",
			wantSource: TransportSourceSocketPath, wantArgs: []string{"-S", "/tmp/probe"},
		},
		{
			name:    "relative explicit path is refused even with valid inherited transport",
			request: TransportRequest{SocketPath: "relative/probe", InheritedTMUX: "/tmp/tmux-1000/projmux,8084,6"},
			wantErr: true,
		},
		{
			name:     "inherited $TMUX supplies the absolute socket path",
			request:  TransportRequest{InheritedTMUX: "/tmp/tmux-1000/projmux,8084,6"},
			wantKind: TransportSocketPath, wantValue: "/tmp/tmux-1000/projmux",
			wantSource: TransportSourceInheritedEnv, wantArgs: []string{"-S", "/tmp/tmux-1000/projmux"},
		},
		{
			name:     "a relative inherited value is discarded, not guessed at",
			request:  TransportRequest{InheritedTMUX: "projmux,8084,6"},
			wantKind: TransportNone, wantSource: TransportSourceNone,
		},
		{
			name:     "blank inherited value is no transport",
			request:  TransportRequest{InheritedTMUX: " \t\n"},
			wantKind: TransportNone, wantSource: TransportSourceNone,
		},
		{
			name:     "no transport at all is a legal state",
			request:  TransportRequest{},
			wantKind: TransportNone, wantSource: TransportSourceNone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transport, err := ResolveTransport(test.request)
			if test.wantErr {
				if err == nil {
					t.Fatalf("resolved %+v, want an error", transport)
				}
				if transport.Present() {
					t.Fatalf("a failed resolution produced a usable transport %+v", transport)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTransport: %v", err)
			}
			if transport.Kind != test.wantKind || transport.Value != test.wantValue || transport.Source != test.wantSource {
				t.Fatalf("transport = %+v, want kind %q value %q source %q",
					transport, test.wantKind, test.wantValue, test.wantSource)
			}
			if !slices.Equal(transport.Args(), test.wantArgs) {
				t.Fatalf("args = %v, want %v", transport.Args(), test.wantArgs)
			}
			if transport.Present() && transport.Flag() != test.wantArgs[0] {
				t.Fatalf("Flag() = %q, want %q", transport.Flag(), test.wantArgs[0])
			}
			if transport.Present() != (len(test.wantArgs) > 0) {
				t.Fatalf("Present() = %v with args %v", transport.Present(), transport.Args())
			}
			if !transport.Present() && transport.String() != "no tmux transport" {
				t.Fatalf("absent transport renders as %q", transport.String())
			}
		})
	}
}

func TestResolveTransportConflictIsTyped(t *testing.T) {
	t.Parallel()
	_, err := ResolveTransport(TransportRequest{SocketName: "a", SocketPath: "/tmp/b"})
	if !errors.Is(err, ErrTransportConflict) {
		t.Fatalf("err = %v, want ErrTransportConflict", err)
	}
}

func TestTransportNeverProjectsAnInvalidTargetToBareTmux(t *testing.T) {
	t.Parallel()
	for _, transport := range []Transport{
		{},
		{Kind: TransportNone, Value: "ignored"},
		{Kind: TransportKind("future"), Value: "socket"},
		{Kind: TransportSocketName},
		{Kind: TransportSocketName, Value: " socket "},
		{Kind: TransportSocketPath, Value: "relative/socket"},
		{Kind: TransportSocketPath, Value: "/tmp/../relative/socket"},
	} {
		if transport.Present() || transport.Args() != nil || transport.Flag() != "" {
			t.Fatalf("invalid transport projected a tmux route: %+v args=%v flag=%q", transport, transport.Args(), transport.Flag())
		}
	}
}

func TestTransportSourceIsNotRouteIdentity(t *testing.T) {
	t.Parallel()
	explicit := Transport{Kind: TransportSocketPath, Value: "/tmp/projmux", Source: TransportSourceSocketPath}
	inherited := Transport{Kind: TransportSocketPath, Value: "/tmp/projmux", Source: TransportSourceInheritedEnv}
	if !explicit.SameRoute(inherited) {
		t.Fatalf("the same socket path split route identity by diagnostic source: explicit=%+v inherited=%+v", explicit, inherited)
	}
	if explicit.SameRoute(Transport{Kind: TransportSocketName, Value: "/tmp/projmux", Source: TransportSourceSocketName}) {
		t.Fatal("socket name and socket path collapsed into one route")
	}
	if projected := inherited.ExplicitProjection(); projected.Source != TransportSourceSocketPath || !projected.SameRoute(inherited) {
		t.Fatalf("explicit projection = %+v, want the same -S route with explicit-path report source", projected)
	}
}

// TestHostModeFromAppMarkerOnlyTrustsTheExactValue keeps app ownership from being
// widened by a mistyped or hand-set option.
func TestHostModeFromAppMarkerOnlyTrustsTheExactValue(t *testing.T) {
	t.Parallel()
	tests := map[string]HostMode{
		"1":       HostModeAppOwned,
		" 1\n":    HostModeAppOwned,
		"":        HostModeStandalone,
		"0":       HostModeStandalone,
		"true":    HostModeStandalone,
		"11":      HostModeStandalone,
		"1 1":     HostModeStandalone,
		"projmux": HostModeStandalone,
	}
	for value, want := range tests {
		if got := HostModeFromAppMarker(value); got != want {
			t.Fatalf("HostModeFromAppMarker(%q) = %q, want %q", value, got, want)
		}
	}
}

// TestClassesAndScopesAreClosedSets guards the vocabularies every later consumer
// switches on: adding a member is a deliberate contract change, and the ordering
// helpers must stay total.
func TestClassesAndScopesAreClosedSets(t *testing.T) {
	t.Parallel()
	if got := Classes(); len(got) != 7 {
		t.Fatalf("classes = %v, want the seven-member attribution set", got)
	}
	seen := map[Class]bool{}
	for _, class := range Classes() {
		if seen[class] {
			t.Fatalf("class %q listed twice", class)
		}
		seen[class] = true
	}
	for _, kind := range ObjectKinds() {
		if objectKindRank(kind) < 0 {
			t.Fatalf("object kind %q has no rank", kind)
		}
	}
	for _, scope := range Scopes() {
		if scopeRank(scope) < 0 {
			t.Fatalf("scope %q has no rank", scope)
		}
	}
	if objectKindRank(ObjectSession) >= objectKindRank(ObjectWindow) ||
		objectKindRank(ObjectWindow) >= objectKindRank(ObjectPane) {
		t.Fatalf("object kinds are not ordered outermost first")
	}
}
