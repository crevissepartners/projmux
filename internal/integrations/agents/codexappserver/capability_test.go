package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
)

func TestReviewCapabilityUsesNegotiatedVersionOnly(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		version   string
		available bool
	}{
		{version: "codex-cli/0.148.0", available: false},
		{version: "codex-cli/0.149.0", available: true},
		{version: "0.150.0-beta.1", available: true},
		{version: "unknown", available: false},
	} {
		if got := reviewCapabilityForVersion(test.version); got.Available != test.available {
			t.Fatalf("reviewCapabilityForVersion(%q) = %#v, want available=%v", test.version, got, test.available)
		}
	}
}

func TestInstalledCodexModelCapabilitySmoke(t *testing.T) {
	if os.Getenv("PROJMUX_CODEX_CAPABILITY_INSTALLED_SMOKE") != "1" {
		t.Skip("set PROJMUX_CODEX_CAPABILITY_INSTALLED_SMOKE=1 to probe the installed Codex app-server")
	}
	discoveryCtx, cancelDiscovery := context.WithTimeout(context.Background(), 30*time.Second)
	session, err := OpenDefaultCapabilitySession(discoveryCtx, "capability-installed-smoke")
	cancelDiscovery()
	if err != nil {
		t.Fatalf("open installed Codex capability session: %v", err)
	}
	defer session.Close()
	refreshCtx, cancelRefresh := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRefresh()
	snapshot, err := session.Refresh(refreshCtx)
	if err != nil {
		t.Fatalf("refresh installed Codex capabilities after discovery context closed: %v", err)
	}
	if !snapshot.Epoch.Valid() || len(snapshot.Models) == 0 {
		t.Fatalf("installed capability snapshot = %#v", snapshot)
	}
	for _, model := range snapshot.Models {
		t.Logf("model=%q launch=%q default=%v defaultEffort=%q efforts=%v modalities=%v personality=%v", model.ID, model.LaunchName, model.Default, model.DefaultEffort, model.Efforts, model.InputModalities, model.SupportsPersonality)
	}
	t.Logf("reviewAvailable=%v reason=%q negotiatedVersion=%q", snapshot.Review.Available, snapshot.Review.Reason, snapshot.Epoch.Version)
}

func TestModelCapabilityNormalizationKeepsVisibleSupportedSubset(t *testing.T) {
	t.Parallel()
	snapshot := normalizeCapabilitySnapshot(corecap.Epoch{Connection: "connection-1", Version: "codex-cli/0.149.0"}, []wireModel{
		{ID: "hidden", Model: "hidden", Hidden: true, SupportedReasoningEfforts: []wireReasoningEffortOption{{Effort: "high"}}},
		{ID: "visible", Model: "gpt-visible", DisplayName: "GPT Visible", Default: true, DefaultReasoningEffort: "medium",
			SupportedReasoningEfforts: []wireReasoningEffortOption{{Effort: "low"}, {Effort: "medium"}, {Effort: "medium"}, {Effort: ""}},
			InputModalities:           []string{"text", "image", "future", "text"}, SupportsPersonality: true},
		{ID: "duplicate", Model: "first", Default: true, SupportedReasoningEfforts: []wireReasoningEffortOption{{Effort: "low"}}},
		{ID: "duplicate", Model: "second", SupportedReasoningEfforts: []wireReasoningEffortOption{{Effort: "high"}}},
		{ID: "", Model: "invalid", SupportedReasoningEfforts: []wireReasoningEffortOption{{Effort: "high"}}},
	})
	if len(snapshot.Models) != 2 {
		t.Fatalf("models = %#v, want two visible unique valid models", snapshot.Models)
	}
	model := snapshot.Models[0]
	if model.ID != "visible" || model.LaunchName != "gpt-visible" || !model.Default || model.DefaultEffort != "medium" {
		t.Fatalf("visible model = %#v", model)
	}
	if !reflect.DeepEqual(model.Efforts, []string{"low", "medium"}) || !reflect.DeepEqual(model.InputModalities, []string{"text", "image"}) || !model.SupportsPersonality {
		t.Fatalf("normalized capabilities = %#v", model)
	}
	if snapshot.Models[1].Default {
		t.Fatal("a second upstream default survived normalization")
	}
	if !snapshot.Review.Available {
		t.Fatalf("review capability = %#v", snapshot.Review)
	}
}

