package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/tmuxconfig"
)

type ApplyStatus string

const (
	ApplyOK      ApplyStatus = "ok"
	ApplySkipped ApplyStatus = "skipped"
	ApplyFailed  ApplyStatus = "failed"
)

type ApplyStage struct {
	Status ApplyStatus
	Detail string
}

// StatusbarResourcesApply shares generation and guarded sequence retirement.
// Skip and ResolveRoute belong to the caller: CLI invocation and SDK explicit
// routing retain their distinct admission policies.
type StatusbarResourcesApply struct {
	Generate     func() (string, error)
	Skip         func() (bool, string)
	Runner       mutationruntime.TmuxCommandRunner
	ResolveRoute func(context.Context) (mutationruntime.RuntimeMutationRoute, error)
	Retire       func(context.Context, mutationruntime.RuntimeMutationRoute) error
}

func applyDiagnostic(stage string, err error) string {
	if err == nil {
		return stage
	}
	return stage + ": " + err.Error()
}

func (s StatusbarResourcesApply) Apply() (prepared ApplyStage, live ApplyStage, err error) {
	configPath, genErr := s.Generate()
	if genErr != nil {
		prepared = ApplyStage{Status: ApplyFailed, Detail: applyDiagnostic("generated tmux config", genErr)}
		live = ApplyStage{Status: ApplySkipped, Detail: "generated tmux config failed"}
		return prepared, live, genErr
	}
	prepared = ApplyStage{Status: ApplyOK, Detail: "generated tmux config: " + configPath}
	if s.Skip != nil {
		if skip, detail := s.Skip(); skip {
			return prepared, ApplyStage{Status: ApplySkipped, Detail: detail}, nil
		}
	}
	if s.Runner == nil {
		return prepared, ApplyStage{Status: ApplySkipped, Detail: "exact tmux command runner is not configured"}, nil
	}
	ctx := context.Background()
	route, routeErr := s.ResolveRoute(ctx)
	if routeErr != nil || route.ExpectedSocketPath == "" {
		if routeErr == nil {
			routeErr = errors.New("Settings live reload has no exact physical socket")
		}
		return prepared, ApplyStage{Status: ApplyFailed, Detail: applyDiagnostic("exact live tmux route", routeErr)}, routeErr
	}
	if route.Authority != nil && route.Authority.Class == mutationruntime.RuntimeMutationRouteStandalone {
		routeErr = errors.New("Settings live reload refuses an operator-owned standalone tmux server")
		return prepared, ApplyStage{Status: ApplyFailed, Detail: applyDiagnostic("exact live tmux route", routeErr)}, routeErr
	}
	retire := s.Retire
	if retire == nil {
		retire = func(ctx context.Context, route mutationruntime.RuntimeMutationRoute) error {
			return RetireCurrentTmuxKeySequenceState(ctx, s.Runner, route)
		}
	}
	if retireErr := retire(ctx, route); retireErr != nil {
		return prepared, ApplyStage{Status: ApplyFailed, Detail: applyDiagnostic("live tmux sequence cleanup", retireErr)}, retireErr
	}
	if guardErr := mutationruntime.GuardResolvedRuntimeMutationRoute(ctx, s.Runner, route); guardErr != nil {
		return prepared, ApplyStage{Status: ApplyFailed, Detail: applyDiagnostic("live tmux reload guard", guardErr)}, guardErr
	}
	if _, reloadErr := s.Runner.Run(ctx, "tmux", "-S", route.ExpectedSocketPath, "source-file", configPath); reloadErr != nil {
		return prepared, ApplyStage{Status: ApplyFailed, Detail: applyDiagnostic("live tmux reload", reloadErr)}, reloadErr
	}
	return prepared, ApplyStage{Status: ApplyOK}, nil
}

// RetireCurrentTmuxKeySequenceState preserves a guard before every read/write.
func RetireCurrentTmuxKeySequenceState(ctx context.Context, runner mutationruntime.TmuxCommandRunner, route mutationruntime.RuntimeMutationRoute) error {
	read := func(option string) (string, error) {
		if err := mutationruntime.GuardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
			return "", err
		}
		out, err := runner.Run(ctx, "tmux", "-S", route.ExpectedSocketPath, "show-options", "-gqv", option)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", option, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	roots, err := read(tmuxconfig.TmuxSequenceRootsOption)
	if err != nil {
		return err
	}
	tables, err := read(tmuxconfig.TmuxSequenceTablesOption)
	if err != nil {
		return err
	}
	for _, command := range tmuxconfig.KeySequenceRetireCommandsWithPrefix([]string{"tmux"}, roots, tables) {
		if err := mutationruntime.GuardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
			return err
		}
		if _, err := runner.Run(ctx, "tmux", append([]string{"-S", route.ExpectedSocketPath}, command[1:]...)...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(command[1:], " "), err)
		}
	}
	return nil
}
