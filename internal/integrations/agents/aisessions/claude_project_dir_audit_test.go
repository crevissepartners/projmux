package aisessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeProjectDirNamesMatchRealStore is opt-in, read-only evidence that
// EncodeClaudeProjectPath reproduces the project directory names the installed
// Claude Code wrote. For every top-level transcript it takes the first recorded
// cwd (the launch cwd) and compares its encoding with the enclosing directory
// name. Only counts are logged: no path, directory name, or transcript content.
// It never writes anything.
func TestClaudeProjectDirNamesMatchRealStore(t *testing.T) {
	if os.Getenv("PROJMUX_CLAUDE_PROJECT_DIR_AUDIT") != "1" {
		t.Skip("set PROJMUX_CLAUDE_PROJECT_DIR_AUDIT=1 for a read-only audit of the real Claude projects directory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	dirs, err := os.ReadDir(projectsDir)
	if err != nil {
		t.Fatalf("read Claude projects directory: %v", errors.Unwrap(err))
	}
	var dirsTotal, checked, withoutCWD, matched, mismatched, dirsWithMismatch int
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		dirsTotal++
		dirName := dir.Name()
		entries, err := os.ReadDir(filepath.Join(projectsDir, dirName))
		if err != nil {
			t.Fatalf("read a Claude project directory: %v", errors.Unwrap(err))
		}
		dirMismatch := false
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			cwd, err := firstRecordedClaudeCWD(filepath.Join(projectsDir, dirName, entry.Name()))
			if err != nil {
				t.Fatalf("read a Claude transcript: %v", errors.Unwrap(err))
			}
			if cwd == "" {
				withoutCWD++
				continue
			}
			checked++
			if EncodeClaudeProjectPath(cwd) == dirName {
				matched++
			} else {
				mismatched++
				dirMismatch = true
			}
		}
		if dirMismatch {
			dirsWithMismatch++
		}
	}
	t.Logf("dirs_total=%d transcripts_checked=%d transcripts_without_cwd=%d", dirsTotal, checked, withoutCWD)
	t.Logf("matched=%d mismatched=%d dirs_with_mismatch=%d", matched, mismatched, dirsWithMismatch)
	if mismatched > 0 {
		t.Fatalf("%d transcripts sit in a directory whose name differs from EncodeClaudeProjectPath(launch cwd)", mismatched)
	}
}

// firstRecordedClaudeCWD returns the first non-empty top-level cwd recorded in
// a Claude transcript, or "" when no record carries one.
func firstRecordedClaudeCWD(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			var record struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal([]byte(trimmed), &record) == nil && record.CWD != "" {
				return record.CWD, nil
			}
		}
		if readErr == io.EOF {
			return "", nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}
