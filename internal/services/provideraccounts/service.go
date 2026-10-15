// Package provideraccounts implements the shared CLI/SDK account catalog.
package provideraccounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	account "github.com/crevissepartners/projmux/internal/core/provideraccounts"
	"golang.org/x/sys/unix"
)

type Service struct{ Paths func() (config.Paths, error) }

func (s Service) store() (Store, error) {
	p, err := s.Paths()
	if err != nil {
		return Store{}, account.Failure(account.IOFailure, "catalog", err)
	}
	if !filepath.IsAbs(p.ConfigDir) {
		return Store{}, account.Refuse(account.StoreInvalid, "catalog")
	}
	return Store{Path: p.ProviderAccountsFile()}, nil
}
func (s Service) Register(ctx context.Context, r account.RegisterAccountRequest) (account.ProviderAccount, error) {
	if err := ctx.Err(); err != nil {
		return account.ProviderAccount{}, account.Failure(account.Canceled, "request", err)
	}
	if err := account.ValidateRegister(r); err != nil {
		return account.ProviderAccount{}, err
	}
	if !filepath.IsAbs(r.Root) {
		return account.ProviderAccount{}, account.Refuse(account.RootInvalid, "root")
	}
	root, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		return account.ProviderAccount{}, account.Refuse(account.RootInvalid, "root")
	}
	root = filepath.Clean(root)
	proof, err := directoryProof(root)
	if err != nil {
		return account.ProviderAccount{}, err
	}
	store, err := s.store()
	if err != nil {
		return account.ProviderAccount{}, err
	}
	var out account.ProviderAccount
	err = store.update(ctx, func(c *catalog) (bool, error) {
		for _, v := range c.Records {
			if v.Account.State != account.Registered {
				continue
			}
			if v.Account.Name == r.Name {
				return false, account.Refuse(account.NameDuplicate, "name")
			}
			if v.Account.Provider == r.Provider && v.Account.Root == root {
				return false, account.Refuse(account.RootDuplicate, "root")
			}
		}
		// Recheck only directory metadata under the catalog lock.
		current, e := directoryProof(root)
		if e != nil {
			return false, e
		}
		if current != proof {
			return false, account.Refuse(account.Stale, "root")
		}
		var b [16]byte
		if _, e = rand.Read(b[:]); e != nil {
			return false, account.Failure(account.IOFailure, "uid", e)
		}
		uid := "account-" + hex.EncodeToString(b[:])
		for _, v := range c.Records {
			if v.Account.UID == uid {
				return false, account.Refuse(account.IOFailure, "uid")
			}
		}
		out = account.ProviderAccount{UID: uid, Name: r.Name, Provider: r.Provider, Root: root, Version: account.RegistrationVersion, State: account.Registered, AuthenticationStatus: account.Unverified}
		c.Records = append(c.Records, record{Account: out, Proof: proof})
		return true, nil
	})
	if err != nil {
		return account.ProviderAccount{}, err
	}
	return out, nil
}
func (s Service) List(ctx context.Context) ([]account.ProviderAccount, error) {
	if err := ctx.Err(); err != nil {
		return nil, account.Failure(account.Canceled, "request", err)
	}
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	c, err := store.read()
	if err != nil {
		return nil, err
	}
	out := make([]account.ProviderAccount, 0, len(c.Records))
	for _, v := range c.Records {
		if v.Account.State == account.Registered {
			out = append(out, v.Account)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, account.Failure(account.Canceled, "request", err)
	}
	return out, nil
}
func (s Service) Remove(ctx context.Context, r account.RemoveAccountRequest) (account.ProviderAccount, error) {
	if err := ctx.Err(); err != nil {
		return account.ProviderAccount{}, account.Failure(account.Canceled, "request", err)
	}
	if err := account.ValidateRef(r.Ref); err != nil {
		return account.ProviderAccount{}, err
	}
	store, err := s.store()
	if err != nil {
		return account.ProviderAccount{}, err
	}
	var out account.ProviderAccount
	err = store.update(ctx, func(c *catalog) (bool, error) {
		for i := range c.Records {
			v := &c.Records[i]
			match := r.Ref == "uid:"+v.Account.UID || (!strings.HasPrefix(r.Ref, "uid:") && r.Ref == v.Account.Name && v.Account.State == account.Registered)
			if !match {
				continue
			}
			out = v.Account
			if out.State == account.Removed {
				return false, nil
			}
			v.Account.State = account.Removed
			out = v.Account
			return true, nil
		}
		return false, account.Refuse(account.NotFound, "ref")
	})
	if err != nil {
		return account.ProviderAccount{}, err
	}
	return out, nil
}

// rootProof is directory metadata only, not an authentication identity.
type rootProof struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func directoryProof(root string) (rootProof, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return rootProof{}, account.Refuse(account.RootInvalid, "root")
	}
	var st unix.Stat_t
	if err = unix.Stat(root, &st); err != nil {
		return rootProof{}, account.Refuse(account.RootInvalid, "root")
	}
	return rootProof{Device: uint64(st.Dev), Inode: uint64(st.Ino)}, nil
}
