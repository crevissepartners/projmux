// Package projectlinks owns each Project's label link rules: the rules that
// turn a label (a key and a value, such as jira=ABC-123) into a link.
//
// A Project's rules are one JSON file, <ConfigDir>/project-links/<uid>.json,
// named by the Project UID so a rename or a moved root keeps its rules. This
// package is the only owner of that path, of the JSON format, of what a valid
// rule set is, and of how a label resolves to a link. The web client edits the
// rules through a backend that calls this package; there is no CLI edit
// command.
//
// The JSON format, with every field always written:
//
//	{
//	  "jiraURL": "https://jira.example.com",
//	  "repoURL": "https://github.com/example/repo",
//	  "links": [
//	    {"labelKey": "jira", "template": "{jira}/browse/{value}"},
//	    {"labelKey": "pr", "template": "{repo}/pull/{value}"}
//	  ]
//	}
//
// A template may use three placeholders: {value} (required: the label value,
// path-escaped), {jira} (JiraURL) and {repo} (RepoURL).
package projectlinks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/state"
)

// DirName is the directory below ConfigDir that holds the rule files.
const DirName = "project-links"

// FileExt is the extension of every rule file.
const FileExt = ".json"

// Limits. A rule set past any of them is invalid, and a stored file larger
// than MaxFileSize is refused on Load rather than truncated. Write refuses an
// encoding past MaxFileSize too, so it never writes a file Load would refuse.
const (
	// MaxLinks is the largest number of link rules in one Project.
	MaxLinks = 64
	// MaxURLLength is the longest JiraURL or RepoURL, in bytes.
	MaxURLLength = 2048
	// MaxTemplateLength is the longest link template, in bytes.
	MaxTemplateLength = 2048
	// MaxLabelKeyLength is the longest label key, in bytes.
	MaxLabelKeyLength = 64
	// MaxFileSize is the largest rule file accepted, in bytes (64 KiB).
	MaxFileSize = 64 * 1024
)

// Template placeholders.
const (
	PlaceholderValue = "{value}"
	PlaceholderJira  = "{jira}"
	PlaceholderRepo  = "{repo}"
)

// sampleValue is the label value Validate expands every template with to prove
// it yields a usable link.
const sampleValue = "SAMPLE-1"

// Rules is one Project's link rules.
type Rules struct {
	// JiraURL is the base {jira} expands to. Empty means unset.
	JiraURL string `json:"jiraURL"`
	// RepoURL is the base {repo} expands to. Empty means unset.
	RepoURL string `json:"repoURL"`
	// Links are the label rules, at most one per LabelKey.
	Links []Link `json:"links"`
}

// Link is one label rule: a label whose key is LabelKey links to Template
// expanded with the label's value.
type Link struct {
	LabelKey string `json:"labelKey"`
	Template string `json:"template"`
}

// ValidationError is one invalid field. Field is the JSON path of the field
// (jiraURL, links[1].template) and Reason says why it is invalid.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Reason
}

func invalidProjectLinkField(field, format string, args ...any) error {
	return &ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Validate reports the first invalid field of r, or nil.
//
//   - JiraURL and RepoURL may be empty. A set one is an absolute http or https
//     URL with a host, at most MaxURLLength bytes, with no userinfo
//     (credentials), no query, no fragment and no "{" or "}": a template
//     appends a path to it, which a query or fragment would swallow.
//   - Links holds at most MaxLinks rules.
//   - A LabelKey is 1 to MaxLabelKeyLength bytes of ASCII letters, digits,
//     ".", "_" and "-". It is compared exactly, case included, so "Jira" and
//     "jira" are two keys; whitespace is refused, not trimmed. Two rules may
//     not share a key.
//   - A Template is at most MaxTemplateLength bytes, contains {value}, uses no
//     placeholder other than {value}, {jira} and {repo} (and no other "{" or
//     "}"), uses {jira} or {repo} only when that URL is set, and expanded with
//     a sample value is an absolute http or https URL with a host and no
//     userinfo.
func (r Rules) Validate() error {
	if err := validateBaseURL("jiraURL", r.JiraURL); err != nil {
		return err
	}
	if err := validateBaseURL("repoURL", r.RepoURL); err != nil {
		return err
	}
	if len(r.Links) > MaxLinks {
		return invalidProjectLinkField("links", "has %d rules; the limit is %d", len(r.Links), MaxLinks)
	}
	seen := make(map[string]int, len(r.Links))
	for i, link := range r.Links {
		field := fmt.Sprintf("links[%d]", i)
		if err := validateLabelKey(field+".labelKey", link.LabelKey); err != nil {
			return err
		}
		if first, ok := seen[link.LabelKey]; ok {
			return invalidProjectLinkField(field+".labelKey", "duplicates links[%d].labelKey %q", first, link.LabelKey)
		}
		seen[link.LabelKey] = i
		if err := validateTemplate(field+".template", link.Template, r); err != nil {
			return err
		}
	}
	return nil
}

func validateBaseURL(field, raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > MaxURLLength {
		return invalidProjectLinkField(field, "is %d bytes; the limit is %d", len(raw), MaxURLLength)
	}
	if strings.ContainsAny(raw, "{}") {
		return invalidProjectLinkField(field, "must not contain \"{\" or \"}\"")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return invalidProjectLinkField(field, "is not a URL: %v", err)
	}
	if err := checkLinkURL(parsed); err != nil {
		return invalidProjectLinkField(field, "%s", err)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return invalidProjectLinkField(field, "must not have a query")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return invalidProjectLinkField(field, "must not have a fragment")
	}
	return nil
}

// checkLinkURL is the rule every base URL and every expanded link meets.
func checkLinkURL(parsed *url.URL) error {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("must be an absolute http or https URL")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("must have a host")
	}
	if parsed.User != nil {
		return errors.New("must not contain userinfo (credentials)")
	}
	return nil
}

