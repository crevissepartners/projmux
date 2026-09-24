package codexappserver

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestAZeroThreadPolicyKeepsThreadStartAndResumeRequestsByteIdentical pins
// that a thread started or resumed without a policy sends exactly the request
// it sent before the policy fields existed: the raw frame, compared byte for
// byte, carries neither key.
func TestAZeroThreadPolicyKeepsThreadStartAndResumeRequestsByteIdentical(t *testing.T) {
	t.Parallel()
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadStart:  `{"thread":{"id":"thread-plain"}}`,
		methodThreadResume: `{"thread":{"id":"thread-plain"}}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.StartThread(ctx, "/work/project", nil, "", ThreadPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResumeThread(ctx, "thread-plain", "/work/project", nil, ThreadPolicy{}); err != nil {
		t.Fatal(err)
	}
	methods, params := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadStart, methodThreadResume}) {
		t.Fatalf("methods = %v", methods)
	}
	for i, want := range []string{
		`{"cwd":"/work/project"}`,
		`{"threadId":"thread-plain","cwd":"/work/project","excludeTurns":true}`,
	} {
		if string(params[i]) != want {
			t.Fatalf("%s params = %s, want exactly %s", methods[i], params[i], want)
		}
	}
}

// TestThreadStartAndResumeSendTheRequestedPolicyInTheWireVocabulary pins the
// request half: each requested field is sent under its upstream key and
// spelling, on thread/start and on thread/resume alike, and an answer that
// reports the same policy passes.
func TestThreadStartAndResumeSendTheRequestedPolicyInTheWireVocabulary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		policy   ThreadPolicy
		answer   string
		wantKeys string
	}{
		{"read-only never", ThreadPolicy{Sandbox: SandboxReadOnly, ApprovalPolicy: ApprovalNever},
			`"sandbox":{"type":"readOnly","access":{"type":"fullAccess"}},"approvalPolicy":"never"`,
			`"sandbox":"read-only","approvalPolicy":"never"`},
		{"danger-full-access on-request", ThreadPolicy{Sandbox: SandboxDangerFullAccess, ApprovalPolicy: ApprovalOnRequest},
			`"sandbox":{"type":"dangerFullAccess"},"approvalPolicy":"on-request"`,
			`"sandbox":"danger-full-access","approvalPolicy":"on-request"`},
		{"workspace-write only", ThreadPolicy{Sandbox: SandboxWorkspaceWrite},
			`"sandbox":{"type":"workspaceWrite","writableRoots":[]},"approvalPolicy":{"granular":{}}`,
			`"sandbox":"workspace-write"`},
		{"untrusted only", ThreadPolicy{ApprovalPolicy: ApprovalUntrusted},
			`"sandbox":{"type":"externalSandbox"},"approvalPolicy":"untrusted"`,
			`"approvalPolicy":"untrusted"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			answer := `{"thread":{"id":"thread-policy"},` + test.answer + `}`
			client, collect := scriptedEndpoint(t, map[string]string{methodThreadStart: answer, methodThreadResume: answer})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := client.StartThread(ctx, "/work/project", nil, "", test.policy); err != nil {
				t.Fatalf("start: %v", err)
			}
			if _, err := client.ResumeThread(ctx, "thread-policy", "/work/project", nil, test.policy); err != nil {
				t.Fatalf("resume: %v", err)
			}
			_, params := collect()
			for i, want := range []string{
				`{"cwd":"/work/project",` + test.wantKeys + `}`,
				`{"threadId":"thread-policy","cwd":"/work/project","excludeTurns":true,` + test.wantKeys + `}`,
			} {
				if string(params[i]) != want {
					t.Fatalf("params[%d] = %s, want exactly %s", i, params[i], want)
				}
			}
		})
	}
}

