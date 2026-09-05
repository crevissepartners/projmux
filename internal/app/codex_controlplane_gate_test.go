package app

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// codexUncapturedReasonTokens are the values that mean "nothing recorded why".
//
// They are legal to read and illegal to expect. Each one is either an explicit
// nothing-recorded bucket, a literal a pre-instrumentation binary wrote before
// any reason was captured, or the fallback a bounded read renders for a value
// outside its vocabulary. None of them is the outcome of an observation, so a
// test that fixes one as its expected value certifies that no observation
// happened — which is what a whole neighbouring track shipped green against.
var codexUncapturedReasonTokens = []string{
	"unrecorded",
	"disconnected",
	"bounded reason unavailable",
	"target-unmatched",
}

// codexUncapturedDefaultMarker admits one occurrence.
//
// The marker must carry a reason, because the point of the sweep is not to
// count occurrences but to force each survivor to say why that value is a real
// expectation. Every current survivor is a different vocabulary that happens to
// spell a token the same way.
const codexUncapturedDefaultMarker = "uncaptured-default:"

// codexUncapturedGateFile is this file, which declares the vocabulary and is
// therefore the one place the tokens appear as literals rather than as
// expectations.
const codexUncapturedGateFile = "codex_controlplane_gate_test.go"

// TestControlPlaneTestsNeverExpectAnUncapturedReason is the false-certification
// gate.
//
// It exists because the failure it guards against already happened here in a
// worse form than an untested surface: `test/e2e/codex-lifecycle.sh` asserted
// the literal `disconnected` for a managed Codex Pane's authority reason, and
// `disconnected` is the bucket meaning no reason was captured. The suite was
// not failing to reach that surface. It reached it and pinned the broken state
// as the passing condition, so capturing a real reason would have turned the
// job red.
//
// The rule is deliberately about expectations rather than mentions. A test may
// drive an uncaptured token as input all it likes; what it may not do is
// require one to come back.
func TestControlPlaneTestsNeverExpectAnUncapturedReason(t *testing.T) {
	root := repoRootForGate(t)
	goToken := regexp.MustCompile(`"(` + strings.Join(codexUncapturedReasonTokens, "|") + `)"`)
	shellToken := regexp.MustCompile(`(^|[\s"'=])(` + strings.Join(codexUncapturedReasonTokens, "|") + `)($|[\s"'\\])`)
	var unjustified []string
	for _, scope := range []struct {
		dir     string
		match   func(string) bool
		pattern *regexp.Regexp
	}{
		{dir: "internal", match: func(name string) bool { return strings.HasSuffix(name, "_test.go") }, pattern: goToken},
		{dir: filepath.Join("test", "e2e"), match: func(name string) bool { return strings.HasSuffix(name, ".sh") }, pattern: shellToken},
	} {
		err := filepath.WalkDir(filepath.Join(root, scope.dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !scope.match(entry.Name()) {
				return err
			}
			payload, readErr := os.ReadFile(path) // #nosec G304 -- repository source under test.
			if readErr != nil {
				return readErr
			}
			relative, _ := filepath.Rel(root, path)
			lines := strings.Split(string(payload), "\n")
			if entry.Name() == codexUncapturedGateFile {
				// The file that declares the vocabulary is the one place the
				// tokens have to appear as themselves.
				return nil
			}
			for index, line := range lines {
				if isGateComment(line) || !scope.pattern.MatchString(line) {
					continue
				}
				if uncapturedDefaultJustified(lines, index) {
					continue
				}
				unjustified = append(unjustified, relative+":"+strconv.Itoa(index+1)+": "+strings.TrimSpace(line))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", scope.dir, err)
		}
	}
	if len(unjustified) > 0 {
		t.Fatalf("%d expectation(s) fix a value that means no reason was captured.\n"+
			"Expect a captured token instead, or state on the line above why this value is a real expectation "+
			"with a `%s <reason>` comment:\n  %s",
			len(unjustified), codexUncapturedDefaultMarker, strings.Join(unjustified, "\n  "))
	}
}

// isGateComment reports whether a line is prose rather than code. A comment
// discussing an uncaptured token is not an expectation of one, and this gate
// must not push the vocabulary out of the comments that explain it.
func isGateComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#")
}

// uncapturedDefaultJustified reports whether an occurrence carries its reason
// on its own line or in the comment block directly above it.
//
// The whole block is searched rather than one line, because a reason worth
// stating rarely fits on one and a gate that forced it to would be answered
// with a shorter reason instead of a better one.
func uncapturedDefaultJustified(lines []string, index int) bool {
	if markerCarriesReason(lines[index]) {
		return true
	}
	for above := index - 1; above >= 0 && isGateComment(lines[above]); above-- {
		if markerCarriesReason(lines[above]) {
			return true
		}
	}
	return false
}

// markerCarriesReason reports whether one line states a reason after the
// marker. An empty marker admits nothing: the sweep exists to collect reasons,
// not to collect markers.
func markerCarriesReason(line string) bool {
	_, after, ok := strings.Cut(line, codexUncapturedDefaultMarker)
	if !ok {
		return false
	}
	return strings.TrimSpace(after) != ""
}

// TestControlPlaneContractCellsNameLiveTests is the mechanical half of the
// release check the roadmap contract carries: every Enforcement cell of the
// five product contracts must name a test that exists.
//
// A contract cell naming a renamed or deleted test is not a weaker guarantee,
// it is a false one: the cell reads as enforced and nothing enforces it. Squash
// merges erase the branch history that would otherwise tie the two together, so
// the tie has to live somewhere a build can check.
func TestControlPlaneContractCellsNameLiveTests(t *testing.T) {
	root := repoRootForGate(t)
	declared := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return err
		}
		payload, readErr := os.ReadFile(path) // #nosec G304 -- repository source under test.
		if readErr != nil {
			return readErr
		}
		for _, match := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(string(payload), -1) {
			declared[match[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	var missing []string
	for contract, tests := range codexControlPlaneContractEnforcement {
		for _, name := range tests {
			if !declared[name] {
				missing = append(missing, contract+" -> "+name)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d contract Enforcement entr(ies) name no live test:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// TestControlPlaneContractEnforcementCoversEverySurface holds the other half:
// each of the five surfaces this diagnosis names has at least one contract cell
// behind it, and the contract map has no surface the diagnosis cannot render.
func TestControlPlaneContractEnforcementCoversEverySurface(t *testing.T) {
	for _, surface := range codexControlPlaneSurfaceOrder {
		contract, ok := codexControlPlaneSurfaceContract[surface]
		if !ok {
			t.Fatalf("surface %q names no product contract", surface)
		}
		if len(codexControlPlaneContractEnforcement[contract]) == 0 {
			t.Fatalf("surface %q maps to contract %q, which enforces nothing", surface, contract)
		}
	}
	for surface := range codexControlPlaneSurfaceContract {
		found := slices.Contains(codexControlPlaneSurfaceOrder, surface)
		if !found {
			t.Fatalf("contract map names surface %q, which the diagnosis does not render", surface)
		}
	}
}

// repoRootForGate walks up from the package directory to the module root.
func repoRootForGate(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("module root not found above the package directory")
	return ""
}
