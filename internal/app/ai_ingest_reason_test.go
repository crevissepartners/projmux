package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// aiIngestReasonForbiddenSubstrings are the shapes that must never reach the
// reason column. They are the spellings this track actually observed in the
// field: an absolute path, the transport label's own rendering, and the
// subprocess phrases that appear when a raw error is passed through.
var aiIngestReasonForbiddenSubstrings = []string{
	"/tmp/", "/home/", "-S/", "-L/",
	"exit status", "no server running", "error connecting to",
}

// aiIngestReasonPoisonedError carries every forbidden shape at once, so a test
// that folds it and finds the log clean has excluded all of them together
// rather than one at a time.
type aiIngestReasonPoisonedError struct{}

func (aiIngestReasonPoisonedError) Error() string {
	return "exit status 1: no server running on -S/tmp/tmux-1000/default; " +
		"error connecting to -L/home/someone/.cache/x"
}

// TestIngestReasonVocabularyIsClosed is the type half of the closure.
//
// Every constant declared with the vocabulary type must appear in the aggregate
// list. Without this a constant can be declared, used at a record site and
// never admitted, which is how a 70-token list quietly stops describing itself.
func TestIngestReasonVocabularyIsClosed(t *testing.T) {
	declared := aiIngestReasonDeclaredConstants(t)
	if len(declared) == 0 {
		t.Fatal("no aiIngestReason constants found; the scan is looking in the wrong place")
	}
	var missing []string
	for name, value := range declared {
		if !slices.Contains(aiIngestReasons, aiIngestReason(value)) {
			missing = append(missing, name+" = "+strconv.Quote(value))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d declared reason constant(s) are not in aiIngestReasons:\n  %s\n\n"+
			"A constant outside the aggregate is a token the admission function will refuse, "+
			"so a record site using it would log the unclassified token instead.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestIngestReasonAdmissionRefusesAValueOutsideTheVocabulary is the gate
// checking itself. A rule written down is not a rule that holds.
func TestIngestReasonAdmissionRefusesAValueOutsideTheVocabulary(t *testing.T) {
	if got := aiIngestReasonFor("no matching pane"); got != aiPaneMatchReasonNoMatch {
		t.Fatalf("aiIngestReasonFor(member) = %q, want the member admitted", got)
	}
	for _, outside := range []string{
		"exit status 1",
		"no server running on /tmp/tmux-1000/default",
		"some reason nobody declared",
	} {
		if got := aiIngestReasonFor(outside); got != "" {
			t.Fatalf("aiIngestReasonFor(%q) = %q, want the empty reason for a non-member", outside, got)
		}
		if got := aiIngestRecordReason(outside); got != aiIngestReasonUnclassified {
			t.Fatalf("aiIngestRecordReason(%q) = %q, want the unclassified token", outside, got)
		}
	}
	if got := aiIngestRecordReason("   "); got != "" {
		t.Fatalf("aiIngestRecordReason(blank) = %q, want the empty reason preserved", got)
	}
}

// TestIngestReasonTokensCarryNoForbiddenText checks the vocabulary itself.
//
// The admission function can only be as clean as the set it admits from: a
// token that carried a path would be let through by every other check here.
func TestIngestReasonTokensCarryNoForbiddenText(t *testing.T) {
	for _, reason := range aiIngestReasons {
		for _, forbidden := range aiIngestReasonForbiddenSubstrings {
			if strings.Contains(string(reason), forbidden) {
				t.Fatalf("vocabulary token %q contains %q", reason, forbidden)
			}
		}
	}
}

// TestIngestReasonVocabularyCoversBorrowedVocabularies keeps the admitted half
// from drifting behind the producers it does not own.
//
// The observer and semantic halves are taken from their own sources, so they
// cannot drift; the two read here are the ones this file cannot reference. Both
// readers depend on a name prefix and a file path, so neither is complete: a
// constant added under a different name or in a different file is invisible to
// them, and the unclassified token in the log is the remaining backstop. Two
// layers covering each other's blind spots is not the same as being complete,
// and this comment stays so the next reader does not assume it is.
func TestIngestReasonVocabularyCoversBorrowedVocabularies(t *testing.T) {
	for _, reason := range codexObserverReasons {
		if !slices.Contains(aiIngestReasons, aiIngestReason(reason)) {
			t.Fatalf("observer token %q is written to the reason column but not admitted", reason)
		}
	}
	for name, value := range aiIngestReasonDeclaredPrefixed(t, "ai_ingest.go", "aiPaneMatchReason") {
		if !slices.Contains(aiIngestReasons, aiIngestReason(value)) {
			t.Fatalf("attribution constant %s = %q is not admitted", name, value)
		}
	}
	for name, value := range aiIngestReasonDeclaredPrefixed(t, "ai_pane_write.go", "aiPaneWriteReason") {
		if !slices.Contains(aiIngestReasons, aiIngestReason(value)) {
			t.Fatalf("pane write constant %s = %q is not admitted", name, value)
		}
	}
	for _, literal := range aiIngestReasonNativeRouteLiterals(t) {
		if !slices.Contains(aiIngestReasons, aiIngestReason(literal)) {
			t.Fatalf("native route reason %q is returned into the reason column but not admitted", literal)
		}
	}
	// The hook action and semantic producers build their reasons by
	// concatenation, so no literal scan could recover the result. Running them
	// over their whole input domain is what proves the list covers them.
	for _, reason := range aiIngestHookActionReasons() {
		if reason == "" {
			continue
		}
		if !slices.Contains(aiIngestReasons, reason) {
			t.Fatalf("hook action reason %q is not admitted", reason)
		}
	}
	for _, reason := range aiIngestSemanticReasons() {
		if reason == "" {
			continue
		}
		if !slices.Contains(aiIngestReasons, reason) {
			t.Fatalf("semantic policy reason %q is not admitted", reason)
		}
	}
}

// TestIngestReasonFoldingKeepsOperationsDistinguishable is the defence for the
// trap in this change.
//
// Folding every failure onto one token would satisfy every other test in this
// file while destroying the instrumentation the fold exists to protect. A
// closed vocabulary means the set is bounded, not that it is small.
func TestIngestReasonFoldingKeepsOperationsDistinguishable(t *testing.T) {
	operations := []aiIngestReason{
		aiIngestReasonHookPayloadInvalid,
		aiIngestReasonNotifyStoreFailed,
		aiIngestReasonNotifyPushFailed,
		aiIngestReasonStatusApplyFailed,
		aiIngestReasonSemanticDeliverFailed,
		aiIngestReasonAuthorityFenceFailed,
		aiIngestReasonReadinessWriteFailed,
	}
	seen := map[aiIngestReason]bool{}
	for _, operation := range operations {
		folded := aiIngestFailureReason(operation, aiIngestReasonPoisonedError{})
		if folded != operation {
			t.Fatalf("aiIngestFailureReason(%q, poisoned) = %q, want the operation preserved", operation, folded)
		}
		if seen[folded] {
			t.Fatalf("operation %q collapsed onto a token already used by another operation", operation)
		}
		seen[folded] = true
	}
	if len(seen) != len(operations) {
		t.Fatalf("folded %d operations onto %d tokens; the cause must survive the fold", len(operations), len(seen))
	}
}

// TestIngestReasonAuthorityIsNamedNotEchoed covers the value read back from a
// pane option, which is not a closed set at the moment it is read.
func TestIngestReasonAuthorityIsNamedNotEchoed(t *testing.T) {
	for authority, want := range map[string]aiIngestReason{
		codexAuthorityPending:      aiIngestReasonAuthorityPending,
		codexAuthorityControlPlane: aiIngestReasonAuthorityControlPlane,
		codexAuthorityInvalidating: aiIngestReasonAuthorityInvalidating,
		codexAuthorityHook:         aiIngestReasonAuthorityHook,
	} {
		if got := aiIngestAuthorityReason(authority); got != want {
			t.Fatalf("aiIngestAuthorityReason(%q) = %q, want %q", authority, got, want)
		}
	}
	poisoned := "/home/someone/.cache/whatever"
	if got := aiIngestAuthorityReason(poisoned); got != aiIngestReasonAuthorityUnrecognized {
		t.Fatalf("aiIngestAuthorityReason(unknown) = %q, want the unrecognized token", got)
	}
}

// TestIngestReasonDeliveryClassificationSurvivesTheFold keeps the delivery
// path's own four-way cause from being flattened onto the operation token.
func TestIngestReasonDeliveryClassificationSurvivesTheFold(t *testing.T) {
	delivery := &codexHookDeliveryError{
		Reason: codexHookRouteForeignPaneReason,
		Detail: "app-owned route: -S/tmp/tmux-1000/default",
	}
	got := aiIngestFailureReason(aiIngestReasonStatusApplyFailed, delivery)
	if got != aiIngestReason(codexHookRouteForeignPaneReason) {
		t.Fatalf("aiIngestFailureReason(delivery) = %q, want the route token preserved", got)
	}
	if strings.Contains(string(got), "/tmp/") {
		t.Fatalf("delivery Detail reached the reason column: %q", got)
	}
}

// TestIngestReasonLogFileHoldsNoForbiddenTextPerProvider is the dynamic half.
//
// It drives a real payload failure through each provider's ingest entry point
// and folds a poisoned error through every operation token, then reads the
// whole log file back. Reading the file rather than the source is deliberate:
// a source scan trips over the very examples a comment needs to name.
func TestIngestReasonLogFileHoldsNoForbiddenTextPerProvider(t *testing.T) {
	for _, provider := range []struct {
		name   string
		ingest func(*aiCommand) error
	}{
		{"codex", func(c *aiCommand) error { return c.ingestCodexHook([]byte("{"), "") }},
		{"claude", func(c *aiCommand) error { return c.ingestClaudeHook([]byte("{"), "") }},
		{"antigravity", func(c *aiCommand) error { return c.ingestAntigravityHook([]byte("{"), "", "") }},
	} {
		t.Run(provider.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.readFile = os.ReadFile

			// A malformed payload is the one failure every provider reaches
			// without a Registry or a live pane behind it.
			_ = provider.ingest(cmd)

			// Every operation token, folded from an error carrying all seven
			// forbidden shapes at once.
			for _, operation := range []aiIngestReason{
				aiIngestReasonHookPayloadInvalid,
				aiIngestReasonNotifyStoreFailed,
				aiIngestReasonNotifyPushFailed,
				aiIngestReasonStatusApplyFailed,
				aiIngestReasonSemanticDeliverFailed,
				aiIngestReasonAuthorityFenceFailed,
				aiIngestReasonReadinessWriteFailed,
			} {
				cmd.appendAIIngestLog(aiIngestLogEntry{
					Source: provider.name + "-hook",
					Result: "error",
					Reason: aiIngestFailureReason(operation, aiIngestReasonPoisonedError{}),
				})
			}
			// The provider-originated shapes that used to be spliced in.
			cmd.appendAIIngestLog(aiIngestLogEntry{
				Source: provider.name + "-hook",
				Result: "quiet",
				Reason: aiIngestReasonToolError,
			})
			cmd.appendAIIngestLog(aiIngestLogEntry{
				Source: provider.name + "-hook",
				Result: "notify",
				Reason: antigravityStopDiagnosticReason(antigravityHookPayload{
					TerminationReason: "died reading /home/someone/.cache/x",
				}),
			})

			path, err := cmd.aiIngestLogPath()
			if err != nil {
				t.Fatalf("aiIngestLogPath() error = %v", err)
			}
			payload, err := os.ReadFile(path) // #nosec G304 -- test-owned temp home.
			if err != nil {
				t.Fatalf("read ingest log: %v", err)
			}
			if len(payload) == 0 {
				t.Fatal("ingest log is empty; the injection wrote nothing to assert about")
			}
			for _, forbidden := range aiIngestReasonForbiddenSubstrings {
				if strings.Contains(string(payload), forbidden) {
					t.Fatalf("ingest log contains %q after failure injection:\n%s", forbidden, payload)
				}
			}
		})
	}
}

// TestIngestReasonSuccessRecordsAreUnchanged is the control.
//
// A fold that quietly rewrote the successful path would pass every absence
// assertion above while changing what the log means.
func TestIngestReasonSuccessRecordsAreUnchanged(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: "Stop", Result: "notify", Pane: "%1"})
	cmd.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: "SubagentStop", Result: "quiet", Reason: aiIngestReasonHighVolumeEvent, Pane: "%2"})

	path, err := cmd.aiIngestLogPath()
	if err != nil {
		t.Fatalf("aiIngestLogPath() error = %v", err)
	}
	payload, err := os.ReadFile(path) // #nosec G304 -- test-owned temp home.
	if err != nil {
		t.Fatalf("read ingest log: %v", err)
	}
	got := string(payload)
	if !strings.Contains(got, `"result":"notify"`) || strings.Contains(got, `"result":"notify","reason"`) {
		t.Fatalf("a successful record grew a reason it did not have:\n%s", got)
	}
	if !strings.Contains(got, `"reason":"high-volume event"`) {
		t.Fatalf("an existing reason changed spelling:\n%s", got)
	}
}

