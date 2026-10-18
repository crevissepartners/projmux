package agentguidance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"
)

// ProjectFile keeps exact stored bytes separate from Load's effective guidance.
// Revision identifies UID, presence and raw bytes, or an oversize bounded fingerprint.
type ProjectFile struct {
	Oversize        bool   `json:"oversize"`
	PrefixDigest    string `json:"prefixDigest"`
	PrefixBytes     int    `json:"prefixBytes"`
	Path            string `json:"path"`
	Present         bool   `json:"present"`
	Text            string `json:"text"`
	Bytes           int    `json:"bytes"`
	Digest          string `json:"digest"`
	Revision        string `json:"revision"`
	MaxBytes        int    `json:"maxBytes"`
	ProjectUID      string `json:"projectUID"`
	Source          string `json:"source"`
	EffectiveText   string `json:"effectiveText"`
	EffectiveDigest string `json:"effectiveDigest"`
}

// ProjectValidation describes a candidate and the target state it can replace.
type ProjectValidation struct {
	Text       string `json:"text"`
	Bytes      int    `json:"bytes"`
	Digest     string `json:"digest"`
	Revision   string `json:"revision"`
	MaxBytes   int    `json:"maxBytes"`
	ProjectUID string `json:"projectUID"`
}

type projectEditState struct {
	UID      string
	Present  bool
	Content  []byte
	oversize *projectOversizeState
}

// Only oversize revisions include stat identity; normal raw revisions stay stable.
type projectOversizeState struct {
	Bytes        int64
	Device       uint64
	Inode        uint64
	Mtime        int64
	PrefixDigest string
	PrefixBytes  int
}

