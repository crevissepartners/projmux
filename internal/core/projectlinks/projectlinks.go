// Package projectlinks owns each Project's label link rules: the rules that
// turn a label (a key and a value, such as jira=ABC-123) into a link.
//
// A Project's rules are one JSON file, <ConfigDir>/project-links/<uid>.json,
// named by the Project UID so a rename or a moved root keeps its rules. This
// package is the only owner of that path, of the JSON format, of what a valid
// rule set is, and of how a label resolves to a link. The rules belong to the
// Project, and every surface shares them: a Claude Agent's system prompt shows
// them, and a client that edits them, such as the web client, reads and writes
// them through this package. There is no CLI edit command.
//
// The JSON format, with every field always written:
//
//	{
//	  "jira": ["https://jira.example.com", "https://jira.partner.example.com"],
//	  "repo": ["https://github.com/example/repo"],
//	  "urls": {"wiki": "https://wiki.example.com/spaces/APP"},
//	  "links": [
//	    {"labelKey": "jira", "template": "{jira}/browse/{value}"},
//	    {"labelKey": "partner", "template": "{jira[1]}/browse/{value}"},
//	    {"labelKey": "pr", "template": "{repo}/pull/{value}"},
//	    {"labelKey": "doc", "template": "{wiki}/{project.name}/{value}"}
//	  ]
//	}
//
// A template may use these placeholders:
//
//   - {value} (required): the label value, path-escaped.
//   - {jira[N]} and {repo[N]}: the Nth URL of jira or repo, counting from 0;
//     {jira} and {repo} are {jira[0]} and {repo[0]}.
//   - {<name>}: the URL urls names <name>.
//   - {project.uid}, {project.name} and {project.labels.<key>}: the Project's
//     UID, name and the value of its label <key>, path-escaped. Nothing else
//     of the Project (not its root, not its annotations) is a placeholder.
package projectlinks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	// MaxJiraURLs is the largest number of Jira URLs in one Project.
	MaxJiraURLs = 16
	// MaxRepoURLs is the largest number of Repo URLs in one Project.
	MaxRepoURLs = 16
	// MaxNamedURLs is the largest number of named URLs in one Project.
	MaxNamedURLs = 32
	// MaxLinks is the largest number of link rules in one Project.
	MaxLinks = 64
	// MaxURLLength is the longest Jira, Repo or named URL, in bytes.
	MaxURLLength = 2048
	// MaxTemplateLength is the longest link template, in bytes.
	MaxTemplateLength = 2048
	// MaxLabelKeyLength is the longest label key, in bytes.
	MaxLabelKeyLength = 64
	// MaxURLNameLength is the longest name of a named URL, in bytes.
	MaxURLNameLength = 32
	// MaxFileSize is the largest rule file accepted, in bytes (64 KiB).
	MaxFileSize = 64 * 1024
)

// Template placeholders with a fixed spelling. {jira[N]}, {repo[N]}, {<name>}
// and {project.labels.<key>} are families; see the package comment.
const (
	PlaceholderValue       = "{value}"
	PlaceholderJira        = "{jira}"
	PlaceholderRepo        = "{repo}"
	PlaceholderProjectUID  = "{project.uid}"
	PlaceholderProjectName = "{project.name}"
	// PlaceholderProjectLabel is the pattern of the Project label family:
	// <key> stands for any label key.
	PlaceholderProjectLabel = "{project.labels.<key>}"
)

// reservedURLNames are the names a named URL may not take: each is (or
// starts) a placeholder of its own.
var reservedURLNames = []string{"value", "jira", "repo", "project"}

// sampleValue is the label value Validate expands every template with to prove
// it yields a usable link.
const sampleValue = "SAMPLE-1"

// Rules is one Project's link rules.
type Rules struct {
	// Jira are the URLs {jira[N]} expand to; {jira} is the first.
	Jira []string `json:"jira"`
	// Repo are the URLs {repo[N]} expand to; {repo} is the first.
	Repo []string `json:"repo"`
	// URLs are the named URLs: {<name>} expands to URLs[name].
	URLs map[string]string `json:"urls"`
	// Links are the label rules, at most one per LabelKey.
	Links []Link `json:"links"`
}

// Link is one label rule: a label whose key is LabelKey links to Template
// expanded with the label's value.
type Link struct {
	LabelKey string `json:"labelKey"`
	Template string `json:"template"`
}

// Project is what a template may read of a Project: its UID, its name and its
// labels. It deliberately holds nothing else, so a rule can never put the
// Project's root, its annotations or its Windows into a link.
type Project struct {
	UID    string
	Name   string
	Labels map[string]string
}

// ProjectOf takes a Registry Project's UID, name and labels (cloned), and
// nothing else of it.
func ProjectOf(p coremetadata.Project) Project {
	return Project{UID: p.Metadata.UID, Name: p.Metadata.Name, Labels: maps.Clone(p.Metadata.Labels)}
}

