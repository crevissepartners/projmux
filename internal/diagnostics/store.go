package diagnostics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	lockBudget    = 200 * time.Millisecond
	lockRetry     = 5 * time.Millisecond
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
	path       string
	lockBudget time.Duration
	lockRetry  time.Duration
	now        func() time.Time
	sleep      func(time.Duration)
}

func NewStore(path string) *Store {
	return &Store{path: path, lockBudget: lockBudget, lockRetry: lockRetry, now: time.Now, sleep: time.Sleep}
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
	file, err := os.OpenFile(s.path, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open diagnostics tail: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat diagnostics tail: %w", err)
	}
	if info.Size() == 0 {
		return nil
	}
	last := []byte{0}
	if _, err := file.ReadAt(last, info.Size()-1); err != nil {
		return fmt.Errorf("read diagnostics tail byte: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}

	const chunkSize = int64(4096)
	keep := int64(0)
	for end := info.Size(); end > 0; {
		start := max(end-chunkSize, 0)
		chunk := make([]byte, end-start)
		if _, err := file.ReadAt(chunk, start); err != nil {
			return fmt.Errorf("read diagnostics tail chunk: %w", err)
		}
		if newline := bytes.LastIndexByte(chunk, '\n'); newline >= 0 {
			keep = start + int64(newline) + 1
			break
		}
		end = start
	}
	if err := file.Truncate(keep); err != nil {
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
	return decodeCompleteRecords(file, home).Events, nil
}

// ReadResult describes a tolerant journal read without exposing malformed
// input. Report consumers use the counts to explain omissions while the event
// decoder remains shared with the interactive viewer.
type ReadResult struct {
	Events    []Event
	Malformed int
	Truncated bool
	Missing   bool
}

// ReadOnly reads valid complete records without creating, locking, chmodding,
// repairing, or truncating any source path. It is the support-report boundary;
// Read intentionally keeps its historical best-effort permission repair for
// the diagnostics log viewer.
func (s *Store) ReadOnly() (ReadResult, error) {
	if err := s.rejectSymlinks(); err != nil {
		return ReadResult{}, err
	}
	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReadResult{Missing: true}, nil
		}
		return ReadResult{}, fmt.Errorf("open diagnostics log: %w", err)
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

func decodeCompleteRecords(reader io.Reader, home string) ReadResult {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	var result ReadResult
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = bytes.TrimSpace(line)
			var event Event
			if len(line) > 0 && json.Unmarshal(line, &event) == nil {
				if safe, validationErr := sanitizeEvent(event, home); validationErr == nil {
					result.Events = append(result.Events, safe)
				} else {
					result.Malformed++
				}
			} else if len(line) > 0 {
				result.Malformed++
			}
		} else if len(line) > 0 {
			result.Truncated = true
		}
		if err != nil {
			return result
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
	if err := replaceFile(tmpPath, s.path); err != nil {
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
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}

func (s *Store) withLock(action func() error) error {
	lockPath := s.path + lockSuffix
	// #nosec G304 -- lockPath is the fixed .lock sibling of the caller-scoped diagnostics path after Append creates the private directory and rejects symlinks; no event payload controls it.
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open diagnostics lock: %w", err)
	}
	defer lock.Close()
	bestEffortPrivateFile(lockPath)

	deadline := s.now().Add(s.lockBudget)
	for {
		err := tryPlatformLock(lock)
		if err == nil {
			defer unlockPlatformLock(lock)
			return action()
		}
		if !errors.Is(err, errLockBusy) {
			return fmt.Errorf("lock diagnostics log: %w", err)
		}
		remaining := deadline.Sub(s.now())
		if remaining <= 0 {
			return errLockBusy
		}
		delay := min(s.lockRetry, remaining)
		s.sleep(delay)
	}
}

func bestEffortPrivateDir(path string) {
	if runtime.GOOS != "windows" {
		// #nosec G302 -- 0700 is the intentional private mode for the local diagnostics state directory.
		_ = os.Chmod(path, 0o700)
	}
}

func bestEffortPrivateFile(path string) {
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o600)
	}
}
