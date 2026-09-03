package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/version"
)

// codexBrokerSubcommands lists the hidden broker runtime routes, in help order.
var codexBrokerSubcommands = []string{"serve", "probe"}

const (
	// codexBrokerReadyLine is the single content-free readiness line the
	// runtime prints once it is published and serving. A launcher that needs a
	// synchronous signal reads it; Ensure does not, because it proves readiness
	// by dialing the socket rather than by trusting a line of text.
	codexBrokerReadyLine = "codex-broker ready"
	// codexBrokerAttachTimeout bounds the upstream attach handshake.
	codexBrokerAttachTimeout = 5 * time.Second
	// codexBrokerProbeTimeout bounds one probe's discovery and startup wait.
	codexBrokerProbeTimeout = 15 * time.Second
)

// codexBrokerEndpointRoute is the process-local transport half of one durable
// generation route. The discovery key carries only the durable endpoint
// identity; this value tells the broker process how to attach to that exact
// endpoint without granting it daemon lifecycle authority.
type codexBrokerEndpointRoute struct {
	StateDomainID        string
	EndpointGenerationID string
	SocketPath           string
	Default              bool
}

func (route codexBrokerEndpointRoute) endpointKey() (codexbroker.EndpointKey, error) {
	if route == (codexBrokerEndpointRoute{}) {
		return codexbroker.DefaultEndpointKey, nil
	}
	if route.Default == (strings.TrimSpace(route.SocketPath) != "") {
		return "", errors.New("codex broker generation requires exactly one default or private endpoint transport")
	}
	if !route.Default && !filepath.IsAbs(strings.TrimSpace(route.SocketPath)) {
		return "", errors.New("codex broker generation private endpoint socket must be absolute")
	}
	key, err := codexbroker.NewEndpointKey(route.StateDomainID, route.EndpointGenerationID)
	if err != nil {
		return "", fmt.Errorf("resolve codex broker generation key: %s", codexbroker.RefusalOf(err))
	}
	return key, nil
}

func (route codexBrokerEndpointRoute) opener() codexbroker.Opener {
	if route == (codexBrokerEndpointRoute{}) || route.Default {
		return codexbroker.DefaultOpener(version.String(), codexappserver.AttachOptions{
			Timeout: codexBrokerAttachTimeout, ExperimentalAPI: true,
		})
	}
	socketPath := filepath.Clean(route.SocketPath)
	return func(ctx context.Context) (codexbroker.Endpoint, error) {
		return codexappserver.OpenPrivateUnix(ctx, socketPath, codexBrokerAttachTimeout, version.String(), true)
	}
}

// codexBrokerCommand is the minimal executable seam for the Codex endpoint
// broker runtime.
//
// It is deliberately thin. Everything about singletons, discovery, credentials,
// protocol negotiation, draining, and shutdown lives in `codexbroker`; this
// file only resolves the state domain from the process environment, hands the
// runtime an endpoint opener, and keeps the process alive. Duplicating any of
// the runtime's decisions here would give the broker two owners.
//
// The product path reaches the runtime through the binding client rather than
// through these routes: `serve` is what a client launches when discovery finds
// no published runtime, and `probe` is the operator-reachable proof of life.
// Since the per-Agent observer retirement this runtime is the only producer of
// native Codex lifecycle, control, and approval.
type codexBrokerCommand struct {
	lookupEnv  func(string) string
	homeDir    func() (string, error)
	executable func() (string, error)
}

func newCodexBrokerCommand() *codexBrokerCommand {
	return &codexBrokerCommand{lookupEnv: os.Getenv, homeDir: os.UserHomeDir, executable: os.Executable}
}

// Run dispatches one `internal codex-broker <subcommand>` invocation.
func (c *codexBrokerCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("internal codex-broker requires a subcommand: %s",
			strings.Join(codexBrokerSubcommands, ", ")))
	}
	switch args[0] {
	case "serve":
		return c.runServe(args[1:], stdout, stderr)
	case "probe":
		return c.runProbe(args[1:], stdout, stderr)
	default:
		return usageError(fmt.Sprintf("internal codex-broker %s is not available; this release implements: %s",
			args[0], strings.Join(codexBrokerSubcommands, ", ")))
	}
}

// runServe hosts one broker runtime until it idles out or is signalled.
func (c *codexBrokerCommand) runServe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("internal codex-broker serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDomain := fs.String("state-domain", "", "absolute state domain the runtime singleton is scoped to")
	endpointStateDomain := fs.String("endpoint-state-domain", "", "durable Codex endpoint state-domain identity")
	endpointGeneration := fs.String("endpoint-generation", "", "durable Codex endpoint generation identity")
	endpointSocket := fs.String("endpoint-socket", "", "absolute private Codex endpoint socket")
	endpointDefault := fs.Bool("endpoint-default", false, "attach the durable generation to the unmanaged default endpoint")
	idle := fs.Duration("idle-timeout", 0, "bounded idle shutdown after the last binding is removed")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("internal codex-broker serve does not accept positional arguments")
	}
	route := codexBrokerEndpointRoute{
		StateDomainID: strings.TrimSpace(*endpointStateDomain), EndpointGenerationID: strings.TrimSpace(*endpointGeneration),
		SocketPath: strings.TrimSpace(*endpointSocket), Default: *endpointDefault,
	}
	endpointKey, err := route.endpointKey()
	if err != nil {
		return usageError(err.Error())
	}
	discovery, err := c.discoveryFor(*stateDomain, endpointKey)
	if err != nil {
		return err
	}
	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Endpoint: discovery.Endpoint(),
		Opener:   route.opener(),
	})
	if err != nil {
		return fmt.Errorf("start codex broker: %w", err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{
		Discovery:   discovery,
		Broker:      broker,
		IdleTimeout: *idle,
	})
	if err != nil {
		_ = broker.Close()
		return fmt.Errorf("publish codex broker runtime: %w", err)
	}
	fmt.Fprintln(stdout, codexBrokerReadyLine)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-host.Done():
	case <-signals:
	}
	return host.Close()
}

