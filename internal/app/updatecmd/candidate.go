package updatecmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CandidateMetadata is the state-free `version -o json` output contract.
// Commit is "unknown" when the build has no revision stamp (e.g. worktrees).
type CandidateMetadata struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	SchemaVersion int    `json:"schema_version"`
}

func schemaChange(current, candidate CandidateMetadata) string {
	if current.SchemaVersion <= 0 || candidate.SchemaVersion <= 0 {
		return "unknown"
	}
	if candidate.SchemaVersion > current.SchemaVersion {
		return "bump"
	}
	if candidate.SchemaVersion < current.SchemaVersion {
		return "downgrade"
	}
	return "same"
}

// A metadata probe gets no inherited projmux, tmux, provider or XDG routing.
// PATH is inherited for executable lookup; version metadata must not invoke helpers.
// It runs only a copy, in a private HOME, and rejects any created state files.
func probeCandidateMetadata(exe string) (CandidateMetadata, error) {
	root, err := os.MkdirTemp("", "projmux-metadata-*")
	if err != nil {
		return CandidateMetadata{}, err
	}
	defer os.RemoveAll(root)
	copied := filepath.Join(root, "candidate")
	if err := verifyGoPublishedBinary(exe); err != nil {
		return CandidateMetadata{}, err
	}
	if err := copyRegularFile(exe, copied); err != nil {
		return CandidateMetadata{}, err
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return CandidateMetadata{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateVersionProbeTimeout)
	defer cancel()
	// #nosec G204 -- copied is a validated candidate snapshot; fixed metadata argv, private HOME and bounded lifetime.
	cmd := exec.CommandContext(ctx, copied, "version", "-o", "json")
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "XDG_STATE_HOME=" + filepath.Join(home, "state"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "TMPDIR=" + home, "PATH=" + os.Getenv("PATH")}
	var output boundedMetadataOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return CandidateMetadata{}, fmt.Errorf("metadata probe: %w", err)
	}
	if err := filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != home && !entry.IsDir() {
			return errors.New("metadata probe wrote state in isolated HOME")
		}
		return nil
	}); err != nil {
		return CandidateMetadata{}, err
	}
	var metadata CandidateMetadata
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	if err := decoder.Decode(&metadata); err != nil {
		return metadata, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return metadata, errors.New("metadata probe returned trailing output")
	}
	if _, ok := parseUpdateSemver(metadata.Version); !ok || metadata.SchemaVersion <= 0 || strings.TrimSpace(metadata.Commit) == "" {
		return CandidateMetadata{}, errors.New("metadata probe returned incomplete version/commit/schema_version")
	}
	return metadata, nil
}

type boundedMetadataOutput struct{ bytes.Buffer }

