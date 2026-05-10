package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type Target string

const (
	TargetSystem Target = "system"
	TargetTmux   Target = "tmux"
)

type Result struct {
	Target Target
	Tool   string
}

func (r Result) SystemClipboard() bool {
	return r.Target == TargetSystem
}

type Runner func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)

type Options struct {
	GOOS     string
	Env      func(string) string
	LookPath func(string) (string, error)
	Run      Runner
}

func Copy(ctx context.Context, text string, opts Options) (Result, error) {
	opts = normalizeOptions(opts)
	var errs []error
	for _, candidate := range systemCandidates(opts) {
		if _, err := opts.LookPath(candidate.Name); err != nil {
			errs = append(errs, fmt.Errorf("%s unavailable: %w", candidate.Name, err))
			continue
		}
		if _, err := opts.Run(ctx, candidate.Name, candidate.Args, text); err != nil {
			errs = append(errs, fmt.Errorf("%s failed: %w", candidate.Name, err))
			continue
		}
		return Result{Target: TargetSystem, Tool: candidate.Display}, nil
	}

	if _, err := opts.Run(ctx, "tmux", []string{"load-buffer", "-w", "-"}, text); err == nil {
		return Result{Target: TargetTmux, Tool: "tmux load-buffer -w"}, nil
	} else {
		errs = append(errs, fmt.Errorf("tmux load-buffer -w failed: %w", err))
	}
	if _, err := opts.Run(ctx, "tmux", []string{"set-buffer", "-w", text}, ""); err == nil {
		return Result{Target: TargetTmux, Tool: "tmux set-buffer -w"}, nil
	} else {
		errs = append(errs, fmt.Errorf("tmux set-buffer -w failed: %w", err))
	}
	if _, err := opts.Run(ctx, "tmux", []string{"set-buffer", text}, ""); err == nil {
		return Result{Target: TargetTmux, Tool: "tmux set-buffer"}, nil
	} else {
		errs = append(errs, fmt.Errorf("tmux set-buffer failed: %w", err))
	}

	return Result{}, errors.Join(errs...)
}

type commandCandidate struct {
	Name    string
	Args    []string
	Display string
}

func systemCandidates(opts Options) []commandCandidate {
	var candidates []commandCandidate
	if opts.Env("WSL_DISTRO_NAME") != "" || opts.Env("WSL_INTEROP") != "" {
		candidates = append(candidates, commandCandidate{Name: "clip.exe", Display: "clip.exe"})
	}
	if opts.GOOS == "darwin" {
		candidates = append(candidates, commandCandidate{Name: "pbcopy", Display: "pbcopy"})
	}
	if opts.Env("WAYLAND_DISPLAY") != "" {
		candidates = append(candidates, commandCandidate{Name: "wl-copy", Display: "wl-copy"})
	}
	if opts.Env("DISPLAY") != "" {
		candidates = append(candidates,
			commandCandidate{Name: "xclip", Args: []string{"-selection", "clipboard"}, Display: "xclip"},
			commandCandidate{Name: "xsel", Args: []string{"--clipboard", "--input"}, Display: "xsel"},
		)
	}
	return candidates
}

func normalizeOptions(opts Options) Options {
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.Env == nil {
		opts.Env = os.Getenv
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Run == nil {
		opts.Run = defaultRunner
	}
	return opts
}

func defaultRunner(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}
