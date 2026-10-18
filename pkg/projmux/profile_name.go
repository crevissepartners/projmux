package projmux

import "github.com/crevissepartners/projmux/internal/core/profile"

// ValidateProfileName checks the existing profile name rules and reserved names.
// It requires no Client or context and performs no environment, file, Registry,
// path or network access, writes or locking. It does not check profile existence.
// Invalid names return a *Refusal with the existing profile code, name and text.
func ValidateProfileName(name string) error {
	return profileRefusal(profile.ValidateName(name))
}
