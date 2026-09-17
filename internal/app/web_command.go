package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/web"
)

const defaultWebAddr = "127.0.0.1:8787"

// webCommand runs `projmux web`: the HTTP API and browser client.
type webCommand struct {
	serve     func(ctx context.Context, backend web.Backend, opts web.Options) error
	newToken  func() (string, error)
	backend   func() web.Backend
	paths     func() (config.Paths, error)
	unsetenv  func(string) error
	notifyCtx func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc)
}

func newWebCommand() *webCommand {
	return &webCommand{
		serve:     web.Serve,
		newToken:  web.NewToken,
		backend:   func() web.Backend { return newWebBackend() },
		paths:     config.DefaultPathsFromEnv,
		unsetenv:  os.Unsetenv,
		notifyCtx: signal.NotifyContext,
	}
}

func (c *webCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", defaultWebAddr, "loopback TCP address to serve on; empty disables TCP")
	socket := fs.String("socket", "", "unix socket path (default <state>/web/api.sock); \"-\" disables the socket")
	verbose := fs.Bool("v", false, "log every request")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("web does not accept positional arguments; got %q", fs.Arg(0)))
	}
	if *addr != "" {
		if err := requireLoopback(*addr); err != nil {
			return usageError(err.Error())
		}
	}
	socketPath := *socket
	switch socketPath {
	case "-":
		socketPath = ""
	case "":
		paths, err := c.paths()
		if err != nil {
			return fmt.Errorf("web: resolve state paths: %w", err)
		}
		socketPath = filepath.Join(paths.StateDir, "web", "api.sock")
	}
	if *addr == "" && socketPath == "" {
		return usageError("web: --addr and --socket cannot both be disabled")
	}

	// Every route names its targets. The CLI falls back to $TMUX and
	// $TMUX_PANE for implicit ones, and a server launched from a pane would
	// otherwise aim requests at that pane.
	for _, name := range []string{"TMUX", "TMUX_PANE"} {
		if err := c.unsetenv(name); err != nil {
			return fmt.Errorf("web: clear %s: %w", name, err)
		}
	}

	// The token exists only in this process and in the one line below that
	// hands it to the operator.
	token := ""
	if *addr != "" {
		generated, err := c.newToken()
		if err != nil {
			return err
		}
		token = generated
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := c.notifyCtx(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return c.serve(ctx, c.backend(), web.Options{
		Addr:       *addr,
		SocketPath: socketPath,
		Token:      token,
		Log:        log,
		Ready: func(bound string) {
			if bound != "" {
				_, _ = fmt.Fprintf(stdout, "projmux web: http://%s/?token=%s\n", bound, token)
			}
			if socketPath != "" {
				_, _ = fmt.Fprintf(stdout, "projmux web: unix:%s\n", socketPath)
			}
		},
	})
}

// requireLoopback refuses any TCP address that is not loopback. The start
// token keeps other local programs out, but it travels in cleartext over plain
// http, so the listener must still never leave the machine.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("web: --addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("web: --addr %q is not a loopback address; the API is plain http and must stay on this machine", addr)
	}
	return nil
}
