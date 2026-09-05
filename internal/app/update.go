package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/version"
)

const (
	updateCacheFileName = "update.json"
	updateCacheMaxAge   = 24 * time.Hour
	updateHTTPTimeout   = 10 * time.Second
	// updateVersionProbeTimeout bounds the `projmux version` call that reads
	// the installed version back. A wedged binary must fail verification, not
	// hang the apply that just published it.
	updateVersionProbeTimeout = 10 * time.Second
	updateReleaseURL          = "https://api.github.com/repos/crevissepartners/projmux/releases/latest"
	// updateNPMRegistryURL is the availability authority for npm installs. The
	// registry is asked the same question the GitHub API is asked for the other
	// channels: what is the newest version a user of this channel can install
	// right now.
	updateNPMRegistryURL = "https://registry.npmjs.org/projmux"
	// updateReleaseListURL is the rc channel's GitHub authority. releases/latest
	// is defined to skip prereleases, so it can never answer for a channel that
	// must see them; the list carries the stable line as well, which is what
	// lets one request answer "newest of stable and rc".
	updateReleaseListURL = "https://api.github.com/repos/crevissepartners/projmux/releases?per_page=30"

	updateMaxCompressedBytes   int64 = 64 << 20
	updateMaxTarBytes          int64 = 256 << 20
	updateMaxTotalRegularBytes int64 = 192 << 20
	updateMaxRegularFileBytes  int64 = 128 << 20
	updateMaxTarEntries              = 256
	updateMaxRedirects               = 5
)

// The availability sources an update judgment can come from. A judgment is
// only meaningful against the channel that will perform the install, so the
// source is chosen from the detected installer and recorded alongside the
// answer it produced.
const (
	updateSourceGitHubRelease = "github-release"
	updateSourceNPMRegistry   = "npm-registry"
)

// The release channels an update judgment can be made against.
//
// This axis is orthogonal to the install path above: every install path can be
// judged on either channel, and the two are combined rather than merged. The
// default is stable, and it is the default in the strong sense — an unset, an
// empty, and an unrecognised value all mean stable, so no configuration error
// can silently opt an install into prereleases.
const (
	updateReleaseChannelStable = "stable"
	updateReleaseChannelRC     = "rc"
)

// updateReleaseChannelEnv is the opt-in switch for the rc channel. It mirrors
// PROJMUX_INSTALLER: an explicit value selects the axis, and the resolver seam
// on updateCommand is what a stored setting will drive later.
const updateReleaseChannelEnv = "PROJMUX_RELEASE_CHANNEL"

var errUpdateTarTooLarge = errors.New("release archive exceeded extracted byte limit")

type updateHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type updateArchiveLimits struct {
	compressedBytes   int64
	tarBytes          int64
	totalRegularBytes int64
	regularFileBytes  int64
	entries           int
}

type updateCommand struct {
	now         func() time.Time
	getenv      func(string) string
	cacheDir    func() (string, error)
	client      updateHTTPClient
	apiURL      string
	npmURL      string
	releasesURL string
	// releaseChannelSource resolves the opted-in release channel. It is a seam
	// rather than a plain field so the stored Settings toggle can own the value
	// without the judgment having to know where it was persisted; when it is
	// unset the environment answers, and when that is unset the answer is
	// stable.
	releaseChannelSource func() string
	executable           func() (string, error)
	lookPath             func(string) (string, error)
	runExternal          func(name string, args []string, stdout, stderr io.Writer) error
	// probeVersion reads the raw `projmux version` output of one exact
	// executable. It is deliberately a separate seam from runExternal: the
	// probe is a reading, not one of the staged apply commands, so it must
	// never appear in the published command sequence.
	probeVersion func(exe string) (string, error)
	goos         string
	goarch       string
	mkdirTemp    func(dir, pattern string) (string, error)
	removeAll    func(path string) error
	rename       func(oldpath, newpath string) error
	chmod        func(name string, mode os.FileMode) error
	remove       func(name string) error
	copyFile     func(src, dst string) error
	buildInfo    func() (*debug.BuildInfo, bool)
	userHomeDir  func() (string, error)
	limits       updateArchiveLimits
}

