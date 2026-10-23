package settings

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/tmuxconfig"
)

type resourcesApplyRunner struct {
	Calls                 [][]string
	Semantic              []string
	Fault                 string
	Cause                 error
	Hits, Guards, DriftAt int
	Drift                 string
}

func (r *resourcesApplyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.Calls = append(r.Calls, append([]string{name}, args...))
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != "/statusbar-resources/fake" {
		return nil, fmt.Errorf("unexpected exact transport %s %q", name, args)
	}
	a := args[2:]
	if slices.Contains(a, ";") {
		r.Guards++
		var out strings.Builder
		start := 0
		for i := 0; i <= len(a); i++ {
			if i < len(a) && a[i] != ";" {
				continue
			}
			part, err := r.answer(a[start:i])
			out.Write(part)
			if len(part) > 0 && part[len(part)-1] != '\n' {
				out.WriteByte('\n')
			}
			if err != nil {
				return []byte(out.String()), err
			}
			start = i + 1
		}
		return []byte(out.String()), nil
	}
	return r.answer(a)
}

func (r *resourcesApplyRunner) answer(a []string) ([]byte, error) {
	last := a[len(a)-1]
	if last == mutationruntime.RouteReadBoundary {
		return []byte(last + "\n"), nil
	}
	drift := r.DriftAt > 0 && r.Guards == r.DriftAt
	switch last {
	case "#{socket_path}":
		if drift && r.Drift == "socket" {
			r.Hits++
			return []byte("/other/socket\n"), nil
		}
		return []byte("/statusbar-resources/fake\n"), nil
	case "#{pid}":
		if drift && r.Drift == "pid" {
			r.Hits++
			return []byte("9999\n"), nil
		}
		return []byte("4242\n"), nil
	case "@projmux_app":
		if drift && r.Drift == "app" {
			r.Hits++
			return []byte("0\n"), nil
		}
		return []byte("1\n"), nil
	case mutationruntime.RuntimeMutationSocketNameOption:
		if drift && r.Drift == "logical" {
			r.Hits++
			return []byte("other\n"), nil
		}
		return []byte("projmux\n"), nil
	}
	effect := ""
	switch {
	case last == tmuxconfig.TmuxSequenceRootsOption:
		effect = "roots"
	case last == tmuxconfig.TmuxSequenceTablesOption:
		effect = "tables"
	case a[0] == "unbind-key" && a[1] == "-q":
		effect = "unbind-root"
	case a[0] == "unbind-key":
		effect = "unbind-table"
	case a[0] == "source-file":
		effect = "source"
	default:
		return nil, fmt.Errorf("unexpected command %q", a)
	}
	r.Semantic = append(r.Semantic, effect)
	if r.Fault == effect {
		r.Hits++
		return nil, r.Cause
	}
	if effect == "roots" {
		return []byte("C-k\n"), nil
	}
	if effect == "tables" {
		return []byte("projmux-seq-fixture\n"), nil
	}
	return nil, nil
}

func resourcesOwnedRoute() mutationruntime.RuntimeMutationRoute {
	return mutationruntime.RuntimeMutationRoute{Target: mutationruntime.TmuxTransport{Kind: mutationruntime.TmuxSocketPath, Value: "/statusbar-resources/fake"}, ExpectedSocketPath: "/statusbar-resources/fake", SocketName: "projmux", Authority: &mutationruntime.RuntimeMutationRouteAuthority{Class: mutationruntime.RuntimeMutationRouteApp, ServerPID: "4242"}}
}

func TestStatusbarResourcesApplyOrderAndFailures(t *testing.T) {
	all := []string{"roots", "tables", "unbind-root", "unbind-table", "source"}
	for _, fault := range append([]string{""}, all...) {
		t.Run("effect-"+fault, func(t *testing.T) {
			_, dest := resourcesTestRoot(t)
			cause := errors.New("designated " + fault)
			r := &resourcesApplyRunner{Fault: fault, Cause: cause}
			p, l, err := (StatusbarResourcesApply{Generate: func() (string, error) { return "/exact/config/tmux.conf", nil }, Runner: r, ResolveRoute: func(context.Context) (mutationruntime.RuntimeMutationRoute, error) { return resourcesOwnedRoute(), nil }}).Apply()
			resourcesRecord(t, filepath.Join(dest, "trace.json"), map[string]any{"calls": r.Calls, "semantic": r.Semantic, "hits": r.Hits, "guards": r.Guards, "prepared": p, "live": l, "error": resourcesError(err), "unwrap": fmt.Sprint(errors.Unwrap(err)), "is": errors.Is(err, cause)})
			want := all
			if fault != "" {
				want = all[:slices.Index(all, fault)+1]
			}
			if !reflect.DeepEqual(r.Semantic, want) || p.Status != ApplyOK || r.Guards != len(want) {
				t.Fatal(r.Semantic, r.Guards, want, p, l, err)
			}
			if fault == "" {
				if err != nil || l.Status != ApplyOK || r.Hits != 0 {
					t.Fatal(l, err, r.Hits)
				}
			} else if !errors.Is(err, cause) || r.Hits != 1 || l.Status != ApplyFailed {
				t.Fatal(l, err, r.Hits)
			}
			if fault == "" || fault == "source" {
				if !reflect.DeepEqual(r.Calls[len(r.Calls)-1], []string{"tmux", "-S", "/statusbar-resources/fake", "source-file", "/exact/config/tmux.conf"}) {
					t.Fatal(r.Calls)
				}
			}
		})
	}
	for _, drift := range []string{"socket", "pid", "app", "logical"} {
		for at := 1; at <= 5; at++ {
			t.Run(fmt.Sprintf("%s-before-%d", drift, at), func(t *testing.T) {
				_, dest := resourcesTestRoot(t)
				r := &resourcesApplyRunner{Drift: drift, DriftAt: at}
				p, l, err := (StatusbarResourcesApply{Generate: func() (string, error) { return "/exact/tmux.conf", nil }, Runner: r, ResolveRoute: func(context.Context) (mutationruntime.RuntimeMutationRoute, error) { return resourcesOwnedRoute(), nil }}).Apply()
				resourcesRecord(t, filepath.Join(dest, "drift.json"), map[string]any{"calls": r.Calls, "semantic": r.Semantic, "hits": r.Hits, "guards": r.Guards, "error": resourcesError(err)})
				if err == nil || p.Status != ApplyOK || l.Status != ApplyFailed || r.Hits != 1 || r.Guards != at || !slices.Equal(r.Semantic, all[:at-1]) {
					t.Fatal(p, l, err, r.Hits, r.Guards, r.Semantic)
				}
			})
		}
	}
}

