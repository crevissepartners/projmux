package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func newRegistryReconciler(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
	return newRegistryReconcilerWithRoute(runner, sessions, runtimeMutationRoute{})
}

func newRegistryReconcilerWithRoute(runner tmuxCommandRunner, sessions sessionLister, route runtimeMutationRoute) *registryReconciler {
	mirror := intmetadata.NewMirror(runner)
	home, err := os.UserHomeDir()
	namer := coresessions.NewNamer(home)
	reconciler := &registryReconciler{
		MarkApplyPhase: func(ctx context.Context, commit bool) {
			phase := diagnostics.ApplyLockPhaseObserve
			if commit {
				phase = diagnostics.ApplyLockPhaseCommit
			}
			markApplyLockPhase(ctx, phase)
		},
		DiscoverRoots: func() ([]string, error) {
			if err != nil {
				return nil, err
			}
			return discoverProjectRoots(home, os.Getenv)
		},
		LiveSessions: sessions.ExistingSessions,
		Mirror:       mirror,
		Shell:        configuredShell(os.Getenv),
		// Every production caller is an automatic/default recovery path unless
		// it explicitly opts into the approved D3 matcher. D2 import therefore
		// starts closed even for create-time convergence.
		RefuseForeign:            true,
		RefusedSessions:          map[string]bool{},
		RefusedSessionDivergence: map[string]resourcegraph.Divergence{},
		RefusedSessionReasons:    map[string]string{},
		ExactProjects:            map[string]string{},
		SessionNameFor: func(root string) string {
			return namer.SessionName(root)
		},
	}
	routeSocketPath := route.ExpectedSocketPath
	reconciler.SessionSocketPath = func() string { return routeSocketPath }
	if _, recording := runner.(*resourcePlanTmuxRunner); recording {
		// Project identity is already an explicit resource-plan item. Repeating
		// it inside the shadow reconciler would mutate the observation used to
		// classify D2/D3 rows and double-count the same desired write.
		reconciler.MirrorProjectWrite = func(context.Context, string, coremetadata.Project) error { return nil }
		reconciler.MirrorWindowWrite = mirror.MirrorWindow
		reconciler.MirrorPaneWrite = func(ctx context.Context, target, _ string, pane coremetadata.Pane) error {
			return mirror.MirrorPane(ctx, target, pane)
		}
		reconciler.ObserveLegacy = mirror.ObserveLegacySessionTargets
	} else {
		reconciler.UseTypedMirror(runtimeMutationMetadataMirror{Runner: runner, Route: route})
		reconciler.ObserveServer = mirror.ObserveServer
		reconciler.ObserveLegacy = func(ctx context.Context, session string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
			if snapshot := reconciler.PassServerSnapshot(ctx); snapshot != nil {
				if legacy, targets, ok := snapshot.LegacySessionTargets(session); ok {
					return legacy, targets, nil
				}
			}
			return mirror.ObserveLegacySessionTargets(ctx, session)
		}
	}
	return reconciler
}

// sessionLister is the live tmux session inventory seam.
type sessionLister interface {
	ExistingSessions(ctx context.Context) (map[string]bool, error)
}

// configuredShell resolves the shell whose basename seeds default Window and
// Pane names. It is the same `$SHELL` tmux itself falls back to.
func configuredShell(lookupEnv func(string) string) string {
	if lookupEnv == nil {
		return ""
	}
	return strings.TrimSpace(lookupEnv("SHELL"))
}

// discoverProjectRoots returns the selectable workdirs in a deterministic order.
//
// It reads exactly the sources the project picker reads -- candidate pins, the
// resolved repo root, and the configured managed roots -- through
// candidates.DiscoverProjectRoots, which deliberately excludes the home
// directory and the current path: those are picker conveniences, not evidence
// that a directory is a managed project.
//
// Only candidate pins are a discovery source. A managed pin names a Registry
// Project uid, and a Project's existence is the Registry's statement rather than
// something a filesystem scan has to rediscover; feeding managed pins back into
// discovery would make a presentation preference look like a scan root.
//
// The result is sorted by resolved (symlink-free) absolute path so candidate
// order never depends on filesystem scan order.
func discoverProjectRoots(homeDir string, lookupEnv func(string) string) ([]string, error) {
	homeDir = filepath.Clean(homeDir)
	repoRoot, _ := resolveProjdir(homeDir, lookupEnv, tmuxProjdirOption, config.LoadProjdir)

	var pinned []string
	homes := config.Homes{
		HomeDir:    homeDir,
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}
	if paths, err := homes.Paths(); err == nil {
		if set, err := pins.NewStore(paths.PinFile()).Load(); err == nil {
			pinned = set.CandidatePaths()
		}
	}

	return candidates.DiscoverProjectRoots(candidates.Inputs{
		HomeDir:      homeDir,
		RepoRoot:     repoRoot,
		ManagedRoots: switchManagedRoots(homeDir, repoRoot, extraProjdirRoots(lookupEnv), lookupEnv, config.LoadWorkdirs),
		Pins:         pinned,
	})
}
