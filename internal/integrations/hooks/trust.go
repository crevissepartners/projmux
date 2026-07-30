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
	"github.com/crevissepartners/projmux/internal/core/terminaltext"
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
	ArtifactKind   string
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

func (r *Runner) authorizeProjectConfig(event Event, h projectConfigFile) bool {
	return r.authorizeProjectFile(event, h.repo, h.rel, h.path, "project config")
}

func (r *Runner) authorizeProjectFile(event Event, repo, rel, path, kind string) bool {
	sum, preview, err := hashHookFile(path)
	if err != nil {
		r.warnf(event, "%s %q could not be hashed: %v", kind, path, err)
		return false
	}
	return r.authorizeProjectDigest(event, repo, rel, path, kind, sum, preview)
}

func (r *Runner) authorizeProjectDigest(event Event, repo, rel, path, kind, sum, preview string) bool {
	if r.authorizedProjectFile(repo, rel, sum) {
		return true
	}

	storePath := strings.TrimSpace(r.TrustStorePath)
	if storePath == "" {
		storePath = defaultTrustStorePath()
	}
	if storePath == "" {
		r.warnf(event, "%s %q requires trust, but the trust store path could not be resolved", kind, path)
		return false
	}

	store, err := loadTrustedProjects(storePath)
	if err != nil {
		r.warnf(event, "%s %q requires trust, but the trust store could not be read: %v", kind, path, err)
		return false
	}
	if existing, ok := store.trustedFile(repo, rel); ok && existing.SHA256 == sum {
		r.rememberAuthorizedProjectFile(repo, rel, sum)
		return true
	}

	req := ProjectHookPromptRequest{
		RepoPath:       repo,
		HookPath:       path,
		RelativePath:   rel,
		ArtifactKind:   kind,
		SHA256:         sum,
		PreviousSHA256: store.previousHash(repo, rel),
		Preview:        preview,
	}
	decision, prompted := r.promptProjectHookTrust(req)
	if !prompted {
		if req.PreviousSHA256 != "" {
			r.warnf(event, "%s %q hash changed; trusted sha256=%s current sha256=%s; skipping in non-interactive context", kind, rel, req.PreviousSHA256, req.SHA256)
		} else {
			r.warnf(event, "%s %q requires trust; skipping in non-interactive context", kind, rel)
		}
		return false
	}

	switch decision {
	case ProjectHookAllowOnce:
		r.rememberAuthorizedProjectFile(repo, rel, sum)
		return true
	case ProjectHookAllowAlways:
		store.trust(repo, rel, sum, time.Now().UTC())
		if err := store.save(storePath); err != nil {
			r.warnf(event, "%s %q trust could not be saved: %v", kind, rel, err)
			return false
		}
		r.rememberAuthorizedProjectFile(repo, rel, sum)
		return true
	default:
		return false
	}
}

// AuthorizeProjectLayoutArtifact gates executable commands parsed from one
// project-local layout. contents must be the exact bytes used to parse the
// in-memory layout that will be restored; this function never reopens path.
//
// Unlike project hook discovery, this gate is intentionally independent of
// PROJMUX_PROJECT_HOOKS and the Labs project-hooks toggle.
func (r *Runner) AuthorizeProjectLayoutArtifact(repoPath, relativePath, path string, contents []byte, commands []string) (bool, error) {
	if r == nil {
		return false, errors.New("project layout trust authorizer is not configured")
	}
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return false, errors.New("repo path is required")
	}
	repo, err := filepath.Abs(repoPath)
	if err != nil {
		return false, err
	}
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relativePath)))
	if rel == "." || rel == "" || !strings.HasPrefix(rel, ".projmux/layouts/") || filepath.Ext(rel) != ".toml" {
		return false, fmt.Errorf("invalid project layout artifact path %q", relativePath)
	}
	expectedPath := filepath.Join(repo, filepath.FromSlash(rel))
	absolutePath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return false, err
	}
	if absolutePath != expectedPath {
		return false, fmt.Errorf("project layout artifact path %q does not match %q", absolutePath, expectedPath)
	}
	if len(commands) == 0 {
		return true, nil
	}
	sum := sha256.Sum256(contents)
	preview := layoutCommandPreview(commands)
	return r.authorizeProjectDigest(
		EventPreCreate,
		repo,
		rel,
		absolutePath,
		"project layout",
		hex.EncodeToString(sum[:]),
		preview,
	), nil
}

