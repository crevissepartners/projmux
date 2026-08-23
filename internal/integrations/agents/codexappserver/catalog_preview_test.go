package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCatalogPreviewReadsOnlyExactThreadTurnsAndProjectsLatestPair(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
	const id = "019f0000-0000-7000-8000-000000000041"
	params := make(chan threadReadParams, 1)
	go func() {
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		var request wireRequest
		_ = json.Unmarshal(line, &request)
		encoded, _ := json.Marshal(request.Params)
		var got threadReadParams
		_ = json.Unmarshal(encoded, &got)
		params <- got
		_, _ = serverConn.Write([]byte(`{"id":1,"result":{"thread":{"id":"` + id + `","updatedAt":42,"turns":[{"items":[{"type":"userMessage","content":[{"type":"text","text":"older"}]},{"type":"agentMessage","text":"old reply"}]},{"items":[{"type":"userMessage","content":[{"type":"text","text":"최신 질문"}]},{"type":"agentMessage","text":"latest answer"},{"type":"commandExecution","text":"secret command"}]}]}}}` + "\n"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	preview, err := client.ReadCatalogPreview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-params; got.ThreadID != id || !got.IncludeTurns {
		t.Fatalf("params = %#v", got)
	}
	if preview.ThreadID != id || preview.User != "최신 질문" || preview.Assistant != "latest answer" || !preview.UpdatedAt.Equal(time.Unix(42, 0)) {
		t.Fatalf("preview = %#v", preview)
	}
	if !utf8.ValidString(boundedPreviewText(string(make([]byte, 3999))+"한글", 4000)) {
		t.Fatal("bounded preview split UTF-8")
	}
}
