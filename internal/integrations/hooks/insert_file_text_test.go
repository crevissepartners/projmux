package hooks

import (
	"strings"
	"testing"
)

func TestParseInsertFileTextSourceDefaultsTrimTrue(t *testing.T) {
	cfg, err := ParseProjectConfig(`[insert_file_text.latest_screenshot]
path = "~/.screenshots/latest.path"
`)
	if err != nil {
		t.Fatalf("ParseProjectConfig returned error: %v", err)
	}
	source, ok := cfg.InsertFileText["latest_screenshot"]
	if !ok {
		t.Fatalf("insert_file_text source not parsed: %#v", cfg.InsertFileText)
	}
	if source.Path != "~/.screenshots/latest.path" {
		t.Fatalf("source path = %q, want the raw ~ path (expansion happens at read time)", source.Path)
	}
	if !source.Trim {
		t.Fatalf("trim = %v, want default true", source.Trim)
	}
}

func TestParseInsertFileTextTrimOptOut(t *testing.T) {
	cfg, err := ParseProjectConfig(`[insert_file_text.raw]
path = "/tmp/raw.txt"
trim = false
`)
	if err != nil {
		t.Fatalf("ParseProjectConfig returned error: %v", err)
	}
	if cfg.InsertFileText["raw"].Trim {
		t.Fatalf("trim = true, want false after opt-out")
	}
}

func TestParseInsertFileTextRejectsInvalidName(t *testing.T) {
	_, err := ParseProjectConfig(`[insert_file_text.bad name]
path = "/tmp/x"
`)
	if err == nil {
		t.Fatalf("expected an error for an invalid source name")
	}
	if !strings.Contains(err.Error(), "unsupported section") {
		t.Fatalf("error = %v, want unsupported section", err)
	}
}

func TestParseInsertFileTextRejectsUnknownKey(t *testing.T) {
	_, err := ParseProjectConfig(`[insert_file_text.x]
path = "/tmp/x"
mode = "append"
`)
	if err == nil || !strings.Contains(err.Error(), "unsupported insert_file_text key") {
		t.Fatalf("error = %v, want unsupported insert_file_text key", err)
	}
}

func TestParseInsertFileTextRejectsNonBoolTrim(t *testing.T) {
	_, err := ParseProjectConfig(`[insert_file_text.x]
path = "/tmp/x"
trim = "yes"
`)
	if err == nil || !strings.Contains(err.Error(), "boolean") {
		t.Fatalf("error = %v, want a boolean parse error", err)
	}
}

func TestValidateProjectConfigRequiresInsertFileTextPath(t *testing.T) {
	cfg, err := ParseProjectConfig(`[insert_file_text.x]
path = ""
`)
	if err != nil {
		t.Fatalf("ParseProjectConfig returned error: %v", err)
	}
	if err := validateProjectConfig(cfg); err == nil {
		t.Fatalf("expected a validation error for an empty path")
	}
}

func TestRenderInsertFileTextRoundTrips(t *testing.T) {
	original := `[insert_file_text.raw]
path = "/tmp/raw.txt"
trim = false

[insert_file_text.screenshot]
path = "~/.screenshots/latest.path"
`
	cfg, err := ParseProjectConfig(original)
	if err != nil {
		t.Fatalf("ParseProjectConfig returned error: %v", err)
	}
	rendered := renderProjectConfig(cfg)

	reparsed, err := ParseProjectConfig(rendered)
	if err != nil {
		t.Fatalf("re-parse of rendered config failed: %v\nrendered:\n%s", err, rendered)
	}
	if len(reparsed.InsertFileText) != 2 {
		t.Fatalf("round-trip lost sources: %#v", reparsed.InsertFileText)
	}
	if reparsed.InsertFileText["raw"].Trim {
		t.Fatalf("round-trip lost trim=false opt-out")
	}
	if !reparsed.InsertFileText["screenshot"].Trim {
		t.Fatalf("round-trip lost trim default true")
	}
	// The trim-on default is not persisted; only the opt-out is written.
	if strings.Contains(rendered, "trim = true") {
		t.Fatalf("render wrote the default trim = true:\n%s", rendered)
	}
	if !strings.Contains(rendered, "trim = false") {
		t.Fatalf("render dropped the trim = false opt-out:\n%s", rendered)
	}
}

func TestUnconfiguredInsertFileTextIsNil(t *testing.T) {
	cfg, err := ParseProjectConfig("[startup]\nrun = \"echo hi\"\n")
	if err != nil {
		t.Fatalf("ParseProjectConfig returned error: %v", err)
	}
	if cfg.InsertFileText != nil {
		t.Fatalf("InsertFileText = %#v, want nil when unconfigured", cfg.InsertFileText)
	}
}
