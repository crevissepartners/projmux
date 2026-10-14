package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSDKAccountCatalogCapturedPathsAndCopies(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "catalog")
	options := Options{ConfigDir: dir}
	first, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	options.ConfigDir = filepath.Join(t.TempDir(), "elsewhere")
	request := core.RegisterAccountRequest{Name: "work", Provider: "claude", Root: t.TempDir()}
	a, err := first.RegisterAccount(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "changed"
	request.Root = "/other"
	rows, err := second.ListAccounts(ctx)
	if err != nil || len(rows) != 1 || rows[0] != a {
		t.Fatalf("list %+v %v", rows, err)
	}
	rows[0].Name = "changed"
	again, err := first.ListAccounts(ctx)
	if err != nil || again[0] != a {
		t.Fatal("return alias")
	}
	// A separate captured ConfigDir stays independent without changing globals.
	third, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := third.ListAccounts(ctx)
	if err != nil || len(empty) != 0 || empty == nil {
		t.Fatal("cross-client alias", empty, err)
	}
	removed, err := second.RemoveAccount(ctx, core.RemoveAccountRequest{Ref: a.Name})
	if err != nil || removed.State != "removed" {
		t.Fatal(removed, err)
	}
	b, err := first.RegisterAccount(ctx, core.RegisterAccountRequest{Name: a.Name, Provider: a.Provider, Root: a.Root})
	if err != nil || b.UID == a.UID {
		t.Fatal("UID reused", err)
	}
	old, err := second.RemoveAccount(ctx, core.RemoveAccountRequest{Ref: "uid:" + a.UID})
	if err != nil || old.UID != a.UID || old.State != "removed" {
		t.Fatal("old ref migrated", old, err)
	}
	_, err = first.RegisterAccount(ctx, core.RegisterAccountRequest{Name: a.Name, Provider: "codex", Root: t.TempDir()})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeAccountNameDuplicate {
		t.Fatal("duplicate refusal", err)
	}
	data, _ := json.Marshal(a)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	if len(fields) != 7 || fields["authenticationStatus"] != "unverified" {
		t.Fatal("public auth/proof leak", string(data))
	}
}
func TestSDKAccountTypedFailuresAndCancellation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "catalog")
	c, err := NewClient(Options{ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.ListAccounts(ctx)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeAccountCanceled || !errors.Is(err, context.Canceled) {
		t.Fatal("typed cancel", err)
	}
	_, err = c.RemoveAccount(context.Background(), core.RemoveAccountRequest{Ref: "missing"})
	if !errors.As(err, &refusal) || refusal.Code != CodeAccountNotFound {
		t.Fatal("typed missing", err)
	}
	if err = os.WriteFile(filepath.Join(dir, "provider-accounts.json"), []byte("corrupt private data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = c.ListAccounts(context.Background())
	if !errors.As(err, &refusal) || refusal.Code != CodeAccountStoreInvalid {
		t.Fatal("typed store", err)
	}
}
