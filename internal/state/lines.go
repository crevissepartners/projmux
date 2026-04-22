package state

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const fileMode = 0o644

// LinesFile stores newline-delimited text in a simple inspectable file.
type LinesFile struct {
	path string
}

// NewLinesFile builds a file-backed line store for the provided path.
func NewLinesFile(path string) LinesFile {
	return LinesFile{path: path}
}

// Path returns the on-disk location used by this line store.
func (f LinesFile) Path() string {
	return f.path
}

// Read returns all non-empty lines from the file. Missing files read as empty.
func (f LinesFile) Read() ([]string, error) {
	file, err := os.Open(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lines := make([]string, 0)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lines, nil
}

// Write replaces the file contents atomically with the provided lines.
func (f LinesFile) Write(lines []string) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return err
	}

	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()

	if err := temp.Chmod(fileMode); err != nil {
		_ = temp.Close()
		return err
	}

	writer := bufio.NewWriter(temp)
	for _, line := range lines {
		if _, err := writer.WriteString(line + "\n"); err != nil {
			_ = temp.Close()
			return err
		}
	}

	if err := writer.Flush(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempName, f.path); err != nil {
		return err
	}

	cleanup = false
	return nil
}
