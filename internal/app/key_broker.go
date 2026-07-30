package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/platformkeys"
)

const (
	keyBrokerClientFormat = "#{client_name}\t#{client_flags}"
)

type keyBrokerCommand struct {
	source      platformkeys.Source
	runner      tmuxRunner
	homeDir     func() (string, error)
	lookupEnv   func(string) string
	readFile    func(string) ([]byte, error)
	writeFile   func(string, []byte, os.FileMode) error
	nativeKeys  func() bool
	pollEvery   time.Duration
	startupWait time.Duration
}

func newKeyBrokerCommand() *keyBrokerCommand {
	return &keyBrokerCommand{
		source:      platformkeys.NewSource(),
		runner:      shellTmuxExecRunner{},
		homeDir:     os.UserHomeDir,
		lookupEnv:   os.Getenv,
		readFile:    os.ReadFile,
		writeFile:   os.WriteFile,
		nativeKeys:  platformkeys.Available,
		pollEvery:   300 * time.Millisecond,
		startupWait: 10 * time.Second,
	}
}

func (c *keyBrokerCommand) Run(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("key-broker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", defaultAppSocket, "tmux socket name")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return usageError("key-broker does not accept positional arguments")
	}
	if !nativeKeysEnabled(c.lookupEnv, c.homeDir) {
		return nil
	}
	available := c.nativeKeys
	if available == nil {
		available = platformkeys.Available
	}
	if !available() || c.source == nil {
		return nil
	}
	socketName := nonEmpty(strings.TrimSpace(*socket), defaultAppSocket)
	release, acquired, err := platformkeys.AcquireLease(socketName)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer release()

	bindings, err := c.loadBindings()
	if err != nil {
		return err
	}
	if err := c.source.Replace(bindings); err != nil {
		return fmt.Errorf("configure native key source: %w", err)
	}
	c.source.SetEnabled(false)
	showNativeKeysConsentHint(stderr, c.lookupEnv, c.homeDir, c.readFile, c.writeFile)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sourceErr := make(chan error, 1)
	go func() {
		permissionNotice := false
		for {
			err := c.source.Run(ctx)
			if !errors.Is(err, platformkeys.ErrPermissionRequired) {
				sourceErr <- err
				return
			}
			if !permissionNotice {
				fmt.Fprintln(stderr, "projmux native keybindings are waiting for macOS Accessibility approval")
				permissionNotice = true
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				sourceErr <- nil
				return
			case <-timer.C:
			}
		}
	}()

	pollEvery := c.pollEvery
	if pollEvery <= 0 {
		pollEvery = 300 * time.Millisecond
	}
	startupWait := c.startupWait
	if startupWait <= 0 {
		startupWait = 10 * time.Second
	}
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	started := time.Now()
	focusedClient := ""
	sawServer := false

	for {
		select {
		case <-ctx.Done():
			c.source.SetEnabled(false)
			return nil
		case err := <-sourceErr:
			c.source.SetEnabled(false)
			return err
		case chord := <-c.source.Events():
			if focusedClient == "" || strings.TrimSpace(chord) == "" {
				continue
			}
			if err := c.sendChord(ctx, socketName, focusedClient, chord); err != nil {
				// Client focus can change between the inventory poll and this
				// dispatch. Disable capture until the next successful poll so
				// the original key is never swallowed repeatedly.
				focusedClient = ""
				c.source.SetEnabled(false)
			}
		case <-ticker.C:
			updated, err := c.loadBindings()
			if err == nil && !reflect.DeepEqual(updated, bindings) {
				if err := c.source.Replace(updated); err == nil {
					bindings = updated
				}
			}

			client, serverAvailable, err := c.focusedClient(ctx, socketName)
			if err != nil {
				focusedClient = ""
				c.source.SetEnabled(false)
				continue
			}
			if !serverAvailable {
				focusedClient = ""
				c.source.SetEnabled(false)
				if sawServer || time.Since(started) >= startupWait {
					return nil
				}
				continue
			}
			sawServer = true
			focusedClient = client
			c.source.SetEnabled(focusedClient != "")
		}
	}
}

func (c *keyBrokerCommand) loadBindings() ([]platformkeys.Binding, error) {
	catalog, _, err := loadMergedKeyBindingCatalog(keymapLoader{
		homeDir:   c.homeDir,
		lookupEnv: c.lookupEnv,
		readFile:  c.readFile,
	})
	if err != nil {
		return nil, err
	}
	var chords []string
	for _, action := range catalog {
		if action.Kind == keyBindingActionPickerInternal {
			continue
		}
		chords = append(chords, keyBindingEffectivePlainChords(action)...)
	}
	return platformkeys.ParseBindings(chords), nil
}

func (c *keyBrokerCommand) focusedClient(ctx context.Context, socket string) (string, bool, error) {
	if c.runner == nil {
		return "", false, errors.New("native key broker tmux runner is not configured")
	}
	out, err := c.runner.Run(ctx, "tmux", "-L", socket, "list-clients", "-F", keyBrokerClientFormat)
	if err != nil {
		if isNoServerLikeError(err) {
			return "", false, nil
		}
		return "", false, err
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		name, flags, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		for flag := range strings.SplitSeq(flags, ",") {
			if strings.TrimSpace(flag) == "focused" {
				return strings.TrimSpace(name), true, nil
			}
		}
	}
	return "", true, nil
}

func (c *keyBrokerCommand) sendChord(ctx context.Context, socket, client, chord string) error {
	if c.runner == nil {
		return errors.New("native key broker tmux runner is not configured")
	}
	_, err := c.runner.Run(ctx, "tmux", "-L", socket, "send-keys", "-K", "-c", client, chord)
	return err
}
