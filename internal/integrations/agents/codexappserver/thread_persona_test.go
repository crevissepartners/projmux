package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestThreadStartCarriesDeveloperInstructionsOnlyWhenGiven pins both halves of
// the persona wire contract on the exact bytes of the request.
//
// A thread started with a persona sends its content verbatim as
// developerInstructions -- upstream records that once as the thread's
// `developer` message and replays it on every resume, which is why nothing
// re-sends it later. A thread started without one must send the request it
// sent before the field existed: the key absent, not an empty string, so an
// older app-server that has never heard of it sees no new field at all.
func TestThreadStartCarriesDeveloperInstructionsOnlyWhenGiven(t *testing.T) {
	t.Parallel()
	const instructions = "You are the persona KIWI.\nBegin every reply with KIWI-7731.\n"

	for _, test := range []struct {
		name         string
		instructions string
		wantKey      bool
	}{
		{name: "with a persona", instructions: instructions, wantKey: true},
		{name: "without a persona", instructions: "", wantKey: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client, collect := scriptedEndpoint(t, map[string]string{
				methodThreadStart: `{"thread":{"id":"thread-persona"}}`,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			binding, err := client.StartThread(ctx, "/work/project", nil, test.instructions)
			if err != nil {
				t.Fatal(err)
			}
			if binding.ThreadID != "thread-persona" {
				t.Fatalf("binding = %#v", binding)
			}
			methods, rawParams := collect()
			if !reflect.DeepEqual(methods, []string{methodThreadStart}) {
				t.Fatalf("methods = %v, want exactly one thread/start", methods)
			}

			// The raw frame, not the decoded struct: an omitted key and an
			// empty one decode the same and only the bytes tell them apart.
			if got := bytes.Contains(rawParams[0], []byte(`"developerInstructions"`)); got != test.wantKey {
				t.Fatalf("thread/start params = %s, developerInstructions present = %v, want %v",
					rawParams[0], got, test.wantKey)
			}
			var params threadStartParams
			if err := json.Unmarshal(rawParams[0], &params); err != nil {
				t.Fatal(err)
			}
			if params.DeveloperInstructions != test.instructions {
				t.Fatalf("developerInstructions = %q, want %q", params.DeveloperInstructions, test.instructions)
			}
			if params.CWD != "/work/project" {
				t.Fatalf("cwd = %q, want the workspace unchanged", params.CWD)
			}
		})
	}
}

// TestThreadResumeNeverCarriesDeveloperInstructions pins the other half of
// decision A1 on the wire: upstream neither records nor applies a persona
// given to thread/resume, so the resume request has no such field to fill in
// and none may appear on it by accident.
func TestThreadResumeNeverCarriesDeveloperInstructions(t *testing.T) {
	t.Parallel()
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadResume: `{"thread":{"id":"thread-persona"}}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.ResumeThread(ctx, "thread-persona", "/work/project", nil); err != nil {
		t.Fatal(err)
	}
	methods, rawParams := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadResume}) {
		t.Fatalf("methods = %v, want exactly one thread/resume", methods)
	}
	if bytes.Contains(rawParams[0], []byte(`"developerInstructions"`)) {
		t.Fatalf("thread/resume params = %s, want no developerInstructions", rawParams[0])
	}
	if _, ok := reflect.TypeFor[threadResumeParams]().FieldByName("DeveloperInstructions"); ok {
		t.Fatal("threadResumeParams gained a DeveloperInstructions field; a resume cannot carry a persona")
	}
}
