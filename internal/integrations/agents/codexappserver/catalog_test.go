package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCatalogListAndReadUseExactContentFreeContract(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })

	type observed struct {
		method string
		list   threadListParams
		read   threadReadParams
	}
	observations := make(chan observed, 2)
	go func() {
		reader := bufio.NewReader(serverConn)
		for id := 1; id <= 2; id++ {
			line, _ := reader.ReadBytes('\n')
			var request wireRequest
			_ = json.Unmarshal(line, &request)
			encoded, _ := json.Marshal(request.Params)
			got := observed{method: request.Method}
			if request.Method == methodThreadList {
				_ = json.Unmarshal(encoded, &got.list)
				_, _ = serverConn.Write([]byte(`{"id":1,"result":{"data":[{"id":"019f0000-0000-7000-8000-000000000041","cwd":"/work/app","name":"Provider title","createdAt":10,"updatedAt":20,"recencyAt":30,"gitInfo":{"branch":"feat/catalog"},"status":{"type":"active","activeFlags":["waitingOnUserInput"]},"preview":"must never leave wire","turns":[{"secret":"ignored"}]}],"nextCursor":"opaque:2"}}` + "\n"))
			} else {
				_ = json.Unmarshal(encoded, &got.read)
				_, _ = serverConn.Write([]byte(`{"id":2,"result":{"thread":{"id":"019f0000-0000-7000-8000-000000000041","cwd":"/work/app","name":"Provider title","createdAt":10,"updatedAt":20,"recencyAt":30,"gitInfo":{"branch":"feat/catalog"},"status":{"type":"idle","activeFlags":[]},"preview":"ignored","turns":[]}}}` + "\n"))
			}
			observations <- got
		}
	}()

	cursor := "opaque:1"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	page, err := client.ListCatalogThreads(ctx, CatalogQuery{Cursor: &cursor, CWD: "/work/app"})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := client.ReadCatalogThread(ctx, page.Threads[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	listCall, readCall := <-observations, <-observations
	if listCall.method != methodThreadList || listCall.list.Cursor == nil || *listCall.list.Cursor != cursor ||
		listCall.list.CWD != "/work/app" || listCall.list.Limit != DefaultCatalogPageSize || listCall.list.Archived || listCall.list.SortKey != "recency_at" ||
		listCall.list.SortDirection != "desc" || !listCall.list.UseStateDbOnly ||
		!reflect.DeepEqual(listCall.list.SourceKinds, []string{"cli", "vscode", "appServer"}) {
		t.Fatalf("thread/list params = %#v", listCall.list)
	}
	if readCall.method != methodThreadRead || readCall.read.ThreadID != page.Threads[0].ID || readCall.read.IncludeTurns {
		t.Fatalf("thread/read params = %#v", readCall.read)
	}
	if page.NextCursor == nil || *page.NextCursor != "opaque:2" || page.Threads[0].Name != "Provider title" ||
		page.Threads[0].RuntimeStatus != "active" || !page.Threads[0].RecencyAt.Equal(time.Unix(30, 0).UTC()) ||
		thread.ID != page.Threads[0].ID || thread.RuntimeStatus != "idle" {
		t.Fatalf("page=%#v read=%#v", page, thread)
	}
}

func TestCatalogRejectsMalformedRuntimeStatus(t *testing.T) {
	name := "name"
	base := wireCatalogThread{ID: "thread-a", CWD: "/work", Name: &name, CreatedAt: 1, UpdatedAt: 2, Status: wireThreadStatus{Type: "future"}}
	if _, err := normalizeCatalogThread(base); err == nil {
		t.Fatal("unknown runtime status was accepted")
	}
	base.Status = wireThreadStatus{Type: "active", ActiveFlags: []string{"future"}}
	if _, err := normalizeCatalogThread(base); err == nil {
		t.Fatal("unknown active flag was accepted")
	}
	for _, status := range []string{"notLoaded", "idle", "systemError"} {
		base.Status = wireThreadStatus{Type: status}
		if _, err := normalizeCatalogThread(base); err != nil {
			t.Fatalf("closed runtime status %q rejected: %v", status, err)
		}
	}
}

func TestLoadedThreadListIsReadOnlyIdentityEvidence(t *testing.T) {
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadLoadedList: `{"data":["thread-a","thread-b"],"nextCursor":null}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	loaded, err := client.ListLoadedThreadIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, []string{"thread-a", "thread-b"}) {
		t.Fatalf("loaded ids = %v", loaded)
	}
	methods, params := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadLoadedList}) {
		t.Fatalf("methods = %v", methods)
	}
	var loadedParams threadLoadedListParams
	if err := json.Unmarshal(params[0], &loadedParams); err != nil {
		t.Fatal(err)
	}
	if loadedParams.Cursor != nil || loadedParams.Limit != nil {
		t.Fatalf("loaded params = %+v, want one identity-only request", loadedParams)
	}
}

func TestLoadedThreadListRejectsBlankAndDuplicateIdentity(t *testing.T) {
	for _, payload := range []string{
		`{"data":["thread-a",""]}`,
		`{"data":["thread-a","thread-a"]}`,
	} {
		client, collect := scriptedEndpoint(t, map[string]string{methodThreadLoadedList: payload})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := client.ListLoadedThreadIDs(ctx)
		cancel()
		collect()
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("payload %s = %v, want protocol refusal", payload, err)
		}
	}
}

func TestCatalogReadRejectsDifferentIdentity(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
	go func() {
		reader := bufio.NewReader(serverConn)
		_, _ = reader.ReadBytes('\n')
		_, _ = serverConn.Write([]byte(`{"id":1,"result":{"thread":{"id":"thread-other","cwd":"/work","createdAt":1,"updatedAt":2,"status":{"type":"idle"}}}}` + "\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.ReadCatalogThread(ctx, "thread-requested"); err == nil {
		t.Fatal("thread/read accepted a different returned identity")
	}
}

// listEndpoint records the exact outbound thread/list frames and answers each
// of them with reply, which is either a JSON-RPC `result` or `error` object.
// It is deliberately unscripted for every other method so an unexpected
// request cannot be mistaken for a passing wire audit.
func listEndpoint(t *testing.T, reply string) (*Client, func() []json.RawMessage) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
	frames := make(chan json.RawMessage, 8)
	go func() {
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				close(frames)
				return
			}
			var request struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &request) != nil || request.Method != methodThreadList {
				continue
			}
			frames <- append(json.RawMessage(nil), request.Params...)
			_, _ = serverConn.Write([]byte(`{"id":` + string(request.ID) + `,` + reply + `}` + "\n"))
		}
	}()
	return client, func() []json.RawMessage {
		_ = client.Close()
		var observed []json.RawMessage
		for frame := range frames {
			observed = append(observed, frame)
		}
		return observed
	}
}

// TestCatalogListSendsStateDbOnlyWithoutOtherWireDrift pins the exact outbound
// params bytes. useStateDbOnly is explicit and true on every request, and the
// cursor, limit, sort, sourceKinds, archived, and cwd bytes around it are
// byte-identical to the contract that shipped before the field existed.
func TestCatalogListSendsStateDbOnlyWithoutOtherWireDrift(t *testing.T) {
	cursor := "opaque:1"
	for name, testCase := range map[string]struct {
		query CatalogQuery
		want  string
	}{
		"first page tree scoped": {
			query: CatalogQuery{},
			want:  `{"limit":100,"sortKey":"recency_at","sortDirection":"desc","sourceKinds":["cli","vscode","appServer"],"archived":false,"useStateDbOnly":true}`,
		},
		"continuation exact cwd": {
			query: CatalogQuery{Cursor: &cursor, CWD: "/work/app/"},
			want:  `{"cursor":"opaque:1","limit":100,"sortKey":"recency_at","sortDirection":"desc","sourceKinds":["cli","vscode","appServer"],"archived":false,"useStateDbOnly":true,"cwd":"/work/app"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, collect := listEndpoint(t, `"result":{"data":[],"nextCursor":null}`)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := client.ListCatalogThreads(ctx, testCase.query); err != nil {
				t.Fatal(err)
			}
			frames := collect()
			if len(frames) != 1 {
				t.Fatalf("thread/list frames = %d, want exactly one", len(frames))
			}
			if got := string(frames[0]); got != testCase.want {
				t.Fatalf("thread/list params bytes =\n%s\nwant\n%s", got, testCase.want)
			}
		})
	}
}

