package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/state"
)

// SettingsDirName is the directory below StateDir that holds the Claude
// settings snapshots a profile's allow and deny rules are launched with.
const SettingsDirName = "profile-settings"

// Resolve returns one profile exactly as stored together with its checked
// content, and refuses exactly when List would mark that profile invalid: the
// checks Write applies to one file (Parse and the instructions lookup), then
// the cross-profile role check, so a profile listing a role that another
// otherwise-valid profile also lists is refused with profile-role-claimed.
// The refusal carries the same reason token List reports.
func (s Store) Resolve(name string) (Profile, Spec, error) {
	loaded, err := s.Load(name)
	if err != nil {
		return Profile{}, Spec{}, err
	}
	spec, err := s.validate(loaded.Content)
	if err != nil {
		return Profile{}, Spec{}, named(err, name)
	}
	if len(spec.Roles) > 0 {
		entries, err := s.List()
		if err != nil {
			return Profile{}, Spec{}, err
		}
		for _, entry := range entries {
			if entry.Name == name && !entry.Valid && entry.Reason == ReasonRoleClaimed {
				return Profile{}, Spec{}, &Error{Reason: ReasonRoleClaimed, Name: name, Detail: entry.Detail}
			}
		}
	}
	return loaded, spec, nil
}

// HasPermissions reports whether any [permissions] key is set.
func (p Permissions) HasPermissions() bool {
	return p.Sandbox != "" || p.Approval != "" || len(p.Allow) > 0 || len(p.Deny) > 0
}

// claudeSettings is the Claude settings document a snapshot holds. Only the
// permission rules are written, and an empty list is left out.
type claudeSettings struct {
	Permissions claudeSettingsPermissions `json:"permissions"`
}

type claudeSettingsPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// SettingsContent is the exact Claude settings JSON for p's allow and deny
// rules: {"permissions":{"allow":[...],"deny":[...]}} with an empty list left
// out, no trailing newline, and no HTML escaping. It is nil when neither list
// has a rule. The same rules always give the same bytes.
func SettingsContent(p Permissions) ([]byte, error) {
	if len(p.Allow) == 0 && len(p.Deny) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(claudeSettings{Permissions: claudeSettingsPermissions{Allow: p.Allow, Deny: p.Deny}}); err != nil {
		return nil, fmt.Errorf("encode profile settings: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// SettingsSnapshotPath is where the snapshot of content lives:
// <stateDir>/profile-settings/sha256-<hex>.json, named by the sha256 of the
// exact bytes.
func SettingsSnapshotPath(stateDir string, content []byte) string {
	sum := sha256.Sum256(content)
	return filepath.Join(stateDir, SettingsDirName, "sha256-"+hex.EncodeToString(sum[:])+".json")
}

// WriteSettingsSnapshot writes the Claude settings snapshot of p's allow and
// deny rules below stateDir and returns its path, or "" when p has no rule.
// The snapshot is content addressed, so the same rules always land at the same
// path; the directory is 0700 and the file 0600, written atomically. A file
// that already holds exactly these bytes is kept (its mode repaired).
func WriteSettingsSnapshot(stateDir string, p Permissions) (string, error) {
	content, err := SettingsContent(p)
	if err != nil || content == nil {
		return "", err
	}
	path := SettingsSnapshotPath(stateDir, content)
	if settingsSnapshotHolds(filepath.Dir(path), filepath.Base(path), content) {
		if err := state.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			return "", fmt.Errorf("write profile settings snapshot: %w", err)
		}
		state.RepairPrivateFile(path)
		return path, nil
	}
	if err := writeAtomic(path, content); err != nil {
		return "", fmt.Errorf("write profile settings snapshot: %w", err)
	}
	return path, nil
}

// settingsSnapshotHolds reports whether the snapshot file name in dir already
// holds exactly content. The file is opened through an os.Root on dir, so the
// read cannot leave it, and never more than len(content)+1 bytes are read.
// Any failure -- a missing directory or file, or a path that is not a regular
// file -- is "no", which makes the caller write the snapshot.
func settingsSnapshotHolds(dir, name string, content []byte) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	existing, err := io.ReadAll(io.LimitReader(file, int64(len(content))+1))
	return err == nil && bytes.Equal(existing, content)
}
