// Command managed-preflight verifies the real official manager lifecycle of
// explicitly mounted releases before an installed client conformance run. It
// runs only in a private PID/mount/network namespace with an init reaper.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

type input struct {
	Root           string            `json:"root"`
	Releases       []string          `json:"releases"`
	HostNamespaces map[string]string `json:"hostNamespaces"`
}

type receipt struct {
	Result         string                              `json:"result"`
	Proofs         []codexinstalled.ManagedDaemonProof `json:"proofs"`
	ProcessesAfter []codexinstalled.OwnedProcess       `json:"processesAfter"`
	Cleanup        bool                                `json:"cleanup"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return errors.New("managed-preflight requires one explicit JSON input file")
	}
	raw, err := codexinstalled.ReadExplicitInput(os.Args[1])
	if err != nil {
		return err
	}
	var options input
	if json.Unmarshal(raw, &options) != nil || !strings.HasPrefix(options.Root, "/tmp/") || len(options.Releases) == 0 {
		return errors.New("private root and explicit mounted releases required")
	}
	isolation := codexinstalled.ManagerIsolation{HostNamespaces: options.HostNamespaces}
	if _, err := isolation.Verify(); err != nil {
		return err
	}
	if err := os.Setenv("PATH", filepath.Join(options.Releases[0], "bin")+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return err
	}
	fixture, err := codexinstalled.NewClean(options.Root)
	if err != nil {
		return err
	}
	for key, leaf := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_RUNTIME_DIR": "runtime", "TMUX_TMPDIR": "tmux"} {
		path := filepath.Join(options.Root, leaf)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if err := os.Setenv(key, path); err != nil {
			return err
		}
	}
	result := receipt{Result: "FAIL"}
	defer func() { _ = json.NewEncoder(os.Stdout).Encode(result) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, release := range options.Releases {
		if err := fixture.SelectManagedRelease(release); err != nil {
			return err
		}
		var envErr error
		fixture.ApplyEnv(func(key, value string) { envErr = errors.Join(envErr, os.Setenv(key, value)) })
		if envErr != nil {
			return envErr
		}
		daemon, err := fixture.StartManagedRecovery(ctx, isolation)
		if err != nil {
			return err
		}
		result.Proofs = append(result.Proofs, daemon.Proof)
		if err := daemon.Stop(ctx); err != nil {
			return err
		}
	}
	result.ProcessesAfter, err = fixture.OwnedProcesses()
	if err != nil {
		return err
	}
	if len(result.ProcessesAfter) != 0 {
		return errors.New("private managed processes remain after official stops")
	}
	if err := fixture.Cleanup(); err != nil {
		return err
	}
	if err := os.RemoveAll(options.Root); err != nil {
		return err
	}
	result.Result = "PASS"
	result.Cleanup = true
	return nil
}
