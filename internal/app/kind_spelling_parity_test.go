package app

import (
	"io"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// The kind-spelling parity matrix.
//
// The four resource verbs used to disagree about singular and plural -- `get
// panes` next to `describe pane` next to `delete pane` -- so an operator had to
// remember a form per verb. Both forms are now accepted everywhere, and the
// contract that makes that safe is stronger than "both work": an alias is
// normalized to the canonical token before dispatch, so the two spellings are
// byte-identical on stdout, on stderr, and in the error they return.
//
// These tests derive the matrix from the command manifest rather than listing
// it, so a kind added later cannot silently escape coverage: the enumeration
// test below fails when a manifest child has no case here.

// recordedRawArgv captures a forwarded invocation of a parity-alias handler.
type recordedRawArgv struct {
	calls  [][]string
	stdout string
}

func (r *recordedRawArgv) Run(args []string, stdout, _ io.Writer) error {
	r.calls = append(r.calls, append([]string{}, args...))
	_, err := io.WriteString(stdout, r.stdout)
	return err
}

// kindSpellingCase is one canonical spelling paired with the alias that must
// behave identically to it.
type kindSpellingCase struct {
	// verb is the top-level route.
	verb string
	// canonical is the manifest child name.
	canonical string
	// alias is the alternate spelling under test.
	alias string
	// tail is the argv after the kind token, identical for both spellings.
	tail []string
	// run executes one invocation against a freshly built route and returns the
	// captured streams and error. Each call gets its own store so a mutating
	// verb's two runs start from the same fixture.
	run func(t *testing.T, args []string) (string, string, error)
}

// runKindSpellingRoute is the shared invocation seam. Every verb builds its
// route the same way its own tests do, so this measures the real dispatch and
// not a reimplementation of it.
func kindSpellingCases(t *testing.T) []kindSpellingCase {
	t.Helper()

	runGetRoute := func(t *testing.T, args []string) (string, string, error) {
		t.Helper()
		cmd := newTestListGetCommand(t, newFakeResourceStore(t))
		return runRoute(t, cmd, args...)
	}
	runForwardedGetRoute := func(t *testing.T, args []string) (string, string, error) {
		t.Helper()
		forwarded := &recordedRawArgv{stdout: "forwarded\n"}
		cmd := newTestListGetCommand(t, newFakeResourceStore(t))
		cmd.notify = forwarded
		cmd.snapshots = forwarded
		stdout, stderr, err := runRoute(t, cmd, args...)
		if len(forwarded.calls) != 1 {
			t.Fatalf("%v forwarded %d times, want 1", args, len(forwarded.calls))
		}
		// The forwarded argv is part of the observable contract: an alias that
		// reached the handler with a different sub-command would produce the same
		// stdout here while doing something else entirely.
		return stdout + "argv=" + strings.Join(forwarded.calls[0], " ") + "\n", stderr, err
	}
	runDescribeRoute := func(t *testing.T, args []string) (string, string, error) {
		t.Helper()
		cmd := newTestDescribeCommand(t, newFakeResourceStore(t))
		return runRoute(t, cmd, args...)
	}
	runDeleteRoute := func(t *testing.T, args []string) (string, string, error) {
		t.Helper()
		cmd := newTestDeleteCommand(newFakeResourceStore(t), false, false, nil)
		return runRoute(t, cmd, args...)
	}
	runForwardedDeleteRoute := func(t *testing.T, args []string) (string, string, error) {
		t.Helper()
		forwarded := &recordedRawArgv{stdout: "forwarded\n"}
		cmd := newTestDeleteCommand(newFakeResourceStore(t), false, false, nil)
		cmd.notify = forwarded
		cmd.snapshots = forwarded
		stdout, stderr, err := runRoute(t, cmd, args...)
		if len(forwarded.calls) != 1 {
			t.Fatalf("%v forwarded %d times, want 1", args, len(forwarded.calls))
		}
		return stdout + "argv=" + strings.Join(forwarded.calls[0], " ") + "\n", stderr, err
	}
	runRenameRoute := func(t *testing.T, args []string) (string, string, error) {
		t.Helper()
		cmd := newTestRenameCommand(newFakeResourceStore(t))
		return runRoute(t, cmd, args...)
	}

	return []kindSpellingCase{
		{verb: "get", canonical: "projects", alias: "project", tail: []string{"-o", "name"}, run: runGetRoute},
		{verb: "get", canonical: "windows", alias: "window", tail: []string{"--project", "alpha", "-o", "ref"}, run: runGetRoute},
		{verb: "get", canonical: "agents", alias: "agent", tail: []string{"-o", "json"}, run: runGetRoute},
		{verb: "get", canonical: "notifications", alias: "notification", tail: []string{"--json"}, run: runForwardedGetRoute},
		{verb: "get", canonical: "snapshots", alias: "snapshot", tail: nil, run: runForwardedGetRoute},

		{verb: "describe", canonical: "project", alias: "projects", tail: []string{"alpha"}, run: runDescribeRoute},
		{verb: "describe", canonical: "window", alias: "windows", tail: []string{"review", "--project", "alpha"}, run: runDescribeRoute},
		{verb: "describe", canonical: "pane", alias: "panes", tail: []string{"log", "--project", "alpha"}, run: runDescribeRoute},
		{verb: "describe", canonical: "agent", alias: "agents", tail: []string{"codex", "--project", "alpha"}, run: runDescribeRoute},

		{verb: "delete", canonical: "project", alias: "projects", tail: []string{"alpha", "--dry-run"}, run: runDeleteRoute},
		{verb: "delete", canonical: "window", alias: "windows", tail: []string{"review", "--project", "alpha", "--dry-run"}, run: runDeleteRoute},
		{verb: "delete", canonical: "pane", alias: "panes", tail: []string{"log", "--project", "alpha", "--dry-run"}, run: runDeleteRoute},
		{verb: "delete", canonical: "agent", alias: "agents", tail: []string{"codex", "--project", "alpha", "--dry-run"}, run: runDeleteRoute},
		{verb: "delete", canonical: "notification", alias: "notifications", tail: []string{"7"}, run: runForwardedDeleteRoute},
		{verb: "delete", canonical: "snapshot", alias: "snapshots", tail: []string{"alpha"}, run: runForwardedDeleteRoute},

		{verb: "rename", canonical: "project", alias: "projects", tail: []string{"alpha", "--name", "renamed"}, run: runRenameRoute},
		{verb: "rename", canonical: "window", alias: "windows", tail: []string{"review", "--project", "alpha", "--name", "renamed"}, run: runRenameRoute},
		{verb: "rename", canonical: "pane", alias: "panes", tail: []string{"log", "--project", "alpha", "--name", "renamed"}, run: runRenameRoute},
		{verb: "rename", canonical: "agent", alias: "agents", tail: []string{"codex", "--project", "alpha", "--window", "main", "--name", "reviewer"}, run: runRenameRoute},
	}
}

// TestKindSpellingAliasesAreByteIdenticalToTheCanonicalSpelling is acceptance
// criteria 1, 2, and 5: every alias produces the same bytes as the spelling it
// aliases, on both streams and in its error.
func TestKindSpellingAliasesAreByteIdenticalToTheCanonicalSpelling(t *testing.T) {
	t.Parallel()

	for _, test := range kindSpellingCases(t) {
		canonicalArgs := append([]string{test.canonical}, test.tail...)
		aliasArgs := append([]string{test.alias}, test.tail...)

		wantStdout, wantStderr, wantErr := test.run(t, canonicalArgs)
		gotStdout, gotStderr, gotErr := test.run(t, aliasArgs)

		// Every case is a success case on purpose. Two spellings that both fail
		// identically would satisfy byte-equality while proving nothing about the
		// route they reach, so the canonical run has to have actually done the
		// work before the comparison below means anything.
		if wantErr != nil {
			t.Fatalf("%s %v error = %v, want the canonical spelling to succeed", test.verb, canonicalArgs, wantErr)
		}
		if wantStdout == "" {
			t.Fatalf("%s %v wrote no stdout; the case proves nothing", test.verb, canonicalArgs)
		}
		if gotStdout != wantStdout {
			t.Fatalf("%s %s stdout = %q, want the %s bytes %q",
				test.verb, test.alias, gotStdout, test.canonical, wantStdout)
		}
		if gotStderr != wantStderr {
			t.Fatalf("%s %s stderr = %q, want the %s bytes %q",
				test.verb, test.alias, gotStderr, test.canonical, wantStderr)
		}
		if errText(gotErr) != errText(wantErr) {
			t.Fatalf("%s %s error = %v, want the %s error %v",
				test.verb, test.alias, gotErr, test.canonical, wantErr)
		}
	}
}

// TestEveryManifestKindHasAParityCase is the "zero missing combinations" gate.
// A kind added to a resource verb without a case above fails here rather than
// shipping untested.
func TestEveryManifestKindHasAParityCase(t *testing.T) {
	t.Parallel()

	covered := map[string]bool{}
	for _, test := range kindSpellingCases(t) {
		covered[test.verb+" "+test.canonical] = true
	}

	// getPaneIsItsOwnRoute is the single manifest child with no alias, by the
	// product decision this track recorded: `get pane` is the exact-one Pane
	// read that owns `--current -o cwd`, not the singular of `get panes`.
	// Its behavior is pinned by TestGetPaneSpellingIsUnchanged instead.
	aliasFree := map[string]bool{"get pane": true, "get panes": true}

	for _, verb := range []string{"get", "describe", "delete", "rename"} {
		route, ok := cli.LookupRoute(verb)
		if !ok {
			t.Fatalf("%s is not a top-level route", verb)
		}
		for _, child := range route.Children {
			// A namespace child groups sub-routes rather than naming a resource
			// kind. `get runtime` addresses tmux objects, which have no singular
			// resource read to be the alias of, so the kind-parity matrix has
			// nothing to say about it; its own contract lives in the runtime
			// diagnostics tests.
			if child.Namespace {
				continue
			}
			key := verb + " " + child.Name
			switch {
			case aliasFree[key]:
				if len(child.Aliases) > 0 {
					t.Fatalf("%s now declares aliases %v but is listed as alias-free", key, child.Aliases)
				}
			case len(child.Aliases) == 0:
				t.Fatalf("%s declares no alias and is not listed as alias-free", key)
			case !covered[key]:
				t.Fatalf("%s has no parity case in kindSpellingCases", key)
			}
		}
	}
}

// TestUnknownKindRefusalsListBothForms is the error-message half of the
// contract: the refusal an operator actually reads names every spelling the
// verb accepts.
func TestUnknownKindRefusalsListBothForms(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		verb string
		run  func(t *testing.T) (string, string, error)
		want []string
	}{
		{
			verb: "get",
			run: func(t *testing.T) (string, string, error) {
				return runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), "zzz")
			},
			want: []string{"projects|project", "windows|window", "panes", "agents|agent",
				"notifications|notification", "snapshots|snapshot", "pane"},
		},
		{
			verb: "describe",
			run: func(t *testing.T) (string, string, error) {
				return runRoute(t, newTestDescribeCommand(t, newFakeResourceStore(t)), "zzz")
			},
			want: []string{"project|projects", "window|windows", "pane|panes", "agent|agents"},
		},
		{
			verb: "delete",
			run: func(t *testing.T) (string, string, error) {
				return runRoute(t, newTestDeleteCommand(newFakeResourceStore(t), false, false, nil), "zzz")
			},
			want: []string{"window|windows", "pane|panes", "agent|agents",
				"notification|notifications", "snapshot|snapshots"},
		},
		{
			verb: "rename",
			run: func(t *testing.T) (string, string, error) {
				return runRoute(t, newTestRenameCommand(newFakeResourceStore(t)), "zzz")
			},
			want: []string{"project|projects", "window|windows", "pane|panes", "agent|agents"},
		},
	} {
		stdout, _, err := test.run(t)
		if err == nil || !IsUsageError(err) {
			t.Fatalf("%s zzz error = %v, want a usage error", test.verb, err)
		}
		if stdout != "" {
			t.Fatalf("%s zzz wrote %q to stdout, want 0 bytes", test.verb, stdout)
		}
		for _, spelling := range test.want {
			if !strings.Contains(err.Error(), spelling) {
				t.Fatalf("%s zzz refusal %q omits %q", test.verb, err, spelling)
			}
		}
		// The refusal names the token the operator typed, not the normalized one.
		if !strings.Contains(err.Error(), test.verb+" zzz is not available") {
			t.Fatalf("%s zzz refusal %q does not name the rejected token", test.verb, err)
		}
	}
}

