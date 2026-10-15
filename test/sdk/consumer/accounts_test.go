package consumer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalAccountCatalogConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "catalog")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var registered core.ProviderAccount
	registered, err = c.RegisterAccount(ctx, core.RegisterAccountRequest{Name: "work", Provider: "codex", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if registered.AuthenticationStatus != "unverified" || registered.State != "registered" || registered.Version != 1 {
		t.Fatal("reference is not a verified login", registered)
	}
	var rows []core.ProviderAccount
	rows, err = c.ListAccounts(ctx)
	if err != nil || len(rows) != 1 || rows[0] != registered {
		t.Fatal(rows, err)
	}
	removed, err := c.RemoveAccount(ctx, core.RemoveAccountRequest{Ref: "uid:" + registered.UID})
	if err != nil || removed.State != "removed" {
		t.Fatal(removed, err)
	}
	fresh, err := c.RegisterAccount(ctx, core.RegisterAccountRequest{Name: registered.Name, Provider: registered.Provider, Root: registered.Root})
	if err != nil || fresh.UID == registered.UID {
		t.Fatal(fresh, err)
	}
	old, err := c.RemoveAccount(ctx, core.RemoveAccountRequest{Ref: "uid:" + registered.UID})
	if err != nil || old.State != "removed" || old.UID != registered.UID {
		t.Fatal(old, err)
	}
	_, err = c.RemoveAccount(ctx, core.RemoveAccountRequest{Ref: "missing"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAccountNotFound {
		t.Fatal("typed refusal", err)
	}
}
