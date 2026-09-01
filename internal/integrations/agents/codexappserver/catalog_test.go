package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
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
		listCall.list.SortDirection != "desc" || !reflect.DeepEqual(listCall.list.SourceKinds, []string{"cli", "vscode", "appServer"}) {
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
