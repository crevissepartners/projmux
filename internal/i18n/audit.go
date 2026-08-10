package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// StringAuditClassification identifies how the string-literal audit classified a literal.
type StringAuditClassification string

const (
	StringAuditKoreanCandidate  StringAuditClassification = "korean-user-facing"
	StringAuditEnglishCandidate StringAuditClassification = "english-user-facing"
	StringAuditIgnoredLiteral   StringAuditClassification = "ignored-literal"
	StringAuditIgnoredData      StringAuditClassification = "ignored-data"
	StringAuditIgnoredDebug     StringAuditClassification = "ignored-debug"
)

// StringAuditFinding describes one Go string literal seen by the audit.
type StringAuditFinding struct {
	File           string
	Line           int
	Column         int
	Value          string
	Classification StringAuditClassification
	Reason         string
}

type AuditFile = StringAuditFile
type AuditOptions = StringAuditOptions
type StringCandidate = StringAuditFinding
type StringClassification = StringAuditClassification

// IsCandidate reports whether the finding is a catalog-bypass candidate.
func (f StringAuditFinding) IsCandidate() bool {
	return f.Classification == StringAuditKoreanCandidate || f.Classification == StringAuditEnglishCandidate
}

// StringAuditFile is an in-memory Go source file for tests and synthetic audits.
type StringAuditFile struct {
	Path   string
	Source []byte
}

// StringAuditOptions controls the conservative hardcoded-string audit.
type StringAuditOptions struct {
	IncludeIgnored           bool
	DisableKoreanCandidates  bool
	DisableEnglishCandidates bool
	// RestrictEnglishToPickerChrome narrows English candidates to literals that
	// sit in a user-facing picker-chrome context (a Title/Prompt/Header/Footer
	// composite-literal field, or a projmuxFooter(...) argument). This is the
	// Phase 3 governance guard: it catches new hardcoded English picker chrome
	// that bypasses the catalog without flooding the audit with the many benign
	// English data/format literals elsewhere in the codebase.
	RestrictEnglishToPickerChrome bool
	PathFilter                    func(path string) bool
}

// RuntimeKoreanStringAuditOptions returns the Phase 6 guard used by tests:
// runtime Go files are scanned for Korean catalog-bypass candidates while
// catalog data, formatter locale fragments, and test fixtures remain allowed.
func RuntimeKoreanStringAuditOptions() StringAuditOptions {
	return StringAuditOptions{
		DisableEnglishCandidates: true,
		PathFilter: func(path string) bool {
			path = filepath.ToSlash(path)
			if strings.Contains(path, "/testdata/") || strings.HasPrefix(path, "testdata/") {
				return false
			}
			if strings.HasSuffix(path, "_test.go") {
				return false
			}
			switch path {
			case "internal/i18n/default_catalog.go", "internal/i18n/formatter.go":
				return false
			default:
				return true
			}
		},
	}
}

// PickerChromeStringAuditOptions returns the Phase 3 governance guard for
// user-facing picker chrome. It keeps only English literals that sit in a
// picker-chrome field context (Title/Prompt/Header/Footer or projmuxFooter)
// within the migrated picker/footer/label surfaces, so newly hardcoded English
// chrome that bypasses the catalog is flagged while data/log/debug literals and
// the rest of the codebase stay exempt.
//
// The set of registered literals (uiTextKeys) is filtered out by the caller,
// which lives in the app package and owns that registry.
func PickerChromeStringAuditOptions() StringAuditOptions {
	scoped := map[string]bool{
		"internal/app/notify.go":                true,
		"internal/app/switch.go":                true,
		"internal/app/sessions.go":              true,
		"internal/app/session_state.go":         true,
		"internal/app/sessionstate_settings.go": true,
		"internal/app/trust.go":                 true,
		"internal/app/ai.go":                    true,
		"internal/app/hookmaker.go":             true,
	}
	return StringAuditOptions{
		DisableKoreanCandidates:       true,
		RestrictEnglishToPickerChrome: true,
		PathFilter: func(path string) bool {
			path = filepath.ToSlash(path)
			if strings.HasSuffix(path, "_test.go") {
				return false
			}
			return scoped[path]
		},
	}
}

