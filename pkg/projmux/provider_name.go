package projmux

import metadata "github.com/crevissepartners/projmux/internal/core/metadata"

// NormalizeProvider maps a provider spelling onto its registered id (codex,
// claude, antigravity) the way Agent records are classified, ignoring case
// and surrounding space, and returns "" for an empty or unknown provider. It
// requires no Client or context and performs no environment, file, Registry
// or network access.
func NormalizeProvider(provider string) string {
	return metadata.NormalizeProvider(provider)
}
