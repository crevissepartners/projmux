package codexappserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapturedProxyEnvironmentReachesProbeManagerAndOpen(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	provider := filepath.Join(root, "provider")
	for _, d := range []string{home, provider} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	trace := filepath.Join(root, "environment.log")
	executable := filepath.Join(root, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s|%%s|%%s|%%s\\n' \"$1 $2 $3\" \"$HOME\" \"$CODEX_HOME\" \"$PATH\" >> %q\nif [ \"$1 $2 $3\" = 'app-server daemon version' ]; then printf '%%s\\n' '{\"status\":\"running\",\"backend\":\"pid\",\"cliVersion\":\"0.149.0\",\"appServerVersion\":\"0.149.0\"}'; exit 0; fi\nexec %q -test.run=TestProxyProbeHelperProcess -- healthy\n", trace, os.Args[0])
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := &ProxyEnvironment{Executable: executable, HomeDir: home, Values: map[string]string{"PATH": "/captured-path", "CODEX_HOME": provider, "GO_WANT_PROXY_HELPER": "1"}}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PATH", "/ambient-missing")
	client, health, err := AttachDefaultEndpointWithEnvironment(t.Context(), "test", AttachOptions{Timeout: 3 * time.Second}, env)
	if err != nil {
		t.Fatalf("attach: %v %+v", err, health)
	}
	_ = client.Close()
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("probe/manager/open cardinality: %s", raw)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, "|"+home+"|"+provider+"|/captured-path") {
			t.Fatalf("ambient environment reached child: %q", line)
		}
	}
	if !strings.HasPrefix(lines[1], "app-server daemon version|") {
		t.Fatalf("unexpected lifecycle argv: %s", raw)
	}
	// Invalid explicit executable cannot fall back to an ambient executable.
	env.Executable = "relative"
	health = ProbeDefaultProxyWithEnvironment(context.Background(), time.Second, "test", true, env)
	if health.ProbeReason != ReasonExecutableMissing {
		t.Fatalf("explicit route fell back: %+v", health)
	}
}
