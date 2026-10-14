package config

import "path/filepath"

// ProviderAccountsFileName is the central, non-sensitive account reference catalog.
const ProviderAccountsFileName = "provider-accounts.json"

func (p Paths) ProviderAccountsFile() string {
	return filepath.Join(p.ConfigDir, ProviderAccountsFileName)
}