// TestKindSpellingAliasesNeverReachAnotherRoute is the negative half: an alias
// resolves to its own canonical kind and never to a neighbouring one.
func TestKindSpellingAliasesNeverReachAnotherRoute(t *testing.T) {
	t.Parallel()

	// Each singular `get` alias must answer with its own kind's inventory, so
	// comparing `-o ref` output across kinds proves the alias did not slide onto
	// a sibling list.
	refs := map[string]string{}
	for _, kind := range []string{"projects", "windows", "panes", "agents"} {
		stdout, _, err := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), kind, "-o", "ref")
		if err != nil {
			t.Fatalf("get %s error = %v", kind, err)
		}
		refs[kind] = stdout
	}
	for alias, kind := range map[string]string{"project": "projects", "window": "windows", "agent": "agents"} {
		stdout, _, err := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), alias, "-o", "ref")
		if err != nil {
			t.Fatalf("get %s error = %v", alias, err)
		}
		if stdout != refs[kind] {
			t.Fatalf("get %s = %q, want the get %s bytes %q", alias, stdout, kind, refs[kind])
		}
		for otherKind, other := range refs {
			if otherKind != kind && stdout == other {
				t.Fatalf("get %s is indistinguishable from get %s", alias, otherKind)
			}
		}
	}
}

