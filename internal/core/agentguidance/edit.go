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
	"slices"
	"strings"
	"unicode/utf8"
)

// EditError classifies failures of the public editing surface.
type EditError struct {
	Code string
	Err  error
}

func (e *EditError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *EditError) Unwrap() error { return e.Err }
func editError(code string, err error) error {
	if err == nil {
		return nil
	}
	return &EditError{Code: "agent-guidance-" + code, Err: err}
}

// File describes only the user file, independently of effective guidance.
type File struct {
	Path          string `json:"path"`
	Present       bool   `json:"present"`
	Text          string `json:"text"`
	Bytes         int    `json:"bytes"`
	Digest        string `json:"digest"`
	Revision      string `json:"revision"`
	MaxBytes      int    `json:"maxBytes"`
	DefaultText   string `json:"defaultText"`
	DefaultDigest string `json:"defaultDigest"`
}

// Validation describes a candidate and the current inputs it was checked against.
type Validation struct {
	Text                  string `json:"text"`
	Bytes                 int    `json:"bytes"`
	Digest                string `json:"digest"`
	Revision              string `json:"revision"`
	MaxBytes              int    `json:"maxBytes"`
	AssembledBytes        int    `json:"assembledBytes"`
	ProcessAssembledBytes int    `json:"processAssembledBytes"`
}

type editPart struct {
	Name string
	Text []byte
}
type editState struct {
	User      []byte
	Present   bool
	Policy    []byte
	Explicit  bool
	Fragments []editPart
	settings  Settings
}

func (v editState) revision() string {
	// Canonicalize empty bytes: a nil SDK candidate and a read empty file identify
	// the same existing user file, while Present still distinguishes absence.
	v.User = append([]byte{}, v.User...)
	v.Policy = append([]byte{}, v.Policy...)
	b, _ := json.Marshal(v)
	return Digest(b)
}

