package app

import (
	"context"
	"encoding/json"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"testing"
)

func TestProcessSupervisorRefusesSelectorsBeforeDescriptors(t *testing.T) {
	command := &processHostSupervisorCommand{}
	for _, args := range [][]string{{"--pid", "1"}, {"--help"}, {"agent"}} {
		if err := command.Run(args, io.Discard, io.Discard); err == nil {
			t.Fatalf("selector admitted: %v", args)
		}
	}
}

// This copied test owner invokes the real product's inherited-FD entry. Its
// death closes ownership pipes; it never finds or adopts a provider by PID.
func runProcessSupervisorOwnerFixture() error {
	ctx := context.Background()
	current := func(ctx context.Context, _ processhost.Binding) error { return ctx.Err() }
	limits := processhost.DefaultLimits()
	limits.Grace = 100 * time.Millisecond
	host, err := processhost.NewHost("supervisor-fixture", processhost.Command{Path: os.Getenv("PMX_TEST_CLI"), Args: []string{"internal", "process-host-supervisor"}, Env: os.Environ()}, processhost.Transactions{Reserve: current, Current: current, Commit: func(ctx context.Context, b processhost.Binding, _ string) error { return current(ctx, b) }}, limits)
	if err != nil {
		return err
	}
	binding := processhost.Binding{Host: "supervisor-fixture", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
	handle, err := host.Start(ctx, processhost.Launch{Binding: binding, Command: processhost.Command{Path: "python3", Args: []string{"-u", "-c", "import sys; sys.stdin.read()"}, Env: os.Environ()}})
	if err != nil {
		return err
	}
	snapshot, err := handle.Observe(binding)
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := json.NewEncoder(os.Stdout).Encode(snapshot); err != nil {
		return err
	}
	eof := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(eof) }()
	select {
	case <-eof:
	case <-signals:
	}
	if err := handle.Stop(binding); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	snapshot, err = handle.Wait(wait, binding)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(snapshot)
}

func TestProcessSupervisorActualCLIIsolated(t *testing.T) {
	product := os.Getenv("PMX_TEST_CLI")
	if product == "" {
		t.Skip("set PMX_TEST_CLI to an isolated copied product binary")
	}
	for _, mode := range []string{"EOF", "INT", "TERM", "KILL"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", "/tmp")
			root := t.TempDir()
			t.Logf("isolated provider HOME=%s", root)
			binary := filepath.Join(root, "owner.test")
			raw, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binary, raw, 0700); err != nil {
				t.Fatal(err)
			}
			context, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(context, binary, "internal", "process-host-owner-fixture")
			env := []string{}
			for _, value := range os.Environ() {
				key := value[:strings.IndexByte(value, '=')]
				if key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") || key == "HOME" || strings.HasPrefix(key, "XDG_") {
					continue
				}
				env = append(env, value)
			}
			command.Env = append(env, "HOME="+root, "XDG_CONFIG_HOME="+root+"/config", "XDG_STATE_HOME="+root+"/state", "XDG_CACHE_HOME="+root+"/cache", "PMX_TEST_PROCESS_CLAUDE_CHILD=1")
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				_ = input.Close()
				if !waited {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			decoder := json.NewDecoder(output)
			var first processhost.Snapshot
			if err := decoder.Decode(&first); err != nil {
				t.Fatal(err)
			}
			child, _, err := localipc.Process(first.PID)
			if err != nil {
				t.Fatal(err)
			}
			supervisor, _, err := localipc.Process(first.SupervisorPID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "EOF" {
				_ = input.Close()
			} else {
				selected := map[string]os.Signal{"INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL}[mode]
				if err := command.Process.Signal(selected); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "KILL" {
				var last processhost.Snapshot
				if err := decoder.Decode(&last); err != nil || last.Exit == nil || last.State != "exited" {
					t.Fatalf("actual Wait receipt: %+v %v", last, err)
				}
			}
			err = command.Wait()
			waited = true
			if mode != "KILL" && err != nil {
				t.Fatal(err)
			}
			for _, identity := range []coremetadata.ProcessIdentity{child, supervisor} {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					current, _, err := localipc.Process(identity.PID)
					if err != nil || current != identity {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if current, _, err := localipc.Process(identity.PID); err == nil && current == identity {
					t.Fatalf("owned process remains: %+v", identity)
				}
			}
		})
	}
}
