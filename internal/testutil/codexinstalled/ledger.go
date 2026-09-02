package codexinstalled

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type CommandScope string

const (
	ScopeIsolated CommandScope = "isolated"
	ScopeAmbient  CommandScope = "ambient"
)

type MutationClass string

const (
	MutationNone              MutationClass = "none"
	MutationEndpointLifecycle MutationClass = "endpoint-lifecycle"
	MutationProtocolSession   MutationClass = "protocol-session"
)

type Command struct {
	Scope     CommandScope
	Operation string
	Mutation  MutationClass
}

type Ledger struct {
	path   string
	mu     sync.Mutex
	direct []Command
}

func newLedger(path string) *Ledger { return &Ledger{path: path} }

func (ledger *Ledger) record(command Command) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.direct = append(ledger.direct, command)
}

func (ledger *Ledger) Commands() ([]Command, error) {
	ledger.mu.Lock()
	commands := append([]Command(nil), ledger.direct...)
	ledger.mu.Unlock()

	file, err := os.Open(ledger.path) // #nosec G304 -- the path is an exact fixture-owned ledger.
	if os.IsNotExist(err) {
		return commands, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open installed Codex ledger: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 2 {
			return nil, fmt.Errorf("malformed installed Codex ledger record")
		}
		scope := CommandScope(fields[0])
		if scope != ScopeIsolated && scope != ScopeAmbient {
			return nil, fmt.Errorf("malformed installed Codex ledger scope")
		}
		count, err := strconv.Atoi(fields[1])
		if err != nil || count != len(fields)-2 {
			return nil, fmt.Errorf("malformed installed Codex ledger record")
		}
		commands = append(commands, classifyCodexCommand(scope, fields[2:]))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read installed Codex ledger: %w", err)
	}
	return commands, nil
}

func (ledger *Ledger) AmbientMutations() ([]Command, error) {
	commands, err := ledger.Commands()
	if err != nil {
		return nil, err
	}
	mutations := make([]Command, 0)
	for _, command := range commands {
		if command.Scope == ScopeAmbient && command.Mutation != MutationNone {
			mutations = append(mutations, command)
		}
	}
	return mutations, nil
}

func (ledger *Ledger) AssertNoAmbientMutation() error {
	mutations, err := ledger.AmbientMutations()
	if err != nil {
		return err
	}
	if len(mutations) != 0 {
		return fmt.Errorf("installed Codex fixture recorded %d ambient mutations: %v", len(mutations), mutations)
	}
	return nil
}

func (ledger *Ledger) AssertNoLifecycleMutation() error {
	commands, err := ledger.Commands()
	if err != nil {
		return err
	}
	for _, command := range commands {
		if command.Mutation == MutationEndpointLifecycle {
			return fmt.Errorf("installed Codex fixture recorded lifecycle mutation %q", command.Operation)
		}
	}
	return nil
}

func (ledger *Ledger) HasOperation(operation string) (bool, error) {
	commands, err := ledger.Commands()
	if err != nil {
		return false, err
	}
	for _, command := range commands {
		if command.Operation == operation {
			return true, nil
		}
	}
	return false, nil
}

func (ledger *Ledger) DistinctOperations() ([]string, error) {
	commands, err := ledger.Commands()
	if err != nil {
		return nil, err
	}
	distinct := make([]string, 0)
	for _, command := range commands {
		seen := slices.Contains(distinct, command.Operation)
		if !seen {
			distinct = append(distinct, command.Operation)
		}
	}
	return distinct, nil
}

func classifyCodexCommand(scope CommandScope, argv []string) Command {
	command := Command{Scope: scope, Operation: "unknown", Mutation: MutationEndpointLifecycle}
	switch {
	case len(argv) == 1 && argv[0] == "--version":
		command.Operation, command.Mutation = "cli-version", MutationNone
	case equalArgv(argv, "app-server", "proxy"):
		command.Operation, command.Mutation = "proxy-session", MutationProtocolSession
	case equalArgv(argv, "app-server", "daemon", "version"):
		command.Operation, command.Mutation = "daemon-version", MutationNone
	case equalArgv(argv, "app-server", "daemon", "start"):
		command.Operation = "managed-start"
	case equalArgv(argv, "app-server", "daemon", "stop"):
		command.Operation = "managed-stop"
	case equalArgv(argv, "app-server", "--listen", "unix://"):
		command.Operation = "direct-start"
	case len(argv) == 4 && argv[0] == "resume" && argv[1] == "--remote" && argv[2] == "unix://" && strings.TrimSpace(argv[3]) != "":
		command.Operation, command.Mutation = "pre-turn-cli-remote-resume", MutationProtocolSession
	}
	return command
}

func equalArgv(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func writeLedgerShim(root string) (string, error) {
	bin := filepath.Join(root, "fixture-bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return "", fmt.Errorf("create installed Codex shim directory: %w", err)
	}
	shimPath := filepath.Join(bin, "codex")
	const shim = `#!/bin/sh
scope=ambient
if [ "$CODEX_HOME" = "$PROJMUX_CODEX_INSTALLED_HOME" ]; then scope=isolated; fi
{
	  printf '%s\t%s' "$scope" "$#"
	  for arg do printf '\t%s' "$arg"; done
	  printf '\n'
} >> "$PROJMUX_CODEX_INSTALLED_LEDGER"
if [ "$#" -eq 3 ] && [ "$1" = "app-server" ] && [ "$2" = "daemon" ] && [ "$3" = "start" ]; then
  "$PROJMUX_CODEX_INSTALLED_REAL" "$@" > "$PROJMUX_CODEX_INSTALLED_START_RESULT"
  status=$?
  /bin/cat "$PROJMUX_CODEX_INSTALLED_START_RESULT"
  exit "$status"
fi
exec "$PROJMUX_CODEX_INSTALLED_REAL" "$@"
`
	if err := os.WriteFile(shimPath, []byte(shim), 0o700); err != nil { // #nosec G306 -- a command shim must be executable.
		cleanupErr := os.RemoveAll(bin)
		return "", errors.Join(fmt.Errorf("write installed Codex shim: %w", err), cleanupErr)
	}
	return shimPath, nil
}
