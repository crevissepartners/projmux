package processhost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestClaudeFirstInputLargerThanActualPipe(t *testing.T) {
	for _, order := range []string{"hook-first", "write-first"} {
		for _, escaped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/escaped=%t", order, escaped), func(t *testing.T) {
				fifo := filepath.Join(t.TempDir(), "stdin-gate")
				if err := unix.Mkfifo(fifo, 0600); err != nil {
					t.Fatal(err)
				}
				gate, err := os.OpenFile(fifo, os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Close()
				h := testHost(t, func(_ *Transactions, l *Limits) { l.Write = time.Second })
				cmd := fixtureCommand("first-input-barrier")
				cmd.Env = append(cmd.Env, "PROCESSHOST_FIRST_INPUT_GATE="+fifo)
				p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: cmd})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					_ = p.Stop(binding())
					ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
					defer c()
					if _, err := p.Wait(ctx, binding()); err != nil {
						t.Error(err)
					}
				}()
				capacity, err := unix.FcntlInt(p.stdin.Fd(), unix.F_GETPIPE_SZ, 0)
				if err != nil {
					t.Fatal(err)
				}
				prompt := strings.Repeat("a", capacity+(64<<10))
				if escaped {
					prompt = strings.Repeat("<", (capacity+(64<<10))/6+1)
				}
				a := authority(p)
				bind := func() {
					s, err := p.Observe(binding())
					if err != nil {
						t.Fatal(err)
					}
					if err = p.BindClaudeHook(context.Background(), binding(), s.PID, "session"); err != nil {
						t.Fatal(err)
					}
				}
				if order == "hook-first" {
					bind()
					a.Session = "session"
				}
				done := make(chan error, 1)
				go func() { done <- p.Turn(context.Background(), a, "first", prompt) }()
				timer := time.After(2 * time.Second)
				for {
					unread, err := unix.IoctlGetInt(int(p.stdin.Fd()), unix.TIOCINQ)
					if err != nil {
						t.Fatal(err)
					}
					if unread == capacity {
						t.Logf("actual pipe capacity=%d payload=%d full unread pipe during first write", capacity, len(prompt))
						break
					}
					select {
					case err := <-done:
						t.Fatalf("write ended before release %v", err)
					case <-timer:
						t.Fatal("pipe never filled")
					case <-time.After(time.Millisecond):
					}
				}
				if order == "write-first" {
					bind()
				}
				s, err := p.Observe(binding())
				if err != nil || s.Session != "" || s.State != "starting" {
					t.Fatalf("hook promoted stream %+v %v", s, err)
				}
				if err = p.CheckClaudeHook(context.Background(), binding(), s.PID, "session"); err != nil {
					t.Fatal(err)
				}
				if _, err = gate.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
					if err != nil {
						t.Fatal("large first frame", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("write did not complete")
				}
				s = observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
				if s.Failure != "" || s.Session != "session" {
					t.Fatal(s)
				}
				events, _, _ := p.Events(binding(), 0)
				results := 0
				for _, e := range events {
					if e.Kind == "turn-result" {
						results++
					}
				}
				if results != 1 {
					t.Fatalf("first frame consumption count=%d", results)
				}
				if err = p.Turn(context.Background(), authority(p), "first", prompt); err != ErrStale {
					t.Fatal("first operation replay", err)
				}
			})
		}
	}
}