// TestThreadPolicyRefusesAnUnknownValueBeforeTheWire pins that a value outside
// the request vocabulary -- the profile spelling `full-access` included -- is
// refused as a protocol error with no request sent.
func TestThreadPolicyRefusesAnUnknownValueBeforeTheWire(t *testing.T) {
	t.Parallel()
	for _, policy := range []ThreadPolicy{
		{Sandbox: "full-access"},
		{Sandbox: "readOnly"},
		{ApprovalPolicy: "on-failure"},
		{ApprovalPolicy: "granular"},
	} {
		client, collect := scriptedEndpoint(t, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, startErr := client.StartThread(ctx, "/work/project", nil, "", policy)
		_, resumeErr := client.ResumeThread(ctx, "thread-policy", "/work/project", nil, policy)
		cancel()
		if !errors.Is(startErr, ErrProtocol) || !errors.Is(resumeErr, ErrProtocol) {
			t.Fatalf("policy %+v: start=%v resume=%v, want protocol refusals", policy, startErr, resumeErr)
		}
		if methods, _ := collect(); len(methods) != 0 {
			t.Fatalf("policy %+v reached the wire: %v", policy, methods)
		}
	}
}

// TestAThreadWhoseEffectivePolicyDiffersIsATypedMismatchThatNeverFallsBack
// pins the answer half: a requested field the answer reports differently --
// another sandbox type, another approval, a granular approval object, or no
// policy at all -- is a *PolicyMismatchError carrying the stable reason token
// and naming both sides, and it is never a safe fallback.
func TestAThreadWhoseEffectivePolicyDiffersIsATypedMismatchThatNeverFallsBack(t *testing.T) {
	t.Parallel()
	requested := ThreadPolicy{Sandbox: SandboxReadOnly, ApprovalPolicy: ApprovalNever}
	for _, test := range []struct {
		name      string
		answer    string
		effective ThreadPolicy
	}{
		{"different sandbox type", `,"sandbox":{"type":"workspaceWrite"},"approvalPolicy":"never"`,
			ThreadPolicy{Sandbox: "workspaceWrite", ApprovalPolicy: ApprovalNever}},
		{"external sandbox", `,"sandbox":{"type":"externalSandbox"},"approvalPolicy":"never"`,
			ThreadPolicy{Sandbox: "externalSandbox", ApprovalPolicy: ApprovalNever}},
		{"different approval", `,"sandbox":{"type":"readOnly"},"approvalPolicy":"on-request"`,
			ThreadPolicy{Sandbox: "readOnly", ApprovalPolicy: ApprovalOnRequest}},
		{"granular approval", `,"sandbox":{"type":"readOnly"},"approvalPolicy":{"granular":{"sandbox_approval":true}}`,
			ThreadPolicy{Sandbox: "readOnly", ApprovalPolicy: "granular"}},
		{"missing policy", ``, ThreadPolicy{Sandbox: "missing", ApprovalPolicy: "missing"}},
		{"null policy", `,"sandbox":null,"approvalPolicy":null`, ThreadPolicy{Sandbox: "missing", ApprovalPolicy: "missing"}},
		{"unknown sandbox shape", `,"sandbox":"read-only","approvalPolicy":"never"`,
			ThreadPolicy{Sandbox: "unrecognized", ApprovalPolicy: ApprovalNever}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			answer := `{"thread":{"id":"thread-policy"}` + test.answer + `}`
			client, _ := scriptedEndpoint(t, map[string]string{methodThreadStart: answer, methodThreadResume: answer})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, startErr := client.StartThread(ctx, "/work/project", nil, "", requested)
			_, resumeErr := client.ResumeThread(ctx, "thread-policy", "/work/project", nil, requested)
			for method, err := range map[string]error{methodThreadStart: startErr, methodThreadResume: resumeErr} {
				var mismatch *PolicyMismatchError
				if !errors.As(err, &mismatch) || !errors.Is(err, ErrPolicyMismatch) || CanFallback(err) {
					t.Fatalf("%s = %v, want a non-fallback *PolicyMismatchError", method, err)
				}
				if mismatch.Method != method || mismatch.Requested != requested || mismatch.Effective != test.effective {
					t.Fatalf("%s mismatch = %+v, want effective %+v", method, *mismatch, test.effective)
				}
				message := err.Error()
				if !strings.HasPrefix(message, ReasonPolicyMismatch+": ") || !strings.Contains(message, "sandbox read-only and approval never were requested") {
					t.Fatalf("%s message = %q", method, message)
				}
			}
		})
	}
}

