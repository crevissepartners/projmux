package codexappserver

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// threadSettingsTestAnswer is a thread/resume answer reporting model, effort
// and policy the way upstream 0.159.3 does.
func threadSettingsTestAnswer(model, effort, sandbox, approval string) string {
	return `{"thread":{"id":"thread-settings"},"model":"` + model + `","reasoningEffort":"` + effort +
		`","sandbox":{"type":"` + sandbox + `"},"approvalPolicy":"` + approval + `"}`
}

var threadSettingsTestRequest = ThreadSettings{
	Model: "gpt-new", Effort: "high", Policy: ThreadPolicy{Sandbox: SandboxReadOnly, ApprovalPolicy: ApprovalNever},
}

// TestZeroThreadSettingsResumeOnceAsAZeroPolicyResumeDoes pins that a resume
// asking for no settings sends exactly the one request a zero-policy
// ResumeThread sends, and checks nothing.
func TestZeroThreadSettingsResumeOnceAsAZeroPolicyResumeDoes(t *testing.T) {
	t.Parallel()
	client, collect := scriptedEndpoint(t, map[string]string{methodThreadResume: `{"thread":{"id":"thread-settings"}}`})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	binding, err := client.ResumeThreadWithSettings(ctx, "thread-settings", "/work/project", nil, ThreadSettings{})
	if err != nil || binding.ThreadID != "thread-settings" {
		t.Fatalf("resume = %+v, %v", binding, err)
	}
	methods, params := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadResume}) {
		t.Fatalf("methods = %v", methods)
	}
	if want := `{"threadId":"thread-settings","cwd":"/work/project","excludeTurns":true}`; string(params[0]) != want {
		t.Fatalf("params = %s, want exactly %s", params[0], want)
	}
}

// TestAThreadThatTakesTheResumeSettingsIsNotUpdated pins that a thread whose
// resume answer already reports every requested field -- a thread no other
// client held, which took the policy on load -- gets no update.
func TestAThreadThatTakesTheResumeSettingsIsNotUpdated(t *testing.T) {
	t.Parallel()
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadResume: threadSettingsTestAnswer("gpt-new", "high", "readOnly", "never"),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.ResumeThreadWithSettings(ctx, "thread-settings", "/work/project", nil, threadSettingsTestRequest); err != nil {
		t.Fatal(err)
	}
	if methods, _ := collect(); !reflect.DeepEqual(methods, []string{methodThreadResume}) {
		t.Fatalf("methods = %v, want the resume alone", methods)
	}
}

// TestAThreadThatKeepsItsSettingsOnResumeIsUpdatedAndReadAgain pins the
// relaunch case: a thread another client still holds answers the resume with
// its own settings, so every requested field is sent with
// thread/settings/update on the same connection -- the sandbox in the answer
// vocabulary -- and the thread is read again with a zero-policy resume.
func TestAThreadThatKeepsItsSettingsOnResumeIsUpdatedAndReadAgain(t *testing.T) {
	t.Parallel()
	client, collect := sequencedEndpoint(t, map[string][]string{
		methodThreadResume: {
			threadSettingsTestAnswer("gpt-old", "low", "dangerFullAccess", "on-request"),
			threadSettingsTestAnswer("gpt-new", "high", "readOnly", "never"),
		},
		methodThreadSettingsUpdate: {`{}`},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	binding, err := client.ResumeThreadWithSettings(ctx, "thread-settings", "/work/project", nil, threadSettingsTestRequest)
	if err != nil || binding.ThreadID != "thread-settings" {
		t.Fatalf("resume = %+v, %v", binding, err)
	}
	methods, params := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadResume, methodThreadSettingsUpdate, methodThreadResume}) {
		t.Fatalf("methods = %v", methods)
	}
	for i, want := range []string{
		`{"threadId":"thread-settings","cwd":"/work/project","excludeTurns":true,"sandbox":"read-only","approvalPolicy":"never"}`,
		`{"threadId":"thread-settings","model":"gpt-new","effort":"high","approvalPolicy":"never","sandboxPolicy":{"type":"readOnly"}}`,
		`{"threadId":"thread-settings","cwd":"/work/project","excludeTurns":true}`,
	} {
		if string(params[i]) != want {
			t.Fatalf("%s params = %s, want exactly %s", methods[i], params[i], want)
		}
	}
}

