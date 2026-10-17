package agentguidance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// ProjectFile keeps exact stored bytes separate from Load's effective guidance.
// Revision identifies the selected UID, presence and raw bytes.
type ProjectFile struct {
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
	UID     string
	Present bool
	Content []byte
}

func (v projectEditState) revision() string {
	v.Content = append([]byte{}, v.Content...)
	b, _ := json.Marshal(v)
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
	v.Content, v.Present, err = editPartRead(root, filepath.Base(path))
	return v, err
}
func (s ProjectStore) fileView(v projectEditState) ProjectFile {
	path, _ := s.Path(v.UID)
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
		v.Content, v.Present = content, true
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
		v.Content, v.Present = nil, false
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