// TestCatalogListRejectionIsTerminalWithoutOmittedOrFalseRetry closes the
// compatibility contract: a field or protocol refusal ends the native attempt
// after exactly one request so the caller can hand off to the bounded rollout
// fallback. There is no second list with useStateDbOnly false or omitted,
// because that is the scan-and-repair read this request exists to avoid.
func TestCatalogListRejectionIsTerminalWithoutOmittedOrFalseRetry(t *testing.T) {
	cursor := "opaque:1"
	for name, testCase := range map[string]struct {
		reply      string
		query      CatalogQuery
		attributed bool
	}{
		"method unsupported": {
			reply:      `"error":{"code":-32601,"message":"unknown method"}`,
			attributed: true,
		},
		"unknown request field": {
			reply:      `"error":{"code":-32602,"message":"unknown field useStateDbOnly"}`,
			attributed: true,
		},
		"unrelated refusal": {
			reply:      `"error":{"code":-32000,"message":"internal endpoint error"}`,
			attributed: false,
		},
		"continuation cursor refused": {
			reply:      `"error":{"code":-32602,"message":"invalid cursor"}`,
			query:      CatalogQuery{Cursor: &cursor},
			attributed: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, collect := listEndpoint(t, testCase.reply)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := client.ListCatalogThreads(ctx, testCase.query)
			if err == nil {
				t.Fatal("refused thread/list returned no error")
			}
			if errors.Is(err, ErrStateDbOnlyRejected) != testCase.attributed {
				t.Fatalf("state-db-only attribution = %v, want %v (%v)", !testCase.attributed, testCase.attributed, err)
			}
			if testCase.attributed && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("attributed refusal = %v, want an unsupported classification", err)
			}
			frames := collect()
			if len(frames) != 1 {
				t.Fatalf("thread/list frames = %d, want exactly one and no retry", len(frames))
			}
			var retried threadListParams
			if err := json.Unmarshal(frames[0], &retried); err != nil {
				t.Fatal(err)
			}
			if !retried.UseStateDbOnly {
				t.Fatalf("the only thread/list request sent useStateDbOnly = %v", retried.UseStateDbOnly)
			}
		})
	}
}

// TestStateDbOnlyIsAListOnlyRequestField is the negative wire audit for the
// change-forbidden boundary. thread/read, thread/resume, thread/start, and
// turn/start keep their exact params, so a list-only compatibility patch
// cannot reach create or resume semantics.
func TestStateDbOnlyIsAListOnlyRequestField(t *testing.T) {
	for name, params := range map[string]any{
		methodThreadRead:       threadReadParams{ThreadID: "thread-a", IncludeTurns: false},
		methodThreadResume:     threadResumeParams{ThreadID: "thread-a", CWD: "/work/app", ExcludeTurns: true},
		methodThreadStart:      threadStartParams{CWD: "/work/app"},
		methodTurnStart:        turnStartParams{ThreadID: "thread-a", Input: []wireUserInput{{Type: "text", Text: "hello"}}},
		methodThreadLoadedList: threadLoadedListParams{},
	} {
		encoded, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "useStateDbOnly") {
			t.Fatalf("%s params carry the list-only field: %s", name, encoded)
		}
	}
}