func layoutCommandPreview(commands []string) string {
	lines := []string{"commands to run:"}
	for _, command := range commands {
		lines = append(lines, "  "+terminaltext.EscapeControls(command))
	}
	return strings.Join(lines, "\n")
}

func (r *Runner) authorizedProjectFile(repo, rel, sum string) bool {
	r.trustMu.Lock()
	defer r.trustMu.Unlock()
	if r.authorized == nil {
		return false
	}
	return r.authorized[repo+"\x00"+rel] == sum
}

func (r *Runner) rememberAuthorizedProjectFile(repo, rel, sum string) {
	r.trustMu.Lock()
	defer r.trustMu.Unlock()
	if r.authorized == nil {
		r.authorized = map[string]string{}
	}
	r.authorized[repo+"\x00"+rel] = sum
}

func projectHooksDisabled(event Event, settingPath string, logger io.Writer) bool {
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
		warnf(logger, event, "project hook setting could not be read: %v", err)
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

// trustProjectFile hashes the file at repoPath/relPath and records the hash
// in the trust store so the runner will accept it on the next invocation.
// relPath must be a slash-separated path relative to the repo root (e.g.
// ".projmux/config.toml"). An empty relPath is rejected to keep callers
// explicit about which surface they trust.
//
// This is an internal helper. The only public surface is TrustProjectConfig,
// which targets the well-known .projmux/config.toml file. Script files are no
// longer a trust surface — the runner refuses to execute them and the
// migrator flips them to declarative entries instead.
func trustProjectFile(repoPath, relPath, trustStorePath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", errors.New("repo path is required")
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", errors.New("relative path is required")
	}
	repo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", err
	}
	rel := filepath.ToSlash(filepath.Clean(relPath))
	path := filepath.Join(repo, filepath.FromSlash(rel))
	sum, _, err := hashHookFile(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(trustStorePath) == "" {
		trustStorePath = defaultTrustStorePath()
	}
	if strings.TrimSpace(trustStorePath) == "" {
		return "", errors.New("trust store path could not be resolved")
	}
	store, err := loadTrustedProjects(trustStorePath)
	if err != nil {
		return "", err
	}
	store.trust(repo, rel, sum, time.Now().UTC())
	if err := store.save(trustStorePath); err != nil {
		return "", err
	}
	return sum, nil
}

// TrustProjectConfig hashes the project-local .projmux/config.toml file and
// records the hash so the runner will accept it on the next invocation.
func TrustProjectConfig(repoPath, trustStorePath string) (string, error) {
	return trustProjectFile(repoPath, projectConfigRelativePath, trustStorePath)
}

// AuthorizeProjectConfig prompts for trust on the project-local
// .projmux/config.toml file without running any lifecycle hook. It shares the
// same authorization cache and trust-store policy as Run, so an allow-once
// decision made here is honored by the subsequent lifecycle hooks in this
// Runner instance.
func (r *Runner) AuthorizeProjectConfig(repoPath string) (bool, error) {
	if r == nil {
		return true, nil
	}
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return false, errors.New("repo path is required")
	}
	if !r.DiscoverProjectHooks || projectHooksDisabled(EventPreCreate, r.ProjectHooksFilePath, r.Logger) {
		return true, nil
	}
	repo, err := filepath.Abs(repoPath)
	if err != nil {
		return false, err
	}
	rel := projectConfigRelativePath
	path := filepath.Join(repo, filepath.FromSlash(rel))
	info, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) || (statErr == nil && info.IsDir()) {
		return true, nil
	}
	if statErr != nil {
		return false, statErr
	}
	return r.authorizeProjectConfig(EventPreCreate, projectConfigFile{
		repo: repo,
		rel:  rel,
		path: path,
	}), nil
}

// ProjectConfigTrustState classifies the trust standing of a project's
// .projmux/config.toml file relative to the trust store.
type ProjectConfigTrustState string

