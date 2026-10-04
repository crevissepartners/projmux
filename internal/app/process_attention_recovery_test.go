package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessAttentionDamagedStoreRecoveryPreservesBackup(t *testing.T) {
	for _, raw := range []string{`{"broken":`, `null`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			s := newProcessAttentionStore(t.TempDir())
			original := []byte(raw)
			if err := os.WriteFile(s.path, original, 0600); err != nil {
				t.Fatal(err)
			}
			// Reads remain observations: corruption must not be silently hidden.
			if _, err := s.read(); err == nil {
				t.Fatal("damaged store read succeeded")
			}
			before, _ := os.ReadFile(s.path)
			if !bytes.Equal(before, original) {
				t.Fatal("read mutated damaged store")
			}
			b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
			if err := s.activate(b, "claude", ""); err != nil {
				t.Fatal("recovery failed", err)
			}
			records, err := s.read()
			if err != nil || records[b.Pane].Binding != b {
				t.Fatal(records, err)
			}
			backups, err := filepath.Glob(s.path + ".damaged-*")
			if err != nil || len(backups) != 1 {
				t.Fatal("backup missing", backups, err)
			}
			saved, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(saved, original) {
				t.Fatal("original evidence lost", err)
			}
			info, err := os.Stat(backups[0])
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("backup permissions", info, err)
			}
			if err := s.activate(b, "claude", ""); err != nil {
				t.Fatal(err)
			}
			backups2, _ := filepath.Glob(s.path + ".damaged-*")
			if len(backups2) != 1 {
				t.Fatal("healthy store backed up", backups2)
			}
		})
	}
}

func TestProcessAttentionRecoveryDoesNotHideIOFailure(t *testing.T) {
	s := newProcessAttentionStore(t.TempDir())
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	called := false
	err := s.update(func(map[string]processAttentionRecord) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("I/O error was reset as corrupt JSON", err, called)
	}
	backups, _ := filepath.Glob(s.path + ".damaged-*")
	if len(backups) != 0 {
		t.Fatal("I/O failure backed up", backups)
	}
}

func TestProcessAttentionRecoveryPersistsResetEvenWhenChangeFails(t *testing.T) {
	s := newProcessAttentionStore(t.TempDir())
	original := []byte(`{"truncated":`)
	if err := os.WriteFile(s.path, original, 0600); err != nil {
		t.Fatal(err)
	}
	refusal := errors.New("change refused")
	if err := s.update(func(map[string]processAttentionRecord) error { return refusal }); !errors.Is(err, refusal) {
		t.Fatal(err)
	}
	records, err := s.read()
	if err != nil || len(records) != 0 {
		t.Fatal("recreated store absent", records, err)
	}
	backups, _ := filepath.Glob(s.path + ".damaged-*")
	if len(backups) != 1 {
		t.Fatal("backup missing", backups)
	}
}

// This wrapper has observations but deliberately lacks ownership validation.
type processAttentionObservationOnly struct{ handle processAttentionHost }

func (h processAttentionObservationOnly) Events(b processhost.Binding, after uint64) ([]processhost.Event, processhost.Snapshot, error) {
	return h.handle.Events(b, after)
}

func TestProcessAttentionRecoveryRequiresCurrentOwnedHost(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var handle processAttentionHost
			var b processhost.Binding
			var root string
			var invalidate func()
			if provider == "claude" {
				f := newProcessClaudeFixture(t, nil)
				f.turn(t, "initial", "ordinary")
				f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
				handle, b, root = f.handle, f.binding, f.root
				invalidate = func() {
					_, err := f.store.Update(func(r *coremetadata.Registry) error {
						p, _ := r.Pane(b.Pane)
						p.Status.Activation.Generation = "replaced-generation"
						p.Status.Activation.Process.Binding.Generation = "replaced-generation"
						p.Status.ProcessSession.Binding.Generation = "replaced-generation"
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			} else {
				f := newProcessCodexFixture(t, nil)
				f.turn(t, "initial", "ordinary")
				f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
				handle, b, root = f.endpoint.handle, f.endpoint.binding, f.root
				invalidate = func() {
					_, err := f.store.Update(func(r *coremetadata.Registry) error {
						p, _ := r.Pane(b.Pane)
						p.Status.Activation.Generation = "replaced-generation"
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			s, q, p := newProcessAttentionFixture(t, root, b, provider)
			if err := os.WriteFile(s.path, []byte(`{"damaged":`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := p.sync(processAttentionObservationOnly{handle}, b); !errors.Is(err, processhost.ErrStale) {
				t.Fatal("observation gained authority", err)
			}
			records, err := s.read()
			if err != nil || len(records) != 0 {
				t.Fatal("unverified writer recreated a record", records, err)
			}
			if err := p.sync(handle, b); err != nil {
				t.Fatal("owned host could not recover projection", err)
			}
			r := attentionRecord(t, s, b.Pane)
			if r.Binding != b || r.Provider != provider || r.Sequence == 0 {
				t.Fatal("projection not reconstructed", r)
			}
			notices, err := q.List()
			if err != nil || len(notices) != 1 {
				t.Fatal("current state notice lost", notices, err)
			}
			backups, _ := filepath.Glob(s.path + ".damaged-*")
			if len(backups) != 1 {
				t.Fatal("backup missing", backups)
			}
			// An old host still has Events but no current generation authority.
			invalidate()
			if err := os.WriteFile(s.path, []byte(`null`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := p.sync(handle, b); !errors.Is(err, processhost.ErrStale) {
				t.Fatal("stale host regained projection", err)
			}
			records, err = s.read()
			if err != nil || len(records) != 0 {
				t.Fatal("stale writer persisted state", records, err)
			}
		})
	}
}

func TestProcessAttentionBackupFailureLeavesOriginalIntact(t *testing.T) {
	// Linux and Darwin support this 250-byte source name and its 255-byte
	// lock sibling, but the backup suffix must exceed the component limit.
	// This causes a deterministic backup failure, including when run as root.
	s := &processAttentionStore{path: filepath.Join(t.TempDir(), strings.Repeat("a", 250))}
	original := []byte(`{"damaged":`)
	if err := os.WriteFile(s.path, original, 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.update(func(map[string]processAttentionRecord) error { called = true; return nil }); err == nil || called {
		t.Fatal("backup failure reached recreation/change", err, called)
	}
	after, err := os.ReadFile(s.path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("backup failure discarded original", err)
	}
	backups, _ := filepath.Glob(s.path + ".damaged-*")
	if len(backups) != 0 {
		t.Fatal("unexpected backup", backups)
	}
}