func validateLabelKey(field, key string) error {
	if key == "" {
		return invalidProjectLinkField(field, "must not be empty")
	}
	if len(key) > MaxLabelKeyLength {
		return invalidProjectLinkField(field, "is %d bytes; the limit is %d", len(key), MaxLabelKeyLength)
	}
	for _, r := range key {
		if !labelKeyRuneAllowed(r) {
			return invalidProjectLinkField(field, "contains %q; only ASCII letters, digits, \".\", \"_\" and \"-\" are allowed", string(r))
		}
	}
	return nil
}

func labelKeyRuneAllowed(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '_', r == '-':
		return true
	}
	return false
}

func validateTemplate(field, template string, r Rules) error {
	if len(template) > MaxTemplateLength {
		return invalidProjectLinkField(field, "is %d bytes; the limit is %d", len(template), MaxTemplateLength)
	}
	usesValue := false
	rest := template
	for {
		open := strings.IndexAny(rest, "{}")
		if open < 0 {
			break
		}
		if rest[open] == '}' {
			return invalidProjectLinkField(field, "has a \"}\" outside a placeholder")
		}
		end := strings.IndexAny(rest[open+1:], "{}")
		if end < 0 || rest[open+1+end] != '}' {
			return invalidProjectLinkField(field, "has an unterminated placeholder")
		}
		placeholder := rest[open : open+1+end+1]
		switch placeholder {
		case PlaceholderValue:
			usesValue = true
		case PlaceholderJira:
			if r.JiraURL == "" {
				return invalidProjectLinkField(field, "uses {jira} but jiraURL is empty")
			}
		case PlaceholderRepo:
			if r.RepoURL == "" {
				return invalidProjectLinkField(field, "uses {repo} but repoURL is empty")
			}
		default:
			return invalidProjectLinkField(field, "has unknown placeholder %s; only {value}, {jira} and {repo} are allowed", placeholder)
		}
		rest = rest[open+1+end+1:]
	}
	if !usesValue {
		return invalidProjectLinkField(field, "must contain {value}")
	}
	parsed, err := url.Parse(expandProjectLinkTemplate(template, r, sampleValue))
	if err != nil {
		return invalidProjectLinkField(field, "does not expand to a URL: %v", err)
	}
	if err := checkLinkURL(parsed); err != nil {
		return invalidProjectLinkField(field, "expanded %s", err)
	}
	return nil
}

// expandProjectLinkTemplate substitutes every placeholder of template in one
// pass, so text that a substitution inserts is never expanded again. {jira}
// and {repo} become the base URL with its trailing "/" characters trimmed, so
// "{jira}/browse/..." has one slash whether or not the base ends in one;
// {value} becomes url.PathEscape(value). Validate has already refused any
// other brace.
func expandProjectLinkTemplate(template string, r Rules, value string) string {
	return strings.NewReplacer(
		PlaceholderValue, url.PathEscape(value),
		PlaceholderJira, strings.TrimRight(r.JiraURL, "/"),
		PlaceholderRepo, strings.TrimRight(r.RepoURL, "/"),
	).Replace(template)
}

// Resolve returns the link for the label key=value: the template of the rule
// whose LabelKey equals key exactly, expanded as described on
// expandProjectLinkTemplate. It returns false for a key with no rule, an empty
// value, or a result that is not an absolute http or https URL with a host and
// no userinfo (possible only for rules that were never validated).
func Resolve(rules Rules, key, value string) (string, bool) {
	if value == "" {
		return "", false
	}
	for _, link := range rules.Links {
		if link.LabelKey != key {
			continue
		}
		resolved := expandProjectLinkTemplate(link.Template, rules, value)
		parsed, err := url.Parse(resolved)
		if err != nil || checkLinkURL(parsed) != nil {
			return "", false
		}
		return resolved, true
	}
	return "", false
}

// ValidateProjectUID accepts exactly the shape a Project UID is minted in
// (coremetadata.IsProjectUIDShaped: proj-<26 lowercase base32>). That shape
// has no path separator, no "." and no "..", so a rule file name built from it
// cannot leave the rules directory. It is a shape test only: whether the
// Project exists is the Registry's to say.
func ValidateProjectUID(uid string) error {
	if !coremetadata.IsProjectUIDShaped(uid) {
		return fmt.Errorf("project links: project uid %q is not a Project UID (proj-<26 lowercase base32>)", uid)
	}
	return nil
}