// TestGetPaneSpellingIsUnchanged is acceptance criterion 3. `get pane` is the
// exact-one Pane read and `get panes` is the inventory; this track added no
// alias between them, so both keep exactly the behavior they shipped with.
func TestGetPaneSpellingIsUnchanged(t *testing.T) {
	t.Parallel()

	singular, _, err := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)),
		"pane", "--pane", "log", "--project", "alpha", "-o", "ref")
	if err != nil {
		t.Fatalf("get pane error = %v", err)
	}
	if singular != "pane/log\n" {
		t.Fatalf("get pane -o ref = %q, want the exact-one read %q", singular, "pane/log\n")
	}

	plural, _, err := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), "panes", "-o", "ref")
	if err != nil {
		t.Fatalf("get panes error = %v", err)
	}
	if plural == singular {
		t.Fatalf("get panes collapsed onto the exact-one read: %q", plural)
	}
	if !strings.Contains(plural, "pane/log\n") || strings.Count(plural, "\n") < 2 {
		t.Fatalf("get panes = %q, want the multi-row inventory", plural)
	}

	// The `cwd` field projection is the route-local half of `get pane`, and it
	// is the reason the singular is not an alias. It still answers only there.
	current := &stubCurrentPath{path: "/srv/alpha/worktree"}
	cmd := newTestListGetCommand(t, newFakeResourceStore(t))
	cmd.currentPath = current
	stdout, _, err := runRoute(t, cmd, "pane", "--current", "-o", "cwd")
	if err != nil || stdout != "/srv/alpha/worktree\n" {
		t.Fatalf("get pane --current -o cwd = (%q, %v)", stdout, err)
	}
	if _, _, err := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)),
		"panes", "--current", "-o", "cwd"); err == nil {
		t.Fatal("get panes accepted --current -o cwd, which belongs to the exact-one read")
	}
}

// errText renders an error for comparison without collapsing nil onto "".
func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return "error:" + err.Error()
}

// compile-time assertion that the recorder satisfies the forwarding seam.
var _ rawArgvCommand = (*recordedRawArgv)(nil)