const (
	// ProjectConfigTrustAbsent means the project does not have a
	// .projmux/config.toml file on disk, so trust is moot.
	ProjectConfigTrustAbsent ProjectConfigTrustState = "absent"
	// ProjectConfigTrustUntrusted means a config.toml exists but the trust
	// store has no entry for it (or the store could not be opened in a way
	// that would let a stored hash exist).
	ProjectConfigTrustUntrusted ProjectConfigTrustState = "untrusted"
	// ProjectConfigTrustTrusted means the stored hash matches the current
	// file contents — the runner will accept the config without prompting.
	ProjectConfigTrustTrusted ProjectConfigTrustState = "trusted"
	// ProjectConfigTrustStale means an entry exists but the on-disk file
	// hash differs from the stored hash; the runner will prompt before
	// running anything declared in the changed file.
	ProjectConfigTrustStale ProjectConfigTrustState = "stale"
)

// ProjectConfigTrustReport bundles the trust verdict together with the
// hashes a caller needs to render meaningful UI (e.g. previous-vs-current
// in a Settings badge).
type ProjectConfigTrustReport struct {
	State       ProjectConfigTrustState
	CurrentHash string
	StoredHash  string
}

// InspectProjectConfigTrust reports whether the project-local
// .projmux/config.toml file is registered in the trust store and whether
// the recorded hash still matches the file on disk. It does not mutate
// the trust store. The function is read-only on purpose so a UI surface
// (Settings Project tab) can render a "Trust" badge without side effects.
func InspectProjectConfigTrust(repoPath, trustStorePath string) (ProjectConfigTrustReport, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return ProjectConfigTrustReport{}, errors.New("repo path is required")
	}
	repo, err := filepath.Abs(repoPath)
	if err != nil {
		return ProjectConfigTrustReport{}, err
	}
	rel := projectConfigRelativePath
	path := filepath.Join(repo, filepath.FromSlash(rel))

	info, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) || (statErr == nil && info.IsDir()) {
		return ProjectConfigTrustReport{State: ProjectConfigTrustAbsent}, nil
	}
	if statErr != nil {
		return ProjectConfigTrustReport{}, statErr
	}

	currentHash, _, err := hashHookFile(path)
	if err != nil {
		return ProjectConfigTrustReport{}, err
	}

	storePath := strings.TrimSpace(trustStorePath)
	if storePath == "" {
		storePath = defaultTrustStorePath()
	}
	if storePath == "" {
		// No trust store available — treat the config as untrusted so the
		// caller surfaces a "register required" badge instead of silently
		// claiming trust.
		return ProjectConfigTrustReport{
			State:       ProjectConfigTrustUntrusted,
			CurrentHash: currentHash,
		}, nil
	}
	store, err := loadTrustedProjects(storePath)
	if err != nil {
		return ProjectConfigTrustReport{}, err
	}
	stored, ok := store.trustedFile(repo, rel)
	if !ok {
		return ProjectConfigTrustReport{
			State:       ProjectConfigTrustUntrusted,
			CurrentHash: currentHash,
		}, nil
	}
	if stored.SHA256 == currentHash {
		return ProjectConfigTrustReport{
			State:       ProjectConfigTrustTrusted,
			CurrentHash: currentHash,
			StoredHash:  stored.SHA256,
		}, nil
	}
	return ProjectConfigTrustReport{
		State:       ProjectConfigTrustStale,
		CurrentHash: currentHash,
		StoredHash:  stored.SHA256,
	}, nil
}