// runProbe reaches the runtime for this state domain, starting one when
// discovery proves none is there, and reports the content-free outcome.
//
// It exists so the runtime has an operator-reachable proof of life that does
// not require changing any product default. With --thread it additionally
// opens one exact-thread binding and waits for the snapshot that opens control
// authority, which is the smallest observation that proves the whole path.
func (c *codexBrokerCommand) runProbe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("internal codex-broker probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDomain := fs.String("state-domain", "", "absolute state domain the runtime singleton is scoped to")
	thread := fs.String("thread", "", "exact thread id to bind while probing")
	noStart := fs.Bool("no-start", false, "refuse instead of starting a runtime when none is published")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("internal codex-broker probe does not accept positional arguments")
	}
	discovery, err := c.discovery(*stateDomain)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexBrokerProbeTimeout)
	defer cancel()
	cfg := codexbroker.EnsureConfig{StartupTimeout: codexBrokerProbeTimeout}
	if !*noStart {
		cfg.Launch = c.launcher(discovery)
	}
	conn, err := codexbroker.Ensure(ctx, discovery, cfg)
	if err != nil {
		return fmt.Errorf("reach codex broker runtime: %s", codexbroker.RefusalOf(err))
	}
	defer conn.Close()
	fmt.Fprintf(stdout, "runtime=%s protocol=%d\n", conn.Runtime(), conn.Protocol())
	if strings.TrimSpace(*thread) == "" {
		return nil
	}
	binding, err := conn.Bind(ctx, *thread, "", nil)
	if err != nil {
		return fmt.Errorf("bind codex broker thread: %s", codexbroker.RefusalOf(err))
	}
	defer binding.Close()
	select {
	case event, ok := <-binding.Events():
		if !ok {
			return fmt.Errorf("bind codex broker thread: %s", binding.Revocation())
		}
		fence, authorityErr := binding.ControlAuthority()
		if authorityErr != nil {
			return fmt.Errorf("open codex broker control: %s", codexbroker.RefusalOf(authorityErr))
		}
		fmt.Fprintf(stdout, "binding origin=%s connection=%d binding=%d\n",
			event.Origin, fence.Connection, fence.Binding)
	case <-ctx.Done():
		return errors.New("codex broker binding snapshot did not arrive")
	}
	return nil
}

// launcher starts one detached runtime process for this discovery contract.
func (c *codexBrokerCommand) launcher(discovery codexbroker.Discovery) codexbroker.Launcher {
	return func(context.Context) error {
		executable := c.executable
		if executable == nil {
			executable = os.Executable
		}
		path, err := executable()
		if err != nil {
			return err
		}
		return startCodexBrokerRuntimeProcess(path, discovery)
	}
}

// discovery resolves the runtime singleton contract for this process.
func (c *codexBrokerCommand) discovery(override string) (codexbroker.Discovery, error) {
	return c.discoveryFor(override, codexbroker.DefaultEndpointKey)
}

func (c *codexBrokerCommand) discoveryFor(override string, endpoint codexbroker.EndpointKey) (codexbroker.Discovery, error) {
	domain := strings.TrimSpace(override)
	if domain == "" {
		resolved, err := c.stateDomain()
		if err != nil {
			return codexbroker.Discovery{}, err
		}
		domain = resolved
	}
	if !filepath.IsAbs(domain) {
		return codexbroker.Discovery{}, usageError("internal codex-broker requires an absolute --state-domain")
	}
	return codexBrokerDiscoveryForEndpoint(domain, endpoint)
}

func codexBrokerDiscoveryForEndpoint(domain string, endpoint codexbroker.EndpointKey) (codexbroker.Discovery, error) {
	discovery, err := codexbroker.NewDiscovery(domain, endpoint)
	if err != nil {
		return codexbroker.Discovery{}, fmt.Errorf("resolve codex broker discovery: %s", codexbroker.RefusalOf(err))
	}
	return discovery, nil
}

// stateDomain resolves this process's projmux state directory.
func (c *codexBrokerCommand) stateDomain() (string, error) {
	lookupEnv := c.lookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home := c.homeDir
	if home == nil {
		home = os.UserHomeDir
	}
	return codexBrokerStateDomain(lookupEnv, home)
}

// codexBrokerStateDomain resolves the projmux state directory the broker
// singleton is scoped to.
func codexBrokerStateDomain(lookupEnv func(string) string, home func() (string, error)) (string, error) {
	homePath, err := home()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	paths, err := config.Homes{
		HomeDir:    homePath,
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return "", fmt.Errorf("resolve projmux state paths: %w", err)
	}
	return paths.StateDir, nil
}
