package agentguidance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// FragmentsDirName holds app-managed guidance fragments below ConfigDir.
const FragmentsDirName = "agent-guidance.d"

func (s Store) load(process bool) (Guidance, error) {
	settings, explicit, err := s.LoadSettings()
	if err != nil {
		return Guidance{}, err
	}
	if settings.Disabled {
		return Guidance{Source: SourceOff}, nil
	}
	root, err := os.OpenRoot(s.configDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Guidance{}, fmt.Errorf("read agent guidance: %w", err)
	}
	var user []byte
	var exists bool
	if root != nil {
		defer root.Close()
		user, exists, err = readGuidancePart(root, FileName)
		if err != nil {
			return Guidance{}, err
		}
	}
	if !explicit && exists && len(bytes.TrimSpace(user)) == 0 {
		return Guidance{Source: SourceOff}, nil
	}
	text := Default()
	if process {
		text = append(text, processText...)
	}
	source := SourceDefault
	appendPart := func(part []byte) error {
		if len(part) == 0 {
			return nil
		}
		if len(text)+2+len(part) > MaxSize {
			return fmt.Errorf("assembled agent guidance exceeds the limit of %d bytes", MaxSize)
		}
		text = append(text, '\n', '\n')
		text = append(text, part...)
		source = SourceFile
		return nil
	}
	if root != nil {
		if err := appendFragments(root, settings, appendPart); err != nil {
			return Guidance{}, err
		}
	}
	if len(bytes.TrimSpace(user)) != 0 {
		if err := appendPart(user); err != nil {
			return Guidance{}, err
		}
	}
	if len(text) > MaxSize {
		return Guidance{}, fmt.Errorf("assembled agent guidance exceeds the limit of %d bytes", MaxSize)
	}
	return Guidance{Text: text, Source: source, Digest: Digest(text)}, nil
}

func appendFragments(root *os.Root, settings Settings, appendPart func([]byte) error) error {
	info, err := root.Lstat(FragmentsDirName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read agent guidance fragments: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agent guidance fragments %s is not a directory", FragmentsDirName)
	}
	dir, err := root.OpenRoot(FragmentsDirName)
	if err != nil {
		return fmt.Errorf("read agent guidance fragments: %w", err)
	}
	defer dir.Close()
	file, err := dir.Open(".")
	if err != nil {
		return fmt.Errorf("read agent guidance fragments: %w", err)
	}
	entries, err := file.ReadDir(-1)
	_ = file.Close()
	if err != nil {
		return fmt.Errorf("read agent guidance fragments: %w", err)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || slices.Contains(settings.DisabledFragments, name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("read agent guidance fragment %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		content, exists, err := readGuidancePart(dir, name)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("read agent guidance fragment %s: %w", name, fs.ErrNotExist)
		}
		if err := appendPart(content); err != nil {
			return err
		}
	}
	return nil
}

// readGuidancePart bounds reads as well as checking metadata: a file can grow
// after stat. os.Root confines any links in the existing user-file path.
func readGuidancePart(root *os.Root, name string) ([]byte, bool, error) {
	info, err := root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read agent guidance %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("agent guidance %s is not a regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, false, fmt.Errorf("read agent guidance %s: %w", name, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	if err != nil {
		return nil, false, fmt.Errorf("read agent guidance %s: %w", name, err)
	}
	if len(content) > MaxSize {
		return nil, false, fmt.Errorf("agent guidance %s exceeds the limit of %d bytes", name, MaxSize)
	}
	return content, true, nil
}