// AuditGoStrings scans Go string literals from in-memory files.
func AuditGoStrings(files []AuditFile, opts AuditOptions) ([]StringCandidate, error) {
	return AuditGoStringLiterals(files, opts)
}

// AuditGoStringLiterals scans parsed Go string literals from in-memory files.
func AuditGoStringLiterals(files []StringAuditFile, opts StringAuditOptions) ([]StringAuditFinding, error) {
	var findings []StringAuditFinding
	for _, file := range files {
		path := filepath.ToSlash(file.Path)
		if opts.PathFilter != nil && !opts.PathFilter(path) {
			continue
		}
		fileFindings, err := auditGoStringLiteralsInFile(file)
		if err != nil {
			return nil, err
		}
		for _, finding := range fileFindings {
			if shouldKeepStringAuditFinding(finding, opts) {
				findings = append(findings, finding)
			}
		}
	}
	sortStringAuditFindings(findings)
	return findings, nil
}

// AuditGoStringLiteralsInDir scans .go files below root.
func AuditGoStringLiteralsInDir(root string, opts StringAuditOptions) ([]StringAuditFinding, error) {
	var files []StringAuditFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".wt", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if opts.PathFilter != nil && !opts.PathFilter(rel) {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, StringAuditFile{Path: rel, Source: source})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return AuditGoStringLiterals(files, opts)
}

func auditGoStringLiteralsInFile(file StringAuditFile) ([]StringAuditFinding, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.Path, file.Source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", file.Path, err)
	}
	var findings []StringAuditFinding
	var stack []ast.Node
	ast.Inspect(parsed, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		pos := fset.Position(lit.Pos())
		classification, reason := classifyStringLiteral(value, stack)
		findings = append(findings, StringAuditFinding{
			File:           filepath.ToSlash(file.Path),
			Line:           pos.Line,
			Column:         pos.Column,
			Value:          value,
			Classification: classification,
			Reason:         reason,
		})
		return true
	})
	return findings, nil
}

func shouldKeepStringAuditFinding(finding StringAuditFinding, opts StringAuditOptions) bool {
	switch finding.Classification {
	case StringAuditKoreanCandidate:
		return !opts.DisableKoreanCandidates
	case StringAuditEnglishCandidate:
		if opts.DisableEnglishCandidates {
			return false
		}
		if opts.RestrictEnglishToPickerChrome {
			return finding.Reason == pickerChromeReason
		}
		return true
	default:
		return opts.IncludeIgnored
	}
}

func sortStringAuditFindings(findings []StringAuditFinding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Column < findings[j].Column
	})
}

func classifyStringLiteral(value string, stack []ast.Node) (StringAuditClassification, string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return StringAuditIgnoredData, "empty"
	}
	if isDebugStringContext(stack) {
		return StringAuditIgnoredDebug, "debug/log/internal diagnostic context"
	}
	if isDebugStringLiteral(trimmed) {
		return StringAuditIgnoredDebug, "debug literal"
	}
	if isPreservedLiteralString(trimmed) {
		return StringAuditIgnoredLiteral, "preserved literal"
	}
	if isDataStringLiteral(trimmed) {
		return StringAuditIgnoredData, "data/config/path/enum literal"
	}
	if containsHangul(trimmed) {
		return StringAuditKoreanCandidate, "Korean user-facing candidate"
	}
	if isEnglishUserFacingCandidate(trimmed) {
		if isPickerChromeFieldContext(stack) {
			return StringAuditEnglishCandidate, pickerChromeReason
		}
		return StringAuditEnglishCandidate, "English user-facing candidate"
	}
	return StringAuditIgnoredData, "not natural-language UI text"
}

