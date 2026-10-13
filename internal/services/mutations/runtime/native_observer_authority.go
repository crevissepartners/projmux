package runtime

import (
	"context"
	"errors"
	"fmt"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	state "github.com/crevissepartners/projmux/internal/state"
	"os"
	"strings"
	"sync"
	"syscall"
)

type NativeObserverIdentity struct{ AgentUID, PaneUID, RuntimeID, Generation, ThreadID string }

func ExactNativeObserverBinding(reg metadata.Registry, id NativeObserverIdentity) bool {
	a, ok := reg.Agent(id.AgentUID)
	if !ok || a.Status.Phase != metadata.PhaseRunning || a.Status.PaneRef != id.PaneUID || a.Spec.Provider != "codex" {
		return false
	}
	p, ok := reg.Pane(id.PaneUID)
	return ok && p.Metadata.OwnerUID() == id.AgentUID && p.Status.Activation.AgentUID == id.AgentUID && p.Status.Activation.Generation == id.Generation && p.Status.Activation.RuntimeID == id.RuntimeID && p.Status.Activation.Codex != nil && p.Status.Activation.Codex.ThreadID == id.ThreadID
}

// AcquireNativeAuthorityFenceAt locks a path already resolved by the explicit Pane fence adapter.
func AcquireNativeAuthorityFenceAt(path string) (func(), error) {
	// #nosec G304 -- FencePath derives a private state path from the authenticated Pane UID.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, state.PrivateFileMode)
	if err != nil {
		return nil, fmt.Errorf("open Codex authority fence: %w", err)
	}
	state.RepairPrivateFile(path)
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock Codex authority fence: %w", err)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }) }, nil
}

var ErrNativeObservationIgnored = errors.New("managed Agent observation ignored")
var ErrNativeAuthorityNotFrom = errors.New("codex lifecycle authority source changed")

// NativeObserverAuthority serializes the complete presentation triple under
// the same Pane fence used by provider-hook and observer writers.
type NativeObserverAuthority struct {
	Identity NativeObserverIdentity
	Runner   TmuxCommandRunner
	Load     func() (metadata.Registry, error)
	Acquire  func() (func(), error)
}

func (s NativeObserverAuthority) BindingCurrent() bool {
	if s.Load == nil || s.Runner == nil {
		return false
	}
	r, err := s.Load()
	if err != nil || !ExactNativeObserverBinding(r, s.Identity) {
		return false
	}
	out, err := s.Runner.Run(context.Background(), "tmux", "show-options", "-pqv", "-t", s.Identity.RuntimeID, tmuxopts.PaneUID)
	return err == nil && strings.TrimSpace(string(out)) == s.Identity.PaneUID
}
func (s NativeObserverAuthority) Set(from, source, epoch, reason string) error {
	release, err := s.Acquire()
	if err != nil {
		return err
	}
	defer release()
	if !s.BindingCurrent() {
		return ErrNativeObservationIgnored
	}
	fields := []struct{ option, value string }{{AiPaneCodexAuthorityOption, source}, {AiPaneCodexEpochOption, epoch}, {AiPaneCodexReasonOption, reason}}
	before := make(map[string]string, len(fields))
	for _, f := range fields {
		out, err := s.Runner.Run(context.Background(), "tmux", "show-options", "-pqv", "-t", s.Identity.RuntimeID, f.option)
		if err != nil {
			return err
		}
		before[f.option] = strings.TrimSpace(string(out))
	}
	if from != "" && strings.TrimSpace(before[AiPaneCodexAuthorityOption]) != from {
		return ErrNativeAuthorityNotFrom
	}
	write := func(field struct{ option, value string }) error {
		args := []string{"set-option", "-p", "-t", s.Identity.RuntimeID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", s.Identity.RuntimeID, field.option}
		}
		_, err := s.Runner.Run(context.Background(), "tmux", args...)
		return err
	}
	applied := []string{}
	for _, f := range fields {
		if err := write(f); err != nil {
			var compensation []error
			for i := len(applied) - 1; i >= 0; i-- {
				if e := write(struct{ option, value string }{applied[i], before[applied[i]]}); e != nil {
					compensation = append(compensation, fmt.Errorf("write Pane option %s: %w", applied[i], e))
				}
			}
			if joined := errors.Join(compensation...); joined != nil {
				return errors.Join(err, fmt.Errorf("restore Codex authority: %w", joined))
			}
			return err
		}
		applied = append(applied, f.option)
	}
	return nil
}
