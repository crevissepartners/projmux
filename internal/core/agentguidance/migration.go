package agentguidance

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/state"
)

// Historical Default bytes extracted from public origin/main, deduplicated:
// c4fc721e3 (#1321), c79edd8f1 (#1544), 002aad887 (#1600).
// 4cce7c53d and 3c191afbb did not change Default. Never derive this list from
// a user's file, private consumer history, or the evolving current Default.
//
//go:embed legacy/*.md
var legacyDefaults embed.FS

const webBegin = "<!-- projmux-web:artifacts:begin -->"
const webEnd = "<!-- projmux-web:artifacts:end -->"

// migrationReceipt is the durable one-time migration receipt. A prepared receipt
// pins both file versions so a crash between the user rename and completion
// can be retried using the immutable backup, without removing more text.
type migrationReceipt struct {
	Version        int    `json:"version"`
	Complete       bool   `json:"complete"`
	OriginalDigest string `json:"originalDigest,omitempty"`
	ResultDigest   string `json:"resultDigest,omitempty"`
}

func (m *migrationReceipt) validate() error {
	if m == nil || m.Version != 1 {
		return errors.New("unsupported agent guidance migration version")
	}
	for _, digest := range []string{m.OriginalDigest, m.ResultDigest} {
		if digest != "" {
			if err := ValidateDigest(digest); err != nil {
				return err
			}
		}
	}
	if (m.OriginalDigest == "") != (m.ResultDigest == "") || (!m.Complete && m.OriginalDigest == "") {
		return errors.New("invalid agent guidance migration receipt")
	}
	return nil
}

// withPolicyLock serializes first loads and CLI policy read-modify-writes
// across Store values and processes. Closing the descriptor releases flock;
// the persistent inode must never be removed while another caller may wait.
func (s Store) withPolicyLock(fn func() error) error {
	return s.withPolicyLockContext(context.Background(), fn)
}

func (s Store) withPolicyLockContext(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.EnsurePrivateDir(s.configDir); err != nil {
		return err
	}
	held, err := state.AcquireFileLockContext(ctx, filepath.Join(s.configDir, ".agent-guidance.lock"), 5*time.Second, nil)
	if err != nil {
		return err
	}
	defer held.Close()
	return fn()
}

// MigrationFileName is separate from the policy: Task1 readers strictly reject
// any key beyond disabled/disabledFragments in agent-guidance.json.
const MigrationFileName = "agent-guidance-migration.json"

func (s Store) migrationPath() string { return filepath.Join(s.configDir, MigrationFileName) }

func (s Store) loadMigration() (*migrationReceipt, error) {
	root, err := os.OpenRoot(s.configDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(MigrationFileName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("agent guidance migration receipt is not a regular file")
	}
	content, _, err := readGuidancePart(root, MigrationFileName)
	if err != nil {
		return nil, err
	}
	var receipt *migrationReceipt
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return nil, fmt.Errorf("read agent guidance migration receipt: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing migration receipt data")
	}
	if err := receipt.validate(); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (s Store) saveMigration(receipt *migrationReceipt) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	content, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(s.migrationPath(), append(content, '\n'), state.PrivateFileMode); err != nil {
		return fmt.Errorf("write agent guidance migration receipt: %w", err)
	}
	return syncDirectory(s.configDir)
}

func (s Store) migrate() (notice string, err error) {
	receipt, err := s.loadMigration()
	if err != nil {
		return "", err
	}
	if receipt != nil && receipt.Complete {
		return "", nil
	}
	err = s.withPolicyLock(func() error { var e error; notice, e = s.migrateLocked(); return e })
	if err != nil {
		return "", fmt.Errorf("migrate agent guidance: %w", err)
	}
	return notice, nil
}