func TestModelListToSnapshotAndReviewStartWireLifecycle(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	defer client.Close()
	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		var request wireRequest
		if err := json.NewDecoder(reader).Decode(&request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != methodModelList {
			serverDone <- &unexpectedMethodError{got: request.Method, want: methodModelList}
			return
		}
		_, _ = serverConn.Write([]byte(`{"id":1,"result":{"data":[{"id":"m1","model":"gpt-m1","displayName":"M1","description":"","hidden":false,"isDefault":true,"defaultReasoningEffort":"high","supportedReasoningEfforts":[{"reasoningEffort":"high","description":""}],"inputModalities":["text"],"supportsPersonality":false}],"nextCursor":null}}` + "\n"))
		if err := json.NewDecoder(reader).Decode(&request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != methodReviewStart {
			serverDone <- &unexpectedMethodError{got: request.Method, want: methodReviewStart}
			return
		}
		var params reviewStartParams
		data, _ := json.Marshal(request.Params)
		_ = json.Unmarshal(data, &params)
		if params.ThreadID != "thread-exact" {
			serverDone <- &unexpectedMethodError{got: params.ThreadID, want: "thread-exact"}
			return
		}
		_, _ = serverConn.Write([]byte(`{"id":2,"result":{"reviewThreadId":"thread-exact","turn":{"id":"turn-review","status":"inProgress","items":[]}}}` + "\n"))
		serverDone <- nil
	}()

	snapshot, err := client.discoverCapabilities(context.Background(), corecap.Epoch{Connection: "connection-1", Version: "0.149.0"})
	if err != nil || len(snapshot.Models) != 1 || snapshot.Models[0].LaunchName != "gpt-m1" {
		t.Fatalf("discoverCapabilities = %#v, %v", snapshot, err)
	}
	target, err := reviewTargetParams(corecap.ReviewTarget{Kind: corecap.ReviewUncommitted})
	if err != nil {
		t.Fatal(err)
	}
	var response reviewStartResult
	if err := client.Request(context.Background(), methodReviewStart, reviewStartParams{ThreadID: "thread-exact", Target: target}, &response); err != nil {
		t.Fatal(err)
	}
	got := corecap.ReviewResult{ThreadID: response.ReviewThreadID, TurnID: response.Turn.ID, Status: normalizeReviewStatus(response.Turn.Status)}
	want := corecap.ReviewResult{ThreadID: "thread-exact", TurnID: "turn-review", Status: corecap.ReviewInProgress}
	if got != want {
		t.Fatalf("review result = %#v, want %#v", got, want)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestLiveCapabilitySessionRefreshRejectsModelSetReplacement(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		for _, response := range []string{
			`{"id":1,"result":{"data":[{"id":"m1","model":"gpt-m1","displayName":"M1","hidden":false,"isDefault":true,"defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"}],"inputModalities":["text"],"supportsPersonality":false}],"nextCursor":null}}` + "\n",
			`{"id":2,"result":{"data":[{"id":"m1","model":"gpt-m1","displayName":"M1","hidden":false,"isDefault":true,"defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low"}],"inputModalities":["text"],"supportsPersonality":false}],"nextCursor":null}}` + "\n",
		} {
			var request wireRequest
			if err := json.NewDecoder(reader).Decode(&request); err != nil {
				serverDone <- err
				return
			}
			if request.Method != methodModelList {
				serverDone <- &unexpectedMethodError{got: request.Method, want: methodModelList}
				return
			}
			if _, err := serverConn.Write([]byte(response)); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	epoch := corecap.Epoch{Connection: "connection-live", Version: "0.149.0"}
	session := &CapabilitySession{client: client, epoch: epoch}
	defer session.Close()
	initial, err := session.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cache := &corecap.Cache{}
	cache.Replace(initial)
	selection := corecap.Selection{Epoch: epoch, ModelID: "m1", LaunchName: "gpt-m1", Effort: "medium"}
	if _, err := cache.Validate(selection); err != nil {
		t.Fatalf("initial selection rejected: %v", err)
	}
	refreshed, err := session.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cache.Replace(refreshed)
	if _, err := cache.Validate(selection); !errors.Is(err, corecap.ErrStaleSelection) {
		t.Fatalf("selection after live refresh error = %v, want ErrStaleSelection", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

type unexpectedMethodError struct{ got, want string }

func (e *unexpectedMethodError) Error() string { return "got " + e.got + ", want " + e.want }