// TestAThreadPolicyChecksOnlyTheRequestedFields pins that a field that was not
// requested is upstream's to decide: an answer that reports any policy, or
// none, passes for a zero policy, and a sandbox-only request ignores the
// approval the answer reports.
func TestAThreadPolicyChecksOnlyTheRequestedFields(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		policy ThreadPolicy
		answer string
	}{
		{ThreadPolicy{}, ``},
		{ThreadPolicy{}, `,"sandbox":{"type":"dangerFullAccess"},"approvalPolicy":{"granular":{}}`},
		{ThreadPolicy{Sandbox: SandboxReadOnly}, `,"sandbox":{"type":"readOnly"}`},
		{ThreadPolicy{ApprovalPolicy: ApprovalNever}, `,"sandbox":{"type":"externalSandbox"},"approvalPolicy":"never"`},
	} {
		answer := `{"thread":{"id":"thread-policy"}` + test.answer + `}`
		client, _ := scriptedEndpoint(t, map[string]string{methodThreadStart: answer, methodThreadResume: answer})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, startErr := client.StartThread(ctx, "/work/project", nil, "", test.policy)
		_, resumeErr := client.ResumeThread(ctx, "thread-policy", "/work/project", nil, test.policy)
		cancel()
		if startErr != nil || resumeErr != nil {
			t.Fatalf("policy %+v answer %s: start=%v resume=%v", test.policy, test.answer, startErr, resumeErr)
		}
	}
}

// TestBootstrapThreadResumesWithNoPolicy pins the durable-resume barrier's
// and the bootstrap's subscription leg on the wire: it is the request it was
// before a policy existed.
func TestBootstrapThreadResumesWithNoPolicy(t *testing.T) {
	t.Parallel()
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadResume: `{"thread":{"id":"thread-preturn"}}`,
		methodThreadRead:   `{"thread":{"id":"thread-preturn","cwd":"/work/project","createdAt":1,"updatedAt":2,"status":{"type":"idle"}}}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.BootstrapThread(ctx, "thread-preturn", "/work/project", nil); err != nil {
		t.Fatal(err)
	}
	_, params := collect()
	if want := `{"threadId":"thread-preturn","cwd":"/work/project","excludeTurns":true}`; string(params[0]) != want {
		t.Fatalf("bootstrap thread/resume params = %s, want exactly %s", params[0], want)
	}
}

// TestEveryInPackageThreadCallerSendsAZeroPolicy closes the caller set in
// this package: every non-test call of StartThread or ResumeThread here --
// the default create and resume, and the bootstrap the durable-resume barrier
// runs -- passes the literal ThreadPolicy{}. Only internal/app, which owns
// the profile, passes a policy.
func TestEveryInPackageThreadCallerSendsAZeroPolicy(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	calls := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "StartThread" && sel.Sel.Name != "ResumeThread") {
				return true
			}
			calls++
			last, ok := call.Args[len(call.Args)-1].(*ast.CompositeLit)
			var ident *ast.Ident
			if ok {
				ident, ok = last.Type.(*ast.Ident)
			}
			if !ok || ident.Name != "ThreadPolicy" || len(last.Elts) != 0 {
				t.Errorf("%s: %s passes %T, want the literal ThreadPolicy{}", fset.Position(call.Pos()), sel.Sel.Name, call.Args[len(call.Args)-1])
			}
			return true
		})
	}
	if calls != 3 {
		t.Fatalf("found %d in-package StartThread/ResumeThread calls, want 3 (default create, default resume, bootstrap)", calls)
	}
}
