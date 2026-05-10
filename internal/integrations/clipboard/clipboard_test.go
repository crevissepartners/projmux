package clipboard

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCopySelectsSystemClipboardByEnvironmentPriority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		goos     string
		env      map[string]string
		paths    map[string]bool
		wantName string
		wantArgs []string
	}{
		{
			name:     "wsl",
			goos:     "linux",
			env:      map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},
			paths:    map[string]bool{"clip.exe": true, "wl-copy": true, "xclip": true},
			wantName: "clip.exe",
		},
		{
			name:     "macos",
			goos:     "darwin",
			env:      map[string]string{},
			paths:    map[string]bool{"pbcopy": true},
			wantName: "pbcopy",
		},
		{
			name:     "wayland",
			goos:     "linux",
			env:      map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"},
			paths:    map[string]bool{"wl-copy": true, "xclip": true},
			wantName: "wl-copy",
		},
		{
			name:     "x11 xclip",
			goos:     "linux",
			env:      map[string]string{"DISPLAY": ":0"},
			paths:    map[string]bool{"xclip": true},
			wantName: "xclip",
			wantArgs: []string{"-selection", "clipboard"},
		},
		{
			name:     "x11 xsel",
			goos:     "linux",
			env:      map[string]string{"DISPLAY": ":0"},
			paths:    map[string]bool{"xsel": true},
			wantName: "xsel",
			wantArgs: []string{"--clipboard", "--input"},
		},
		{
			name:     "wayland unavailable continues to x11",
			goos:     "linux",
			env:      map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"},
			paths:    map[string]bool{"xsel": true},
			wantName: "xsel",
			wantArgs: []string{"--clipboard", "--input"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := &recordingRunner{}
			result, err := Copy(context.Background(), "/tmp/project", Options{
				GOOS:     tt.goos,
				Env:      mapEnv(tt.env),
				LookPath: mapLookPath(tt.paths),
				Run:      run.run,
			})
			if err != nil {
				t.Fatalf("Copy() error = %v", err)
			}
			if !result.SystemClipboard() {
				t.Fatalf("result = %#v, want system clipboard", result)
			}
			if len(run.calls) != 1 {
				t.Fatalf("calls = %#v, want one call", run.calls)
			}
			got := run.calls[0]
			if got.name != tt.wantName {
				t.Fatalf("command = %q, want %q", got.name, tt.wantName)
			}
			if !reflect.DeepEqual(got.args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", got.args, tt.wantArgs)
			}
			if got.stdin != "/tmp/project" {
				t.Fatalf("stdin = %q, want copied text", got.stdin)
			}
		})
	}
}

func TestCopyFallsBackToTmuxLoadBufferOSC52(t *testing.T) {
	t.Parallel()

	run := &recordingRunner{}
	result, err := Copy(context.Background(), "/tmp/project", Options{
		GOOS:     "linux",
		Env:      mapEnv(nil),
		LookPath: mapLookPath(nil),
		Run:      run.run,
	})
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if result.Target != TargetTmux || result.Tool != "tmux load-buffer -w" {
		t.Fatalf("result = %#v, want tmux load-buffer -w fallback", result)
	}
	if len(run.calls) != 1 {
		t.Fatalf("calls = %#v, want one tmux call", run.calls)
	}
	want := clipboardCall{name: "tmux", args: []string{"load-buffer", "-w", "-"}, stdin: "/tmp/project"}
	if !reflect.DeepEqual(run.calls[0], want) {
		t.Fatalf("call = %#v, want %#v", run.calls[0], want)
	}
}

func TestCopyFallsBackToTmuxSetBufferOSC52WhenLoadBufferFails(t *testing.T) {
	t.Parallel()

	run := &recordingRunner{
		errs: map[string]error{
			"tmux\x00load-buffer\x00-w\x00-": errors.New("no stdin"),
		},
	}
	result, err := Copy(context.Background(), "/tmp/project", Options{
		GOOS:     "linux",
		Env:      mapEnv(nil),
		LookPath: mapLookPath(nil),
		Run:      run.run,
	})
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if result.Target != TargetTmux || result.Tool != "tmux set-buffer -w" {
		t.Fatalf("result = %#v, want tmux set-buffer -w fallback", result)
	}
	if len(run.calls) != 2 {
		t.Fatalf("calls = %#v, want load-buffer then set-buffer -w", run.calls)
	}
	want := clipboardCall{name: "tmux", args: []string{"set-buffer", "-w", "/tmp/project"}}
	if !reflect.DeepEqual(run.calls[1], want) {
		t.Fatalf("second call = %#v, want %#v", run.calls[1], want)
	}
}

func TestCopyFallsBackToLegacyTmuxSetBufferWhenOSC52Fails(t *testing.T) {
	t.Parallel()

	run := &recordingRunner{
		errs: map[string]error{
			"tmux\x00load-buffer\x00-w\x00-":           errors.New("no stdin"),
			"tmux\x00set-buffer\x00-w\x00/tmp/project": errors.New("old tmux"),
		},
	}
	result, err := Copy(context.Background(), "/tmp/project", Options{
		GOOS:     "linux",
		Env:      mapEnv(nil),
		LookPath: mapLookPath(nil),
		Run:      run.run,
	})
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if result.Target != TargetTmux || result.Tool != "tmux set-buffer" {
		t.Fatalf("result = %#v, want legacy tmux set-buffer fallback", result)
	}
	if len(run.calls) != 3 {
		t.Fatalf("calls = %#v, want load-buffer -w, set-buffer -w, set-buffer", run.calls)
	}
	want := clipboardCall{name: "tmux", args: []string{"set-buffer", "/tmp/project"}}
	if !reflect.DeepEqual(run.calls[2], want) {
		t.Fatalf("third call = %#v, want %#v", run.calls[2], want)
	}
}

func TestCopyUsesTmuxFallbackWhenSystemCommandFails(t *testing.T) {
	t.Parallel()

	run := &recordingRunner{
		errs: map[string]error{
			"wl-copy": errors.New("broken compositor"),
		},
	}
	result, err := Copy(context.Background(), "/tmp/project", Options{
		GOOS:     "linux",
		Env:      mapEnv(map[string]string{"WAYLAND_DISPLAY": "wayland-0"}),
		LookPath: mapLookPath(map[string]bool{"wl-copy": true}),
		Run:      run.run,
	})
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if result.Target != TargetTmux {
		t.Fatalf("result = %#v, want tmux fallback", result)
	}
	if len(run.calls) != 2 {
		t.Fatalf("calls = %#v, want system attempt plus tmux fallback", run.calls)
	}
	if run.calls[0].name != "wl-copy" || run.calls[1].name != "tmux" {
		t.Fatalf("calls = %#v, want wl-copy then tmux", run.calls)
	}
}

type clipboardCall struct {
	name  string
	args  []string
	stdin string
}

type recordingRunner struct {
	calls []clipboardCall
	errs  map[string]error
}

func (r *recordingRunner) run(_ context.Context, name string, args []string, stdin string) ([]byte, error) {
	r.calls = append(r.calls, clipboardCall{name: name, args: append([]string(nil), args...), stdin: stdin})
	if err := r.errs[commandKey(name, args)]; err != nil {
		return nil, err
	}
	return nil, nil
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func mapLookPath(paths map[string]bool) func(string) (string, error) {
	return func(name string) (string, error) {
		if paths[name] {
			return "/mock/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func commandKey(name string, args []string) string {
	var key strings.Builder
	key.WriteString(name)
	for _, arg := range args {
		key.WriteString("\x00" + arg)
	}
	return key.String()
}