// ValidationError is one invalid field. Field is the JSON path of the field
// (jira[3], urls.wiki, links[1].template) and Reason says why it is invalid.
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
//   - Jira holds at most MaxJiraURLs URLs, Repo at most MaxRepoURLs and URLs
//     at most MaxNamedURLs. Each URL is an absolute http or https URL with a
//     host, at most MaxURLLength bytes, with no userinfo (credentials), no
//     query, no fragment and no "{" or "}": a template appends a path to it,
//     which a query or fragment would swallow.
//   - A URLs name is a lowercase ASCII letter followed by at most
//     MaxURLNameLength-1 lowercase letters, digits, "_" and "-", and is not
//     value, jira, repo or project.
//   - Links holds at most MaxLinks rules.
//   - A LabelKey is 1 to MaxLabelKeyLength bytes of ASCII letters, digits,
//     ".", "_" and "-". It is compared exactly, case included, so "Jira" and
//     "jira" are two keys; whitespace is refused, not trimmed. Two rules may
//     not share a key.
//   - A Template is at most MaxTemplateLength bytes, contains {value}, has no
//     "{" or "}" outside a placeholder, and uses only the placeholders of the
//     package comment: an index within its list (decimal, no leading zero),
//     a name that URLs defines, and a Project field that exists. A
//     {project.labels.<key>} is checked for its key's syntax only, since the
//     label may be added later. Expanded with a sample value and a sample
//     Project, it is an absolute http or https URL with a host and no
//     userinfo.
func (r Rules) Validate() error {
	if err := validateURLList("jira", r.Jira, MaxJiraURLs); err != nil {
		return err
	}
	if err := validateURLList("repo", r.Repo, MaxRepoURLs); err != nil {
		return err
	}
	if len(r.URLs) > MaxNamedURLs {
		return invalidProjectLinkField("urls", "has %d URLs; the limit is %d", len(r.URLs), MaxNamedURLs)
	}
	for _, name := range slices.Sorted(maps.Keys(r.URLs)) {
		field := "urls." + name
		if err := validateURLName(field, name); err != nil {
			return err
		}
		if err := validateBaseURL(field, r.URLs[name]); err != nil {
			return err
		}
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

func validateURLList(field string, urls []string, limit int) error {
	if len(urls) > limit {
		return invalidProjectLinkField(field, "has %d URLs; the limit is %d", len(urls), limit)
	}
	for i, raw := range urls {
		if err := validateBaseURL(fmt.Sprintf("%s[%d]", field, i), raw); err != nil {
			return err
		}
	}
	return nil
}

func validateBaseURL(field, raw string) error {
	if raw == "" {
		return invalidProjectLinkField(field, "must not be empty")
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

func validateURLName(field, name string) error {
	if slices.Contains(reservedURLNames, name) {
		return invalidProjectLinkField(field, "name %q is reserved; value, jira, repo and project cannot name a URL", name)
	}
	if !isURLName(name) {
		return invalidProjectLinkField(field, "name %q must be a lowercase ASCII letter followed by at most %d lowercase letters, digits, \"_\" or \"-\"", name, MaxURLNameLength-1)
	}
	return nil
}

// isURLName reports whether name has the shape of a named URL:
// [a-z][a-z0-9_-]{0,31}. It does not look at the reserved names.
func isURLName(name string) bool {
	if name == "" || len(name) > MaxURLNameLength || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validateLabelKey(field, key string) error {
	if reason := labelKeyProblem(key); reason != "" {
		return invalidProjectLinkField(field, "%s", reason)
	}
	return nil
}

// labelKeyProblem says why key is not a label key, or "" when it is one.
func labelKeyProblem(key string) string {
	if key == "" {
		return "must not be empty"
	}
	if len(key) > MaxLabelKeyLength {
		return fmt.Sprintf("is %d bytes; the limit is %d", len(key), MaxLabelKeyLength)
	}
	for _, r := range key {
		if !labelKeyRuneAllowed(r) {
			return fmt.Sprintf("contains %q; only ASCII letters, digits, \".\", \"_\" and \"-\" are allowed", string(r))
		}
	}
	return ""
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
	tokens, err := tokenizeTemplate(template)
	if err != nil {
		return invalidProjectLinkField(field, "%s", err)
	}
	usesValue := false
	sample := Project{UID: "sample-uid", Name: "sample-name", Labels: map[string]string{}}
	for _, token := range tokens {
		if !token.placeholder {
			continue
		}
		p := token.parsed
		switch p.kind {
		case placeholderValue:
			usesValue = true
		case placeholderJira:
			if p.index >= len(r.Jira) {
				return invalidProjectLinkField(field, "uses %s but %s", token.text, listLengthPhrase("jira", len(r.Jira)))
			}
		case placeholderRepo:
			if p.index >= len(r.Repo) {
				return invalidProjectLinkField(field, "uses %s but %s", token.text, listLengthPhrase("repo", len(r.Repo)))
			}
		case placeholderNamedURL:
			if _, ok := r.URLs[p.name]; !ok {
				return invalidProjectLinkField(field, "uses %s but urls has no %q", token.text, p.name)
			}
		case placeholderProjectLabel:
			sample.Labels[p.name] = "sample-label"
		}
	}
	if !usesValue {
		return invalidProjectLinkField(field, "must contain {value}")
	}
	expanded, _ := expandTemplate(tokens, r, sample, sampleValue)
	parsed, err := url.Parse(expanded)
	if err != nil {
		return invalidProjectLinkField(field, "does not expand to a URL: %v", err)
	}
	if err := checkLinkURL(parsed); err != nil {
		return invalidProjectLinkField(field, "expanded %s", err)
	}
	return nil
}

func listLengthPhrase(list string, n int) string {
	switch n {
	case 0:
		return list + " has no URLs"
	case 1:
		return list + " has 1 URL"
	}
	return fmt.Sprintf("%s has %d URLs", list, n)
}

// Resolve returns the link for the label key=value of project: the template
// of the rule whose LabelKey equals key exactly, expanded as described on
// expandTemplate. The value is one value: "a,b" is one link, never two. It
// returns false for an empty value, a key with no rule, a template that uses
// a Project label project lacks or has empty, or a result that is not an
// absolute http or https URL with a host and no userinfo (possible only for
// rules that were never validated).
func Resolve(rules Rules, project Project, key, value string) (string, bool) {
	if value == "" {
		return "", false
	}
	for _, link := range rules.Links {
		if link.LabelKey != key {
			continue
		}
		tokens, err := tokenizeTemplate(link.Template)
		if err != nil {
			return "", false
		}
		resolved, ok := expandTemplate(tokens, rules, project, value)
		if !ok {
			return "", false
		}
		parsed, err := url.Parse(resolved)
		if err != nil || checkLinkURL(parsed) != nil {
			return "", false
		}
		return resolved, true
	}
	return "", false
}

// Placeholders lists every placeholder a template may use with rules, in a
// fixed order: {value}; {jira} and {jira[0]} to {jira[n-1]} when Jira has
// URLs; the same for {repo}; {<name>} for each named URL, sorted by name;
// {project.uid}; {project.name}; and last PlaceholderProjectLabel, which is
// a pattern rather than a placeholder: <key> stands for any label key.
func Placeholders(rules Rules) []string {
	out := []string{PlaceholderValue}
	for _, list := range []struct {
		name string
		urls []string
	}{{"jira", rules.Jira}, {"repo", rules.Repo}} {
		if len(list.urls) == 0 {
			continue
		}
		out = append(out, "{"+list.name+"}")
		for i := range list.urls {
			out = append(out, fmt.Sprintf("{%s[%d]}", list.name, i))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(rules.URLs)) {
		out = append(out, "{"+name+"}")
	}
	return append(out, PlaceholderProjectUID, PlaceholderProjectName, PlaceholderProjectLabel)
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
// fail Validate. Empty lists and an empty urls map load as nil, so no rules
// and a file holding none compare equal.
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

// projectLinksFile is the stored JSON. The list and map fields are raw so
// that a value of the wrong type is reported under its field name.
type projectLinksFile struct {
	Jira  json.RawMessage `json:"jira"`
	Repo  json.RawMessage `json:"repo"`
	URLs  json.RawMessage `json:"urls"`
	Links []Link          `json:"links"`
}

// decodeProjectLinks parses exactly one JSON object with no unknown fields.
func decodeProjectLinks(content []byte) (Rules, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var stored projectLinksFile
	if err := decoder.Decode(&stored); err != nil {
		return Rules{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Rules{}, errors.New("unexpected data after the JSON object")
	}
	rules := Rules{Links: stored.Links}
	for _, field := range []struct {
		name string
		raw  json.RawMessage
		into *[]string
	}{
		{"jira", stored.Jira, &rules.Jira},
		{"repo", stored.Repo, &rules.Repo},
	} {
		if field.raw != nil {
			if err := json.Unmarshal(field.raw, field.into); err != nil {
				return Rules{}, fmt.Errorf("%s: %w", field.name, err)
			}
		}
	}
	if stored.URLs != nil {
		if err := json.Unmarshal(stored.URLs, &rules.URLs); err != nil {
			return Rules{}, fmt.Errorf("urls: %w", err)
		}
	}
	if len(rules.Jira) == 0 {
		rules.Jira = nil
	}
	if len(rules.Repo) == 0 {
		rules.Repo = nil
	}
	if len(rules.URLs) == 0 {
		rules.URLs = nil
	}
	if len(rules.Links) == 0 {
		rules.Links = nil
	}
	return rules, nil
}

// Write replaces the Project's rules, atomically: the JSON goes to a temporary
// file in the same directory, is synced, and is renamed over the target, so a
// reader sees either the old file or the new one and never a partial write.
// The file is 0600 and its directory 0700. It always writes the current
// format, all four fields included. An invalid uid, rules that fail Validate,
// or an encoding larger than MaxFileSize are refused before anything is
// written, so an existing file is left byte-identical.
func (s Store) Write(uid string, rules Rules) error {
	path, err := s.Path(uid)
	if err != nil {
		return err
	}
	if err := rules.Validate(); err != nil {
		return err
	}
	if rules.Jira == nil {
		rules.Jira = []string{}
	}
	if rules.Repo == nil {
		rules.Repo = []string{}
	}
	if rules.URLs == nil {
		rules.URLs = map[string]string{}
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