func (b *boundedMetadataOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16<<10 {
		return 0, errors.New("metadata output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func (c *Command) metadata(exe string) (CandidateMetadata, error) {
	if c.ProbeMetadata != nil {
		return c.ProbeMetadata(exe)
	}
	return probeCandidateMetadata(exe)
}
func candidateDigest(path string) (string, error) {
	// #nosec G304 -- path is the explicit candidate or installed binary selected by update; hash-only read.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (c *Command) pathPlan(target string, stdout io.Writer) error {
	path := "unknown"
	if c.LookPath != nil {
		if found, err := c.LookPath("projmux"); err == nil {
			path = found
		}
	}
	match := "different"
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil && filepath.Clean(resolved) == filepath.Clean(target) {
		match = "same"
	}
	_, err = fmt.Fprintf(stdout, "target: %s\nPATH projmux: %s (%s)\n", target, path, match)
	return err
}
func (c *Command) unpreparedPlan(target string, stdout io.Writer) error {
	if err := c.pathPlan(target, stdout); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, "candidate: channel latest (not prepared; download/build required)\ndigest: unknown\nschema: unknown (pure preview; no candidate executed)\nplan: stop → backup → install → resume (not automated yet)\nmanual procedure: docs/registry.md")
	return err
}
func (c *Command) candidateGate(candidate, target string, dryRun bool, stdout io.Writer) error {
	if err := verifyGoPublishedBinary(target); err != nil {
		return fmt.Errorf("update target: %w", err)
	}
	digest, err := candidateDigest(candidate)
	if err != nil {
		return err
	}
	current, currentErr := c.metadata(target)
	next, nextErr := c.metadata(candidate)
	change := schemaChange(current, next)
	if currentErr != nil || nextErr != nil {
		change = "unknown"
	}
	if err := c.pathPlan(target, stdout); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "candidate: %s version=%s commit=%s\ndigest: sha256:%s\nschema: %s (%d → %d)\n", candidate, next.Version, next.Commit, digest, change, current.SchemaVersion, next.SchemaVersion); err != nil {
		return err
	}
	if currentErr != nil || nextErr != nil {
		fmt.Fprintf(stdout, "metadata unavailable: current=%v candidate=%v\n", currentErr, nextErr)
	}
	if _, err := fmt.Fprintln(stdout, "plan: stop → backup → install → resume (not automated yet)\nmanual procedure: docs/registry.md"); err != nil {
		return err
	}
	after, err := candidateDigest(candidate)
	if err != nil {
		return err
	}
	if after != digest {
		return errors.New("update-candidate-changed: candidate changed during metadata probe")
	}
	if !dryRun && change != "same" {
		return fmt.Errorf("update-schema-%s: refusing before publication; follow the manual stop, backup, install and resume procedure in docs/registry.md", change)
	}
	return nil
}
func (c *Command) runFromApply(from string, dryRun, noApply bool, stdout, stderr io.Writer) error {
	target, err := c.currentExecutable()
	if err != nil {
		return err
	}
	if err := verifyGoPublishedBinary(from); err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	scratch, err := os.MkdirTemp("", "projmux-from-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	candidate := filepath.Join(scratch, "projmux")
	if err := copyRegularFile(from, candidate); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "source: %s (pinned private copy)\n", from); err != nil {
		return err
	}
	if err := c.candidateGate(candidate, target, dryRun, stdout); err != nil {
		return err
	}
	if dryRun {
		_, err := fmt.Fprintf(stdout, "would replace: %s (atomic via temp file)\n", target)
		return err
	}
	before := c.probeActiveVersion()
	if !noApply {
		pre := updateApplyCommand{Stage: updateApplyPrePublication, Name: target, Args: preUpdateApplyArgs(target, c.AppSocket)}
		if err := c.externalRunner()(pre.Name, pre.Args, stdout, stderr); err != nil {
			return c.updateApplyStageError(pre, err)
		}
	}
	if err := c.atomicReplaceRelease(candidate, target); err != nil {
		return err
	}
	post := updateApplyCommand{Stage: updateApplyVerification, Name: target, Args: postUpdateApplyArgs(noApply)}
	if noApply {
		post.Stage = updateApplyConfigOnly
	}
	if err := c.externalRunner()(post.Name, post.Args, stdout, stderr); err != nil {
		return c.updateApplyStageError(post, err)
	}
	if noApply {
		if err := writeUpdateExplicitApplyRequired(stdout, c.AppSocket); err != nil {
			return err
		}
	}
	next, err := c.metadata(target)
	if err != nil {
		return err
	}
	return c.verifyPublishedVersion(stdout, "from", next.Version, before)
}
func (c *Command) pinGoVersion() (string, error) {
	if c.resolveGoVersion != nil {
		return c.resolveGoVersion()
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateVersionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-json", "github.com/crevissepartners/projmux@latest")
	var output boundedMetadataOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("resolve Go latest: %w", err)
	}
	var module struct{ Version string }
	if err := json.Unmarshal(output.Bytes(), &module); err != nil {
		return "", err
	}
	if _, ok := parseUpdateSemver(module.Version); !ok {
		return "", errors.New("go latest did not resolve to an exact version")
	}
	return module.Version, nil
}
