package projectlinks

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// A template is literal text and placeholders. tokenizeTemplate splits it
// once, and both Validate and expansion walk the same tokens, so what Validate
// accepted is exactly what expansion substitutes.

// placeholderKind is what a placeholder reads.
type placeholderKind int

const (
	placeholderValue placeholderKind = iota
	placeholderJira
	placeholderRepo
	placeholderNamedURL
	placeholderProjectUID
	placeholderProjectName
	placeholderProjectLabel
)

// projectFieldPrefix starts every Project placeholder.
const projectFieldPrefix = "project."

// projectLabelPrefix starts a Project label placeholder.
const projectLabelPrefix = "project.labels."

// maxIndexDigits bounds the digits of an index that are parsed. Any longer
// index is past every list limit, so it is out of range, never an overflow.
const maxIndexDigits = 4

// outOfRangeIndex is the index of one with more than maxIndexDigits digits.
const outOfRangeIndex = 1 << 30

// placeholder is one parsed placeholder: index for {jira[N]} and {repo[N]},
// name for a named URL (its name) and a Project label (its key).
type placeholder struct {
	kind  placeholderKind
	index int
	name  string
}

// templateToken is literal text, or a placeholder whose text is the whole
// "{...}".
type templateToken struct {
	text        string
	placeholder bool
	parsed      placeholder
}

// tokenizeTemplate splits template into literal text and parsed placeholders.
// A "}" outside a placeholder, a "{" that is not closed before the next brace,
// and a placeholder outside the grammar are errors; whether an index, a name
// or a label exists is the caller's to check.
func tokenizeTemplate(template string) ([]templateToken, error) {
	var tokens []templateToken
	rest := template
	for rest != "" {
		open := strings.IndexAny(rest, "{}")
		if open < 0 {
			tokens = append(tokens, templateToken{text: rest})
			break
		}
		if rest[open] == '}' {
			return nil, errors.New("has a \"}\" outside a placeholder")
		}
		if open > 0 {
			tokens = append(tokens, templateToken{text: rest[:open]})
		}
		end := strings.IndexAny(rest[open+1:], "{}")
		if end < 0 || rest[open+1+end] != '}' {
			return nil, errors.New("has an unterminated placeholder")
		}
		text := rest[open : open+1+end+1]
		parsed, err := parsePlaceholder(text[1 : len(text)-1])
		if err != nil {
			return nil, fmt.Errorf("has %s: %w", text, err)
		}
		tokens = append(tokens, templateToken{text: text, placeholder: true, parsed: parsed})
		rest = rest[open+1+end+1:]
	}
	return tokens, nil
}

// parsePlaceholder parses the text between the braces of one placeholder.
func parsePlaceholder(inner string) (placeholder, error) {
	if inner == "value" {
		return placeholder{kind: placeholderValue}, nil
	}
	if strings.HasPrefix(inner, projectFieldPrefix) {
		return parseProjectPlaceholder(inner)
	}
	for _, list := range []struct {
		name string
		kind placeholderKind
	}{{"jira", placeholderJira}, {"repo", placeholderRepo}} {
		if inner == list.name {
			return placeholder{kind: list.kind}, nil
		}
		if after, ok := strings.CutPrefix(inner, list.name+"["); ok {
			index, err := parseIndex(after)
			if err != nil {
				return placeholder{}, err
			}
			return placeholder{kind: list.kind, index: index}, nil
		}
	}
	if isURLName(inner) && inner != "project" {
		return placeholder{kind: placeholderNamedURL, name: inner}, nil
	}
	return placeholder{}, errors.New("unknown placeholder; only {value}, {jira}, {jira[N]}, {repo}, {repo[N]}, a named URL {<name>}, {project.uid}, {project.name} and {project.labels.<key>} are allowed")
}

// parseIndex parses "N]": N is decimal, with no leading zero unless it is
// "0".
func parseIndex(rest string) (int, error) {
	digits, ok := strings.CutSuffix(rest, "]")
	if !ok || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, errors.New("malformed index; an index is a decimal number in [ ]")
	}
	if len(digits) > 1 && digits[0] == '0' {
		return 0, errors.New("index has a leading zero")
	}
	if len(digits) > maxIndexDigits {
		return outOfRangeIndex, nil
	}
	index, err := strconv.Atoi(digits)
	if err != nil {
		return 0, errors.New("malformed index; an index is a decimal number in [ ]")
	}
	return index, nil
}

func parseProjectPlaceholder(inner string) (placeholder, error) {
	switch inner {
	case "project.uid":
		return placeholder{kind: placeholderProjectUID}, nil
	case "project.name":
		return placeholder{kind: placeholderProjectName}, nil
	}
	if key, ok := strings.CutPrefix(inner, projectLabelPrefix); ok {
		if reason := labelKeyProblem(key); reason != "" {
			return placeholder{}, fmt.Errorf("the Project label key %q %s", key, reason)
		}
		return placeholder{kind: placeholderProjectLabel, name: key}, nil
	}
	return placeholder{}, errors.New("unknown Project field; only {project.uid}, {project.name} and {project.labels.<key>} are allowed")
}

// expandTemplate substitutes every placeholder of tokens in one pass, so text
// that a substitution inserts is never expanded again. A Jira, Repo or named
// URL becomes that URL with its trailing "/" characters trimmed, so
// "{jira}/browse/..." has one slash whether or not the URL ends in one;
// {value} and every Project field become url.PathEscape of their value. It
// returns false when a placeholder has nothing to substitute: an index past
// its list, a name urls lacks, or a Project label project lacks or has empty.
func expandTemplate(tokens []templateToken, r Rules, project Project, value string) (string, bool) {
	var b strings.Builder
	for _, token := range tokens {
		if !token.placeholder {
			b.WriteString(token.text)
			continue
		}
		p := token.parsed
		switch p.kind {
		case placeholderValue:
			b.WriteString(url.PathEscape(value))
		case placeholderJira, placeholderRepo:
			list := r.Jira
			if p.kind == placeholderRepo {
				list = r.Repo
			}
			if p.index >= len(list) {
				return "", false
			}
			b.WriteString(strings.TrimRight(list[p.index], "/"))
		case placeholderNamedURL:
			base, ok := r.URLs[p.name]
			if !ok {
				return "", false
			}
			b.WriteString(strings.TrimRight(base, "/"))
		case placeholderProjectUID:
			b.WriteString(url.PathEscape(project.UID))
		case placeholderProjectName:
			b.WriteString(url.PathEscape(project.Name))
		case placeholderProjectLabel:
			label := project.Labels[p.name]
			if label == "" {
				return "", false
			}
			b.WriteString(url.PathEscape(label))
		}
	}
	return b.String(), true
}