func (s Store) migrateLocked() (string, error) {
	settings, explicit, err := s.LoadSettings()
	if err != nil {
		return "", err
	}
	receipt, err := s.loadMigration()
	if err != nil {
		return "", err
	}
	if receipt != nil && receipt.Complete {
		return "", nil
	}
	root, err := os.OpenRoot(s.configDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Lstat(FileName)
	if errors.Is(err, fs.ErrNotExist) && receipt == nil {
		// Materialize enabled policy even without a user file. A subsequently
		// added blank file must remain an empty addition for old and new readers.
		if !explicit {
			if err := s.saveSettings(settings); err != nil {
				return "", err
			}
		}
		if err := s.migrationCheckpoint("after-policy"); err != nil {
			return "", err
		}
		return "", s.saveMigration(&migrationReceipt{Version: 1, Complete: true})
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("user guidance is not a regular file")
	}
	current, _, err := readGuidancePart(root, FileName)
	if err != nil {
		return "", err
	}
	original := current
	if receipt != nil {
		var exists bool
		original, exists, err = readGuidancePart(root, FileName+".bak")
		if err != nil {
			return "", err
		}
		if !exists || Digest(original) != receipt.OriginalDigest {
			return "", errors.New("migration backup does not match the prepared receipt")
		}
	}
	next, removedDefault, removedBlocks := cleanLegacy(original)
	if receipt != nil && (Digest(next) != receipt.ResultDigest || (Digest(current) != receipt.OriginalDigest && Digest(current) != receipt.ResultDigest)) {
		return "", errors.New("user guidance changed during migration; preserved files for manual recovery")
	}
	if err := s.ensureBackup(original, info.Mode().Perm()); err != nil {
		return "", err
	}
	if err := s.migrationCheckpoint("after-backup"); err != nil {
		return "", err
	}
	if receipt == nil {
		receipt = &migrationReceipt{Version: 1, OriginalDigest: Digest(original), ResultDigest: Digest(next)}
		if err := s.saveMigration(receipt); err != nil {
			return "", err
		}
	}
	if err := s.migrationCheckpoint("after-prepare"); err != nil {
		return "", err
	}
	// Policy precedes any user rewrite, especially a rewrite to empty bytes.
	// A crash at every boundary remains readable by the strict Task1 reader.
	if !explicit {
		settings.Disabled = len(bytes.TrimSpace(original)) == 0
		if err := s.saveSettings(settings); err != nil {
			return "", err
		}
	}
	if err := s.migrationCheckpoint("after-policy"); err != nil {
		return "", err
	}
	latest, exists, err := readGuidancePart(root, FileName)
	if err != nil {
		return "", err
	}
	latestInfo, statErr := root.Lstat(FileName)
	if !exists || statErr != nil || !latestInfo.Mode().IsRegular() || !bytes.Equal(latest, current) {
		return "", errors.New("user guidance changed during migration; preserved files for manual recovery")
	}
	if !bytes.Equal(current, next) {
		if err := writeAtomic(s.Path(), next, info.Mode().Perm()); err != nil {
			return "", err
		}
		if err := syncDirectory(s.configDir); err != nil {
			return "", err
		}
	}
	if err := s.migrationCheckpoint("after-user"); err != nil {
		return "", err
	}
	receipt.Complete = true
	if err := s.migrationCheckpoint("before-complete"); err != nil {
		return "", err
	}
	if err := s.saveMigration(receipt); err != nil {
		return "", err
	}
	notice := fmt.Sprintf("agent guidance migrated; backup: %s.bak; removed historical default prefix=%t, complete managed blocks=%d. User guidance now appends to the latest built-in guidance; replacing only the built-in guidance is no longer supported.", FileName, removedDefault, removedBlocks)
	if len(bytes.TrimSpace(next)) != 0 {
		notice += " Retained user text and any unrecognized prefix, markers, or text outside managed blocks; review with agent guidance show."
	}
	if settings.Disabled {
		notice += " Whole guidance remains disabled."
	}
	return notice, nil
}

func (s Store) migrationCheckpoint(step string) error {
	if s.migrationStep != nil {
		return s.migrationStep(step)
	}
	return nil
}

// ensureBackup publishes a fully synced temporary inode with link(2), which
// cannot overwrite an existing backup. A crash never leaves a partial .bak.
func (s Store) ensureBackup(original []byte, mode os.FileMode) error {
	path := s.Path() + ".bak"
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("existing guidance backup is not a regular file")
		}
		root, err := os.OpenRoot(s.configDir)
		if err != nil {
			return err
		}
		defer root.Close()
		backup, _, err := readGuidancePart(root, FileName+".bak")
		if err != nil {
			return err
		}
		if !bytes.Equal(backup, original) {
			return errors.New("existing guidance backup differs; nothing was overwritten")
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(s.configDir, ".agent-guidance.backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(original); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(temp.Name(), path); err != nil {
		return err
	}
	return syncDirectory(s.configDir)
}

func syncDirectory(path string) error {
	// Admit a directory root first, then open only its fixed current entry.
	// This cannot silently sync a regular file supplied as the config root.
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func cleanLegacy(original []byte) ([]byte, bool, int) {
	text := original
	entries, _ := legacyDefaults.ReadDir("legacy")
	var longest []byte
	for _, entry := range entries {
		old, _ := legacyDefaults.ReadFile("legacy/" + entry.Name())
		if len(old) > len(longest) && bytes.HasPrefix(text, old) {
			longest = old
		}
	}
	text = text[len(longest):]
	// Only remove a non-nested, balanced block with standalone marker lines.
	// Malformed/nested markers and all bytes outside a block remain untouched.
	lines := strings.SplitAfter(string(text), "\n")
	var out strings.Builder
	var pending strings.Builder
	depth, blocks := 0, 0
	valid := true
	for _, line := range lines {
		marker := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch marker {
		case webBegin:
			if depth == 0 {
				pending.Reset()
				valid = true
			} else {
				valid = false
			}
			depth++
			pending.WriteString(line)
		case webEnd:
			if depth == 0 {
				out.WriteString(line)
				continue
			}
			depth--
			pending.WriteString(line)
			if depth == 0 {
				if valid {
					blocks++
				} else {
					out.WriteString(pending.String())
				}
				pending.Reset()
			}
		default:
			if depth > 0 {
				pending.WriteString(line)
			} else {
				out.WriteString(line)
			}
		}
	}
	out.WriteString(pending.String())
	return []byte(out.String()), len(longest) > 0, blocks
}
