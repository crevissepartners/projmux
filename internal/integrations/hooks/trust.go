package hooks

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
)

const (
	trustedProjectsFileName = "trusted-projects.json"
	maxHookPreviewBytes     = 2048
	maxHookPreviewLines     = 12
)

// ProjectHookDecision is the trust response for a project-local hook.
type ProjectHookDecision string

const (
	ProjectHookAllowOnce   ProjectHookDecision = "allow-once"
	ProjectHookAllowAlways ProjectHookDecision = "allow-always"
	ProjectHookDeny        ProjectHookDecision = "deny"
)

// ProjectHookPromptRequest describes the hook that needs trust confirmation.
type ProjectHookPromptRequest struct {
	RepoPath       string
	HookPath       string
	RelativePath   string
	SHA256         string
	PreviousSHA256 string
	Preview        string
}

// ProjectHookPrompt decides whether a project-local hook should run.
type ProjectHookPrompt func(ProjectHookPromptRequest) ProjectHookDecision

type trustedProjects map[string]trustedProject

type trustedProject struct {
	TrustedAt time.Time              `json:"trusted_at"`
	Files     map[string]trustedFile `json:"files"`
}

type trustedFile struct {
	SHA256    string    `json:"sha256"`
	TrustedAt time.Time `json:"trusted_at"`
}

func (r *PostCreateRunner) authorizeProjectHook(h projectPostCreateHook) bool {
	sum, preview, err := hashHookFile(h.path)
	if err != nil {
		warnf(r.Logger, "project hook %q could not be hashed: %v", h.path, err)
		return false
	}

	storePath := strings.TrimSpace(r.TrustStorePath)
	if storePath == "" {
		storePath = defaultTrustStorePath()
	}
	if storePath == "" {
		warnf(r.Logger, "project hook %q requires trust, but the trust store path could not be resolved", h.path)
		return false
	}

	store, err := loadTrustedProjects(storePath)
	if err != nil {
		warnf(r.Logger, "project hook %q requires trust, but the trust store could not be read: %v", h.path, err)
		return false
	}
	if existing, ok := store.trustedFile(h.repo, h.rel); ok && existing.SHA256 == sum {
		return true
	}

	req := ProjectHookPromptRequest{
		RepoPath:       h.repo,
		HookPath:       h.path,
		RelativePath:   h.rel,
		SHA256:         sum,
		PreviousSHA256: store.previousHash(h.repo, h.rel),
		Preview:        preview,
	}
	decision, prompted := r.promptProjectHookTrust(req)
	if !prompted {
		if req.PreviousSHA256 != "" {
			warnf(r.Logger, "project hook %q hash changed; trusted sha256=%s current sha256=%s; skipping in non-interactive context", h.rel, req.PreviousSHA256, req.SHA256)
		} else {
			warnf(r.Logger, "project hook %q requires trust; skipping in non-interactive context", h.rel)
		}
		return false
	}

	switch decision {
	case ProjectHookAllowOnce:
		return true
	case ProjectHookAllowAlways:
		store.trust(h.repo, h.rel, sum, time.Now().UTC())
		if err := store.save(storePath); err != nil {
			warnf(r.Logger, "project hook %q trust could not be saved: %v", h.rel, err)
			return false
		}
		return true
	default:
		return false
	}
}

func projectHooksDisabled(settingPath string, logger io.Writer) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PROJMUX_PROJECT_HOOKS")), "off") {
		return true
	}
	settingPath = strings.TrimSpace(settingPath)
	if settingPath == "" {
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			return false
		}
		settingPath = paths.ProjectHooksFile()
	}
	mode, err := config.LoadProjectHooksFile(settingPath)
	if err != nil {
		warnf(logger, "project hook setting could not be read: %v", err)
		return false
	}
	return mode == config.ProjectHooksOff
}

func defaultTrustStorePath() string {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return ""
	}
	return filepath.Join(paths.StateDir, trustedProjectsFileName)
}

