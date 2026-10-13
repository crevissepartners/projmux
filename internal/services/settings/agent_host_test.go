package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestAgentHostServiceAdmission(t *testing.T) {
	p := config.Paths{ConfigDir: filepath.Join(t.TempDir(), "config")}
	calls := 0
	s := Service{Paths: func() (config.Paths, error) { calls++; return p, nil }}
	process := "process"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Set(ctx, Update{Key: "agent-host", DefaultHost: &process}); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancel: %v calls%d", err, calls)
	}
	for _, raw := range []string{"", "invalid"} {
		_, err := s.Set(context.Background(), Update{Key: "agent-host", DefaultHost: &raw})
		var input *InputError
		if !errors.As(err, &input) || input.Code != "config-value-invalid" || calls != 0 {
			t.Fatalf("invalid: %v calls%d", err, calls)
		}
	}
	if _, err := os.Stat(p.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusal created config")
	}
	_, err := s.Set(context.Background(), Update{Key: "unknown", DefaultHost: &process})
	var input *InputError
	if !errors.As(err, &input) || input.Code != "config-key-invalid" || calls != 0 {
		t.Fatalf("unknown: %v", err)
	}
	v, err := s.Set(context.Background(), Update{Key: "agent-host"})
	if err != nil || v.DefaultHost != "tmux" {
		t.Fatalf("nil: %+v %v", v, err)
	}
	if _, err := os.Stat(p.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nil created config")
	}
	v, err = s.Set(context.Background(), Update{Key: "agent-host", DefaultHost: &process})
	if err != nil || v.DefaultHost != "process" {
		t.Fatalf("save: %+v %v", v, err)
	}
	before, _ := os.Stat(p.AgentDefaultHostFile())
	v, err = s.Set(context.Background(), Update{Key: "agent-host"})
	after, _ := os.Stat(p.AgentDefaultHostFile())
	if err != nil || v.DefaultHost != "process" || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("nil did not preserve")
	}
	if err := os.Remove(p.AgentDefaultHostFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.AgentDefaultHostFile(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(context.Background(), Update{Key: "agent-host", DefaultHost: &process}); err == nil {
		t.Fatal("write error hidden")
	}
}

func TestAgentHostCanceledDuringPathResolution(t *testing.T) {
	p := config.Paths{ConfigDir: filepath.Join(t.TempDir(), "config")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := Service{Paths: func() (config.Paths, error) { cancel(); return p, nil }}
	host := "process"
	if _, err := s.Set(ctx, Update{Key: "agent-host", DefaultHost: &host}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled before write created config")
	}
}