// editPartRead keeps reads bounded and confines symlinks to the config root.
func editPartRead(root *os.Root, name string) ([]byte, bool, error) {
	info, err := root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, editError("unreadable", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, editError("unreadable", fmt.Errorf("%s is not a regular file", name))
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, false, editError("unreadable", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, false, editError("unreadable", err)
	}
	if len(b) > MaxSize {
		return nil, false, editError("too-large", fmt.Errorf("%s exceeds %d bytes", name, MaxSize))
	}
	return b, true, nil
}
func (s Store) editState() (v editState, err error) {
	root, err := os.OpenRoot(s.configDir)
	if errors.Is(err, fs.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, editError("unreadable", err)
	}
	defer root.Close()
	v.User, v.Present, err = editPartRead(root, FileName)
	if err != nil {
		return v, err
	}
	if info, statErr := root.Lstat(SettingsFileName); statErr == nil && !info.Mode().IsRegular() {
		return v, editError("unreadable", errors.New("guidance settings is not a regular file"))
	}
	v.Policy, v.Explicit, err = editPartRead(root, SettingsFileName)
	if err != nil {
		return v, err
	}
	if v.Explicit {
		v.settings, err = decodeSettings(v.Policy)
		if err != nil {
			return v, editError("unreadable", err)
		}
	}
	// Whole-disabled guidance and masked fragments contribute no assembly bytes.
	if v.settings.Disabled {
		return v, nil
	}
	info, err := root.Lstat(FragmentsDirName)
	if errors.Is(err, fs.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, editError("unreadable", err)
	}
	if !info.IsDir() {
		return v, editError("unreadable", errors.New("guidance fragments is not a directory"))
	}
	dir, err := root.OpenRoot(FragmentsDirName)
	if err != nil {
		return v, editError("unreadable", err)
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		return v, editError("unreadable", err)
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return v, editError("unreadable", err)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || slices.Contains(v.settings.DisabledFragments, name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return v, editError("unreadable", err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		b, present, err := editPartRead(dir, name)
		if err != nil {
			return v, err
		}
		if !present {
			return v, editError("unreadable", fs.ErrNotExist)
		}
		v.Fragments = append(v.Fragments, editPart{name, b})
	}
	return v, nil
}
func (s Store) fileView(v editState) File {
	return File{Path: s.Path(), Present: v.Present, Text: string(v.User), Bytes: len(v.User), Digest: Digest(v.User), Revision: v.revision(), MaxBytes: MaxSize, DefaultText: string(Default()), DefaultDigest: Digest(Default())}
}

// ReadFile reads current edit inputs without migration, locks or other writes.
func (s Store) ReadFile(ctx context.Context) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	v, err := s.editState()
	if err != nil {
		return File{}, err
	}
	if !utf8.Valid(v.User) {
		return File{}, editError("unreadable", errors.New("existing user guidance is not UTF-8"))
	}
	return s.fileView(v), ctx.Err()
}
func validateCandidate(v editState, content []byte) (Validation, error) {
	if len(content) > MaxSize {
		return Validation{}, editError("too-large", fmt.Errorf("candidate is %d bytes; limit %d", len(content), MaxSize))
	}
	if !utf8.Valid(content) || bytes.ContainsRune(content, 0) {
		return Validation{}, editError("invalid", errors.New("candidate must be UTF-8 without NUL"))
	}
	out := Validation{Text: string(content), Bytes: len(content), Digest: Digest(content), Revision: v.revision(), MaxBytes: MaxSize}
	for _, process := range []bool{false, true} {
		g, err := assembleGuidance(v.settings, v.Explicit, content, true, process, func(appendPart func([]byte) error) error {
			for _, p := range v.Fragments {
				if err := appendPart(p.Text); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return Validation{}, editError("too-large", err)
		}
		if process {
			out.ProcessAssembledBytes = len(g.Text)
		} else {
			out.AssembledBytes = len(g.Text)
		}
	}
	return out, nil
}

// ValidateFile checks a candidate without creating any filesystem entries.
func (s Store) ValidateFile(ctx context.Context, content []byte) (Validation, error) {
	if err := ctx.Err(); err != nil {
		return Validation{}, err
	}
	v, err := s.editState()
	if err != nil {
		return Validation{}, err
	}
	out, err := validateCandidate(v, content)
	if err != nil {
		return out, err
	}
	return out, ctx.Err()
}

// WriteFile checks revision and both assemblies under the shared policy lock.
// Cancellation before admission writes nothing; an admitted atomic write finishes.
func (s Store) WriteFile(ctx context.Context, content []byte, revision string) (out File, err error) {
	content = bytes.Clone(content)
	err = s.edit(ctx, revision, func(v editState) error {
		_, err := validateCandidate(v, content)
		return err
	}, func(v editState) error {
		if err := s.Save(content); err != nil {
			return editError("unreadable", err)
		}
		v.User, v.Present = content, true
		out = s.fileView(v)
		return nil
	})
	return
}

// ResetFile retains Reset's multi-file limitation: removal can succeed before
// resetting policy fails. Fragment masks and migration receipts are preserved.
func (s Store) ResetFile(ctx context.Context, revision string) (out File, err error) {
	err = s.edit(ctx, revision, nil, func(v editState) error {
		if err := s.resetLocked(); err != nil {
			return editError("unreadable", err)
		}
		v, err := s.editState()
		if err != nil {
			return err
		}
		out = s.fileView(v)
		return nil
	})
	return
}
func (s Store) edit(ctx context.Context, revision string, check, fn func(editState) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if revision == "" {
		return editError("invalid", errors.New("revision is required"))
	}
	// Preflight refusals do not create even the lock or config directory.
	v, err := s.editState()
	if err != nil {
		return err
	}
	if v.revision() != revision {
		return editError("stale", errors.New("guidance inputs changed; read or validate again"))
	}
	if check != nil {
		if err := check(v); err != nil {
			return err
		}
	}
	err = s.withPolicyLockContext(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, err := s.editState()
		if err != nil {
			return err
		}
		if v.revision() != revision {
			return editError("stale", errors.New("guidance inputs changed; read or validate again"))
		}
		if check != nil {
			if err := check(v); err != nil {
				return err
			}
		}
		return fn(v)
	})
	var refused *EditError
	if err != nil && !errors.As(err, &refused) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return editError("unreadable", err)
	}
	return err
}
