package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	now      func() time.Time
	getenv   func(string) string
	cacheDir func() (string, error)
	client   updateHTTPClient
	apiURL   string
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
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
}

func newUpdateCommand() *updateCommand {
	return &updateCommand{
		now:      time.Now,
		getenv:   os.Getenv,
		cacheDir: defaultUpdateCacheDir,
		client:   &http.Client{Timeout: updateHTTPTimeout},
		apiURL:   updateReleaseURL,
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
	case "help", "--help", "-h":
		printUpdateUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown update subcommand: %s", args[0])
	}
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

	rel, err := c.fetchLatestRelease(context.Background())
	if err != nil {
		return err
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
		return errors.New("update check: latest release response did not include tag_name")
	}
	if err := c.saveCache(cache); err != nil {
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
	url := c.apiURL
	if url == "" {
		url = updateReleaseURL
	}
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
		return updateInstaller{Source: source, Note: "Installed by npm shim; update apply support is future work."}
	case "go":
		return updateInstaller{Source: source, Note: "Installed with Go tooling; use projmux upgrade or go install until update apply exists."}
	case "github-release":
		return updateInstaller{Source: source, Note: "Installed from a GitHub release binary; update apply support is future work."}
	case "source":
		return updateInstaller{Source: source, Note: "Installed from source; update from the source checkout until update apply exists."}
	case "":
		return updateInstaller{Source: "unknown", Note: "Set PROJMUX_INSTALLER=npm|go|github-release|source to make update guidance installer-aware."}
	default:
		return updateInstaller{Source: "unknown", Note: "Unrecognized PROJMUX_INSTALLER=" + source + "; expected npm|go|github-release|source."}
	}
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
	_, err := fmt.Fprintln(w, "apply: future work; no update was installed")
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