func loadTrustedProjects(path string) (trustedProjects, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return trustedProjects{}, nil
		}
		return nil, err
	}
	defer file.Close()

	var store trustedProjects
	dec := json.NewDecoder(file)
	if err := dec.Decode(&store); err != nil {
		if errors.Is(err, io.EOF) {
			return trustedProjects{}, nil
		}
		return nil, err
	}
	if store == nil {
		store = trustedProjects{}
	}
	return store, nil
}

func (s trustedProjects) save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, trustedProjectsFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s trustedProjects) trustedFile(repo, rel string) (trustedFile, bool) {
	project, ok := s[repo]
	if !ok || project.Files == nil {
		return trustedFile{}, false
	}
	file, ok := project.Files[rel]
	return file, ok
}

func (s trustedProjects) previousHash(repo, rel string) string {
	file, ok := s.trustedFile(repo, rel)
	if !ok {
		return ""
	}
	return file.SHA256
}

func (s trustedProjects) trust(repo, rel, sum string, at time.Time) {
	project := s[repo]
	project.TrustedAt = at
	if project.Files == nil {
		project.Files = map[string]trustedFile{}
	}
	project.Files[rel] = trustedFile{
		SHA256:    sum,
		TrustedAt: at,
	}
	s[repo] = project
}

func hashHookFile(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	hasher := sha256.New()
	preview := bytes.Buffer{}
	if err := copyHashAndPreview(hasher, &preview, file); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), sanitizeHookPreview(preview.String()), nil
}

func copyHashAndPreview(hasher hash.Hash, preview *bytes.Buffer, src io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, writeErr := hasher.Write(chunk); writeErr != nil {
				return writeErr
			}
			if preview.Len() < maxHookPreviewBytes {
				remaining := min(maxHookPreviewBytes-preview.Len(), len(chunk))
				_, _ = preview.Write(chunk[:remaining])
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func sanitizeHookPreview(raw string) string {
	raw = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 32 || r == 127:
			return ' '
		default:
			return r
		}
	}, raw)

	lines := strings.Split(raw, "\n")
	if len(lines) > maxHookPreviewLines {
		lines = append(lines[:maxHookPreviewLines], "...")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (r *PostCreateRunner) promptProjectHookTrust(req ProjectHookPromptRequest) (ProjectHookDecision, bool) {
	if r.ProjectHookPrompt != nil {
		return r.ProjectHookPrompt(req), true
	}

	reader := r.PromptReader
	if reader == nil {
		reader = os.Stdin
	}
	if !isInteractiveReader(reader) {
		return ProjectHookDeny, false
	}

	writer := r.PromptWriter
	if writer == nil {
		writer = os.Stderr
	}
	return terminalProjectHookPrompt(reader, writer, req), true
}

func isInteractiveReader(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func terminalProjectHookPrompt(reader io.Reader, writer io.Writer, req ProjectHookPromptRequest) ProjectHookDecision {
	fmt.Fprintln(writer, "projmux: project-local hook requires trust")
	fmt.Fprintf(writer, "repo: %s\n", req.RepoPath)
	fmt.Fprintf(writer, "hook: %s\n", req.RelativePath)
	if req.PreviousSHA256 != "" {
		fmt.Fprintf(writer, "trusted sha256: %s\n", req.PreviousSHA256)
	}
	fmt.Fprintf(writer, "current sha256: %s\n", req.SHA256)
	if strings.TrimSpace(req.Preview) != "" {
		fmt.Fprintln(writer, "preview:")
		for line := range strings.SplitSeq(req.Preview, "\n") {
			fmt.Fprintf(writer, "  %s\n", line)
		}
	}

	input := bufio.NewReader(reader)
	for range 3 {
		fmt.Fprint(writer, "Allow this hook? [o]nce/[a]lways/[d]eny: ")
		line, err := input.ReadString('\n')
		if err != nil && len(line) == 0 {
			fmt.Fprintln(writer)
			return ProjectHookDeny
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "o", "once":
			return ProjectHookAllowOnce
		case "a", "always":
			return ProjectHookAllowAlways
		case "d", "deny", "n", "no":
			return ProjectHookDeny
		}
		fmt.Fprintln(writer, "Please enter once, always, or deny.")
	}
	return ProjectHookDeny
}
