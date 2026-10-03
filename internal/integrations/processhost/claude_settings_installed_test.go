package processhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The native lane executes the same resolved profile argv that tmux would
// receive; print output exposes init without opening a terminal or live server.
func TestInstalledClaudeSettingSourcesParity(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CLAUDE") != "1" {
		t.Skip("opt-in installed provider qualification")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, sources := range []string{"inherited", "user,project,local"} {
		t.Run(sources, func(t *testing.T) {
			root := t.TempDir()
			t.Logf("isolated provider HOME=%s", root)
			workspace := filepath.Join(root, "workspace")
			for _, base := range []string{root, workspace} {
				skill := filepath.Join(base, ".claude", "skills", "parity-skill")
				commands := filepath.Join(base, ".claude", "commands")
				if err := os.MkdirAll(skill, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(commands, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: parity-skill\ndescription: Isolated parity fixture\n---\nReply PARITY.\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(commands, "parity-command.md"), []byte("Reply PARITY.\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			profile := filepath.Join(root, "profile.json")
			if err := os.WriteFile(profile, []byte(`{"hooks":{}}`), 0600); err != nil {
				t.Fatal(err)
			}
			server := installedClaudeStub(t, false)
			defer server.Close()
			env := installedClaudeEnv(root, server.URL)
			resolved := []string{"--model", "haiku", "--settings", profile, "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence"}
			if sources != "inherited" {
				resolved = append(resolved, "--setting-sources", sources)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			native := exec.CommandContext(ctx, path, append(slices.Clone(resolved), "--print", "--verbose", "--output-format", "stream-json", "--", "Reply STREAM_OK.")...)
			native.Dir = workspace
			native.Env = env
			output, err := native.Output()
			if err != nil {
				t.Fatalf("native profile init: %v", err)
			}
			var expected claudeInitCapabilities
			scanner := bufio.NewScanner(bytes.NewReader(output))
			for scanner.Scan() {
				var init claudeInitCapabilities
				if json.Unmarshal(scanner.Bytes(), &init) == nil && init.Type == "system" && init.Subtype == "init" {
					expected = init
					break
				}
			}
			if !slices.Contains(expected.Skills, "parity-skill") || !slices.Contains(expected.SlashCommands, "parity-command") {
				t.Fatalf("native profile did not load fixture: %+v", expected)
			}
			command, err := ClaudeCommand(path, workspace, env, resolved)
			if err != nil {
				t.Fatal(err)
			}
			// Capture native init bytes in a test-only transparent relay; the host
			// intentionally does not retain system/init payloads in its public events.
			capture := filepath.Join(root, "process-init.jsonl")
			command.Args = append([]string{"-u", "-c", claudeInitCaptureRelay, capture, command.Path}, command.Args...)
			command.Path = "python3"
			h := testHost(t, func(_ *Transactions, l *Limits) { l.Startup = 15 * time.Second })
			h.supervisor.Path = installedClaudeSupervisor(t, root)
			p, err := h.Start(ctx, Launch{Binding: binding(), Command: command})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = p.Stop(binding())
				wait, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := p.Wait(wait, binding()); err != nil {
					t.Error(err)
				}
			}()
			turn(t, p, "parity", "Reply STREAM_OK.")
			observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
			var actual claudeInitCapabilities
			captured, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			scanner = bufio.NewScanner(bytes.NewReader(captured))
			for scanner.Scan() {
				var init claudeInitCapabilities
				if json.Unmarshal(scanner.Bytes(), &init) == nil && init.Type == "system" && init.Subtype == "init" {
					actual = init
					break
				}
			}
			slices.Sort(expected.Skills)
			slices.Sort(expected.SlashCommands)
			slices.Sort(actual.Skills)
			slices.Sort(actual.SlashCommands)
			if !slices.Equal(expected.Skills, actual.Skills) || !slices.Equal(expected.SlashCommands, actual.SlashCommands) {
				t.Fatalf("profile init differs: native=%+v process=%+v", expected, actual)
			}
			t.Logf("same profile skills=%v slash_commands=%v", actual.Skills, actual.SlashCommands)
		})
	}
}

type claudeInitCapabilities struct {
	Type          string   `json:"type"`
	Subtype       string   `json:"subtype"`
	Skills        []string `json:"skills"`
	SlashCommands []string `json:"slash_commands"`
}

const claudeInitCaptureRelay = `
import subprocess,sys
p=subprocess.Popen(sys.argv[2:],stdin=sys.stdin,stdout=subprocess.PIPE,stderr=sys.stderr)
with open(sys.argv[1],'wb') as capture:
 for line in p.stdout:
  capture.write(line);capture.flush()
  sys.stdout.buffer.write(line);sys.stdout.buffer.flush()
sys.exit(p.wait())
`