// pickerChromeReason tags an English candidate that sits in a user-facing
// picker-chrome context so the audit can scope the English guard to those
// surfaces only.
const pickerChromeReason = "English picker-chrome candidate"

// pickerChromeFieldNames are the user-facing Options chrome fields whose string
// literals must resolve through the catalog rather than being hardcoded.
var pickerChromeFieldNames = map[string]bool{
	"Title":  true,
	"Prompt": true,
	"Header": true,
	"Footer": true,
}

// isPickerChromeFieldContext reports whether the literal is the value of a
// picker-chrome composite-literal field (Title/Prompt/Header/Footer) or an
// argument to projmuxFooter(...).
func isPickerChromeFieldContext(stack []ast.Node) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		switch node := stack[i].(type) {
		case *ast.KeyValueExpr:
			if ident, ok := node.Key.(*ast.Ident); ok && pickerChromeFieldNames[ident.Name] {
				return true
			}
		case *ast.CallExpr:
			if strings.EqualFold(callName(node.Fun), "projmuxFooter") {
				return true
			}
		}
	}
	return false
}

func isDebugStringContext(stack []ast.Node) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		switch node := stack[i].(type) {
		case *ast.CallExpr:
			name := strings.ToLower(callName(node.Fun))
			switch {
			case strings.HasPrefix(name, "log."), strings.HasPrefix(name, "slog."):
				return true
			case strings.Contains(name, "debug"), strings.Contains(name, "trace"):
				return true
			case name == "fmt.errorf", name == "errors.new":
				return true
			}
		case *ast.FuncDecl:
			name := strings.ToLower(node.Name.Name)
			if strings.Contains(name, "debug") || strings.Contains(name, "diagnostic") || strings.Contains(name, "trace") {
				return true
			}
		}
	}
	return false
}

func isDebugStringLiteral(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "debug:") || strings.HasPrefix(lower, "trace:")
}

func callName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return callName(expr.X) + "." + expr.Sel.Name
	default:
		return ""
	}
}

func isPreservedLiteralString(value string) bool {
	switch value {
	case "AI", "Codex", "Claude", "GitHub", "npm", "projmux", "psmux", "tmux",
		"Windows Terminal", "Ghostty", "WezTerm", "Kitty", "iTerm2", "Alacritty", "Foot",
		"en-US", "ko-KR", "auto":
		return true
	default:
		return false
	}
}

var (
	envVarPattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)
	enumPattern        = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)+$`)
	localePattern      = regexp.MustCompile(`^[a-z]{2}-[A-Z]{2}$`)
	placeholderPattern = regexp.MustCompile(`^\{[a-zA-Z0-9_]+\}$`)
)

func isDataStringLiteral(value string) bool {
	if envVarPattern.MatchString(value) || enumPattern.MatchString(value) || localePattern.MatchString(value) || placeholderPattern.MatchString(value) {
		return true
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") {
		return true
	}
	if strings.Contains(value, `\`) || strings.Contains(value, ".toml") || strings.Contains(value, ".json") {
		return true
	}
	if strings.Contains(value, "#{") || strings.Contains(value, "#[") || strings.Contains(value, "\x1b") {
		return true
	}
	for _, prefix := range []string{
		"projmux ", "tmux ", "psmux ", "make ", "go ", "git ", "gh ", "docker ", "kubectl ",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func containsHangul(value string) bool {
	for _, r := range value {
		if r >= 0xAC00 && r <= 0xD7AF {
			return true
		}
	}
	return false
}

func isEnglishUserFacingCandidate(value string) bool {
	if !containsASCIILetter(value) {
		return false
	}
	if strings.ContainsAny(value, " \t\n:;,.!?") {
		return true
	}
	r, _ := firstRune(value)
	return unicode.IsUpper(r) && len(value) >= 3
}

func containsASCIILetter(value string) bool {
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return true
		}
	}
	return false
}

func firstRune(value string) (rune, bool) {
	for _, r := range value {
		return r, true
	}
	return 0, false
}
