package projmux

import (
	"context"
	"errors"
	account "github.com/crevissepartners/projmux/internal/core/provideraccounts"
	"github.com/crevissepartners/projmux/internal/services/provideraccounts"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeAccountNameInvalid         Code = "account-name-invalid"
	CodeAccountProviderUnsupported Code = "account-provider-unsupported"
	CodeAccountRootInvalid         Code = "account-root-invalid"
	CodeAccountNameDuplicate       Code = "account-name-duplicate"
	CodeAccountRootDuplicate       Code = "account-root-duplicate"
	CodeAccountNotFound            Code = "account-not-found"
	CodeAccountStale               Code = "account-stale"
	CodeAccountRemoved             Code = "account-removed"
	CodeAccountStoreInvalid        Code = "account-store-invalid"
	CodeAccountLockTimeout         Code = "account-lock-timeout"
	CodeAccountIO                  Code = "account-io"
	CodeAccountCanceled            Code = "account-canceled"
	CodeAccountWriteUnknown        Code = "account-write-unknown"
)

// RegisterAccount stores a directory reference without reading credentials or
// starting a provider. Names are globally unique among active registrations.
func (c *Client) RegisterAccount(ctx context.Context, r core.RegisterAccountRequest) (core.ProviderAccount, error) {
	v, err := (provideraccounts.Service{Paths: c.instructions.Paths}).Register(ctx, account.RegisterAccountRequest(r))
	return core.ProviderAccount(v), accountRefusal(err)
}

// ListAccounts returns caller-owned active references from the captured ConfigDir.
func (c *Client) ListAccounts(ctx context.Context) ([]core.ProviderAccount, error) {
	v, err := (provideraccounts.Service{Paths: c.instructions.Paths}).List(ctx)
	if err != nil {
		return nil, accountRefusal(err)
	}
	out := make([]core.ProviderAccount, len(v))
	for i := range v {
		out[i] = core.ProviderAccount(v[i])
	}
	return out, nil
}

// RemoveAccount tombstones an exact name or uid: reference. Removing an already
// removed UID is idempotent; re-registering a name always creates a fresh UID.
func (c *Client) RemoveAccount(ctx context.Context, r core.RemoveAccountRequest) (core.ProviderAccount, error) {
	v, err := (provideraccounts.Service{Paths: c.instructions.Paths}).Remove(ctx, account.RemoveAccountRequest(r))
	return core.ProviderAccount(v), accountRefusal(err)
}
func accountRefusal(err error) error {
	if err == nil {
		return nil
	}
	var e *account.Error
	if errors.As(err, &e) {
		return &Refusal{Code: Code(e.Reason), Field: e.Field, Reason: string(e.Reason), message: e.Error(), cause: err}
	}
	return err
}
