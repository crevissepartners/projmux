package codexinstalled

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
)

// Declared version-pair and receipt wiring for the isolated generation-pool
// qualification.
//
// These helpers carry two things and nothing else: which version pair the
// operator declares is under test, and the canonical receipt out of the test
// process. Every evidence field stays measured by the harness itself. No
// evidence boolean or counter is ever sourced from an environment variable, a
// flag, or a constant, and neither helper touches the verdict.

// DeclaredGenerationPair resolves the declared old/new version pair.
//
// Validity is decided by the product's own receipt rule rather than a second
// copy of the version-token grammar: an all-zero evidence set evaluates to a
// self-consistent receipt, so Validate reports exactly the version-token
// verdict and nothing else.
func DeclaredGenerationPair(oldVersion, newVersion string) (codexgeneration.VersionPair, error) {
	pair := codexgeneration.VersionPair{Old: strings.TrimSpace(oldVersion), New: strings.TrimSpace(newVersion)}
	if pair.Old == pair.New {
		return codexgeneration.VersionPair{}, fmt.Errorf("declared generation pair must name two different versions, got %q twice", pair.Old)
	}
	if err := codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{}).Validate(); err != nil {
		return codexgeneration.VersionPair{}, fmt.Errorf("declared generation pair %q/%q is not a receipt version pair", pair.Old, pair.New)
	}
	return pair, nil
}

// EmitGenerationQualificationReceipt writes the canonical receipt to path and
// returns the exact bytes written.
//
// The bytes are the receipt's own canonical encoding, so the file content is
// byte-identical to what a consumer embeds in the `qualification` field of an
// `agent app-server upgrade --request` document. The file is replaced on every
// run so a repeated qualification can never publish a stale verdict, and it is
// reopened through DecodeQualificationResult before returning so an unreadable
// or non-round-tripping receipt fails here instead of at the upgrade gate.
func EmitGenerationQualificationReceipt(path string, result codexgeneration.QualificationResult) ([]byte, error) {
	receipt := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(receipt) {
		return nil, fmt.Errorf("generation qualification receipt path must be absolute, got %q", path)
	}
	raw, err := result.JSON()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(receipt), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(receipt); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.WriteFile(receipt, raw, 0o600); err != nil {
		return nil, err
	}
	written, err := os.ReadFile(receipt) // #nosec G304 -- exact caller-declared receipt path.
	if err != nil {
		return nil, err
	}
	reopened, err := codexgeneration.DecodeQualificationResult(written)
	if err != nil {
		return nil, fmt.Errorf("emitted generation qualification receipt does not reopen: %w", err)
	}
	if reopened != result || !bytes.Equal(written, raw) {
		return nil, errors.New("emitted generation qualification receipt is not byte-identical to the canonical result")
	}
	return written, nil
}
