package transcript

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Path resolves where a provider keeps this Agent's conversation.
//
// Claude and Antigravity report an absolute path through their hooks, and the
// Agent keeps it in status.sessionRef. Codex reports only a thread and session
// id and writes rollout files under a dated tree below home, so the path has to
// be found by matching that id in the file name.
//
// The provider comes from spec, which is what the Agent was created for; the
// session ref's own discriminator is the fallback for a document that predates
// the spec field. home is passed in rather than read from the process so a
// caller decides whose provider state is being read.
func Path(agent coremetadata.Agent, home string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(agent.Spec.Provider))
	ref := agent.Status.SessionRef
	if provider == "" && ref != nil {
		provider = strings.ToLower(strings.TrimSpace(ref.Provider))
	}
	switch provider {
	case "claude":
		if ref != nil && ref.Claude != nil && ref.Claude.TranscriptPath != "" {
			return ref.Claude.TranscriptPath, nil
		}
		return "", errors.New("claude agent has no transcript path yet")
	case "antigravity":
		if ref != nil && ref.Antigravity != nil && ref.Antigravity.TranscriptPath != "" {
			return ref.Antigravity.TranscriptPath, nil
		}
		return "", errors.New("antigravity agent has no transcript path yet")
	case "codex":
		var id string
		if ref != nil && ref.Codex != nil {
			id = strings.TrimSpace(ref.Codex.SessionID)
			if id == "" {
				id = strings.TrimSpace(ref.Codex.ThreadID)
			}
		}
		if id == "" {
			return "", errors.New("codex agent has no session id yet")
		}
		return findCodexRollout(home, id)
	case "":
		return "", errors.New("agent has no provider")
	}
	return "", fmt.Errorf("provider %q has no known transcript", provider)
}

// findCodexRollout locates the rollout file whose name ends with the session
// id. Codex names each file rollout-<timestamp>-<id>.jsonl, so when a session
// has been resumed into several files the lexically last one is the newest.
func findCodexRollout(home, sessionID string) (string, error) {
	if home == "" {
		return "", errors.New("home directory is required to find a codex rollout")
	}
	// The id becomes part of a suffix match; a separator in it would let the
	// match reach outside the file name.
	if strings.ContainsAny(sessionID, `/\`) {
		return "", fmt.Errorf("codex session id %q is not a file name component", sessionID)
	}
	root := filepath.Join(home, ".codex", "sessions")
	var found []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable branch: keep walking
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, sessionID+".jsonl") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no codex rollout file for session %s", sessionID)
	}
	sort.Strings(found)
	return found[len(found)-1], nil
}
