package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/version"
)

const (
	updateCacheFileName = "update.json"
	updateCacheMaxAge   = 24 * time.Hour
	updateHTTPTimeout   = 10 * time.Second
	updateReleaseURL    = "https://api.github.com/repos/crevissepartners/projmux/releases/latest"
)

type updateHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type updateCommand struct {
	now         func() time.Time
	getenv      func(string) string
	cacheDir    func() (string, error)
	client      updateHTTPClient
	apiURL      string
	executable  func() (string, error)
	runExternal func(name string, args []string, stdout, stderr io.Writer) error
	goos        string
	goarch      string
	mkdirTemp   func(dir, pattern string) (string, error)
	removeAll   func(path string) error
	rename      func(oldpath, newpath string) error
	chmod       func(name string, mode os.FileMode) error
	remove      func(name string) error
	copyFile    func(src, dst string) error
}

type updateCache struct {
	Version     int       `json:"version"`
	CheckedAt   time.Time `json:"checked_at"`
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
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func newUpdateCommand() *updateCommand {
	return &updateCommand{
		now:         time.Now,
		getenv:      os.Getenv,
		cacheDir:    defaultUpdateCacheDir,
		client:      &http.Client{Timeout: updateHTTPTimeout},
		apiURL:      updateReleaseURL,
		executable:  os.Executable,
		runExternal: runUpdateExternal,
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		mkdirTemp:   os.MkdirTemp,
		removeAll:   os.RemoveAll,
		rename:      os.Rename,
		chmod:       os.Chmod,
		remove:      os.Remove,
		copyFile:    copyRegularFile,
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
	noApply := fs.Bool("no-apply", false, "skip running 'projmux tmux apply' after update")
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
		return nil
	}

	for _, command := range commands {
		if _, err := fmt.Fprintf(stdout, ">> running: %s\n", command.String()); err != nil {
			return err
		}
		if err := c.externalRunner()(command.Name, command.Args, stdout, stderr); err != nil {
			return fmt.Errorf("run %s: %w", command.String(), err)
		}
	}
	return nil
}

type updateApplyCommand struct {
	Name string
	Args []string
}

func (c updateApplyCommand) String() string {
	parts := append([]string{c.Name}, c.Args...)
	return strings.Join(parts, " ")
}

func (c *updateCommand) applyCommands(source string, noApply bool) ([]updateApplyCommand, error) {
	switch source {
	case "npm":
		commands := []updateApplyCommand{{Name: "npm", Args: []string{"update", "-g", "projmux"}}}
		if !noApply {
			commands = append(commands, updateApplyCommand{Name: "projmux", Args: []string{"tmux", "apply"}})
		}
		return commands, nil
	case "go":
		exe, err := c.currentExecutable()
		if err != nil {
			return nil, err
		}
		args := []string{"upgrade"}
		if noApply {
			args = append(args, "--no-apply")
		}
		return []updateApplyCommand{{Name: exe, Args: args}}, nil
	case "github-release":
		return nil, errors.New("update apply for github-release installs is handled by direct release binary replacement")
	case "source":
		return nil, errors.New("update apply for source installs is not supported; update the source checkout and rebuild")
	default:
		return nil, errors.New("update apply requires PROJMUX_INSTALLER=npm, PROJMUX_INSTALLER=go, or PROJMUX_INSTALLER=github-release")
	}
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
		if _, err := fmt.Fprintf(stdout, "would run: %s tmux apply\n", target); err != nil {
			return err
		}
	}
	return nil
}

func (c *updateCommand) runGitHubReleaseApply(noApply bool, stdout, stderr io.Writer) error {
	target, err := c.currentExecutable()
	if err != nil {
		return err
	}
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
	if err := c.downloadAndExtractReleaseAsset(context.Background(), asset.BrowserDownloadURL, extracted); err != nil {
		return err
	}
	if err := c.atomicReplaceRelease(extracted, target); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, ">> atomically replaced %s\n", target); err != nil {
		return err
	}

	if c.removeAll != nil {
		_ = c.removeAll(tmpDir)
	}
	cleanup = false

	if noApply {
		return nil
	}
	if _, err := fmt.Fprintln(stdout, ">> applying live config..."); err != nil {
		return err
	}
	if err := c.externalRunner()(target, []string{"tmux", "apply"}, stdout, stderr); err != nil {
		return fmt.Errorf("apply live config via %s tmux apply: %w", target, err)
	}
	return nil
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

