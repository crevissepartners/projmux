package provideraccounts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	account "github.com/crevissepartners/projmux/internal/core/provideraccounts"
)

func fixtureService(t *testing.T) (Service, config.Paths) {
	t.Helper()
	p := config.Paths{ConfigDir: filepath.Join(t.TempDir(), "config")}
	return Service{Paths: func() (config.Paths, error) { return p, nil }}, p
}
func requireReason(t *testing.T, err error, reason account.Reason) {
	t.Helper()
	var e *account.Error
	if !errors.As(err, &e) || e.Reason != reason {
		t.Fatalf("reason = %T %v, want %s", err, err, reason)
	}
}
func TestCatalogCanonicalDuplicatesTombstonesAndCopies(t *testing.T) {
	ctx := context.Background()
	s, p := fixtureService(t)
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	// Root contents are inert; registration reads no file below the directory.
	if err := os.Mkdir(filepath.Join(root, "auth.json"), 0o000); err != nil {
		t.Fatal(err)
	}
	request := account.RegisterAccountRequest{Name: "work", Provider: "codex", Root: alias}
	a, err := s.Register(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "changed"
	request.Root = "/elsewhere"
	canonical, _ := filepath.EvalSymlinks(root)
	if a.Root != canonical || a.Version != 1 || a.State != account.Registered || a.AuthenticationStatus != account.Unverified {
		t.Fatalf("account %+v", a)
	}
	_, err = s.Register(ctx, account.RegisterAccountRequest{Name: "work", Provider: "claude", Root: t.TempDir()})
	requireReason(t, err, account.NameDuplicate)
	_, err = s.Register(ctx, account.RegisterAccountRequest{Name: "alias", Provider: "codex", Root: root})
	requireReason(t, err, account.RootDuplicate)
	// Same root under another provider is an independent reference.
	b, err := s.Register(ctx, account.RegisterAccountRequest{Name: "other", Provider: "claude", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list %v %v", rows, err)
	}
	rows[0].Root = "changed"
	other := Service{Paths: s.Paths}
	again, err := other.List(ctx)
	if err != nil || again[0].Root != a.Root {
		t.Fatal("aliased list")
	}
	gone, err := s.Remove(ctx, account.RemoveAccountRequest{Ref: "work"})
	if err != nil || gone.State != account.Removed || gone.UID != a.UID {
		t.Fatalf("remove %v %v", gone, err)
	}
	fresh, err := s.Register(ctx, account.RegisterAccountRequest{Name: "work", Provider: "codex", Root: root})
	if err != nil || fresh.UID == a.UID {
		t.Fatalf("fresh %v %v", fresh, err)
	}
	gone, err = other.Remove(ctx, account.RemoveAccountRequest{Ref: "uid:" + a.UID})
	if err != nil || gone.State != account.Removed {
		t.Fatalf("idempotent %v %v", gone, err)
	}
	rows, err = s.List(ctx)
	if err != nil || len(rows) != 2 || rows[0].UID != b.UID || rows[1].UID != fresh.UID {
		t.Fatalf("active %v %v", rows, err)
	}
	c, err := (Store{Path: p.ProviderAccountsFile()}).read()
	if err != nil || len(c.Records) != 3 || c.Records[0].Account.State != account.Removed {
		t.Fatal("tombstone lost", err)
	}
	data, err := os.ReadFile(p.ProviderAccountsFile())
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if json.Unmarshal(data, &stored) != nil {
		t.Fatal("invalid JSON")
	}
}
func TestCatalogInvalidInputAndCorruptionHaveNoEffects(t *testing.T) {
	ctx := context.Background()
	s, p := fixtureService(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("inert"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range []account.RegisterAccountRequest{{Name: "../bad", Provider: "codex", Root: t.TempDir()}, {Name: "work", Provider: "other", Root: t.TempDir()}, {Name: "work", Provider: "codex", Root: "relative"}, {Name: "work", Provider: "codex", Root: file}, {Name: "work", Provider: "codex", Root: file + "-missing"}} {
		if _, err := s.Register(ctx, r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
		if _, err := os.Stat(p.ConfigDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid input effect %v", err)
		}
	}
	_, err := s.Remove(ctx, account.RemoveAccountRequest{Ref: "unknown"})
	requireReason(t, err, account.NotFound)
	if err = os.WriteFile(p.ProviderAccountsFile(), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.List(ctx)
	requireReason(t, err, account.StoreInvalid)
	_, err = s.Register(ctx, account.RegisterAccountRequest{Name: "work", Provider: "codex", Root: t.TempDir()})
	requireReason(t, err, account.StoreInvalid)
	_, err = s.Remove(ctx, account.RemoveAccountRequest{Ref: "unknown"})
	requireReason(t, err, account.StoreInvalid)
	data, _ := os.ReadFile(p.ProviderAccountsFile())
	if string(data) != "broken" {
		t.Fatal("corruption overwritten")
	}
}
func TestCanceledCatalogDoesNotCreateStore(t *testing.T) {
	s, p := fixtureService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Register(ctx, account.RegisterAccountRequest{Name: "work", Provider: "codex", Root: t.TempDir()})
	requireReason(t, err, account.Canceled)
	_, err = s.Remove(ctx, account.RemoveAccountRequest{Ref: "work"})
	requireReason(t, err, account.Canceled)
	_, err = s.List(ctx)
	requireReason(t, err, account.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("context identity lost")
	}
	if _, err = os.Stat(p.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled effect %v", err)
	}
}
