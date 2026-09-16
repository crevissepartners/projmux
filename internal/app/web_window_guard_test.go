package app

import (
	"context"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/web"
)

// A second window create for a Project whose first is still running is
// refused, so pressing the key again while waiting does not make two windows.
func TestWebRefusesAWindowCreateWhileOneIsRunning(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	backend.runCLI = func(argv []string) (string, error) {
		if strings.Join(argv[:2], " ") == "create window" {
			calls++
			if calls == 1 {
				close(started)
				<-release
			}
			return `{"items":[{"metadata":{"uid":"win-new"}}]}`, nil
		}
		return "", nil
	}
	backend.socketPath = func(context.Context) (string, error) { return "/tmp/tmux-1000/projmux", nil }
	handler := web.New(backend, nil).Handler()
	const path = "/api/v1/projects/prj-alpha/windows"

	first := make(chan int)
	go func() {
		code, _ := webSend(t, handler, "POST", path, `{"confirm":true}`)
		first <- code
	}()
	<-started
	code, body := webSend(t, handler, "POST", path, `{"confirm":true}`)
	if code != 409 || errorCode(body) != web.CodeInProgress {
		t.Fatalf("second create = %d %v, want 409 %s", code, body, web.CodeInProgress)
	}
	close(release)
	if code := <-first; code != 201 {
		t.Fatalf("first create = %d", code)
	}
	if code, body := webSend(t, handler, "POST", path, `{"confirm":true}`); code != 201 {
		t.Fatalf("create after the first finished = %d %v", code, body)
	}
	if calls != 2 {
		t.Fatalf("create window ran %d times, want 2", calls)
	}
}
