package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestClaudeDialogueProbeFrameBytes(t *testing.T) {
	got, err := claudeDialogueProbeFrame()
	if err != nil {
		t.Fatalf("claudeDialogueProbeFrame() error = %v", err)
	}
	want := []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"Reply READY.\"}}\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("claudeDialogueProbeFrame() = %q, want %q", got, want)
	}
}

func TestClaudeDialogueProbeFrameStrictDecode(t *testing.T) {
	got, err := claudeDialogueProbeFrame()
	if err != nil {
		t.Fatalf("claudeDialogueProbeFrame() error = %v", err)
	}
	line, ok := bytes.CutSuffix(got, []byte("\n"))
	if !ok || bytes.ContainsAny(line, "\r\n") {
		t.Fatalf("probe frame is not exactly one newline-terminated line: %q", got)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var frame claudeProviderUserFrame
	if err := decoder.Decode(&frame); err != nil {
		t.Fatalf("strict decode error = %v", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing data after frame: err = %v, extra = %q", err, extra)
	}
	if frame.Type != "user" || frame.Message.Role != "user" || frame.Message.Content != "Reply READY." {
		t.Fatalf("decoded frame = %+v", frame)
	}
}

var (
	userFrameTypePattern    = regexp.MustCompile(`"type"\s*:\s*"user"`)
	userFrameRolePattern    = regexp.MustCompile(`"role"\s*:`)
	userFrameContentPattern = regexp.MustCompile(`"content"\s*:`)
)

type userFrameLiteralFinding struct {
	Position token.Position
	Value    string
}

func (f userFrameLiteralFinding) String() string {
	return fmt.Sprintf("%s: hand-typed user frame literal %q", f.Position, f.Value)
}

// scanUserFrameLiterals reports string literals in the non-test Go files of
// dir that look like a hand-typed Claude user frame.
func scanUserFrameLiterals(dir string) (findings []userFrameLiteralFinding, scanned int, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(paths)
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, scanned, err
		}
		scanned++
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				value = lit.Value
			}
			if userFrameTypePattern.MatchString(value) ||
				(userFrameRolePattern.MatchString(value) && userFrameContentPattern.MatchString(value)) {
				findings = append(findings, userFrameLiteralFinding{Position: fset.Position(lit.Pos()), Value: value})
			}
			return true
		})
	}
	return findings, scanned, nil
}

func TestClaudeUserFrameHasNoHandTypedLiteral(t *testing.T) {
	findings, scanned, err := scanUserFrameLiterals(".")
	if err != nil {
		t.Fatalf("scan internal/app: %v", err)
	}
	if scanned == 0 {
		t.Fatal("scan internal/app found no non-test Go files")
	}
	for _, finding := range findings {
		t.Errorf("%s; marshal claudeProviderUserFrame instead", finding)
	}
}

func TestScanUserFrameLiteralsPositiveControl(t *testing.T) {
	dir := t.TempDir()
	source := "package p\n\nvar x = \"{\\\"type\\\":\\\"user\\\",\\\"message\\\":{}}\"\n\nvar y = `{\"role\": \"user\", \"content\": \"hi\"}`\n\ntype z struct {\n\tType string `json:\"type\"`\n\tRole string `json:\"role\"`\n\tContent string `json:\"content\"`\n}\n"
	for name, content := range map[string]string{
		"frame.go":      source,
		"frame_test.go": source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings, scanned, err := scanUserFrameLiterals(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scanned != 1 {
		t.Fatalf("scanned = %d, want 1", scanned)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %v, want 2", findings)
	}
	for i, wantLine := range []int{3, 5} {
		got := findings[i].Position
		if filepath.Base(got.Filename) != "frame.go" || got.Line != wantLine || got.Column != 9 {
			t.Fatalf("finding %d at %s, want frame.go:%d:9", i, got, wantLine)
		}
	}
}