// TestAnUpdateSendsOnlyTheRequestedFields pins that a field left empty stays
// off the wire, so the thread keeps its own value for it: a model-only resume
// updates the model alone, and empty settings send the thread id alone, which
// is the request that changes nothing.
func TestAnUpdateSendsOnlyTheRequestedFields(t *testing.T) {
	t.Parallel()
	client, collect := sequencedEndpoint(t, map[string][]string{
		methodThreadResume: {
			threadSettingsTestAnswer("gpt-old", "low", "readOnly", "never"),
			threadSettingsTestAnswer("gpt-new", "low", "readOnly", "never"),
		},
		methodThreadSettingsUpdate: {`{}`},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.ResumeThreadWithSettings(ctx, "thread-settings", "", nil, ThreadSettings{Model: "gpt-new"}); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateThreadSettings(ctx, "thread-settings", ThreadSettings{}); err != nil {
		t.Fatal(err)
	}
	methods, params := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadResume, methodThreadSettingsUpdate, methodThreadResume, methodThreadSettingsUpdate}) {
		t.Fatalf("methods = %v", methods)
	}
	if want := `{"threadId":"thread-settings","model":"gpt-new"}`; string(params[1]) != want {
		t.Fatalf("model-only update = %s, want exactly %s", params[1], want)
	}
	if want := `{"threadId":"thread-settings"}`; string(params[3]) != want {
		t.Fatalf("empty update = %s, want exactly %s", params[3], want)
	}
}

// TestAThreadThatDoesNotTakeTheUpdateIsATypedRefusal pins the failures a
// resume rolls back on: a thread that still reports another model or effort
// after the update is a *SettingsMismatchError, another policy is a
// *PolicyMismatchError naming thread/settings/update, and a refused update is
// ErrSettingsNotApplied. None of them is a safe fallback.
func TestAThreadThatDoesNotTakeTheUpdateIsATypedRefusal(t *testing.T) {
	t.Parallel()
	old := threadSettingsTestAnswer("gpt-old", "low", "dangerFullAccess", "on-request")
	for _, test := range []struct {
		name   string
		replay map[string][]string
		check  func(error) bool
	}{
		{"model kept", map[string][]string{
			methodThreadResume:         {old, threadSettingsTestAnswer("gpt-old", "high", "readOnly", "never")},
			methodThreadSettingsUpdate: {`{}`},
		}, func(err error) bool {
			var mismatch *SettingsMismatchError
			return errors.As(err, &mismatch) && mismatch.Field == "model" && mismatch.Requested == "gpt-new" &&
				errors.Is(err, ErrSettingsNotApplied) && err.Error() == ReasonSettingsMismatch+": the thread did not take model gpt-new"
		}},
		{"effort kept", map[string][]string{
			methodThreadResume:         {old, threadSettingsTestAnswer("gpt-new", "low", "readOnly", "never")},
			methodThreadSettingsUpdate: {`{}`},
		}, func(err error) bool {
			var mismatch *SettingsMismatchError
			return errors.As(err, &mismatch) && mismatch.Field == "effort" && errors.Is(err, ErrSettingsNotApplied)
		}},
		{"policy kept", map[string][]string{
			methodThreadResume:         {old},
			methodThreadSettingsUpdate: {`{}`},
		}, func(err error) bool {
			var mismatch *PolicyMismatchError
			return errors.As(err, &mismatch) && mismatch.Method == methodThreadSettingsUpdate
		}},
		{"update refused", map[string][]string{
			methodThreadResume: {old},
		}, func(err error) bool {
			return errors.Is(err, ErrSettingsNotApplied) && !errors.Is(err, ErrPolicyMismatch)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client, _ := sequencedEndpoint(t, test.replay)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := client.ResumeThreadWithSettings(ctx, "thread-settings", "/work/project", nil, threadSettingsTestRequest)
			if !test.check(err) || CanFallback(err) {
				t.Fatalf("resume = %v", err)
			}
		})
	}
}

// TestThreadSettingsRefuseAnUnknownPolicyBeforeTheWire pins that the policy of
// a settings request is held to the request vocabulary before anything is
// sent, on the resume and on a bare update alike.
func TestThreadSettingsRefuseAnUnknownPolicyBeforeTheWire(t *testing.T) {
	t.Parallel()
	client, collect := scriptedEndpoint(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	settings := ThreadSettings{Policy: ThreadPolicy{Sandbox: "full-access"}}
	_, resumeErr := client.ResumeThreadWithSettings(ctx, "thread-settings", "", nil, settings)
	updateErr := client.UpdateThreadSettings(ctx, "thread-settings", settings)
	if !errors.Is(resumeErr, ErrProtocol) || !errors.Is(updateErr, ErrProtocol) {
		t.Fatalf("resume = %v, update = %v, want protocol refusals", resumeErr, updateErr)
	}
	if methods, _ := collect(); len(methods) != 0 {
		t.Fatalf("reached the wire: %v", methods)
	}
}
