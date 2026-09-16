package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPaneKeysStayInTheQuestionPackage pins the one exception to projmux's
// rule that agent input does not go through raw pane keys. Answering a Claude
// Code question is allowed to press the widget's keys, in
// internal/web/question, because no answer channel exists yet. No other code
// in the web server may send keys or paste into a pane.
func TestPaneKeysStayInTheQuestionPackage(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), "_") || d.Name() == "dist" || d.Name() == "question" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"send-keys", "paste-buffer", "set-buffer", "load-buffer"} {
			if strings.Contains(string(source), forbidden) {
				t.Errorf("%s uses %q; only internal/web/question may put keys into a pane", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := filepath.Glob("../app/web*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range app {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"send-keys", "paste-buffer", "set-buffer"} {
			if strings.Contains(string(source), forbidden) {
				t.Errorf("%s uses %q; only internal/web/question may put keys into a pane", path, forbidden)
			}
		}
	}
}
