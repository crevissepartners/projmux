// Package codexbundle owns immutable, content-addressed Codex release bundle
// leases. It does not own generation lifecycle or a mutable current pointer.
package codexbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const ManifestSchemaVersion = 1

type Role string

const (
	RoleServer Role = "server"
	RoleTUI    Role = "tui"
	RoleHelper Role = "helper"
)

type ProtocolRange struct {
	Min uint32 `json:"min"`
	Max uint32 `json:"max"`
}

func (r ProtocolRange) Valid() bool { return r.Min > 0 && r.Max >= r.Min }

func (r ProtocolRange) Supports(required ProtocolRange) bool {
	return r.Valid() && required.Valid() && r.Min <= required.Min && r.Max >= required.Max
}

type ArtifactSpec struct {
	Path  string `json:"path"`
	Roles []Role `json:"roles"`
}

type Artifact struct {
	Path   string `json:"path"`
	Roles  []Role `json:"roles"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	Version       string        `json:"version"`
	Protocol      ProtocolRange `json:"protocol"`
	Artifacts     []Artifact    `json:"artifacts"`
}

type Refusal string

const (
	RefusalNone              Refusal = "none"
	RefusalManifestInvalid   Refusal = "manifest-invalid"
	RefusalBundleIncomplete  Refusal = "bundle-incomplete"
	RefusalProtocolMismatch  Refusal = "protocol-mismatch"
	RefusalSourceUnavailable Refusal = "source-unavailable"
	RefusalArtifactHashDrift Refusal = "artifact-hash-drift"
	RefusalArtifactModeDrift Refusal = "artifact-mode-drift"
	RefusalArtifactSizeDrift Refusal = "artifact-size-drift"
	RefusalLeaseDrift        Refusal = "lease-drift"
	RefusalCommitCollision   Refusal = "commit-collision"
)

type Error struct{ Refusal Refusal }

func (e *Error) Error() string { return "Codex bundle lease refused: " + string(e.Refusal) }

func RefusalOf(err error) Refusal {
	var bundleErr *Error
	if errors.As(err, &bundleErr) {
		return bundleErr.Refusal
	}
	return RefusalNone
}

type Lease struct {
	ID       string
	Root     string
	Manifest Manifest
}

// Inspect builds a canonical source manifest without persisting it.
func Inspect(sourceRoot, version string, protocol ProtocolRange, specs []ArtifactSpec) (Manifest, error) {
	if !safeRoot(sourceRoot) {
		return Manifest{}, &Error{Refusal: RefusalManifestInvalid}
	}
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, Version: version, Protocol: protocol}
	for _, spec := range specs {
		path, ok := cleanRelative(spec.Path)
		if !ok || len(spec.Roles) == 0 {
			return Manifest{}, &Error{Refusal: RefusalManifestInvalid}
		}
		artifact, err := inspectArtifact(filepath.Join(sourceRoot, filepath.FromSlash(path)), path, spec.Roles)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	canonicalize(&manifest)
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func inspectArtifact(source, path string, roles []Role) (Artifact, error) {
	// Source layouts commonly expose a mutable `current` symlink. Following a
	// source link is safe here because identity is the bytes/mode/size copied
	// into the lease, and copyVerified requires the resolved file identity to
	// remain the same for the whole copy. The committed lease contains no link.
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return Artifact{}, &Error{Refusal: RefusalSourceUnavailable}
	}
	file, err := os.Open(source) // #nosec G304 -- source is an explicit manifest path under the supplied root.
	if err != nil {
		return Artifact{}, &Error{Refusal: RefusalSourceUnavailable}
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return Artifact{}, &Error{Refusal: RefusalSourceUnavailable}
	}
	return Artifact{
		Path: path, Roles: append([]Role(nil), roles...), SHA256: hex.EncodeToString(digest.Sum(nil)),
		Mode: uint32(info.Mode().Perm()), Size: info.Size(),
	}, nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion || !safeVersionToken(m.Version) || !m.Protocol.Valid() || len(m.Artifacts) == 0 {
		return &Error{Refusal: RefusalManifestInvalid}
	}
	seen := make(map[string]bool, len(m.Artifacts))
	roleCount := map[Role]int{}
	for _, artifact := range m.Artifacts {
		path, ok := cleanRelative(artifact.Path)
		if !ok || path != artifact.Path || seen[path] || artifact.Size <= 0 ||
			artifact.Mode == 0 || artifact.Mode&^0o777 != 0 || len(artifact.Roles) == 0 {
			return &Error{Refusal: RefusalManifestInvalid}
		}
		seen[path] = true
		if len(artifact.SHA256) != sha256.Size*2 {
			return &Error{Refusal: RefusalManifestInvalid}
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil {
			return &Error{Refusal: RefusalManifestInvalid}
		}
		for _, role := range artifact.Roles {
			if role != RoleServer && role != RoleTUI && role != RoleHelper {
				return &Error{Refusal: RefusalManifestInvalid}
			}
			roleCount[role]++
			if artifact.Mode&0o111 == 0 {
				return &Error{Refusal: RefusalManifestInvalid}
			}
		}
	}
	if roleCount[RoleServer] != 1 || roleCount[RoleTUI] != 1 || roleCount[RoleHelper] == 0 {
		return &Error{Refusal: RefusalBundleIncomplete}
	}
	return nil
}

// ContentID hashes the canonical manifest, so identity covers every retained
// executable's role, bytes, mode and size plus the protocol range.
func (m Manifest) ContentID() (string, error) {
	canonical := cloneManifest(m)
	canonicalize(&canonical)
	if err := canonical.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", &Error{Refusal: RefusalManifestInvalid}
	}
	digest := sha256.Sum256(raw)
	return "sha256-" + hex.EncodeToString(digest[:]), nil
}

// Create verifies all source bytes and modes while copying into a private
// staging directory, verifies the complete staging tree again, and only then
// atomically commits the immutable content-addressed directory. It never
// creates or mutates a current symlink.
func Create(storeRoot, sourceRoot string, manifest Manifest, required ProtocolRange) (Lease, error) {
	if !safeRoot(storeRoot) || !safeRoot(sourceRoot) {
		return Lease{}, &Error{Refusal: RefusalManifestInvalid}
	}
	canonicalize(&manifest)
	if err := manifest.Validate(); err != nil {
		return Lease{}, err
	}
	if !manifest.Protocol.Supports(required) {
		return Lease{}, &Error{Refusal: RefusalProtocolMismatch}
	}
	id, err := manifest.ContentID()
	if err != nil {
		return Lease{}, err
	}
	finalRoot := filepath.Join(storeRoot, id)
	if info, err := os.Lstat(finalRoot); err == nil {
		if !info.IsDir() {
			return Lease{}, &Error{Refusal: RefusalCommitCollision}
		}
		return Open(finalRoot, required)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Lease{}, &Error{Refusal: RefusalCommitCollision}
	}
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		return Lease{}, &Error{Refusal: RefusalSourceUnavailable}
	}
	stage, err := os.MkdirTemp(storeRoot, ".lease-stage-")
	if err != nil {
		return Lease{}, &Error{Refusal: RefusalSourceUnavailable}
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, artifact := range manifest.Artifacts {
		source := filepath.Join(sourceRoot, filepath.FromSlash(artifact.Path))
		if err := copyVerified(source, filepath.Join(stage, filepath.FromSlash(artifact.Path)), artifact); err != nil {
			return Lease{}, err
		}
	}
	rawManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(stage, "manifest.json"), rawManifest, 0o600) != nil {
		return Lease{}, &Error{Refusal: RefusalSourceUnavailable}
	}
	if _, err := verifyAt(stage, manifest, required); err != nil {
		return Lease{}, err
	}
	if err := os.Rename(stage, finalRoot); err != nil {
		if _, statErr := os.Lstat(finalRoot); statErr == nil {
			return Lease{}, &Error{Refusal: RefusalCommitCollision}
		}
		return Lease{}, &Error{Refusal: RefusalSourceUnavailable}
	}
	committed = true
	return Lease{ID: id, Root: finalRoot, Manifest: manifest}, nil
}

func copyVerified(source, target string, want Artifact) error {
	before, err := os.Stat(source)
	if err != nil || !before.Mode().IsRegular() {
		return &Error{Refusal: RefusalSourceUnavailable}
	}
	if uint32(before.Mode().Perm()) != want.Mode {
		return &Error{Refusal: RefusalArtifactModeDrift}
	}
	if before.Size() != want.Size {
		return &Error{Refusal: RefusalArtifactSizeDrift}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return &Error{Refusal: RefusalSourceUnavailable}
	}
	in, err := os.Open(source) // #nosec G304 -- exact manifest source.
	if err != nil {
		return &Error{Refusal: RefusalSourceUnavailable}
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(want.Mode)) // #nosec G304 -- exact private staging target.
	if err != nil {
		_ = in.Close()
		return &Error{Refusal: RefusalSourceUnavailable}
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, digest), in)
	closeInErr, closeOutErr := in.Close(), out.Close()
	if copyErr != nil || closeInErr != nil || closeOutErr != nil {
		return &Error{Refusal: RefusalSourceUnavailable}
	}
	after, err := os.Stat(source)
	if err != nil || !os.SameFile(before, after) || uint32(after.Mode().Perm()) != want.Mode || after.Size() != want.Size {
		return &Error{Refusal: RefusalArtifactModeDrift}
	}
	if hex.EncodeToString(digest.Sum(nil)) != want.SHA256 {
		return &Error{Refusal: RefusalArtifactHashDrift}
	}
	return nil
}

// Open re-verifies a committed lease and refuses any manifest, hash, size,
// mode, completeness, or protocol drift before a caller can launch from it.
func Open(root string, required ProtocolRange) (Lease, error) {
	if !safeRoot(root) {
		return Lease{}, &Error{Refusal: RefusalManifestInvalid}
	}
	raw, mode, err := readCommittedRegularFile(root, "manifest.json")
	if err != nil {
		return Lease{}, &Error{Refusal: RefusalLeaseDrift}
	}
	if mode != 0o600 {
		return Lease{}, &Error{Refusal: RefusalLeaseDrift}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if decoder.Decode(&manifest) != nil {
		return Lease{}, &Error{Refusal: RefusalLeaseDrift}
	}
	var trailing any
	if trailingErr := decoder.Decode(&trailing); trailingErr != io.EOF {
		return Lease{}, &Error{Refusal: RefusalLeaseDrift}
	}
	canonicalize(&manifest)
	if _, err := verifyAt(root, manifest, required); err != nil {
		return Lease{}, err
	}
	id, err := manifest.ContentID()
	if err != nil || filepath.Base(filepath.Clean(root)) != id {
		return Lease{}, &Error{Refusal: RefusalLeaseDrift}
	}
	return Lease{ID: id, Root: filepath.Clean(root), Manifest: manifest}, nil
}

func verifyAt(root string, manifest Manifest, required ProtocolRange) (Manifest, error) {
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	if !manifest.Protocol.Supports(required) {
		return Manifest{}, &Error{Refusal: RefusalProtocolMismatch}
	}
	for _, artifact := range manifest.Artifacts {
		got, err := inspectCommittedArtifact(root, artifact)
		if err != nil {
			return Manifest{}, &Error{Refusal: RefusalLeaseDrift}
		}
		switch {
		case got.Mode != artifact.Mode:
			return Manifest{}, &Error{Refusal: RefusalArtifactModeDrift}
		case got.Size != artifact.Size:
			return Manifest{}, &Error{Refusal: RefusalArtifactSizeDrift}
		case got.SHA256 != artifact.SHA256:
			return Manifest{}, &Error{Refusal: RefusalArtifactHashDrift}
		}
	}
	return manifest, nil
}

// inspectCommittedArtifact rejects links in the committed tree. Source
// inspection intentionally follows a mutable upstream `current` symlink, but
// a lease is launch authority only for regular files physically retained under
// its own immutable root.
func inspectCommittedArtifact(root string, want Artifact) (Artifact, error) {
	raw, mode, err := readCommittedRegularFile(root, want.Path)
	if err != nil {
		return Artifact{}, err
	}
	digest := sha256.Sum256(raw)
	return Artifact{
		Path: want.Path, Roles: append([]Role(nil), want.Roles...), SHA256: hex.EncodeToString(digest[:]),
		Mode: uint32(mode), Size: int64(len(raw)),
	}, nil
}

func readCommittedRegularFile(root, relative string) ([]byte, fs.FileMode, error) {
	path, ok := cleanRelative(relative)
	if !ok || path != relative {
		return nil, 0, &Error{Refusal: RefusalLeaseDrift}
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, 0, &Error{Refusal: RefusalLeaseDrift}
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	parent := root
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, filepath.FromSlash(part))
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, 0, &Error{Refusal: RefusalLeaseDrift}
		}
	}
	target := filepath.Join(root, filepath.FromSlash(path))
	before, err := os.Lstat(target)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, 0, &Error{Refusal: RefusalLeaseDrift}
	}
	file, err := os.Open(target) // #nosec G304 -- canonical path below an exact lease root.
	if err != nil {
		return nil, 0, &Error{Refusal: RefusalLeaseDrift}
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, 0, &Error{Refusal: RefusalLeaseDrift}
	}
	raw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	after, afterErr := os.Lstat(target)
	if readErr != nil || closeErr != nil || afterErr != nil || !after.Mode().IsRegular() ||
		after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		return nil, 0, &Error{Refusal: RefusalLeaseDrift}
	}
	return raw, opened.Mode().Perm(), nil
}

func (l Lease) Paths(role Role) []string {
	var paths []string
	for _, artifact := range l.Manifest.Artifacts {
		if slices.Contains(artifact.Roles, role) {
			paths = append(paths, filepath.Join(l.Root, filepath.FromSlash(artifact.Path)))
		}
	}
	return paths
}

func canonicalize(manifest *Manifest) {
	for index := range manifest.Artifacts {
		manifest.Artifacts[index].Path = filepath.ToSlash(filepath.Clean(manifest.Artifacts[index].Path))
		slices.Sort(manifest.Artifacts[index].Roles)
	}
	slices.SortFunc(manifest.Artifacts, func(a, b Artifact) int { return strings.Compare(a.Path, b.Path) })
}

func cloneManifest(manifest Manifest) Manifest {
	out := manifest
	out.Artifacts = make([]Artifact, len(manifest.Artifacts))
	for index, artifact := range manifest.Artifacts {
		out.Artifacts[index] = artifact
		out.Artifacts[index].Roles = append([]Role(nil), artifact.Roles...)
	}
	return out
}

func cleanRelative(path string) (string, bool) {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	return path, path != "" && path != "." && !strings.HasPrefix(path, "../") && !filepath.IsAbs(path) && !strings.Contains(path, "\\")
}

func safeRoot(path string) bool {
	if path == "" || path != strings.TrimSpace(path) {
		return false
	}
	clean := filepath.Clean(path)
	return path == clean && filepath.IsAbs(path) && path != filepath.Clean(string(filepath.Separator))
}

func safeVersionToken(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= '0' && char <= '9') || char == '.' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func (r Refusal) String() string { return string(r) }
