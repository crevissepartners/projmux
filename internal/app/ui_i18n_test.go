package app

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
)

// TestPickerChromeLiteralsResolveThroughCatalog is the Phase 3 governance guard.
// It runs the picker-chrome string audit over the migrated picker/footer/label
// surfaces and asserts that every flagged English literal actually localizes
// through the catalog for ko-KR. A newly hardcoded English picker chrome string
// that bypasses the catalog (one that uiTextKeys does not resolve) fails here,
// keeping the localization gap from reopening.
func TestPickerChromeLiteralsResolveThroughCatalog(t *testing.T) {
	t.Parallel()

	root := repoRootForTest(t)
	findings, err := i18n.AuditGoStringLiteralsInDir(root, i18n.PickerChromeStringAuditOptions())
	if err != nil {
		t.Fatalf("picker-chrome audit failed: %v", err)
	}

	ko := i18n.Locale("ko-KR")
	var unlocalized []string
	for _, finding := range findings {
		if pickerChromeLiteralIsCatalogBacked(ko, finding.Value) {
			continue
		}
		unlocalized = append(unlocalized, finding.File+": "+finding.Value)
	}
	if len(unlocalized) > 0 {
		t.Fatalf("picker-chrome English literals bypass the catalog (register in uiTextKeys + default_catalog.go): %#v", unlocalized)
	}
}

// pickerChromeLiteralIsCatalogBacked reports whether localizing the literal for
// the given non-fallback locale yields a different string, i.e. the literal is
// backed by a catalog key (directly or via composed/path/prefix resolution).
func pickerChromeLiteralIsCatalogBacked(locale i18n.Locale, literal string) bool {
	return localizeUIText(locale, literal) != literal
}

// TestNewHardcodedPickerChromeWouldFailAudit proves the guard has teeth: a
// freshly added English footer/title literal in a picker-chrome context that is
// not registered in the catalog is flagged by the picker-chrome audit, while
// the same literal in a non-chrome context (e.g. a log/data call) is not.
func TestNewHardcodedPickerChromeWouldFailAudit(t *testing.T) {
	t.Parallel()

	source := []byte(`package app

func render() {
	_ = options{
		Footer: "Enter: detonate  |  Esc: flee",
		Title:  "Brand New Untranslated Title",
	}
	_ = projmuxFooter("Another unregistered footer phrase")
	log.Printf("Diagnostic only English phrase here")
	_ = "PROJMUX_LOCALE"
}

`)

	findings, err := i18n.AuditGoStrings([]i18n.AuditFile{{
		Path:   "internal/app/synthetic.go",
		Source: source,
	}}, i18n.StringAuditOptions{RestrictEnglishToPickerChrome: true})
	if err != nil {
		t.Fatalf("audit returned error: %v", err)
	}

	got := map[string]bool{}
	for _, finding := range findings {
		got[finding.Value] = true
	}
	for _, want := range []string{
		"Enter: detonate  |  Esc: flee",
		"Brand New Untranslated Title",
		"Another unregistered footer phrase",
	} {
		if !got[want] {
			t.Fatalf("picker-chrome audit did not flag hardcoded chrome %q; findings=%v", want, got)
		}
	}
	for _, notWant := range []string{
		"Diagnostic only English phrase here",
		"PROJMUX_LOCALE",
	} {
		if got[notWant] {
			t.Fatalf("picker-chrome audit unexpectedly flagged non-chrome literal %q", notWant)
		}
	}
}

func TestPhase15StartupFreshAndProjectionStringsHaveKoreanCatalogEntries(t *testing.T) {
	t.Parallel()

	ko := i18n.Locale("ko-KR")
	fallbacks := []string{
		"Continue project",
		"Open fresh",
		"reuse the canonical Project Window with one shell",
		"projmux: opened %s fresh with its canonical Project Window and shell",
		"Continue project / Open fresh",
		"open every saved Window, shell Pane, and Agent",
		"Enter: open  |  Esc: projects",
		"Enter: discard and start  |  Esc: cancel",
		"Cancel",
		"keep the saved state; nothing is deleted",
		"closed Project startup: show Continue project and Open fresh",
		"closed Project startup: Continue project",
		"show Continue project and Open fresh for a closed Project",
		"Project %s snapshot projection: replace Window %d / Pane %d / Agent %d; delete Window %d / Pane %d / Agent %d; preserve uid %d; lose conversation pointer %d; trust Project open gate pending; snapshot startup command execution 0; Registry writes 0 / tmux writes 0 / snapshot writes 0\n",
		"projmux: snapshot desired state was committed; runtime item was refused: ordinary Project materializer is not configured",
		"restored snapshot into Project %s: Window %d / Pane %d / Agent %d, preserved uid %d\n",
		"projmux: snapshot desired state was committed; runtime item was refused: %s",
		"restore snapshot committed desired Registry; runtime materialization needs another Continue project",
	}
	for _, fallback := range fallbacks {
		if got := localizeUIText(ko, fallback); got == fallback {
			t.Errorf("Phase 15 user-facing string has no ko-KR catalog entry: %q", fallback)
		}
	}
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