// IsProjectConfigTrusted reports whether the project's .projmux/config.toml
// file has a current-hash trust record. The function returns the recorded SHA
// alongside the trust state so callers (e.g. the projmux hook CLI) can render
// a badge consistent with the runner without forcing a hash recomputation.
//
// Note: this is a thin convenience wrapper around the trust store; it only
// reports whether a hash is recorded, not whether that hash still matches
// the on-disk file. Callers that need the absent/untrusted/trusted/stale
// distinction should use InspectProjectConfigTrust instead.
func IsProjectConfigTrusted(repoPath, trustStorePath string) (bool, string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return false, "", errors.New("repo path is required")
	}
	repo, err := filepath.Abs(repoPath)
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(trustStorePath) == "" {
		trustStorePath = defaultTrustStorePath()
	}
	if strings.TrimSpace(trustStorePath) == "" {
		return false, "", errors.New("trust store path could not be resolved")
	}
	store, err := loadTrustedProjects(trustStorePath)
	if err != nil {
		return false, "", err
	}
	file, ok := store.trustedFile(repo, projectConfigRelativePath)
	if !ok {
		return false, "", nil
	}
	return true, file.SHA256, nil
}

// UntrustProjectConfig drops the trusted hash recorded for the project's
// .projmux/config.toml file. The trust store entry is removed entirely when
// it becomes empty so projects with no other trusted files do not linger as
// stale keys. Returns true when an entry was removed, false when nothing was
// trusted (idempotent) so a UI surface can render an idempotent "untrust"
// action without treating the no-op as an error.
func UntrustProjectConfig(repoPath, trustStorePath string) (bool, error) {
	return untrustProjectFile(repoPath, projectConfigRelativePath, trustStorePath)
}

// untrustProjectFile removes the trust store entry for repoPath/relPath and
// reports whether anything was actually removed. relPath must be a
// slash-separated path relative to the repo root. This is an internal helper;
// the public surface is UntrustProjectConfig.
func untrustProjectFile(repoPath, relPath, trustStorePath string) (bool, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return false, errors.New("repo path is required")
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return false, errors.New("relative path is required")
	}
	repo, err := filepath.Abs(repoPath)
	if err != nil {
		return false, err
	}
	rel := filepath.ToSlash(filepath.Clean(relPath))
	storePath := strings.TrimSpace(trustStorePath)
	if storePath == "" {
		storePath = defaultTrustStorePath()
	}
	if storePath == "" {
		return false, errors.New("trust store path could not be resolved")
	}
	store, err := loadTrustedProjects(storePath)
	if err != nil {
		return false, err
	}
	if !store.forget(repo, rel) {
		return false, nil
	}
	if err := store.save(storePath); err != nil {
		return false, err
	}
	return true, nil
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

// forget removes the (repo, rel) entry from the trust store and reports
// whether anything was actually removed. The project entry itself is
// dropped when its last file is forgotten so save() doesn't leak empty
// project keys into the on-disk JSON.
func (s trustedProjects) forget(repo, rel string) bool {
	project, ok := s[repo]
	if !ok || project.Files == nil {
		return false
	}
	if _, ok := project.Files[rel]; !ok {
		return false
	}
	delete(project.Files, rel)
	if len(project.Files) == 0 {
		delete(s, repo)
		return true
	}
	s[repo] = project
	return true
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
	lines := strings.Split(raw, "\n")
	if len(lines) > maxHookPreviewLines {
		lines = append(lines[:maxHookPreviewLines], "...")
	}
	for i, line := range lines {
		lines[i] = terminaltext.EscapeControls(line)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (r *Runner) promptProjectHookTrust(req ProjectHookPromptRequest) (ProjectHookDecision, bool) {
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
	fmt.Fprintln(writer, "projmux: project-local automation requires trust")
	fmt.Fprintf(writer, "repo: %s\n", terminaltext.EscapeControls(req.RepoPath))
	fmt.Fprintf(writer, "artifact: %s\n", terminaltext.EscapeControls(req.RelativePath))
	if req.PreviousSHA256 != "" {
		fmt.Fprintf(writer, "trusted sha256: %s\n", req.PreviousSHA256)
	}
	fmt.Fprintf(writer, "current sha256: %s\n", req.SHA256)
	if strings.TrimSpace(req.Preview) != "" {
		fmt.Fprintln(writer, "preview:")
		for line := range strings.SplitSeq(req.Preview, "\n") {
			fmt.Fprintf(writer, "  %s\n", terminaltext.EscapeControls(line))
		}
	}

	input := bufio.NewReader(reader)
	for range 3 {
		fmt.Fprint(writer, "Allow this automation? [o]nce/[a]lways/[d]eny: ")
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