func TestStatusbarResourcesApplyPreparationAndSkip(t *testing.T) {
	for _, mode := range []string{"prepare", "outside", "runner", "route", "empty", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			cause := errors.New("designated " + mode)
			r := &resourcesApplyRunner{}
			skipCalls, routeCalls := 0, 0
			s := StatusbarResourcesApply{Runner: r, Generate: func() (string, error) {
				if mode == "prepare" {
					return "", cause
				}
				return "/exact/tmux.conf", nil
			}, Skip: func() (bool, string) { skipCalls++; return mode == "outside", "Settings is not running inside tmux" }, ResolveRoute: func(context.Context) (mutationruntime.RuntimeMutationRoute, error) {
				routeCalls++
				if mode == "route" {
					return mutationruntime.RuntimeMutationRoute{}, cause
				}
				route := resourcesOwnedRoute()
				if mode == "empty" {
					route.ExpectedSocketPath = ""
				}
				if mode == "standalone" {
					route.Authority.Class = mutationruntime.RuntimeMutationRouteStandalone
				}
				return route, nil
			}}
			if mode == "runner" {
				s.Runner = nil
			}
			p, l, err := s.Apply()
			if len(r.Calls) != 0 {
				t.Fatal("effect after refused stage", r.Calls)
			}
			switch mode {
			case "prepare":
				if !errors.Is(err, cause) || p.Status != ApplyFailed || l.Detail != "generated tmux config failed" || skipCalls != 0 || routeCalls != 0 {
					t.Fatal(p, l, err)
				}
			case "outside", "runner":
				want := "Settings is not running inside tmux"
				if mode == "runner" {
					want = "exact tmux command runner is not configured"
				}
				if err != nil || p.Status != ApplyOK || l.Status != ApplySkipped || l.Detail != want || skipCalls != 1 || routeCalls != 0 {
					t.Fatal(p, l, err)
				}
			case "route", "empty", "standalone":
				if err == nil || p.Status != ApplyOK || l.Status != ApplyFailed || routeCalls != 1 {
					t.Fatal(p, l, err)
				}
				if mode == "route" && !errors.Is(err, cause) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStatusbarResourcesApplyRetireCallbackExactlyOnce(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			_, dest := resourcesTestRoot(t)
			cause := errors.New("retire callback designated roots failure")
			r := &resourcesApplyRunner{Cause: cause}
			if fail {
				r.Fault = "roots"
			}
			calls := 0
			s := StatusbarResourcesApply{
				Generate:     func() (string, error) { return "/exact/tmux.conf", nil },
				Runner:       r,
				ResolveRoute: func(context.Context) (mutationruntime.RuntimeMutationRoute, error) { return resourcesOwnedRoute(), nil },
				Retire: func(ctx context.Context, route mutationruntime.RuntimeMutationRoute) error {
					calls++
					return RetireCurrentTmuxKeySequenceState(ctx, r, route)
				},
			}
			p, l, err := s.Apply()
			resourcesRecord(t, filepath.Join(dest, "callback.json"), map[string]any{"calls": r.Calls, "semantic": r.Semantic, "callbackCount": calls, "guards": r.Guards, "hits": r.Hits, "error": resourcesError(err)})
			want, guards := []string{"roots", "tables", "unbind-root", "unbind-table", "source"}, 5
			if fail {
				want, guards = []string{"roots"}, 1
			}
			if calls != 1 || !slices.Equal(r.Semantic, want) || r.Guards != guards || p.Status != ApplyOK {
				t.Fatal(calls, r.Semantic, r.Guards, p, l, err)
			}
			if fail {
				if !errors.Is(err, cause) || r.Hits != 1 || l.Status != ApplyFailed || !strings.HasPrefix(l.Detail, "live tmux sequence cleanup: ") {
					t.Fatal(l, err, r.Hits)
				}
			} else if err != nil || l.Status != ApplyOK {
				t.Fatal(l, err)
			}
		})
	}
}