type updateCache struct {
	Version   int       `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
	// Source names the availability authority this answer came from. It is
	// additive: a cache written before channel-aware judgment existed carries
	// no source, and every such cache was necessarily a GitHub release answer.
	Source string `json:"source,omitempty"`
	// Channel names the release channel this answer was judged on. It is
	// additive in the same way Source is: the field is omitted for the default
	// channel, so every cache written before the axis existed — and every cache
	// a default install writes now — reads back as stable.
	Channel     string    `json:"release_channel,omitempty"`
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name,omitempty"`
	HTMLURL     string    `json:"html_url,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

type updateStatus struct {
	CurrentVersion string          `json:"current_version"`
	LatestVersion  string          `json:"latest_version,omitempty"`
	ReleaseURL     string          `json:"release_url,omitempty"`
	CheckedAt      *time.Time      `json:"checked_at,omitempty"`
	CacheState     string          `json:"cache_state"`
	SourceName     string          `json:"availability_source"`
	ReleaseChannel string          `json:"release_channel"`
	UpdateState    string          `json:"update_state"`
	Installer      updateInstaller `json:"installer"`
	CachePath      string          `json:"cache_path"`
}

type updateInstaller struct {
	Source string `json:"source"`
	Note   string `json:"note"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt time.Time            `json:"published_at"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

func newUpdateCommand() *updateCommand {
	return &updateCommand{
		now:          time.Now,
		getenv:       os.Getenv,
		cacheDir:     defaultUpdateCacheDir,
		client:       &http.Client{Timeout: updateHTTPTimeout},
		apiURL:       updateReleaseURL,
		npmURL:       updateNPMRegistryURL,
		releasesURL:  updateReleaseListURL,
		executable:   resolveExecutablePath,
		lookPath:     exec.LookPath,
		runExternal:  runUpdateExternal,
		probeVersion: probeInstalledProjmuxVersion,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		mkdirTemp:    os.MkdirTemp,
		removeAll:    os.RemoveAll,
		rename:       os.Rename,
		chmod:        os.Chmod,
		remove:       os.Remove,
		copyFile:     copyRegularFile,
		buildInfo:    debug.ReadBuildInfo,
		userHomeDir:  os.UserHomeDir,
		limits:       defaultUpdateArchiveLimits(),
	}
}

func (c *updateCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("update requires a subcommand")
	}
	switch args[0] {
	case "status":
		return c.runStatus(args[1:], stdout, stderr)
	case "check":
		return c.runCheck(args[1:], stdout, stderr)
	case "apply":
		return c.runApply(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printUpdateUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown update subcommand: %s", args[0])
	}
}

func (c *updateCommand) runApply(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("update apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print installer-specific update command without running it")
	noApply := fs.Bool("no-apply", false, "skip reloading tmux after 'projmux config apply'")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("update apply does not accept positional arguments")
	}

	installer := c.detectInstaller()
	if installer.Source == "github-release" {
		if *dryRun {
			return c.runGitHubReleaseApplyDryRun(*noApply, stdout)
		}
		return c.runGitHubReleaseApply(*noApply, stdout, stderr)
	}

	commands, err := c.applyCommands(installer.Source, *noApply)
	if err != nil {
		return err
	}
	if *dryRun {
		for _, command := range commands {
			if _, err := fmt.Fprintf(stdout, "would run: %s\n", command.String()); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout, keymapMigrationStagePreviewLine("the updated binary")); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(stdout, managedIngestMigrationStagePreviewLine("the updated binary")); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(stdout, updateApplyVerificationPreviewLine()); err != nil {
			return err
		}
		return nil
	}

	// Read the pre-publication version before anything runs. A failure here is
	// carried rather than raised: the update itself is still worth attempting,
	// and an unreadable baseline must end as "not verified", never as success.
	before := c.probeActiveVersion()
	expected := c.cachedLatestVersion()

	for _, command := range commands {
		if _, err := fmt.Fprintf(stdout, ">> running: %s\n", command.String()); err != nil {
			return err
		}
		if err := c.externalRunner()(command.Name, command.Args, stdout, stderr); err != nil {
			return updateApplyStageError(command, err)
		}
	}
	if *noApply {
		if err := writeUpdateExplicitApplyRequired(stdout, defaultAppSocket); err != nil {
			return err
		}
	}
	return c.verifyPublishedVersion(stdout, installer.Source, expected, before)
}

type updateApplyStage string

const (
	updateApplyPrePublication updateApplyStage = "pre-publication convergence"
	updateApplyPublication    updateApplyStage = "binary publication"
	updateApplyVerification   updateApplyStage = "post-publication verification"
	updateApplyConfigOnly     updateApplyStage = "config-only migration"
)

type updateApplyCommand struct {
	Stage updateApplyStage
	Name  string
	Args  []string
}

// postUpdateApplyArgs is the argv every installer path hands the *new* binary
// once it is in place.
//
// The new binary always runs, even under `--no-apply`. `--no-apply` means "do
// not disturb my running tmux server", and it is honoured by suppressing the
// reload rather than by skipping the step: the keymap schema migration lives
// behind this call, and an installer that skipped it would leave the user on a
// v0 keymap under a binary that writes v1 — exactly the split state the
// versioned schema exists to prevent.
//
// It also has to be the new binary that runs. The old one has no idea what the
// new canonical ids are, so only the freshly installed binary can compute the
// real rename table.
func postUpdateApplyArgs(noApply bool) []string {
	args := []string{"config", "apply"}
	if noApply {
		args = append(args, "--no-reload")
	}
	return args
}

func preUpdateApplyArgs(publishedTarget, socketName string) []string {
	return []string{"config", "apply", "--bin", publishedTarget, "--socket", socketName}
}

func updateApplyRecoveryCommand(socketName string) string {
	return fmt.Sprintf("projmux config apply --socket %s", socketName)
}

func writeUpdateExplicitApplyRequired(stdout io.Writer, socketName string) error {
	_, err := fmt.Fprintf(stdout,
		">> live tmux unchanged; explicit apply required: run `%s` before ordinary mutation\n",
		updateApplyRecoveryCommand(socketName))
	return err
}

func updateApplyStageError(command updateApplyCommand, cause error) error {
	recovery := updateApplyRecoveryCommand(defaultAppSocket)
	switch command.Stage {
	case updateApplyPrePublication:
		return fmt.Errorf("update pre-publication convergence failed; binary publication not started; recovery: run `%s`: run %s: %w", recovery, command.String(), cause)
	case updateApplyPublication:
		return fmt.Errorf("update binary publication failed; update not successful; recovery: run `%s`: run %s: %w", recovery, command.String(), cause)
	case updateApplyVerification:
		return fmt.Errorf("update post-publication convergence failed; update not successful; recovery: run `%s`: run %s: %w", recovery, command.String(), cause)
	case updateApplyConfigOnly:
		return fmt.Errorf("update config-only migration failed; update not successful; live tmux unchanged; recovery: run `%s`: run %s: %w", recovery, command.String(), cause)
	default:
		return fmt.Errorf("run %s: %w", command.String(), cause)
	}
}

// keymapMigrationStagePreviewLine describes the migration step for a dry run.
//
// It names the stage and stops there. The exact old→canonical rename table is
// owned by the candidate binary, which is not installed yet and may not even be
// downloaded, so promising a diff here would be promising something this
// process cannot know.
func keymapMigrationStagePreviewLine(target string) string {
	return fmt.Sprintf(
		"would migrate: keymap schema via %s (the installed binary computes the exact action-id table; this preview does not)",
		target)
}

func managedIngestMigrationStagePreviewLine(target string) string {
	return fmt.Sprintf(
		"would migrate: marker-owned agent hook producers via %s (missing and unmanaged integrations remain untouched)",
		target)
}

func (c updateApplyCommand) String() string {
	parts := append([]string{c.Name}, c.Args...)
	return strings.Join(parts, " ")
}

func (c *updateCommand) applyCommands(source string, noApply bool) ([]updateApplyCommand, error) {
	switch source {
	case "npm":
		// `npm update -g` honors the installed semver range and frequently
		// refuses to cross into a newer minor/major, so global installs get
		// stuck on an old version. `npm install -g projmux@latest` always
		// pulls the newest published release and re-resolves the per-platform
		// optionalDependency, which is what "update" must actually do.
		commands := []updateApplyCommand{{Stage: updateApplyPublication, Name: "npm", Args: []string{"install", "-g", "projmux@latest"}}}
		if !noApply {
			current, err := c.currentExecutable()
			if err != nil {
				return nil, err
			}
			target, err := c.npmPublishedTarget()
			if err != nil {
				return nil, err
			}
			commands = append([]updateApplyCommand{{
				Stage: updateApplyPrePublication,
				Name:  current,
				Args:  preUpdateApplyArgs(target, defaultAppSocket),
			}}, commands...)
		}
		postStage := updateApplyVerification
		if noApply {
			postStage = updateApplyConfigOnly
		}
		commands = append(commands, updateApplyCommand{Stage: postStage, Name: "projmux", Args: postUpdateApplyArgs(noApply)})
		return commands, nil
	case "go":
		exe, err := c.currentExecutable()
		if err != nil {
			return nil, err
		}
		commands := []updateApplyCommand{{Stage: updateApplyPublication, Name: "go", Args: []string{"install", "github.com/crevissepartners/projmux/cmd/projmux@latest"}}}
		if !noApply {
			commands = append([]updateApplyCommand{{
				Stage: updateApplyPrePublication,
				Name:  exe,
				Args:  preUpdateApplyArgs(exe, defaultAppSocket),
			}}, commands...)
		}
		postStage := updateApplyVerification
		if noApply {
			postStage = updateApplyConfigOnly
		}
		commands = append(commands, updateApplyCommand{Stage: postStage, Name: exe, Args: postUpdateApplyArgs(noApply)})
		return commands, nil
	case "github-release":
		return nil, errors.New("update apply for github-release installs is handled by direct release binary replacement")
	case "source":
		return nil, errors.New("update apply is not available for source installs; update the checkout with `git pull --ff-only && make install`")
	default:
		return nil, errors.New("update apply could not detect a supported installer; run from an npm/go/github-release install or set PROJMUX_INSTALLER=npm|go|github-release (source installs update with `git pull --ff-only && make install`)")
	}
}

func (c *updateCommand) npmPublishedTarget() (string, error) {
	if c.lookPath == nil {
		return "", errors.New("update apply: npm published-target resolver is not configured")
	}
	target, err := c.lookPath("projmux")
	if err != nil {
		return "", fmt.Errorf("update apply: resolve npm published target: %w", err)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("update apply: npm published target is empty")
	}
	if !filepath.IsAbs(target) {
		target, err = filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("update apply: make npm published target absolute: %w", err)
		}
	}
	return filepath.Clean(target), nil
}

func (c *updateCommand) runGitHubReleaseApplyDryRun(noApply bool, stdout io.Writer) error {
	target, err := c.currentExecutable()
	if err != nil {
		return err
	}
	goos, goarch := c.targetPlatform()
	archive := releaseArchiveName("latest", goos, goarch)
	if _, err := fmt.Fprintf(stdout, "would fetch: %s\n", c.releaseAPIURL()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "would download: %s\n", archive); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "would replace: %s (atomic via temp file)\n", target); err != nil {
		return err
	}
	if !noApply {
		if _, err := fmt.Fprintf(stdout, "would run before replacement: %s %s\n",
			target, strings.Join(preUpdateApplyArgs(target, defaultAppSocket), " ")); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(stdout, "would run: %s %s\n",
		target, strings.Join(postUpdateApplyArgs(noApply), " ")); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, keymapMigrationStagePreviewLine(target)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, managedIngestMigrationStagePreviewLine(target)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, updateApplyVerificationPreviewLine()); err != nil {
		return err
	}
	if noApply {
		return writeUpdateExplicitApplyRequired(stdout, defaultAppSocket)
	}
	return nil
}

func (c *updateCommand) runGitHubReleaseApply(noApply bool, stdout, stderr io.Writer) error {
	target, err := c.currentExecutable()
	if err != nil {
		return err
	}
	before := c.probeActiveVersion()
	rel, err := c.fetchLatestRelease(context.Background())
	if err != nil {
		return err
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return errors.New("update apply: latest release response did not include tag_name")
	}
	goos, goarch := c.targetPlatform()
	asset, err := findReleaseAsset(rel, goos, goarch)
	if err != nil {
		return err
	}

	tmpDir, err := c.createReleaseScratchDir(target)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup && c.removeAll != nil {
			_ = c.removeAll(tmpDir)
		}
	}()

	extracted := filepath.Join(tmpDir, "projmux")
	if _, err := fmt.Fprintf(stdout, ">> downloading %s\n", asset.Name); err != nil {
		return err
	}
	if err := c.downloadAndExtractReleaseAsset(context.Background(), asset, extracted); err != nil {
		return err
	}
	if !noApply {
		preApply := updateApplyCommand{
			Stage: updateApplyPrePublication,
			Name:  target,
			Args:  preUpdateApplyArgs(target, defaultAppSocket),
		}
		if _, err := fmt.Fprintf(stdout, ">> running: %s\n", preApply.String()); err != nil {
			return err
		}
		if err := c.externalRunner()(preApply.Name, preApply.Args, stdout, stderr); err != nil {
			return updateApplyStageError(preApply, err)
		}
	}
	if err := c.atomicReplaceRelease(extracted, target); err != nil {
		return fmt.Errorf("update binary publication failed; update not successful; recovery: run `%s`: %w",
			updateApplyRecoveryCommand(defaultAppSocket), err)
	}
	if _, err := fmt.Fprintf(stdout, ">> atomically replaced %s\n", target); err != nil {
		return err
	}

	if c.removeAll != nil {
		_ = c.removeAll(tmpDir)
	}
	cleanup = false

	applyArgs := postUpdateApplyArgs(noApply)
	if noApply {
		if _, err := fmt.Fprintln(stdout, ">> migrating config without reloading the live server..."); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(stdout, ">> applying live config..."); err != nil {
		return err
	}
	if err := c.externalRunner()(target, applyArgs, stdout, stderr); err != nil {
		stage := updateApplyVerification
		if noApply {
			stage = updateApplyConfigOnly
		}
		return updateApplyStageError(updateApplyCommand{Stage: stage, Name: target, Args: applyArgs}, err)
	}
	if noApply {
		if err := writeUpdateExplicitApplyRequired(stdout, defaultAppSocket); err != nil {
			return err
		}
	}
	return c.verifyPublishedVersion(stdout, "github-release", rel.TagName, before)
}

func (c *updateCommand) createReleaseScratchDir(target string) (string, error) {
	if c.mkdirTemp == nil {
		return "", errors.New("configure update mkdirTemp: temp directory factory is not configured")
	}
	tmpDir, err := c.mkdirTemp(filepath.Dir(target), ".projmux-release-*")
	if err != nil {
		return "", fmt.Errorf("create release update scratch directory: %w", err)
	}
	return tmpDir, nil
}

func (c *updateCommand) downloadAndExtractReleaseAsset(ctx context.Context, asset githubReleaseAsset, dst string) error {
	if c.client == nil {
		return errors.New("update apply: HTTP client is not configured")
	}
	assetURL := strings.TrimSpace(asset.BrowserDownloadURL)
	if assetURL == "" {
		return errors.New("update apply: release asset did not include browser_download_url")
	}
	parsedURL, err := url.Parse(assetURL)
	if err != nil {
		return fmt.Errorf("update apply: parse release asset URL: %w", err)
	}
	if err := validateReleaseAssetURL(parsedURL); err != nil {
		return fmt.Errorf("update apply: release asset URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return fmt.Errorf("update apply: build release asset request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "projmux/"+version.String())

	resp, err := c.doReleaseAssetRequest(req)
	if err != nil {
		return fmt.Errorf("update apply: download release asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil {
		if err := validateReleaseAssetURL(resp.Request.URL); err != nil {
			return fmt.Errorf("update apply: final release asset URL: %w", err)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update apply: release asset request returned status %d", resp.StatusCode)
	}
	limits := c.archiveLimits()
	if resp.ContentLength > limits.compressedBytes {
		return fmt.Errorf("update apply: release asset compressed size %d exceeds limit %d", resp.ContentLength, limits.compressedBytes)
	}
	archive, err := readBoundedBytes(resp.Body, limits.compressedBytes)
	if err != nil {
		return fmt.Errorf("update apply: read release asset: %w", err)
	}
	if err := verifyReleaseAssetDigest(archive, asset.Digest); err != nil {
		return fmt.Errorf("update apply: verify release asset: %w", err)
	}
	if err := extractProjmuxBinaryWithLimits(archive, dst, limits); err != nil {
		return fmt.Errorf("update apply: extract release asset: %w", err)
	}
	return nil
}

func (c *updateCommand) doReleaseAssetRequest(req *http.Request) (*http.Response, error) {
	client, ok := c.client.(*http.Client)
	if !ok {
		return c.client.Do(req)
	}
	cloned := *client
	originalCheckRedirect := client.CheckRedirect
	cloned.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > updateMaxRedirects {
			return fmt.Errorf("release asset redirect limit exceeded")
		}
		if err := validateReleaseAssetURL(next.URL); err != nil {
			return fmt.Errorf("release asset redirect URL: %w", err)
		}
		if originalCheckRedirect != nil {
			return originalCheckRedirect(next, via)
		}
		return nil
	}
	return cloned.Do(req)
}

func validateReleaseAssetURL(u *url.URL) error {
	if u == nil {
		return errors.New("URL is missing")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not allowed", u.Scheme)
	}
	if u.User != nil {
		return errors.New("userinfo is not allowed")
	}
	if port := u.Port(); port != "" && port != "443" {
		return fmt.Errorf("port %q is not allowed", port)
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return nil
	default:
		return fmt.Errorf("host %q is not allowed", u.Hostname())
	}
}

func defaultUpdateArchiveLimits() updateArchiveLimits {
	return updateArchiveLimits{
		compressedBytes:   updateMaxCompressedBytes,
		tarBytes:          updateMaxTarBytes,
		totalRegularBytes: updateMaxTotalRegularBytes,
		regularFileBytes:  updateMaxRegularFileBytes,
		entries:           updateMaxTarEntries,
	}
}

func (c *updateCommand) archiveLimits() updateArchiveLimits {
	limits := c.limits
	defaults := defaultUpdateArchiveLimits()
	if limits.compressedBytes <= 0 {
		limits.compressedBytes = defaults.compressedBytes
	}
	if limits.tarBytes <= 0 {
		limits.tarBytes = defaults.tarBytes
	}
	if limits.totalRegularBytes <= 0 {
		limits.totalRegularBytes = defaults.totalRegularBytes
	}
	if limits.regularFileBytes <= 0 {
		limits.regularFileBytes = defaults.regularFileBytes
	}
	if limits.entries <= 0 {
		limits.entries = defaults.entries
	}
	return limits
}

func readBoundedBytes(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("compressed size exceeds limit %d", limit)
	}
	return data, nil
}

func verifyReleaseAssetDigest(archive []byte, digest string) error {
	digest = strings.TrimSpace(digest)
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(algorithm), "sha256") {
		return errors.New("release asset digest is missing or does not use sha256")
	}
	want, err := hex.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(want) != sha256.Size {
		return errors.New("release asset sha256 digest is malformed")
	}
	got := sha256.Sum256(archive)
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return errors.New("release asset sha256 digest mismatch")
	}
	return nil
}

func (c *updateCommand) atomicReplaceRelease(src, target string) error {
	replacer := atomicBinaryReplacer{
		rename:        c.rename,
		chmod:         c.chmod,
		remove:        c.remove,
		copyFile:      c.copyFile,
		tempSuffixGen: defaultTempSuffix,
	}
	return replacer.replace(src, target)
}

func (c *updateCommand) targetPlatform() (string, string) {
	goos := strings.TrimSpace(c.goos)
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := strings.TrimSpace(c.goarch)
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (c *updateCommand) releaseAPIURL() string {
	if c.apiURL != "" {
		return c.apiURL
	}
	return updateReleaseURL
}

func (c *updateCommand) npmRegistryAPIURL() string {
	if c.npmURL != "" {
		return c.npmURL
	}
	return updateNPMRegistryURL
}

func (c *updateCommand) releaseListAPIURL() string {
	if c.releasesURL != "" {
		return c.releasesURL
	}
	return updateReleaseListURL
}

// normalizeUpdateReleaseChannel maps any raw value onto the axis.
//
// Only an exact opt-in reaches the rc channel. Everything else — unset, empty,
// misspelled, or a channel a future version knows and this one does not — is
// stable, because the failure this ordering prevents is an install being shown
// prereleases it never asked for.
func normalizeUpdateReleaseChannel(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), updateReleaseChannelRC) {
		return updateReleaseChannelRC
	}
	return updateReleaseChannelStable
}

func (c *updateCommand) releaseChannel() string {
	if c.releaseChannelSource != nil {
		return normalizeUpdateReleaseChannel(c.releaseChannelSource())
	}
	if c.getenv != nil {
		return normalizeUpdateReleaseChannel(c.getenv(updateReleaseChannelEnv))
	}
	return updateReleaseChannelStable
}

// availabilitySourceForInstaller maps an install channel to the authority that
// can answer "is there a newer version I can install".
//
// Only npm moves. go resolves GitHub tags through the module proxy and a
// github-release install downloads the release itself, so for both of them the
// GitHub release is already the channel's own answer; source builds have no
// channel at all and keep the same default rather than gaining a registry they
// do not install from.
func availabilitySourceForInstaller(installer string) string {
	if installer == "npm" {
		return updateSourceNPMRegistry
	}
	return updateSourceGitHubRelease
}

func (c *updateCommand) availabilitySource() string {
	return availabilitySourceForInstaller(c.detectInstaller().Source)
}

// updateAvailabilityRefreshDescription names the authority the check actually
// contacts. It is derived from the same source the judgment used, so the row
// cannot drift from what refreshing the cache will do.
func updateAvailabilityRefreshDescription(source string) string {
	if source == updateSourceNPMRegistry {
		return "refresh cached npm registry metadata"
	}
	return "refresh cached GitHub release metadata"
}

func updateAvailabilitySourceLabel(source string) string {
	if source == updateSourceNPMRegistry {
		return "npm registry"
	}
	return "GitHub releases"
}

// availabilitySource reports which authority produced this cached answer.
//
// An empty field is not unknown: every cache written before the source was
// recorded came from the GitHub release API, so reading it as such keeps the
// unchanged channels from being forced through a needless refetch.
func (cache updateCache) availabilitySource() string {
	if source := strings.TrimSpace(cache.Source); source != "" {
		return source
	}
	return updateSourceGitHubRelease
}

// releaseChannel reports which channel this cached answer was judged on.
//
// An omitted field is the default channel, not an unknown one: that is what a
// cache written before the axis existed carries, and what a default install
// writes today, so reading it as stable keeps the unchanged channel from being
// forced through a needless refetch.
func (cache updateCache) releaseChannel() string {
	return normalizeUpdateReleaseChannel(cache.Channel)
}

// updateCacheChannelField is the value stored for a channel.
//
// The default channel stores nothing, which keeps the on-disk shape of a
// default install byte-identical to the one written before this axis existed.
func updateCacheChannelField(channel string) string {
	if normalizeUpdateReleaseChannel(channel) == updateReleaseChannelRC {
		return updateReleaseChannelRC
	}
	return ""
}

// loadUsableCache returns the cached answer only when it came from the
// (install path, release channel) pair this install must be judged against.
//
// A cache from another channel is not stale; it answers a different question.
// Ageing it out would leave a GitHub answer driving an npm install for up to
// updateCacheMaxAge, which is the exact failure this judgment split exists to
// remove, so a mismatch is discarded outright.
func (c *updateCommand) loadUsableCache() (updateCache, bool, error) {
	cache, ok, err := c.loadCache()
	if err != nil || !ok {
		return updateCache{}, false, err
	}
	if cache.availabilitySource() != c.availabilitySource() {
		return updateCache{}, false, nil
	}
	// The cache key is the pair, not either half. A fresh rc answer is not a
	// stale stable answer, it is an answer to a different question, so a
	// mismatch on this axis is discarded exactly as a source mismatch is.
	if cache.releaseChannel() != c.releaseChannel() {
		return updateCache{}, false, nil
	}
	return cache, true, nil
}

func (c *updateCommand) runStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("update status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the text report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("update status does not accept positional arguments")
	}

	st, err := c.status()
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeUpdateJSON(stdout, st)
	}
	return writeUpdateStatusText(stdout, st)
}

func (c *updateCommand) runCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("update check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the text report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("update check does not accept positional arguments")
	}

	cache, err := c.fetchAndSaveLatestAvailability(context.Background())
	if err != nil {
		return err
	}

	st, err := c.statusFromCache(cache)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeUpdateJSON(stdout, st)
	}
	return writeUpdateCheckText(stdout, st)
}

func (c *updateCommand) status() (updateStatus, error) {
	cache, ok, err := c.loadUsableCache()
	if err != nil {
		return updateStatus{}, err
	}
	if !ok {
		path, err := c.cachePath()
		if err != nil {
			return updateStatus{}, err
		}
		return updateStatus{
			CurrentVersion: version.String(),
			CacheState:     "unknown",
			SourceName:     c.availabilitySource(),
			ReleaseChannel: c.releaseChannel(),
			UpdateState:    "unknown",
			Installer:      c.detectInstaller(),
			CachePath:      path,
		}, nil
	}
	return c.statusFromCache(cache)
}

func (c *updateCommand) refreshCacheIfNeeded(ctx context.Context) error {
	cache, ok, err := c.loadUsableCache()
	if err != nil {
		return err
	}
	if ok {
		checked := cache.CheckedAt.UTC()
		if !checked.IsZero() && c.clock().Sub(checked) <= updateCacheMaxAge {
			return nil
		}
	}
	_, err = c.fetchAndSaveLatestAvailability(ctx)
	return err
}

func (c *updateCommand) fetchAndSaveLatestAvailability(ctx context.Context) (updateCache, error) {
	source := c.availabilitySource()
	cache, err := c.fetchLatestAvailability(ctx, source, c.releaseChannel())
	if err != nil {
		return updateCache{}, err
	}
	if cache.TagName == "" {
		return updateCache{}, fmt.Errorf("update check: %s did not report a latest version", updateAvailabilitySourceLabel(source))
	}
	if err := c.saveCache(cache); err != nil {
		return updateCache{}, err
	}
	return cache, nil
}

// fetchLatestAvailability asks one channel's authority for its newest version.
//
// Both branches go through the same c.client, so the request timeout and the
// redirect ceiling the shell gate already budgets for apply unchanged to the
// npm channel.
func (c *updateCommand) fetchLatestAvailability(ctx context.Context, source, channel string) (updateCache, error) {
	if source == updateSourceNPMRegistry {
		latest, err := c.fetchLatestNPMVersion(ctx, channel)
		if err != nil {
			return updateCache{}, err
		}
		return updateCache{
			Version:   1,
			CheckedAt: c.clock().UTC(),
			Source:    updateSourceNPMRegistry,
			Channel:   updateCacheChannelField(channel),
			TagName:   updateTagFromNPMVersion(latest),
			// The registry publishes no release notes, and the GitHub release
			// for this version is still drafted while npm is ahead of it, so
			// there is no notes URL to offer rather than a link that 404s.
		}, nil
	}
	rel, err := c.fetchReleaseForChannel(ctx, channel)
	if err != nil {
		return updateCache{}, err
	}
	return updateCache{
		Version:     1,
		CheckedAt:   c.clock().UTC(),
		Source:      updateSourceGitHubRelease,
		Channel:     updateCacheChannelField(channel),
		TagName:     strings.TrimSpace(rel.TagName),
		Name:        strings.TrimSpace(rel.Name),
		HTMLURL:     strings.TrimSpace(rel.HTMLURL),
		PublishedAt: rel.PublishedAt.UTC(),
	}, nil
}

// fetchReleaseForChannel picks the GitHub authority the channel is entitled to.
//
// The stable channel keeps releases/latest untouched: GitHub excludes drafts
// and prereleases from it by definition, which is why a default install cannot
// see an rc even in principle. The rc channel cannot use that endpoint at all
// for the same reason, so it reads the release list instead.
func (c *updateCommand) fetchReleaseForChannel(ctx context.Context, channel string) (githubRelease, error) {
	if normalizeUpdateReleaseChannel(channel) == updateReleaseChannelRC {
		return c.fetchNewestReleaseIncludingPrereleases(ctx)
	}
	return c.fetchLatestRelease(ctx)
}

// updateTagFromNPMVersion restores the leading "v" the registry drops, so one
// version string format reaches the gate, the skip state, and the status report
// no matter which authority answered.
func updateTagFromNPMVersion(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

func (c *updateCommand) statusFromCache(cache updateCache) (updateStatus, error) {
	path, err := c.cachePath()
	if err != nil {
		return updateStatus{}, err
	}
	checked := cache.CheckedAt.UTC()
	cacheState := "fresh"
	if checked.IsZero() || c.clock().Sub(checked) > updateCacheMaxAge {
		cacheState = "stale"
	}
	return updateStatus{
		CurrentVersion: version.String(),
		LatestVersion:  strings.TrimSpace(cache.TagName),
		ReleaseURL:     strings.TrimSpace(cache.HTMLURL),
		CheckedAt:      &checked,
		CacheState:     cacheState,
		SourceName:     cache.availabilitySource(),
		ReleaseChannel: cache.releaseChannel(),
		UpdateState:    compareUpdateState(version.String(), cache.TagName),
		Installer:      c.detectInstaller(),
		CachePath:      path,
	}, nil
}

func (c *updateCommand) fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	if c.client == nil {
		return githubRelease{}, errors.New("update check: HTTP client is not configured")
	}
	url := c.releaseAPIURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, fmt.Errorf("update check: build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "projmux/"+version.String())

	resp, err := c.client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("update check: fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("update check: GitHub release request returned status %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return githubRelease{}, fmt.Errorf("update check: parse latest release: %w", err)
	}
	return rel, nil
}

// fetchNewestReleaseIncludingPrereleases answers the rc channel from the
// release list.
//
// "Newest" is decided by semver precedence rather than by the list order,
// because the list is ordered by creation and a patch on an older line can be
// published after an rc. That is also what makes one request enough for the
// contract "newest of stable and rc": both lines are in this list, so the
// moment the stable release of a line lands it outranks that line's rc and an
// opted-in install is offered the stable version.
//
// Drafts are skipped. A drafted release has no downloadable asset yet, so
// offering it would be offering an update that cannot be applied.
func (c *updateCommand) fetchNewestReleaseIncludingPrereleases(ctx context.Context) (githubRelease, error) {
	if c.client == nil {
		return githubRelease{}, errors.New("update check: HTTP client is not configured")
	}
	url := c.releaseListAPIURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, fmt.Errorf("update check: build release list request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "projmux/"+version.String())

	resp, err := c.client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("update check: fetch release list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("update check: GitHub release list request returned status %d", resp.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return githubRelease{}, fmt.Errorf("update check: parse release list: %w", err)
	}
	var (
		newest    githubRelease
		newestVer updateSemver
		found     bool
	)
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		candidate, ok := parseUpdateSemver(rel.TagName)
		if !ok {
			continue
		}
		if !found || compareUpdateSemver(newestVer, candidate) < 0 {
			newest, newestVer, found = rel, candidate, true
		}
	}
	if !found {
		return githubRelease{}, errors.New("update check: GitHub release list did not include a published release")
	}
	return newest, nil
}

// npmPackageDocument is the slice of the registry packument this judgment
// needs. dist-tags.latest is what `npm install -g projmux@latest` resolves, and
// dist-tags.rc is the pointer the rc channel publishes, so the whole map is
// kept rather than one entry: both channels are answered from one response.
type npmPackageDocument struct {
	DistTags map[string]string `json:"dist-tags"`
}

// fetchLatestNPMVersion answers one channel from the dist-tags the registry
// publishes.
//
// The rc channel costs no extra request: the abbreviated packument the stable
// judgment already fetches carries every dist-tag, so reading dist-tags.rc
// alongside dist-tags.latest keeps the shell gate's request budget unchanged.
func (c *updateCommand) fetchLatestNPMVersion(ctx context.Context, channel string) (string, error) {
	tags, err := c.fetchNPMDistTags(ctx)
	if err != nil {
		return "", err
	}
	latest := strings.TrimSpace(tags["latest"])
	if normalizeUpdateReleaseChannel(channel) == updateReleaseChannelRC {
		// Newest of the two pointers, so an rc install rejoins the stable line
		// as soon as that line's release lands rather than being stranded on a
		// prerelease that nothing supersedes.
		newest := newerUpdateVersion(latest, strings.TrimSpace(tags["rc"]))
		if newest == "" {
			return "", errors.New("update check: npm registry response did not include dist-tags.latest or dist-tags.rc")
		}
		return newest, nil
	}
	if latest == "" {
		return "", errors.New("update check: npm registry response did not include dist-tags.latest")
	}
	return latest, nil
}

func (c *updateCommand) fetchNPMDistTags(ctx context.Context) (map[string]string, error) {
	if c.client == nil {
		return nil, errors.New("update check: HTTP client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.npmRegistryAPIURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("update check: build npm registry request: %w", err)
	}
	// The abbreviated packument carries dist-tags without the full version
	// history, which keeps the response small enough for the shell gate budget
	// on a package that has published many versions.
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	req.Header.Set("User-Agent", "projmux/"+version.String())

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update check: fetch npm dist-tags: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update check: npm registry request returned status %d", resp.StatusCode)
	}
	var doc npmPackageDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("update check: parse npm registry response: %w", err)
	}
	return doc.DistTags, nil
}

func findReleaseAsset(rel githubRelease, goos, goarch string) (githubReleaseAsset, error) {
	want := releaseArchiveName(rel.TagName, goos, goarch)
	for _, asset := range rel.Assets {
		if asset.Name == want {
			if strings.TrimSpace(asset.BrowserDownloadURL) == "" {
				return githubReleaseAsset{}, fmt.Errorf("update apply: release asset %s did not include browser_download_url", want)
			}
			return asset, nil
		}
	}
	return githubReleaseAsset{}, fmt.Errorf("update apply: release %s does not include asset %s", strings.TrimSpace(rel.TagName), want)
}

func releaseArchiveName(tag, goos, goarch string) string {
	return fmt.Sprintf("projmux_%s_%s_%s.tar.gz", releaseArchiveVersion(tag), strings.TrimSpace(goos), strings.TrimSpace(goarch))
}

func releaseArchiveVersion(tag string) string {
	version := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if version == "" {
		return "latest"
	}
	return version
}

func extractProjmuxBinaryWithLimits(archive []byte, dst string, limits updateArchiveLimits) (resultErr error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	expanded := &updateMaxBytesReader{
		r:         gz,
		remaining: limits.tarBytes,
	}
	tr := tar.NewReader(expanded)
	var (
		entryCount       int
		totalRegularSize int64
		found            bool
	)
	defer func() {
		if resultErr != nil {
			_ = os.Remove(dst)
		}
	}()
	for {
		header, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read tar entry: %w", err)
		}
		entryCount++
		if entryCount > limits.entries {
			return fmt.Errorf("release archive contains more than %d entries", limits.entries)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size < 0 || header.Size > limits.regularFileBytes {
			return fmt.Errorf("release archive regular file %q size %d exceeds limit %d", header.Name, header.Size, limits.regularFileBytes)
		}
		if header.Size > limits.totalRegularBytes-totalRegularSize {
			return fmt.Errorf("release archive regular file bytes exceed total limit %d", limits.totalRegularBytes)
		}
		totalRegularSize += header.Size
		if filepath.Base(header.Name) != "projmux" {
			continue
		}
		if found {
			return errors.New("release archive contained multiple projmux binaries")
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
		if err != nil {
			return fmt.Errorf("create extracted binary: %w", err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return fmt.Errorf("write extracted binary: %w", err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("close extracted binary: %w", err)
		}
		found = true
	}
	if _, err := io.Copy(io.Discard, expanded); err != nil {
		return fmt.Errorf("read trailing archive data: %w", err)
	}
	if !found {
		return errors.New("release archive did not contain a projmux binary")
	}
	return nil
}

type updateMaxBytesReader struct {
	r         io.Reader
	remaining int64
}

func (r *updateMaxBytesReader) Read(p []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(p)) > r.remaining {
			p = p[:r.remaining]
		}
		n, err := r.r.Read(p)
		r.remaining -= int64(n)
		return n, err
	}
	var probe [1]byte
	n, err := r.r.Read(probe[:])
	if n > 0 {
		return 0, errUpdateTarTooLarge
	}
	return 0, err
}

func (c *updateCommand) loadCache() (updateCache, bool, error) {
	path, err := c.cachePath()
	if err != nil {
		return updateCache{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return updateCache{}, false, nil
		}
		return updateCache{}, false, fmt.Errorf("update status: read cache %s: %w", path, err)
	}
	var cache updateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return updateCache{}, false, fmt.Errorf("update status: parse cache %s: %w", path, err)
	}
	return cache, true, nil
}

func (c *updateCommand) saveCache(cache updateCache) error {
	path, err := c.cachePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("update check: create cache dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("update check: encode cache: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("update check: create temp cache: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update check: write temp cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update check: close temp cache: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("update check: chmod temp cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("update check: rename temp cache: %w", err)
	}
	cleanup = false
	return nil
}

func (c *updateCommand) cachePath() (string, error) {
	if c.cacheDir == nil {
		return "", errors.New("update cache directory resolver is not configured")
	}
	dir, err := c.cacheDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("update cache directory is empty")
	}
	return filepath.Join(dir, updateCacheFileName), nil
}

func (c *updateCommand) detectInstaller() updateInstaller {
	source := ""
	if c.getenv != nil {
		source = strings.TrimSpace(c.getenv("PROJMUX_INSTALLER"))
	}
	// An explicit PROJMUX_INSTALLER always wins. Only fall back to
	// autodetection when it is unset so that update apply works even when the
	// binary was not launched through the npm shim (which is the only path
	// that exports the variable today).
	autodetected := false
	if source == "" {
		source = c.autodetectInstaller()
		autodetected = source != ""
	}
	switch source {
	case "npm":
		note := "Installed by npm; update apply runs `npm install -g projmux@latest`."
		if autodetected {
			note = "Detected npm install; update apply runs `npm install -g projmux@latest`."
		}
		return updateInstaller{Source: source, Note: note}
	case "go":
		note := "Installed with Go tooling; update apply runs `go install ...@latest` and applies canonical config."
		if autodetected {
			note = "Detected `go install` binary; update apply runs `go install ...@latest` and applies canonical config."
		}
		return updateInstaller{Source: source, Note: note}
	case "github-release":
		return updateInstaller{Source: source, Note: "Installed from a GitHub release binary; update apply replaces the current binary atomically."}
	case "source":
		note := "Installed from source; update with `git pull --ff-only && make install`."
		if autodetected {
			note = "Detected source build; update with `git pull --ff-only && make install` (not auto-updated)."
		}
		return updateInstaller{Source: source, Note: note}
	case "":
		return updateInstaller{Source: "unknown", Note: "Could not detect the installer. Set PROJMUX_INSTALLER=npm|go|github-release, or update source builds with `git pull --ff-only && make install`."}
	default:
		return updateInstaller{Source: "unknown", Note: "Unrecognized PROJMUX_INSTALLER=" + source + "; expected npm|go|github-release|source."}
	}
}

// autodetectInstaller infers the installer source from the running binary when
// PROJMUX_INSTALLER is unset. It only recognizes the distribution channels that
// can be updated in place (npm and go install) plus source builds, which it
// reports so callers can print manual guidance instead of failing silently.
// github-release installs are indistinguishable from a hand-placed binary
// without a marker, so they continue to require an explicit PROJMUX_INSTALLER.
func (c *updateCommand) autodetectInstaller() string {
	exe := ""
	if c.executable != nil {
		if resolved, err := c.executable(); err == nil {
			exe = strings.TrimSpace(resolved)
		}
	}
	if exe != "" && isNpmExecutablePath(exe) {
		return "npm"
	}
	// A binary produced by `go build`/`make install` from a local checkout
	// reports a "(devel)" main module version, while `go install …@latest`
	// stamps the resolved tag. This is the only reliable way to tell a source
	// build apart from a go-install binary when both live in GOBIN/~/go/bin.
	if c.isSourceBuild() {
		return "source"
	}
	if exe != "" && c.isGoInstallPath(exe) {
		return "go"
	}
	return ""
}

// isNpmExecutablePath reports whether the resolved binary lives inside an npm
// install tree. The npm platform packages install the real binary at
// node_modules/@projmux/<platform>/bin/projmux.
func isNpmExecutablePath(exe string) bool {
	normalized := filepath.ToSlash(exe)
	if !strings.Contains(normalized, "node_modules/") {
		return false
	}
	return strings.Contains(normalized, "/@projmux/") || strings.Contains(normalized, "node_modules/projmux/")
}

func (c *updateCommand) isSourceBuild() bool {
	if c.buildInfo == nil {
		return false
	}
	info, ok := c.buildInfo()
	if !ok || info == nil {
		return false
	}
	v := strings.TrimSpace(info.Main.Version)
	return v == "" || v == "(devel)"
}

// isGoInstallPath reports whether exe sits in the directory `go install` writes
// to: $GOBIN, else $GOPATH/bin, else ~/go/bin.
func (c *updateCommand) isGoInstallPath(exe string) bool {
	dir := filepath.Dir(exe)
	for _, candidate := range c.goInstallDirs() {
		if candidate == "" {
			continue
		}
		if filepath.Clean(candidate) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

func (c *updateCommand) goInstallDirs() []string {
	getenv := c.getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	var dirs []string
	if gobin := strings.TrimSpace(getenv("GOBIN")); gobin != "" {
		dirs = append(dirs, gobin)
	}
	if gopath := strings.TrimSpace(getenv("GOPATH")); gopath != "" {
		for _, p := range filepath.SplitList(gopath) {
			if p = strings.TrimSpace(p); p != "" {
				dirs = append(dirs, filepath.Join(p, "bin"))
			}
		}
	}
	if c.userHomeDir != nil {
		if home, err := c.userHomeDir(); err == nil {
			if home = strings.TrimSpace(home); home != "" {
				dirs = append(dirs, filepath.Join(home, "go", "bin"))
			}
		}
	}
	return dirs
}

func (c *updateCommand) currentExecutable() (string, error) {
	if c.executable == nil {
		return "", errors.New("update apply: executable resolver is not configured")
	}
	exe, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("update apply: resolve current executable: %w", err)
	}
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return "", errors.New("update apply: current executable path is empty")
	}
	return exe, nil
}

func (c *updateCommand) externalRunner() func(string, []string, io.Writer, io.Writer) error {
	if c.runExternal != nil {
		return c.runExternal
	}
	return runUpdateExternal
}

// updateApplyVersionProbe is one reading of the projmux a user actually runs.
//
// It carries its own error instead of returning one so a failed pre-publication
// reading does not abort an update that is still worth attempting; the error
// resurfaces at verification time, where it becomes "not verified" rather than
// success.
type updateApplyVersionProbe struct {
	exe     string
	version string
	err     error
}

func (c *updateCommand) probeActiveVersion() updateApplyVersionProbe {
	exe, v, err := c.activeExecutableVersion()
	return updateApplyVersionProbe{exe: exe, version: v, err: err}
}

// activeExecutableVersion reports the version of the executable that answers to
// `projmux` right now.
//
// The installer's own report is deliberately never consulted. `npm install -g`
// exits 0 after writing into a prefix that PATH may not resolve first, and
// `go install` has the same failure mode with GOBIN. Both would call that a
// successful upgrade. Asking PATH for the binary the user will run next, and
// then asking that binary for its version, does not — which is the whole point
// of the check.
//
// PATH comes first and the running executable is the fallback, so an install
// that lives outside PATH entirely is still verifiable.
func (c *updateCommand) activeExecutableVersion() (string, string, error) {
	exe, err := c.activeExecutablePath()
	if err != nil {
		return "", "", err
	}
	probe := c.probeVersion
	if probe == nil {
		probe = probeInstalledProjmuxVersion
	}
	raw, err := probe(exe)
	if err != nil {
		return exe, "", fmt.Errorf("run %s version: %w", exe, err)
	}
	v, ok := parseProjmuxVersionOutput(raw)
	if !ok {
		return exe, "", fmt.Errorf("parse the version reported by %s: %q", exe, strings.TrimSpace(raw))
	}
	return exe, v, nil
}

func (c *updateCommand) activeExecutablePath() (string, error) {
	if c.lookPath != nil {
		if exe, err := c.lookPath("projmux"); err == nil {
			if exe = strings.TrimSpace(exe); exe != "" {
				return exe, nil
			}
		}
	}
	exe, err := c.currentExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve the active projmux executable: %w", err)
	}
	return exe, nil
}

func probeInstalledProjmuxVersion(exe string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateVersionProbeTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, "version")
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// parseProjmuxVersionOutput reads the `projmux <version>` line the root version
// route prints. Anything else is treated as unreadable rather than guessed at.
func parseProjmuxVersionOutput(raw string) (string, bool) {
	for line := range strings.SplitSeq(raw, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "projmux ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			continue
		}
		if _, ok := parseUpdateVersion(rest); !ok {
			continue
		}
		return rest, true
	}
	return "", false
}

// cachedLatestVersion is the version the last release check said was available.
//
// It reads the existing cache and never fetches: apply is not a check, and the
// expected version is diagnostic text, not a gate.
func (c *updateCommand) cachedLatestVersion() string {
	cache, ok, err := c.loadUsableCache()
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(cache.TagName)
}

// verifyPublishedVersion turns "every apply stage exited 0" into "the version
// the user will run next actually went up".
//
// Exit codes cannot tell a real upgrade apart from a reinstall of the same
// version, which is exactly what happens while a release exists on GitHub but
// not yet on the channel this install pulls from.
func (c *updateCommand) verifyPublishedVersion(stdout io.Writer, channel, expected string, before updateApplyVersionProbe) error {
	after := c.probeActiveVersion()
	expectedText := strings.TrimSpace(expected)
	if expectedText == "" {
		expectedText = "unknown (no cached release check; run `projmux update check`)"
	}
	if cause := firstUpdateProbeError(before, after); cause != nil {
		return fmt.Errorf(
			"update apply finished but the installed version could not be verified, so it is not reported as success: current version unknown; expected version %s; install channel %s: %w",
			expectedText, channel, cause)
	}

	beforeParts, _ := parseUpdateVersion(before.version)
	afterParts, _ := parseUpdateVersion(after.version)
	switch compareVersionParts(beforeParts, afterParts) {
	case -1:
		_, err := fmt.Fprintf(stdout, ">> verified: projmux %s is now the active executable at %s (was %s)\n",
			after.version, after.exe, before.version)
		return err
	case 0:
		return fmt.Errorf(
			"update apply finished but the installed version did not change: current version %s at %s; expected version %s; install channel %s. %s",
			after.version, after.exe, expectedText, channel,
			updateApplyStalledHint(channel, after.version, expected))
	default:
		return fmt.Errorf(
			"update apply finished but the installed version went backwards: current version %s at %s; expected version %s; install channel %s",
			after.version, after.exe, expectedText, channel)
	}
}

func firstUpdateProbeError(before, after updateApplyVersionProbe) error {
	if after.err != nil {
		return after.err
	}
	return before.err
}

// updateApplyStalledHint separates the two causes a user can act on: waiting for
// the channel to publish, and a binary that landed somewhere PATH does not read.
func updateApplyStalledHint(channel, current, expected string) string {
	if trimmed := strings.TrimSpace(expected); trimmed != "" &&
		strings.TrimPrefix(trimmed, "v") == strings.TrimPrefix(current, "v") {
		return "The active executable already holds the expected version, so this apply published nothing."
	}
	switch channel {
	case "npm":
		return "Either the expected version is not on the npm registry yet, or `npm install -g` placed it outside the PATH entry that resolves `projmux`."
	case "go":
		return "Either the module proxy has not served the expected version yet, or `go install` wrote to a GOBIN that PATH does not resolve first."
	default:
		return "Either the expected version is not published to this channel yet, or the new binary landed outside the PATH entry that resolves `projmux`."
	}
}

func updateApplyVerificationPreviewLine() string {
	return "would verify: the projmux that PATH resolves reports a higher version after publication (the installer's own report is never consulted)"
}

func runUpdateExternal(name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (c *updateCommand) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func defaultUpdateCacheDir() (string, error) {
	cacheHome := strings.TrimRight(os.Getenv("XDG_CACHE_HOME"), string(os.PathSeparator))
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		cacheHome = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheHome, "projmux"), nil
}

func writeUpdateStatusText(w io.Writer, st updateStatus) error {
	if _, err := fmt.Fprintln(w, "projmux update"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  current:   %s\n", st.CurrentVersion); err != nil {
		return err
	}
	latest := st.LatestVersion
	if latest == "" {
		latest = "unknown"
	}
	if _, err := fmt.Fprintf(w, "  latest:    %s (%s)\n", latest, st.CacheState); err != nil {
		return err
	}
	if st.CheckedAt != nil && !st.CheckedAt.IsZero() {
		if _, err := fmt.Fprintf(w, "  checked:   %s\n", st.CheckedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "  state:     %s\n", st.UpdateState); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  source:    %s\n", updateAvailabilitySourceLabel(st.SourceName)); err != nil {
		return err
	}
	// The default channel prints no channel row. Naming it would be new text in
	// a report that every existing install reads, and the axis is only worth a
	// line once it has been moved off its default.
	if normalizeUpdateReleaseChannel(st.ReleaseChannel) == updateReleaseChannelRC {
		if _, err := fmt.Fprintf(w, "  channel:   %s\n", updateReleaseChannelRC); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "  installer: %s - %s\n", st.Installer.Source, st.Installer.Note); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  cache:     %s\n", st.CachePath)
	return err
}

func writeUpdateCheckText(w io.Writer, st updateStatus) error {
	if _, err := fmt.Fprintf(w, "latest: %s\n", st.LatestVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "state: %s\n", st.UpdateState); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "cache: %s\n", st.CachePath); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "apply: run projmux update apply")
	return err
}

func writeUpdateJSON(w io.Writer, st updateStatus) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(st)
}

func printUpdateUsage(w io.Writer) {
	fmt.Fprintln(w, "projmux update")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux update status [--json]")
	fmt.Fprintln(w, "  projmux update check  [--json]")
	fmt.Fprintln(w, "  projmux update apply  [--dry-run] [--no-apply]")
}

func compareUpdateState(current, latest string) string {
	if strings.TrimSpace(latest) == "" {
		return "unknown"
	}
	c, cok := parseUpdateSemver(current)
	l, lok := parseUpdateSemver(latest)
	if cok && lok {
		switch compareUpdateSemver(c, l) {
		case -1:
			return "update_available"
		case 0:
			return "current"
		default:
			return "ahead"
		}
	}
	if strings.TrimPrefix(strings.TrimSpace(latest), "v") == strings.TrimPrefix(strings.TrimSpace(current), "v") {
		return "current"
	}
	return "unknown"
}

// updateVersionPattern captures the numeric core and, when present, the
// prerelease identifiers that follow it.
//
// The suffix used to be discarded, which read v0.15.0-rc.1 and v0.15.0 as the
// same version. That is the whole defect the rc channel trips over: an rc
// install saw its own line's stable release as "current" and could never leave
// the prerelease line, and rc.1 saw rc.2 the same way.
var updateVersionPattern = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?`)