func (c *updateCommand) downloadAndExtractReleaseAsset(ctx context.Context, url, dst string) error {
	if c.client == nil {
		return errors.New("update apply: HTTP client is not configured")
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return errors.New("update apply: release asset did not include browser_download_url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("update apply: build release asset request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "projmux/"+version.String())

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("update apply: download release asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update apply: release asset request returned status %d", resp.StatusCode)
	}
	if err := extractProjmuxBinary(resp.Body, dst); err != nil {
		return fmt.Errorf("update apply: extract release asset: %w", err)
	}
	return nil
}

func (c *updateCommand) atomicReplaceRelease(src, target string) error {
	upgrade := upgradeCommand{
		rename:        c.rename,
		chmod:         c.chmod,
		remove:        c.remove,
		copyFile:      c.copyFile,
		tempSuffixGen: defaultTempSuffix,
	}
	return upgrade.atomicReplace(src, target)
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

	cache, err := c.fetchAndSaveLatestRelease(context.Background())
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
	cache, ok, err := c.loadCache()
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
			UpdateState:    "unknown",
			Installer:      c.detectInstaller(),
			CachePath:      path,
		}, nil
	}
	return c.statusFromCache(cache)
}

func (c *updateCommand) refreshCacheIfNeeded(ctx context.Context) error {
	cache, ok, err := c.loadCache()
	if err != nil {
		return err
	}
	if ok {
		checked := cache.CheckedAt.UTC()
		if !checked.IsZero() && c.clock().Sub(checked) <= updateCacheMaxAge {
			return nil
		}
	}
	_, err = c.fetchAndSaveLatestRelease(ctx)
	return err
}

func (c *updateCommand) fetchAndSaveLatestRelease(ctx context.Context) (updateCache, error) {
	rel, err := c.fetchLatestRelease(ctx)
	if err != nil {
		return updateCache{}, err
	}
	cache := updateCache{
		Version:     1,
		CheckedAt:   c.clock().UTC(),
		TagName:     strings.TrimSpace(rel.TagName),
		Name:        strings.TrimSpace(rel.Name),
		HTMLURL:     strings.TrimSpace(rel.HTMLURL),
		PublishedAt: rel.PublishedAt.UTC(),
	}
	if cache.TagName == "" {
		return updateCache{}, errors.New("update check: latest release response did not include tag_name")
	}
	if err := c.saveCache(cache); err != nil {
		return updateCache{}, err
	}
	return cache, nil
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

func extractProjmuxBinary(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read tar entry: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if filepath.Base(header.Name) != "projmux" {
			continue
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
		return nil
	}
	return errors.New("release archive did not contain a projmux binary")
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
	switch source {
	case "npm":
		return updateInstaller{Source: source, Note: "Installed by npm shim; update with projmux update apply or npm update -g projmux."}
	case "go":
		return updateInstaller{Source: source, Note: "Installed with Go tooling; update apply delegates to projmux upgrade."}
	case "github-release":
		return updateInstaller{Source: source, Note: "Installed from a GitHub release binary; update apply replaces the current binary atomically."}
	case "source":
		return updateInstaller{Source: source, Note: "Installed from source; update from the source checkout until update apply exists."}
	case "":
		return updateInstaller{Source: "unknown", Note: "Set PROJMUX_INSTALLER=npm|go|github-release|source to make update guidance installer-aware."}
	default:
		return updateInstaller{Source: "unknown", Note: "Unrecognized PROJMUX_INSTALLER=" + source + "; expected npm|go|github-release|source."}
	}
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
	c, cok := parseUpdateVersion(current)
	l, lok := parseUpdateVersion(latest)
	if cok && lok {
		switch compareVersionParts(c, l) {
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

var updateVersionPattern = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

func parseUpdateVersion(raw string) ([3]int, bool) {
	var out [3]int
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
		out[i-1] = n
	}
	return out, true
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