func (v projectEditState) revision() string {
	if v.oversize != nil {
		b, _ := json.Marshal(struct {
			UID      string
			Present  bool
			Oversize *projectOversizeState
		}{v.UID, v.Present, v.oversize})
		return Digest(b)
	}
	b, _ := json.Marshal(struct {
		UID     string
		Present bool
		Content []byte
	}{v.UID, v.Present, append([]byte{}, v.Content...)})
	return Digest(b)
}
func (s ProjectStore) editState(uid string) (v projectEditState, err error) {
	v.UID = uid
	path, err := s.Path(uid)
	if err != nil {
		return v, editError("invalid", err)
	}
	root, err := os.OpenRoot(s.configDir)
	if errors.Is(err, fs.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, editError("unreadable", err)
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, editError("unreadable", err)
	}
	if !info.Mode().IsRegular() {
		return v, editError("unreadable", errors.New("project guidance is not a regular file"))
	}
	f, err := root.Open(name)
	if err != nil {
		return v, editError("unreadable", err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return v, editError("unreadable", err)
	}
	if !before.Mode().IsRegular() || !os.SameFile(info, before) {
		return v, editError("unreadable", errors.New("project guidance changed while opening"))
	}
	content, err := readProjectPrefix(f)
	if err != nil {
		return v, editError("unreadable", err)
	}
	after, err := f.Stat()
	if err != nil {
		return v, editError("unreadable", err)
	}
	current, err := root.Stat(name)
	if err != nil {
		return v, editError("unreadable", err)
	}
	// Refuse an inconsistent observation without retrying or exceeding the read budget.
	if !sameProjectFileState(before, after) || !sameProjectFileState(after, current) || int64(len(content)) != min(before.Size(), int64(MaxSize+1)) {
		return v, editError("unreadable", errors.New("project guidance changed while reading"))
	}
	v.Present = true
	if before.Size() > MaxSize {
		stat, ok := before.Sys().(*syscall.Stat_t)
		if !ok || before.Size() != int64(int(before.Size())) {
			return v, editError("unreadable", errors.New("project guidance size or identity is not representable"))
		}
		v.oversize = &projectOversizeState{Bytes: before.Size(), Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Mtime: before.ModTime().UnixNano(), PrefixDigest: Digest(content), PrefixBytes: len(content)}
	} else {
		v.Content = content
	}
	return v, nil
}

// readProjectPrefix bounds total file reads, including the oversize sentinel byte.
func readProjectPrefix(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, MaxSize+1))
}
func sameProjectFileState(a, b fs.FileInfo) bool {
	return b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func (s ProjectStore) fileView(v projectEditState) ProjectFile {
	path, _ := s.Path(v.UID)
	if v.oversize != nil {
		return ProjectFile{Path: path, Present: v.Present, Oversize: true, Bytes: int(v.oversize.Bytes), PrefixDigest: v.oversize.PrefixDigest, PrefixBytes: v.oversize.PrefixBytes, Revision: v.revision(), MaxBytes: MaxSize, ProjectUID: v.UID}
	}
	effective := projectEffective(v.Content)
	return ProjectFile{Path: path, Present: v.Present, Text: string(v.Content), Bytes: len(v.Content), Digest: Digest(v.Content), Revision: v.revision(), MaxBytes: MaxSize, ProjectUID: v.UID, Source: string(effective.Source), EffectiveText: string(effective.Text), EffectiveDigest: effective.Digest}
}

// ReadFile never migrates, locks or writes. Invalid UTF-8 cannot be represented
// losslessly in JSON and is refused; existing NUL bytes are retained.
func (s ProjectStore) ReadFile(ctx context.Context, uid string) (ProjectFile, error) {
	if err := ctx.Err(); err != nil {
		return ProjectFile{}, err
	}
	v, err := s.editState(uid)
	if err != nil {
		return ProjectFile{}, err
	}
	if !utf8.Valid(v.Content) {
		return ProjectFile{}, editError("unreadable", errors.New("existing Project guidance is not UTF-8"))
	}
	return s.fileView(v), ctx.Err()
}
func validateProjectCandidate(content []byte) error {
	if len(content) > MaxSize {
		return editError("too-large", fmt.Errorf("candidate is %d bytes; limit %d", len(content), MaxSize))
	}
	if !utf8.Valid(content) || bytes.ContainsRune(content, 0) {
		return editError("invalid", errors.New("candidate must be UTF-8 without NUL"))
	}
	return nil
}

// ValidateFile creates no filesystem entries and can recover invalid existing UTF-8.
func (s ProjectStore) ValidateFile(ctx context.Context, uid string, content []byte) (ProjectValidation, error) {
	if err := ctx.Err(); err != nil {
		return ProjectValidation{}, err
	}
	v, err := s.editState(uid)
	if err != nil {
		return ProjectValidation{}, err
	}
	if err = validateProjectCandidate(content); err != nil {
		return ProjectValidation{}, err
	}
	return ProjectValidation{Text: string(content), Bytes: len(content), Digest: Digest(content), Revision: v.revision(), MaxBytes: MaxSize, ProjectUID: uid}, ctx.Err()
}

// WriteFile checks the target revision and candidate under the shared writer
// lock, then uses Save's atomic replacement and permissions policy.
func (s ProjectStore) WriteFile(ctx context.Context, uid string, content []byte, revision string) (out ProjectFile, err error) {
	content = bytes.Clone(content)
	err = s.edit(ctx, uid, revision, func(_ projectEditState) error { return validateProjectCandidate(content) }, func(v projectEditState) error {
		if err := s.Save(uid, content); err != nil {
			return editError("unreadable", err)
		}
		v.Content, v.Present, v.oversize = content, true, nil
		out = s.fileView(v)
		return nil
	})
	return
}

// DeleteFile removes only the selected Project file. An admitted atomic mutation
// finishes; cancellation while waiting for admission preserves the context cause.
func (s ProjectStore) DeleteFile(ctx context.Context, uid, revision string) (out ProjectFile, err error) {
	err = s.edit(ctx, uid, revision, nil, func(v projectEditState) error {
		if err := s.Delete(uid); err != nil {
			return editError("unreadable", err)
		}
		v.Content, v.Present, v.oversize = nil, false, nil
		out = s.fileView(v)
		return nil
	})
	return
}
func (s ProjectStore) edit(ctx context.Context, uid, revision string, check, fn func(projectEditState) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if revision == "" {
		return editError("invalid", errors.New("revision is required"))
	}
	apply := func(mutate bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, err := s.editState(uid)
		if err != nil {
			return err
		}
		if v.revision() != revision {
			return editError("stale", errors.New("project guidance changed; read or validate again"))
		}
		if check != nil {
			if err := check(v); err != nil {
				return err
			}
		}
		if mutate {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fn(v)
		}
		return nil
	}
	if err := apply(false); err != nil {
		return err
	}
	// All public guidance writers share the existing policy lock, without reading
	// or changing global user guidance, policy, fragments or migration receipts.
	lock := Store{configDir: filepath.Dir(s.configDir)}
	err := lock.withPolicyLockContext(ctx, func() error { return apply(true) })
	var refused *EditError
	if err != nil && !errors.As(err, &refused) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return editError("unreadable", err)
	}
	return err
}
