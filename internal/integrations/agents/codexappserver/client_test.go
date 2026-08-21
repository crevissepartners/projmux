package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestClientInitializeDeliversTypedResponseAndNotification(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close() })

	serverDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			serverDone <- err
			return
		}
		var request wireRequest
		if err := json.Unmarshal(line, &request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != methodInitialize || request.ID != 1 {
			serverDone <- errors.New("unexpected initialize request")
			return
		}
		_, _ = serverConn.Write([]byte(`{"method":"thread/status/changed","params":{"threadId":"thr-secret","status":{"type":"idle"}}}` + "\n"))
		_, _ = serverConn.Write([]byte(`{"id":1,"result":{"userAgent":"codex-cli/0.149.0","platformFamily":"unix","platformOs":"linux","future":true}}` + "\n"))
		line, err = reader.ReadBytes('\n')
		if err != nil {
			serverDone <- err
			return
		}
		var initialized wireNotification
		if err := json.Unmarshal(line, &initialized); err != nil || initialized.Method != methodInitialized {
			serverDone <- errors.New("missing initialized notification")
			return
		}
		serverDone <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	version, err := client.Initialize(ctx, "0.13.0")
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if version != "codex-cli/0.149.0" {
		t.Fatalf("Initialize() version = %q", version)
	}
	select {
	case event := <-client.events:
		if event.Method != "thread/status/changed" {
			t.Fatalf("notification method = %q", event.Method)
		}
	case <-ctx.Done():
		t.Fatal("notification was not delivered")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientRoutesOutOfOrderResponsesByID(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close() })
	go func() {
		reader := bufio.NewReader(serverConn)
		_, _ = reader.ReadBytes('\n')
		_, _ = reader.ReadBytes('\n')
		_, _ = serverConn.Write([]byte("{\"id\":2,\"result\":{\"value\":\"second\"}}\n"))
		_, _ = serverConn.Write([]byte("{\"id\":1,\"result\":{\"value\":\"first\"}}\n"))
	}()
	type result struct {
		Value string `json:"value"`
	}
	var first, second result
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() { defer wg.Done(); errs <- client.Request(ctx, "one", struct{}{}, &first) }()
	time.Sleep(time.Millisecond)
	go func() { defer wg.Done(); errs <- client.Request(ctx, "two", struct{}{}, &second) }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if first.Value != "first" || second.Value != "second" {
		t.Fatalf("responses = %q, %q", first.Value, second.Value)
	}
}

func TestClientCancellationIsBoundedAndLateResponseDoesNotPoisonReconnect(t *testing.T) {
	before := runtime.NumGoroutine()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)

	requestRead := make(chan struct{})
	allowLateResponse := make(chan struct{})
	go func() {
		reader := bufio.NewReader(serverConn)
		_, _ = reader.ReadBytes('\n')
		close(requestRead)
		<-allowLateResponse
		_, _ = serverConn.Write([]byte("{\"id\":1,\"result\":{}}\n"))
		_, _ = reader.ReadBytes('\n')
		_, _ = serverConn.Write([]byte("{\"id\":2,\"result\":{\"ok\":true}}\n"))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Request(ctx, "slow", struct{}{}, nil) }()
	<-requestRead
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request error = %v", err)
	}
	close(allowLateResponse)
	var got struct {
		OK bool `json:"ok"`
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), time.Second)
	defer probeCancel()
	if err := client.Request(probeCtx, "after-cancel", struct{}{}, &got); err != nil || !got.OK {
		t.Fatalf("request after cancellation = %+v, %v", got, err)
	}
	_ = client.Close()
	_ = serverConn.Close()
	time.Sleep(20 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

func TestClientDisconnectThenReplacementReconnectsWithoutLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	firstClientConn, firstServerConn := net.Pipe()
	first := NewClient(firstClientConn)
	go func() {
		reader := bufio.NewReader(firstServerConn)
		_, _ = reader.ReadBytes('\n')
		_ = firstServerConn.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.Request(ctx, "before-disconnect", struct{}{}, nil); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("request before replacement error = %v, want disconnected", err)
	}
	_ = first.Close()

	secondClientConn, secondServerConn := net.Pipe()
	second := NewClient(secondClientConn)
	go func() {
		reader := bufio.NewReader(secondServerConn)
		line, _ := reader.ReadBytes('\n')
		var request wireRequest
		_ = json.Unmarshal(line, &request)
		_, _ = secondServerConn.Write(fmt.Appendf(nil, "{\"id\":%d,\"result\":{\"ok\":true}}\n", request.ID))
	}()
	var got struct {
		OK bool `json:"ok"`
	}
	if err := second.Request(ctx, "after-reconnect", struct{}{}, &got); err != nil || !got.OK {
		t.Fatalf("request on replacement connection = %+v, %v", got, err)
	}
	_ = second.Close()
	_ = secondServerConn.Close()
	time.Sleep(20 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutine leak after replacement: before=%d after=%d", before, after)
	}
}

