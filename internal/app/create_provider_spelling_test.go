package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Stop at the enabled-provider gate: reaching it with a canonical ID proves
// argv admission (including the process typed request) accepted the spelling,
// without starting a provider or mutating either runtime.
type providerSpellingLauncher struct {
	*fakeAgentLauncher
	providers []string
	stop      error
}

func (f *providerSpellingLauncher) RequireAgentEnabled(provider string) error {
	f.providers = append(f.providers, provider)
	return f.stop
}

func TestCreateProviderSpellingsReachCanonicalAdmission(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, raw := range []string{provider, strings.ToUpper(provider), " \t" + provider + "\n", " " + strings.ToUpper(provider[:1]) + provider[1:] + " "} {
			for _, node := range []string{"agent", "window"} {
				for _, host := range []string{"tmux", "process"} {
					for _, defaultHost := range []bool{false, true} {
						t.Run(strings.Join([]string{node, raw, host, map[bool]string{false: "explicit", true: "default"}[defaultHost]}, "/"), func(t *testing.T) {
							assertProviderSpellingAdmission(t, node, raw, provider, host, defaultHost)
						})
					}
				}
			}
		}
		// Shortcut command tokens themselves retain their existing grammar;
		// unlike --provider values, they are not case/whitespace aliases.
		for _, host := range []string{"tmux", "process"} {
			for _, defaultHost := range []bool{false, true} {
				t.Run(provider+"-shortcut/"+host+"/"+map[bool]string{false: "explicit", true: "default"}[defaultHost], func(t *testing.T) {
					assertProviderSpellingAdmission(t, provider, "", provider, host, defaultHost)
				})
			}
		}
	}
}

func TestCreateUnknownProviderSpellingsRemainEffectFree(t *testing.T) {
	for _, node := range []string{"agent", "window"} {
		var baseline string
		for _, host := range []string{"tmux", "process"} {
			for _, defaultHost := range []bool{false, true} {
				t.Run(node+"/"+host+"/"+map[bool]string{false: "explicit", true: "default"}[defaultHost], func(t *testing.T) {
					err := assertProviderSpellingAdmission(t, node, " Unknown-Provider ", "", host, defaultHost)
					if baseline == "" {
						baseline = err.Error()
					} else if err.Error() != baseline {
						t.Fatalf("unknown provider refusal = %q, want host-independent %q", err, baseline)
					}
				})
			}
		}
	}
}

func assertProviderSpellingAdmission(t *testing.T, node, raw, want, host string, defaultHost bool) error {
	t.Helper()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	command, launcher := newTestAgentCreateCommand(t, store, tmux)
	gate := &providerSpellingLauncher{fakeAgentLauncher: launcher, stop: errors.New("provider spelling admitted")}
	command.agents = gate
	home := t.TempDir()
	command.homeDir = func() (string, error) { return home, nil }
	stateDir := t.TempDir()
	command.store.stateDir = func() (string, error) { return stateDir, nil }
	args := []string{node, "--project", "alpha"}
	if node != "window" {
		args = append(args, "--detach=false")
	}
	// A default-process Window is detached (and has no --detach flag).
	// Stop immediately after its typed request is validated, before spawning
	// the detached launcher. That constructor only accepts canonical IDs.
	detachedWindow := node == "window" && host == "process" && defaultHost
	if detachedWindow {
		command.store.stateDir = func() (string, error) { return "", gate.stop }
	}
	if node == "agent" || node == "window" {
		args = append(args, "--provider", raw)
	}
	if node != "window" {
		args = append(args, "--window", "review")
	}
	if defaultHost {
		path := filepath.Join(home, ".config", "projmux", "agent-default-host")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(host), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		args = append(args, "--host", host)
	}
	before := store.registry.Clone()
	out, _, err := runRoute(t, command, args...)
	if want != "" {
		var wantProviders []string
		if !detachedWindow {
			wantProviders = []string{want}
		}
		if !errors.Is(err, gate.stop) || !reflect.DeepEqual(gate.providers, wantProviders) {
			t.Fatalf("provider %q: gate providers=%q error=%v, want canonical %q at admission gate", raw, gate.providers, err, want)
		}
	} else if err == nil || errors.Is(err, gate.stop) || len(gate.providers) != 0 {
		t.Fatalf("unknown provider reached admission gate: providers=%q error=%v", gate.providers, err)
	}
	if out != "" || store.writes != 0 || !reflect.DeepEqual(before, store.registry) || len(tmux.calls) != 0 || len(launcher.plans) != 0 {
		t.Fatalf("admission check had effects: output=%q writes=%d tmux=%v plans=%v", out, store.writes, tmux.calls, launcher.plans)
	}
	return err
}
