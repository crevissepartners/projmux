package provideraccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	account "github.com/crevissepartners/projmux/internal/core/provideraccounts"
	"github.com/crevissepartners/projmux/internal/state"
)

const maxCatalogBytes = 4 << 20

// Store serializes writers on a persistent kernel lock and atomically replaces
// a private JSON catalog. Path is captured by the caller, never inferred here.
type Store struct {
	Path           string
	lockLimit      time.Duration
	afterContended func()
	beforeReplace  func() error
	replace        func(string, string) error
	syncDir        func(string) error
}
type record struct {
	Account account.ProviderAccount `json:"account"`
	Proof   rootProof               `json:"directoryProof"`
}
type catalog struct {
	Version int      `json:"version"`
	Records []record `json:"records"`
}

func (s Store) lockPath() string {
	return filepath.Join(filepath.Dir(s.Path), "."+filepath.Base(s.Path)+".lock")
}
func plainFile(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return account.Failure(account.IOFailure, "catalog", err)
	}
	if !st.Mode().IsRegular() {
		return account.Refuse(account.StoreInvalid, "catalog")
	}
	return nil
}
func (s Store) read() (catalog, error) {
	empty := catalog{Version: 1, Records: []record{}}
	if err := plainFile(s.Path); err != nil {
		return catalog{}, err
	}
	f, err := os.Open(s.Path) // #nosec G304 -- captured Projmux catalog path, never a provider credential file.
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return catalog{}, account.Failure(account.IOFailure, "catalog", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCatalogBytes+1))
	if err != nil {
		return catalog{}, account.Failure(account.IOFailure, "catalog", err)
	}
	if len(data) > maxCatalogBytes {
		return catalog{}, account.Refuse(account.StoreInvalid, "catalog")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var c catalog
	if err = d.Decode(&c); err != nil {
		return catalog{}, account.Refuse(account.StoreInvalid, "catalog")
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return catalog{}, account.Refuse(account.StoreInvalid, "catalog")
	}
	if err = validateCatalog(c); err != nil {
		return catalog{}, err
	}
	return c, nil
}
func validateCatalog(c catalog) error {
	invalid := func() error { return account.Refuse(account.StoreInvalid, "catalog") }
	if c.Version != 1 || c.Records == nil {
		return invalid()
	}
	uids := map[string]bool{}
	names := map[string]bool{}
	roots := map[string]bool{}
	for _, r := range c.Records {
		a := r.Account
		if !account.ValidUID(a.UID) || uids[a.UID] || account.ValidateRegister(account.RegisterAccountRequest{Name: a.Name, Provider: a.Provider}) != nil || !filepath.IsAbs(a.Root) || filepath.Clean(a.Root) != a.Root || a.Version != 1 || a.AuthenticationStatus != account.Unverified || (a.State != account.Registered && a.State != account.Removed) || r.Proof.Inode == 0 {
			return invalid()
		}
		uids[a.UID] = true
		if a.State == account.Registered {
			key := a.Provider + "\x00" + a.Root
			if names[a.Name] || roots[key] {
				return invalid()
			}
			names[a.Name] = true
			roots[key] = true
		}
	}
	return nil
}
func (s Store) update(ctx context.Context, change func(*catalog) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return account.Failure(account.Canceled, "request", err)
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return account.Failure(account.IOFailure, "catalog", err)
	}
	// Catalog and lock files are private. Refuse unsafe file types before opening.
	if err := plainFile(s.lockPath()); err != nil {
		return err
	}
	held, err := state.AcquireFileLockContext(ctx, s.lockPath(), s.lockLimit, s.afterContended)
	if err != nil {
		reason := account.IOFailure
		if errors.Is(err, state.ErrLockTimeout) {
			reason = account.LockTimeout
		}
		return account.Failure(reason, "lock", err)
	}
	defer held.Close() // Closing the descriptor releases its kernel flock.
	if err = os.Chmod(s.lockPath(), 0o600); err != nil {
		return account.Failure(account.IOFailure, "lock", err)
	}
	c, err := s.read()
	if err != nil {
		return err
	}
	write, err := change(&c)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return account.Failure(account.Canceled, "request", err)
	}
	if !write {
		return nil
	}
	if err = validateCatalog(c); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil || len(data)+1 > maxCatalogBytes {
		return account.Refuse(account.StoreInvalid, "catalog")
	}
	f, err := os.CreateTemp(dir, ".provider-accounts-*")
	if err != nil {
		return account.Failure(account.IOFailure, "catalog", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		return account.Failure(account.IOFailure, "catalog", err)
	}
	if s.beforeReplace != nil {
		if err = s.beforeReplace(); err != nil {
			return account.Failure(account.IOFailure, "catalog", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return account.Failure(account.Canceled, "request", err)
	}
	replace := s.replace
	if replace == nil {
		replace = os.Rename
	}
	// Admission ends at atomic replace. Cancellation after this point does not
	// interrupt durability and must never misreport a committed write as canceled.
	if err = replace(tmp, s.Path); err != nil {
		return &account.Error{Reason: account.Unknown, Field: "catalog", Cause: err}
	}
	syncDir := s.syncDir
	if syncDir == nil {
		syncDir = syncDirectory
	}
	if err = syncDir(dir); err != nil {
		return &account.Error{Reason: account.Unknown, Field: "catalog", Cause: err}
	}
	return nil
}
func syncDirectory(path string) error {
	f, err := os.Open(path) // #nosec G304 -- syncing the captured catalog parent directory only.
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
