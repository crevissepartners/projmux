package codexupgrade

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const qualificationDirName = "qualification"

// QualificationStore is where a produced receipt waits for the gate.
//
// The producer already existed before this store did, and that was the whole
// defect: `scripts/test-generation-pool-qualification.sh` measures a declared
// version pair and writes a canonical receipt, but the only consumer was a
// hand-written `agent app-server upgrade --request` document. The managed
// activation path builds its request in process and reads no document, so a
// receipt could be produced and still never reach the entry path it qualifies.
// The lane was closed not because the pair could not be qualified but because
// nothing carried the answer across.
//
// One receipt per version pair, addressed by the pair itself. A receipt
// qualifies the one pair it names, so the file name is the identity: nothing
// here decides which pair an activation needs, it only answers for the pair it
// is asked about.
type QualificationStore struct{ dir string }

func QualificationDirFor(stateDir string) string {
	return filepath.Join(stateDir, journalDirName, qualificationDirName)
}

func NewQualificationStore(dir string) *QualificationStore { return &QualificationStore{dir: dir} }

func NewQualificationStateStore(stateDir string) *QualificationStore {
	return NewQualificationStore(QualificationDirFor(stateDir))
}

func (store *QualificationStore) Dir() string {
	if store == nil {
		return ""
	}
	return store.dir
}

// Path names the receipt file for one pair.
//
// Receipt version tokens are digits, dots, and dashes only, which the receipt's
// own grammar already enforces, so the pair composes into a file name without
// escaping and without any way to name a path outside this directory. The check
// here is not defence in depth over that grammar; it is what makes the
// composition safe to state as a property.
func (store *QualificationStore) Path(pair codexgeneration.VersionPair) (string, error) {
	if store == nil || !filepath.IsAbs(store.dir) {
		return "", errors.New("codex qualification store path is invalid")
	}
	if codexgeneration.EvaluateQualification(pair, codexgeneration.QualificationEvidence{}).Validate() != nil ||
		pair.Old == pair.New {
		return "", errors.New("codex qualification version pair is invalid")
	}
	name := pair.Old + "_" + pair.New + ".json"
	if name != filepath.Base(name) || strings.ContainsRune(name, filepath.Separator) {
		return "", errors.New("codex qualification version pair is not a file name")
	}
	return filepath.Join(store.dir, name), nil
}

// Load reads the receipt for one pair.
//
// A missing receipt is not an error: it is the ordinary state of a pair nobody
// has qualified yet, and the caller's refusal names the action that fixes it. A
// present-but-unreadable receipt is an error, because a file that exists and
// does not decode is a different fact from no file at all, and collapsing the
// two would hide a corrupted store behind "run the qualification".
func (store *QualificationStore) Load(pair codexgeneration.VersionPair) (codexgeneration.QualificationResult, bool, error) {
	path, err := store.Path(pair)
	if err != nil {
		return codexgeneration.QualificationResult{}, false, err
	}
	body, err := os.ReadFile(path) // #nosec G304 -- exact owner-private receipt path composed from a validated version pair
	if errors.Is(err, fs.ErrNotExist) {
		return codexgeneration.QualificationResult{}, false, nil
	}
	if err != nil {
		return codexgeneration.QualificationResult{}, false, err
	}
	result, err := codexgeneration.DecodeQualificationResult(body)
	if err != nil {
		return codexgeneration.QualificationResult{}, false, fmt.Errorf("decode stored Codex qualification receipt: %w", err)
	}
	if result.Versions != pair {
		return codexgeneration.QualificationResult{}, false, errors.New("stored Codex qualification receipt names another version pair")
	}
	return result, true, nil
}

// Save installs a produced receipt under the pair it names.
//
// The receipt is re-encoded from the decoded value rather than copied byte for
// byte, so what the gate later reads is the canonical form of what was
// accepted here and not whatever spacing or field order the producer's file
// happened to have. Anything that would change meaning has already been
// refused: DecodeQualificationResult rejects unknown fields, trailing JSON, and
// a verdict its evidence does not produce.
func (store *QualificationStore) Save(result codexgeneration.QualificationResult) error {
	path, err := store.Path(result.Versions)
	if err != nil {
		return err
	}
	body, err := result.JSON()
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := localstate.EnsurePrivateDir(store.dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(store.dir, ".qualification-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(localstate.PrivateFileMode); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(body); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	written, err := os.ReadFile(path) // #nosec G304 -- the path this call just wrote
	if err != nil {
		return err
	}
	if !bytes.Equal(written, body) {
		return errors.New("stored Codex qualification receipt is not the bytes that were accepted")
	}
	return nil
}
