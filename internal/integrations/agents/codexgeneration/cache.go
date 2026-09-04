package codexgeneration

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const cacheDirName = "codex-payload-free-capabilities"

type Cache struct{ Root string }

func NewCache(stateDir string) Cache {
	return Cache{Root: filepath.Join(filepath.Clean(stateDir), cacheDirName)}
}

func (cache Cache) path(key string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(cache.Root))
	if !filepath.IsAbs(root) || !digestPattern.MatchString(key) {
		return "", errors.New("capability cache path is invalid")
	}
	return filepath.Join(root, key+".json"), nil
}

// Publish is immutable: a byte-identical replay is a fixed point, while a
// second observation for the same exact tuple must use an explicit new
// evidence store rather than overwriting the first receipt.
func (cache Cache) Publish(record Record) error {
	encoded, err := record.JSON()
	if err != nil {
		return err
	}
	path, err := cache.path(record.CacheKey)
	if err != nil {
		return err
	}
	// #nosec G304 -- path comes only from a validated absolute cache root plus this record's exact 64-hex tuple key; callers cannot supply a filename.
	if existing, readErr := os.ReadFile(path); readErr == nil {
		decoded, decodeErr := DecodeRecord(existing)
		if decodeErr != nil {
			return fmt.Errorf("read immutable capability cache: %w", decodeErr)
		}
		if current, _ := decoded.JSON(); string(current) == string(encoded) {
			return nil
		}
		return errors.New("refuse to overwrite immutable capability record")
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// #nosec G304 -- path comes only from a validated absolute cache root plus this record's exact 64-hex tuple key; callers cannot supply a filename.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(encoded, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(writeErr, closeErr)
	}
	return nil
}

// Lookup returns only an exact tuple hit. Missing, drifted, rebound, or invalid
// evidence is never converted to supported; callers project it as unknown and
// retain the plain fallback.
func (cache Cache) Lookup(tuple Tuple) (Record, bool, error) {
	key, err := tuple.Key()
	if err != nil {
		return Record{}, false, err
	}
	path, err := cache.path(key)
	if err != nil {
		return Record{}, false, err
	}
	// #nosec G304 -- path comes only from a validated absolute cache root plus the tuple's internally computed exact 64-hex key; callers cannot supply a filename.
	encoded, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	record, err := DecodeRecord(encoded)
	if err != nil {
		return Record{}, false, err
	}
	if record.Tuple != tuple || record.CacheKey != key {
		return Record{}, false, errors.New("capability cache tuple mismatch")
	}
	return record, true, nil
}

// Resolve is the fail-closed consumer API. Any miss or unreadable, corrupt,
// future-schema, trailing, drifted record becomes an exact-tuple unknown value;
// cache bytes can therefore never fail open into a supported route.
func (cache Cache) Resolve(tuple Tuple) Record {
	if record, ok, err := cache.Lookup(tuple); err == nil && ok {
		return record
	}
	unknown, err := UnknownRecord(tuple)
	if err != nil {
		return Record{}
	}
	return unknown
}