// updateSemver is a version ordered by semver precedence.
type updateSemver struct {
	core       [3]int
	prerelease []string
}

func parseUpdateSemver(raw string) (updateSemver, bool) {
	var out updateSemver
	match := updateVersionPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return out, false
	}
	for i := 1; i <= 3; i++ {
		if match[i] == "" {
			continue
		}
		n, err := strconv.Atoi(match[i])
		if err != nil {
			return out, false
		}
		out.core[i-1] = n
	}
	if suffix := match[4]; suffix != "" {
		out.prerelease = strings.Split(suffix, ".")
	}
	return out, true
}

// parseUpdateVersion reads just the numeric core.
//
// It is kept as its own reading because the callers that build a tag from a
// version, or only ask whether a line of output is a version at all, have no
// use for prerelease precedence.
func parseUpdateVersion(raw string) ([3]int, bool) {
	parsed, ok := parseUpdateSemver(raw)
	return parsed.core, ok
}

// compareUpdateSemver orders two versions by semver precedence.
//
// Beyond the numeric core the rule is the one the old comparison lacked: a
// version carrying prerelease identifiers ranks below the same core without
// them, so 0.15.0 supersedes 0.15.0-rc.2, and two prereleases of one core are
// compared identifier by identifier.
func compareUpdateSemver(a, b updateSemver) int {
	if core := compareVersionParts(a.core, b.core); core != 0 {
		return core
	}
	switch {
	case len(a.prerelease) == 0 && len(b.prerelease) == 0:
		return 0
	case len(a.prerelease) == 0:
		return 1
	case len(b.prerelease) == 0:
		return -1
	}
	for i := 0; i < len(a.prerelease) && i < len(b.prerelease); i++ {
		if cmp := compareUpdatePrereleaseIdentifier(a.prerelease[i], b.prerelease[i]); cmp != 0 {
			return cmp
		}
	}
	switch {
	case len(a.prerelease) < len(b.prerelease):
		return -1
	case len(a.prerelease) > len(b.prerelease):
		return 1
	default:
		return 0
	}
}

// compareUpdatePrereleaseIdentifier orders one dot-separated identifier pair:
// numeric identifiers compare numerically so rc.10 outranks rc.9, and a numeric
// identifier ranks below an alphanumeric one.
func compareUpdatePrereleaseIdentifier(a, b string) int {
	an, aNumeric := strconv.Atoi(a)
	bn, bNumeric := strconv.Atoi(b)
	switch {
	case aNumeric == nil && bNumeric == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	case aNumeric == nil:
		return -1
	case bNumeric == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// newerUpdateVersion returns whichever version string is newer by semver
// precedence. An empty or unparsable candidate never wins, so a registry that
// publishes only one of the two pointers still yields that one.
func newerUpdateVersion(a, b string) string {
	av, aok := parseUpdateSemver(a)
	bv, bok := parseUpdateSemver(b)
	switch {
	case !aok && !bok:
		return ""
	case !bok:
		return strings.TrimSpace(a)
	case !aok:
		return strings.TrimSpace(b)
	case compareUpdateSemver(av, bv) < 0:
		return strings.TrimSpace(b)
	default:
		return strings.TrimSpace(a)
	}
}

func compareVersionParts(a, b [3]int) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