func TestClientFaultMatrix(t *testing.T) {
	tests := []struct {
		name string
		line string
		want error
	}{
		{name: "unsupported", line: `{"id":1,"error":{"code":-32601,"message":"missing"}}`, want: ErrUnsupported},
		{name: "malformed", line: `{not-json`, want: ErrProtocol},
		{name: "invalid shape", line: `{"id":1,"result":{},"error":{"code":1,"message":"both"}}`, want: ErrProtocol},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := NewClient(clientConn)
			defer client.Close()
			go func() {
				reader := bufio.NewReader(serverConn)
				_, _ = reader.ReadBytes('\n')
				_, _ = serverConn.Write([]byte(tc.line + "\n"))
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.Request(ctx, "probe", struct{}{}, nil); !errors.Is(err, tc.want) {
				t.Fatalf("Request() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestClientTimeoutIsBounded(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	defer client.Close()
	defer serverConn.Close()
	go func() {
		reader := bufio.NewReader(serverConn)
		_, _ = reader.ReadBytes('\n')
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.Request(ctx, "slow", struct{}{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestClientContextBoundsBlockedOutboundWriteWithoutLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	// Deliberately never read serverConn: net.Pipe has no write buffer, so the
	// request remains blocked until the client aborts its owned transport.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.Request(ctx, "blocked-write", struct{}{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("blocked write exceeded context bound: %s", elapsed)
	}
	_ = serverConn.Close()
	deadline := time.Now().Add(250 * time.Millisecond)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutine leak after blocked write: before=%d after=%d", before, after)
	}
}

func TestInitializeContextBoundsBlockedInitializedNotificationWithoutLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	responseSent := make(chan struct{})
	go func() {
		reader := bufio.NewReader(serverConn)
		line, _ := reader.ReadBytes('\n')
		var request wireRequest
		_ = json.Unmarshal(line, &request)
		_, _ = serverConn.Write(fmt.Appendf(nil, "{\"id\":%d,\"result\":{\"userAgent\":\"codex-cli/0.149.0\"}}\n", request.ID))
		close(responseSent)
		// Do not read the required initialized notification.
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Initialize(ctx, "0.13.0")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Initialize() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("blocked initialized notification exceeded context bound: %s", elapsed)
	}
	<-responseSent
	_ = serverConn.Close()
	deadline := time.Now().Add(250 * time.Millisecond)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutine leak after initialized notification: before=%d after=%d", before, after)
	}
}

func TestDecisionTableAndSecretFreeHealth(t *testing.T) {
	tests := []struct {
		availability Availability
		reason       Reason
		hook         bool
		wantSource   Source
	}{
		{AvailabilityAvailable, ReasonNone, true, SourceAppServer},
		{AvailabilityUnsupported, ReasonUnsupported, true, SourceHookFallback},
		{AvailabilityUnavailable, ReasonEndpointUnavailable, true, SourceHookFallback},
		{AvailabilityTimeout, ReasonTimeout, true, SourceHookFallback},
		{AvailabilityProtocolError, ReasonProtocolError, true, SourceHookFallback},
		{AvailabilityUnavailable, ReasonEndpointUnavailable, false, SourceUnavailable},
	}
	for _, tc := range tests {
		health := Decide(tc.availability, tc.reason, "0.149.0 token=/secret path=/home/me prompt=hello", EndpointStdioProxy, ConnectionDisconnected, tc.hook)
		if health.Source != tc.wantSource {
			t.Fatalf("Decide(%s, hook=%v) source = %s", tc.availability, tc.hook, health.Source)
		}
		if health.Version != "0.149.0" {
			t.Fatalf("safe version extraction = %q", health.Version)
		}
	}
}

func TestDiagnosticVersionRejectsPathsAndTokenLikeStrings(t *testing.T) {
	for _, unsafe := range []string{
		"/home/me/secret",
		"token-secret",
		"token/0.149.0/secret",
		"codex-cli/0.149.0/path",
		"0.149",
	} {
		if IsSafeDiagnosticVersion(unsafe) {
			t.Fatalf("IsSafeDiagnosticVersion(%q) = true", unsafe)
		}
		if got := safeVersion(unsafe); got != "" && !IsSafeDiagnosticVersion(got) {
			t.Fatalf("safeVersion(%q) emitted unsafe %q", unsafe, got)
		}
	}
	for _, safe := range []string{"0.149.0", "codex-cli/0.149.0", "codex_cli_rs/0.149.0-beta.1"} {
		if !IsSafeDiagnosticVersion(safe) || safeVersion(safe) != safe {
			t.Fatalf("safe diagnostic version rejected: %q", safe)
		}
	}
}

func TestNotificationRouteAndCloseShareChannelLifetimeGuard(t *testing.T) {
	frame := []byte(`{"method":"thread/status/changed","params":{"status":{"type":"idle"}}}`)
	for range 1000 {
		client := &Client{
			pending: make(map[int64]chan response),
			events:  make(chan notification, 1),
			done:    make(chan struct{}),
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = client.routeFrame(frame) }()
		go func() { defer wg.Done(); client.fail(ErrDisconnected) }()
		wg.Wait()
	}
}
