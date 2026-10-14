// Package provideraccounts defines non-sensitive local provider account references.
package provideraccounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	Registered          = "registered"
	Removed             = "removed"
	Unverified          = "unverified"
	RegistrationVersion = 1
)

// ProviderAccount is a directory reference, never proof of a provider login.
type ProviderAccount struct {
	UID                  string `json:"uid"`
	Name                 string `json:"name"`
	Provider             string `json:"provider"`
	Root                 string `json:"root"`
	Version              int    `json:"version"`
	State                string `json:"state"`
	AuthenticationStatus string `json:"authenticationStatus"`
}
type RegisterAccountRequest struct{ Name, Provider, Root string }
type RemoveAccountRequest struct{ Ref string }

type Reason string

const (
	NameInvalid         Reason = "account-name-invalid"
	ProviderUnsupported Reason = "account-provider-unsupported"
	RootInvalid         Reason = "account-root-invalid"
	NameDuplicate       Reason = "account-name-duplicate"
	RootDuplicate       Reason = "account-root-duplicate"
	NotFound            Reason = "account-not-found"
	Stale               Reason = "account-stale"
	AccountRemoved      Reason = "account-removed"
	StoreInvalid        Reason = "account-store-invalid"
	LockTimeout         Reason = "account-lock-timeout"
	IOFailure           Reason = "account-io"
	Canceled            Reason = "account-canceled"
	Unknown             Reason = "account-write-unknown"
)

// Error exposes only a bounded safe reason and field, never filesystem error text.
// Unknown means a write may have committed; callers must not automatically retry.
type Error struct {
	Reason Reason
	Field  string
	Cause  error
}

func (e *Error) Error() string                 { return fmt.Sprintf("%s: %s", e.Reason, e.Field) }
func (e *Error) Unwrap() error                 { return e.Cause }
func Refuse(reason Reason, field string) error { return &Error{Reason: reason, Field: field} }
func Failure(reason Reason, field string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		reason = Canceled
	}
	return &Error{Reason: reason, Field: field, Cause: cause}
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var uidPattern = regexp.MustCompile(`^account-[0-9a-f]{32}$`)

func ValidUID(uid string) bool { return uidPattern.MatchString(uid) }
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return Refuse(NameInvalid, "name")
	}
	return nil
}
func ValidateRegister(r RegisterAccountRequest) error {
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	if r.Provider != "claude" && r.Provider != "codex" {
		return Refuse(ProviderUnsupported, "provider")
	}
	return nil
}

// ValidateRef accepts only exact active names or opaque UID references.
func ValidateRef(ref string) error {
	if strings.HasPrefix(ref, "uid:") {
		if !ValidUID(strings.TrimPrefix(ref, "uid:")) {
			return Refuse(NotFound, "ref")
		}
		return nil
	}
	return ValidateName(ref)
}
