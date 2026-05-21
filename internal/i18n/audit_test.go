package i18n

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestAuditGoStringsCatchesHardcodedKoreanCandidate(t *testing.T) {
	findings, err := AuditGoStrings([]AuditFile{{
		Path: "internal/app/synthetic.go",
		Source: []byte(`package app

func render() {
	println("설정 열기")
}
`),
	}}, AuditOptions{})
	if err != nil {
		t.Fatalf("AuditGoStrings returned error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one Korean candidate", findings)
	}
	finding := findings[0]
	if finding.Classification != StringAuditKoreanCandidate {
		t.Fatalf("classification = %q, want %q", finding.Classification, StringAuditKoreanCandidate)
	}
	if finding.Value != "설정 열기" {
		t.Fatalf("value = %q, want Korean candidate string", finding.Value)
	}
}

func TestAuditGoStringsReportsEnglishAndIgnoresLiteralsDataDebug(t *testing.T) {
	findings, err := AuditGoStrings([]AuditFile{{
		Path: "internal/app/synthetic.go",
		Source: []byte(`package app

func render(err error) {
	println("Open Settings")
	println("Enter: choose | Esc: cancel")
	println("Codex")
	println("tmux")
	println("PROJMUX_LOCALE")
	println("/tmp/projmux")
	println("debug: %s")
}
`),
	}}, AuditOptions{IncludeIgnored: true})
	if err != nil {
		t.Fatalf("AuditGoStrings returned error: %v", err)
	}

	byValue := map[string]StringAuditFinding{}
	for _, finding := range findings {
		byValue[finding.Value] = finding
	}
	if got := byValue["Open Settings"].Classification; got != StringAuditEnglishCandidate {
		t.Fatalf("Open Settings classification = %q, want %q", got, StringAuditEnglishCandidate)
	}
	if got := byValue["Enter: choose | Esc: cancel"].Classification; got != StringAuditEnglishCandidate {
		t.Fatalf("guide phrase classification = %q, want %q", got, StringAuditEnglishCandidate)
	}
	for _, value := range []string{"Codex", "tmux", "PROJMUX_LOCALE", "/tmp/projmux", "debug: %s"} {
		finding, ok := byValue[value]
		if !ok {
			t.Fatalf("missing finding for %q in %+v", value, findings)
		}
		if finding.IsCandidate() {
			t.Fatalf("%q classified as candidate: %+v", value, finding)
		}
	}
	if got := byValue["Codex"].Classification; got != StringAuditIgnoredLiteral {
		t.Fatalf("Codex classification = %q, want %q", got, StringAuditIgnoredLiteral)
	}
	if got := byValue["tmux"].Classification; got != StringAuditIgnoredLiteral {
		t.Fatalf("tmux classification = %q, want %q", got, StringAuditIgnoredLiteral)
	}
	if got := byValue["PROJMUX_LOCALE"].Classification; got != StringAuditIgnoredData {
		t.Fatalf("PROJMUX_LOCALE classification = %q, want %q", got, StringAuditIgnoredData)
	}
	if got := byValue["/tmp/projmux"].Classification; got != StringAuditIgnoredData {
		t.Fatalf("/tmp/projmux classification = %q, want %q", got, StringAuditIgnoredData)
	}
	if got := byValue["debug: %s"].Classification; got != StringAuditIgnoredDebug {
		t.Fatalf("debug classification = %q, want %q", got, StringAuditIgnoredDebug)
	}
}

func TestRuntimeGoFilesHaveNoUnapprovedKoreanStringCandidates(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	findings, err := AuditGoStringLiteralsInDir(root, RuntimeKoreanStringAuditOptions())
	if err != nil {
		t.Fatalf("AuditGoStringLiteralsInDir returned error: %v", err)
	}
	if len(findings) > 0 {
		t.Fatalf("unapproved Korean string candidates: %+v", findings)
	}
}