// TestIngestReasonGateRejectsAPathBearingVocabulary is the mutation.
//
// A compile error is not evidence that a gate runs. This injects a reason that
// carries a path into the vocabulary check and asserts the check reports it, so
// the failure is observed rather than assumed.
func TestIngestReasonGateRejectsAPathBearingVocabulary(t *testing.T) {
	mutated := append(slices.Clone(aiIngestReasons), aiIngestReason("delivery failed on /tmp/tmux-1000/default"))
	var flagged []aiIngestReason
	for _, reason := range mutated {
		for _, forbidden := range aiIngestReasonForbiddenSubstrings {
			if strings.Contains(string(reason), forbidden) {
				flagged = append(flagged, reason)
				break
			}
		}
	}
	if len(flagged) != 1 {
		t.Fatalf("the vocabulary check flagged %d token(s) in a mutated set, want exactly the injected one", len(flagged))
	}

	// The admission function must refuse it too: a token nobody declared cannot
	// enter the column just because an error happened to spell it.
	if got := aiIngestRecordReason("delivery failed on /tmp/tmux-1000/default"); got != aiIngestReasonUnclassified {
		t.Fatalf("aiIngestRecordReason(path-bearing) = %q, want the unclassified token", got)
	}
}

// aiIngestReasonDeclaredConstants reads every constant in this package declared
// with the vocabulary type.
func aiIngestReasonDeclaredConstants(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, file := range aiIngestReasonPackageFiles(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok || spec.Type == nil {
				return true
			}
			name, ok := spec.Type.(*ast.Ident)
			if !ok || name.Name != "aiIngestReason" {
				return true
			}
			for index, ident := range spec.Names {
				if index >= len(spec.Values) {
					continue
				}
				literal, ok := spec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				if value, err := strconv.Unquote(literal.Value); err == nil {
					found[ident.Name] = value
				}
			}
			return true
		})
	}
	return found
}

