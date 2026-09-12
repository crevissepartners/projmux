package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestRequestFailureDiagnosticPreservesOriginalThroughCatalogWrapping(t *testing.T) {
	secret := "prompt-secret auth=secret Cookie: secret /private/path\n\x1b[31m" + strings.Repeat("x", 9000) + "\xff"
	message, _ := json.Marshal(secret)
	for _, tt := range []struct {
		name, method, frame, want string
		catalog                   bool
		kind                      error
	}{
		{"unsupported", methodThreadRead, `{"id":1,"error":{"code":-32601,"message":` + string(message) + `}}`, `{"method":"thread/read","rpc_code":-32601,"cause":"unsupported"}`, false, ErrUnsupported},
		{"catalog", methodThreadList, `{"id":1,"error":{"code":-32602,"message":` + string(message) + `}}`, `{"method":"thread/list","rpc_code":-32602,"cause":"catalog-rejected"}`, true, ErrUnsupported},
		{"other rpc", methodThreadRead, `{"id":1,"error":{"code":42,"message":` + string(message) + `}}`, `{"method":"thread/read","rpc_code":42,"cause":"rpc-refused"}`, false, ErrProtocol},
		{"protocol", methodThreadRead, `{invalid`, `{"method":"thread/read","rpc_code":null,"cause":"protocol-error"}`, false, ErrProtocol},
		{"missing code", methodThreadRead, `{"id":1,"error":{"message":"secret"}}`, `{"method":"thread/read","rpc_code":null,"cause":"protocol-error"}`, false, ErrProtocol},
		{"null code", methodThreadRead, `{"id":1,"error":{"code":null}}`, `{"method":"thread/read","rpc_code":null,"cause":"protocol-error"}`, false, ErrProtocol},
		// uncaptured-default: FailureDiagnostic.cause explicitly captures this peer EOF as disconnected, in its own closed diagnostic vocabulary.
		{"peer closes without response", methodThreadRead, "", `{"method":"thread/read","rpc_code":null,"cause":"disconnected"}`, false, ErrDisconnected},
		{"unknown method", secret, `{"id":1,"error":{"code":-32601}}`, `{"method":"unknown","rpc_code":-32601,"cause":"unsupported"}`, false, ErrUnsupported},
	} {
		t.Run(tt.name, func(t *testing.T) {
			local, peer := net.Pipe()
			client := NewClient(local)
			defer client.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer peer.Close()
				_, _ = bufio.NewReader(peer).ReadString('\n')
				if tt.frame != "" {
					_, _ = fmt.Fprintln(peer, tt.frame)
				}
			}()
			var err error
			if tt.catalog {
				_, err = client.ListCatalogThreads(t.Context(), CatalogQuery{})
			} else {
				err = client.Request(t.Context(), tt.method, nil, nil)
			}
			<-done
			if !errors.Is(err, tt.kind) {
				t.Fatalf("classification = %v, want %v", err, tt.kind)
			}
			got := Diagnostic(fmt.Errorf("outer catalog boundary: %w", err)).String()
			if got != tt.want || len(got) > MaxFailureDiagnosticBytes {
				t.Fatalf("diagnostic = %s, want %s", got, tt.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("request error leaked payload")
			}
			if tt.catalog && !errors.Is(err, ErrStateDbOnlyRejected) {
				t.Fatal("lost catalog category")
			}
			if tt.name == "unsupported" && errors.Is(err, ErrProtocol) {
				t.Fatal("unsupported acquired protocol category")
			}
		})
	}
}

func TestFailureDiagnosticIPCProjectionIsClosedAndBounded(t *testing.T) {
	for _, unsafe := range []string{"prompt-secret", "/secret/0.1.0", "auth=secret", "Cookie: secret", "\x1b\n\xff", strings.Repeat("secret", 100000)} {
		d := FailureDiagnostic{Method: unsafe, Cause: unsafe}
		got := Diagnostic(WithDiagnostic(context.DeadlineExceeded, d)).String()
		if got != `{"method":"unknown","rpc_code":null,"cause":"unknown"}` || len(got) > MaxFailureDiagnosticBytes {
			t.Fatal("unsafe IPC projection")
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, ErrDisconnected, ErrProtocol, ErrPayloadTooLarge} {
		got := Diagnostic(classifyProxyOpenError(context.Background(), withRequestFailure(methodInitialize, err)))
		if got.Method != methodInitialize || got.RPCCode != nil {
			t.Fatalf("proxy wrapper lost method or invented code: %s", got)
		}
	}
}

func TestLifecycleRPCDiagnosticPreservesCodeAndRejectsUnrepresentableCode(t *testing.T) {
	for _, code := range []string{"-32601", "-32602", "42", "9223372036854775807", "-9223372036854775808"} {
		frame := fmt.Sprintf(`{"id":7,"error":{"code":%s,"message":"secret"}}`, code)
		_, err := projectLifecycleJSON(t.Context(), []byte(frame), 7, "thread-1", 2, 7)
		diagnostic := Diagnostic(withRequestFailure(methodThreadRead, err))
		if diagnostic.Method != methodThreadRead || diagnostic.RPCCode == nil || fmt.Sprint(*diagnostic.RPCCode) != code {
			t.Fatalf("lost exact lifecycle code: %s", diagnostic)
		}
	}
	for _, code := range []string{"1.5", "1e100", "9223372036854775808", "null", "true", `"-32601"`} {
		frame := fmt.Sprintf(`{"id":7,"error":{"code":%s}}`, code)
		_, err := projectLifecycleJSON(t.Context(), []byte(frame), 7, "thread-1", 2, 7)
		diagnostic := Diagnostic(err)
		if diagnostic.Cause != "protocol-error" || diagnostic.RPCCode != nil {
			t.Fatalf("invented lifecycle code: %s", diagnostic)
		}
	}
}