// Store reads and writes rule files below one directory.
type Store struct {
	dir string
}

// NewStore builds a store over configDir/project-links.
func NewStore(configDir string) Store {
	return Store{dir: filepath.Join(configDir, DirName)}
}

// NewDefaultStore builds a store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.ConfigDir)
}

// Dir is the directory holding the rule files.
func (s Store) Dir() string { return s.dir }

// Path returns the rule file path of the Project uid.
func (s Store) Path(uid string) (string, error) {
	if err := ValidateProjectUID(uid); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, uid+FileExt), nil
}

// Load reads the Project's rules. A missing file (or directory) is no rules:
// the zero Rules and a nil error. Anything else that is not a valid rule set
// is an error and never silently empty: a file larger than MaxFileSize, one
// that is not exactly one JSON object of the known fields, or one whose rules
// fail Validate. An empty links list loads as nil, so no rules and a file
// holding none compare equal.
func (s Store) Load(uid string) (Rules, error) {
	path, err := s.Path(uid)
	if err != nil {
		return Rules{}, err
	}
	file, err := openProjectLinksFile(s.dir, uid+FileExt)
	if errors.Is(err, fs.ErrNotExist) {
		return Rules{}, nil
	}
	if err != nil {
		return Rules{}, fmt.Errorf("read project links %q: %w", uid, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Rules{}, fmt.Errorf("read project links %q: %w", uid, err)
	}
	if !info.Mode().IsRegular() {
		return Rules{}, fmt.Errorf("read project links %q: %s is not a regular file", uid, path)
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	if err != nil {
		return Rules{}, fmt.Errorf("read project links %q: %w", uid, err)
	}
	if len(content) > MaxFileSize {
		return Rules{}, fmt.Errorf("read project links %q: %s is at least %d bytes; the limit is %d bytes", uid, path, MaxFileSize+1, MaxFileSize)
	}
	rules, err := decodeProjectLinks(content)
	if err != nil {
		return Rules{}, fmt.Errorf("read project links %q: %s is not valid project links JSON: %w", uid, path, err)
	}
	if err := rules.Validate(); err != nil {
		return Rules{}, fmt.Errorf("read project links %q: %s holds invalid rules: %w", uid, path, err)
	}
	return rules, nil
}

// decodeProjectLinks parses exactly one JSON object with no unknown fields.
func decodeProjectLinks(content []byte) (Rules, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var rules Rules
	if err := decoder.Decode(&rules); err != nil {
		return Rules{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Rules{}, errors.New("unexpected data after the JSON object")
	}
	if len(rules.Links) == 0 {
		rules.Links = nil
	}
	return rules, nil
}

// Write replaces the Project's rules, atomically: the JSON goes to a temporary
// file in the same directory, is synced, and is renamed over the target, so a
// reader sees either the old file or the new one and never a partial write.
// The file is 0600 and its directory 0700. An invalid uid, rules that fail
// Validate, or an encoding larger than MaxFileSize are refused before anything
// is written, so an existing file is left byte-identical.
func (s Store) Write(uid string, rules Rules) error {
	path, err := s.Path(uid)
	if err != nil {
		return err
	}
	if err := rules.Validate(); err != nil {
		return err
	}
	if rules.Links == nil {
		rules.Links = []Link{}
	}
	content, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("write project links %q: %w", uid, err)
	}
	content = append(content, '\n')
	if len(content) > MaxFileSize {
		return fmt.Errorf("write project links %q: encoded rules are %d bytes; the limit is %d bytes", uid, len(content), MaxFileSize)
	}
	if err := writeProjectLinksAtomic(path, content); err != nil {
		return fmt.Errorf("write project links %q: %w", uid, err)
	}
	return nil
}

// Delete removes the Project's rule file. A file or directory that does not
// exist is already deleted and is not an error.
func (s Store) Delete(uid string) error {
	path, err := s.Path(uid)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete project links %q: %w", uid, err)
	}
	defer root.Close()
	fileName := uid + FileExt
	info, err := root.Lstat(fileName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete project links %q: %w", uid, err)
	}
	if info.IsDir() {
		return fmt.Errorf("delete project links %q: %s is not a regular file", uid, path)
	}
	if err := root.Remove(fileName); err != nil {
		return fmt.Errorf("delete project links %q: %w", uid, err)
	}
	return nil
}

// openProjectLinksFile opens name inside dir through an os.Root, so the open
// cannot leave dir: not through "..", and not through a symlink that points
// outside it. A missing dir reports fs.ErrNotExist like a missing file.
func openProjectLinksFile(dir, name string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(name)
}

// writeProjectLinksAtomic writes content to a hidden temporary file beside
// path, syncs it, and renames it into place. The directory is created 0700 and
// the file is 0600.
func writeProjectLinksAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := state.EnsurePrivateDir(dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(state.PrivateFileMode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	committed = true
	state.RepairPrivateFile(path)
	return nil
}