// aiIngestReasonDeclaredPrefixed reads string constants by name prefix from one
// file, without requiring them to carry any particular type.
func aiIngestReasonDeclaredPrefixed(t *testing.T, fileName, prefix string) map[string]string {
	t.Helper()
	found := map[string]string{}
	fileSet := token.NewFileSet()
	path := filepath.Join(aiIngestReasonPackageDir(t), fileName)
	parsed, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", fileName, err)
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, ident := range spec.Names {
			if !strings.HasPrefix(ident.Name, prefix) || index >= len(spec.Values) {
				continue
			}
			literal, ok := spec.Values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			if value, err := strconv.Unquote(literal.Value); err == nil {
				found[ident.Name] = value
			}
		}
		return true
	})
	if len(found) == 0 {
		t.Fatalf("no %s* constants found in %s; the scan is looking in the wrong place", prefix, fileName)
	}
	return found
}

// aiIngestReasonNativeRouteLiterals reads the reason literals the native hook
// routing helpers return.
func aiIngestReasonNativeRouteLiterals(t *testing.T) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	path := filepath.Join(aiIngestReasonPackageDir(t), "agent_session_ref.go")
	parsed, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse agent_session_ref.go: %v", err)
	}
	var literals []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		decl, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if decl.Name.Name != "routeNativeCodexHook" && decl.Name.Name != "nativeCodexHookAllowed" {
			return true
		}
		ast.Inspect(decl, func(inner ast.Node) bool {
			ret, ok := inner.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, result := range ret.Results {
				literal, ok := result.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(literal.Value)
				if err == nil && strings.TrimSpace(value) != "" {
					literals = append(literals, value)
				}
			}
			return true
		})
		return true
	})
	if len(literals) == 0 {
		t.Fatal("no native route reason literals found; the scan is looking in the wrong place")
	}
	return literals
}

func aiIngestReasonPackageDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	return dir
}

func aiIngestReasonPackageFiles(t *testing.T) []*ast.File {
	t.Helper()
	dir := aiIngestReasonPackageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var files []*ast.File
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, parsed)
	}
	return files
}
