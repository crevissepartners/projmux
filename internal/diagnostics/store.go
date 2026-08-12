package diagnostics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	LogDirName    = "logs"
	LogFileName   = "operations.jsonl"
	MaxLogSize    = 5 * 1024 * 1024
	RetainLogSize = 2 * 1024 * 1024
	lockSuffix    = ".lock"
)

var errLockBusy = errors.New("diagnostics lock busy")

// DefaultPath resolves the private operations journal without creating it.
func DefaultPath(lookupEnv func(string) string, homeDir func() (string, error)) (string, error) {
	stateHome := ""
	if lookupEnv != nil {
		stateHome = strings.TrimSpace(lookupEnv("XDG_STATE_HOME"))
	}
	if stateHome == "" {
		if homeDir == nil {
			homeDir = os.UserHomeDir
		}
		home, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", errors.New("home directory is required when XDG_STATE_HOME is unset")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "projmux", LogDirName, LogFileName), nil
}

// Store owns append, bounded retention, and record reading for one log path.
type Store struct {
	path         string
	lockAttempts int
	staleAfter   time.Duration
	sleep        func(time.Duration)
}

func NewStore(path string) *Store {
	return &Store{path: path, lockAttempts: 250, staleAfter: 30 * time.Second, sleep: time.Sleep}
}

// Append writes exactly one complete JSONL record while holding an
// inter-process lock across permission repair, append, and possible trim.
func (s *Store) Append(event Event) error {
	home, _ := os.UserHomeDir()
	safe, err := sanitizeEvent(event, home)
	if err != nil {
		return err
	}
	record, err := json.Marshal(safe)
	if err != nil {
		return fmt.Errorf("encode diagnostics event: %w", err)
	}
	record = append(record, '\n')

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create diagnostics log directory: %w", err)
	}
	if err := s.rejectSymlinks(); err != nil {
		return err
	}
	s.repairPrivateDirs()
	return s.withLock(func() error {
		if err := s.rejectSymlinks(); err != nil {
			return err
		}
		s.repairPrivateDirs()
		bestEffortPrivateFile(s.path)
		if err := s.repairTruncatedTail(); err != nil {
			return err
		}
		file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open diagnostics log: %w", err)
		}
		if _, err := file.Write(record); err != nil {
			_ = file.Close()
			return fmt.Errorf("append diagnostics log: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close diagnostics log: %w", err)
		}
		bestEffortPrivateFile(s.path)
		return s.trimIfNeeded()
	})
}

func (s *Store) repairTruncatedTail() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) || len(data) == 0 || data[len(data)-1] == '\n' {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read diagnostics tail: %w", err)
	}
	last := bytes.LastIndexByte(data, '\n')
	keep := int64(0)
	if last >= 0 {
		keep = int64(last + 1)
	}
	if err := os.Truncate(s.path, keep); err != nil {
		return fmt.Errorf("repair diagnostics tail: %w", err)
	}
	return nil
}

// Read returns valid complete records in file order. Malformed lines and an
// unterminated tail are ignored so a viewer remains useful after corruption.
func (s *Store) Read() ([]Event, error) {
	if err := s.rejectSymlinks(); err != nil {
		return nil, err
	}
	s.repairPrivateDirs()
	bestEffortPrivateFile(s.path)
	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open diagnostics log: %w", err)
	}
	defer file.Close()
	home, _ := os.UserHomeDir()
	return decodeCompleteRecords(file, home), nil
}

func (s *Store) rejectSymlinks() error {
	paths := []string{s.path, filepath.Dir(s.path)}
	if filepath.Base(filepath.Dir(s.path)) == LogDirName {
		paths = append(paths, filepath.Dir(filepath.Dir(s.path)))
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect diagnostics path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlinked diagnostics path: %s", filepath.Base(path))
		}
	}
	return nil
}

func (s *Store) repairPrivateDirs() {
	logsDir := filepath.Dir(s.path)
	if filepath.Base(logsDir) == LogDirName {
		bestEffortPrivateDir(filepath.Dir(logsDir))
	}
	bestEffortPrivateDir(logsDir)
}

func decodeCompleteRecords(reader io.Reader, home string) []Event {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	var events []Event
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = bytes.TrimSpace(line)
			var event Event
			if len(line) > 0 && json.Unmarshal(line, &event) == nil {
				if safe, validationErr := sanitizeEvent(event, home); validationErr == nil {
					events = append(events, safe)
				}
			}
		}
		if err != nil {
			return events
		}
	}
}

func (s *Store) trimIfNeeded() error {
	info, err := os.Stat(s.path)
	if err != nil || info.Size() <= MaxLogSize {
		return err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read diagnostics log for trim: %w", err)
	}
	start := max(len(data)-RetainLogSize, 0)
	if start > 0 {
		newline := bytes.IndexByte(data[start:], '\n')
		if newline < 0 {
			start = len(data)
		} else {
			start += newline + 1
		}
	}
	retained := completeValidLines(data[start:])
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".operations-*.tmp")
	if err != nil {
		return fmt.Errorf("create diagnostics trim file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod diagnostics trim file: %w", err)
	}
	if _, err := tmp.Write(retained); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write diagnostics trim file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close diagnostics trim file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace diagnostics log: %w", err)
	}
	bestEffortPrivateFile(s.path)
	return nil
}

func completeValidLines(data []byte) []byte {
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		return nil
	}
	lines := bytes.Split(data[:last], []byte{'\n'})
	out := make([]byte, 0, last+1)
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		var event Event
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		safe, err := sanitizeEvent(event, "")
		if err != nil {
			continue
		}
		line, err = json.Marshal(safe)
		if err != nil {
			continue
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}

func (s *Store) withLock(action func() error) error {
	lockPath := s.path + lockSuffix
	for attempt := 0; attempt < s.lockAttempts; attempt++ {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(lock, "%d\n", os.Getpid())
			_ = lock.Close()
			defer os.Remove(lockPath)
			return action()
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create diagnostics lock: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > s.staleAfter {
			_ = os.Remove(lockPath)
			continue
		}
		s.sleep(time.Duration(1+rand.Intn(min(2+attempt/5, 50))) * time.Millisecond)
	}
	return errLockBusy
}

func bestEffortPrivateDir(path string) {
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o700)
	}
}

func bestEffortPrivateFile(path string) {
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o600)
	}
}
