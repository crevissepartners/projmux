package runtime

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/internal/version"
)

// NewSDKCreate wires the existing create machine using only captured paths and
// explicit caller inputs. No ambient tmux target or provider lifetime is used.
func NewSDKCreate(paths config.Paths, socket, shell, executable string, sessionName func(string) string, runner TmuxCommandRunner, discoverRoots func() ([]string, error)) (*CreateExecution, *CreateTransaction) {
	storage := store.NewStore(store.PathFor(paths.StateDir))
	runtime := &Materializer{Executable: func() (string, error) { return executable, nil }, LookupEnv: func(key string) string {
		if key == "SHELL" {
			return shell
		}
		return ""
	}, ConfigPath: filepath.Join(paths.ConfigDir, "tmux.conf")}
	execution := &CreateExecution{ProcessInventory: func() resourcegraph.ProcessInventory { return resourcegraph.ProcessInventory{} }, Runtime: runtime, Shell: shell, BindWindowRuntime: func(mut coremetadata.Mutator, reg *coremetadata.Registry, w, s, id string) (coremetadata.Window, error) {
		return mut.ObserveWindowRuntimeBinding(reg, w, s, id)
	}}
	execution.Reconciler = &RegistryReconciler{}
	transaction := &CreateTransaction{Runtime: runtime, Reconciler: execution.Reconciler, Store: &CreateStore{Load: storage.LoadReadOnly, Snapshot: storage.LoadReadOnly, Update: storage.Update, Mutator: store.DefaultMutator}, NewOperationID: mutations.NewOperationID, Now: time.Now}
	execution.IssuePaneActivation = func(reg *coremetadata.Registry, mut coremetadata.Mutator, p, a, op string) (SuperviseSpec, error) {
		spec, err := IssuePaneActivation(nil, reg, mut, p, a, op)
		spec.RegistryPath = store.PathFor(paths.StateDir)
		return spec, err
	}
	execution.AdmitAnchor = func(reg coremetadata.Registry, uid string) error {
		pane, ok := reg.Pane(uid)
		if !ok {
			return nil
		}
		inventory := resourcegraph.ProcessInventory{}
		if !inventory.Declares(*pane) {
			return nil
		}
		_, err := inventory.AdmitProcess(*pane, resourcegraph.ProcessCreatePane)
		return err
	}
	execution.AdmitSplitAnchor = func(reg coremetadata.Registry, uid string) error {
		pane, ok := reg.Pane(uid)
		if !ok {
			return nil
		}
		inventory := resourcegraph.ProcessInventory{}
		if !inventory.Declares(*pane) {
			return nil
		}
		_, err := inventory.AdmitProcess(*pane, resourcegraph.ProcessSplit)
		return err
	}
	bound := false
	bind := func(ctx context.Context) error {
		if bound {
			return nil
		}
		route, err := ResolveProjectStopRoute(ctx, runner, socket)
		if err != nil {
			if socket != "" || !tmux.IsNoServerFailure(err) {
				return &mutations.Failure{Code: "create-route-refused", Cause: err}
			}
			target := TmuxTransport{Kind: TmuxSocketName, Value: DefaultAppSocket, Source: TmuxSocketNameSource}
			if socket != "" {
				target = TmuxTransport{Kind: TmuxSocketPath, Value: socket, Source: TmuxSocketPathSource}
			}
			route = RuntimeMutationRoute{Target: target, SocketName: DefaultAppSocket}
		}
		exact := ExplicitTmuxRunner{Runner: runner, Target: route.Target}
		hook := &hooks.Runner{GlobalConfigPath: filepath.Join(paths.ConfigDir, "config.toml"), DiscoverProjectHooks: true, TrustStorePath: filepath.Join(paths.StateDir, "trusted-projects.json"), Logger: io.Discard, Timeout: hooks.DefaultPostCreateTimeout, Version: version.String()}
		client := tmux.NewClient(exact, tmux.WithSocketName(route.SocketName), tmux.WithLookupEnv(func(string) string { return "" }), tmux.WithLifecycleHookRunner(hook))
		runtime.MaterializeRuntime = MaterializeRuntime{Runner: exact, Sessions: client, Target: route.Target, ExpectedSocketPath: route.ExpectedSocketPath, SocketName: route.SocketName, RouteAuthority: route.Authority, Warn: io.Discard}
		runtime.Mirror = store.NewMirror(exact)
		reconciler := &RegistryReconciler{DiscoverRoots: discoverRoots, LiveSessions: client.ExistingSessions, Mirror: runtime.Mirror, Shell: shell, RefuseForeign: true, SessionNameFor: sessionName, SessionSocketPath: func() string { return runtime.ExpectedSocketPath }}
		reconciler.InitializeRefusalBookkeeping()
		reconciler.UseTypedMirror(RuntimeMutationMetadataMirror{Runner: exact, Route: route})
		reconciler.ShareRouteIdentityScope(runtime)
		reconciler.ObserveServer = runtime.Mirror.ObserveServer
		reconciler.ObserveLegacy = runtime.Mirror.ObserveLegacySessionTargets
		execution.Reconciler = reconciler
		transaction.Reconciler = reconciler
		bound = true
		return nil
	}
	execution.EnsureRuntimeRoute = bind
	transaction.EnsureRuntimeRoute = bind
	return execution, transaction
}
