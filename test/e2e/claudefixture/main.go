// Command claudefixture is an offline Claude process-shape fixture used only by
// the deterministic heterogeneous-dialogue E2E. It owns a private Unix socket,
// accepts the documented auth line plus exactly one frozen user frame per
// connection, and executes explicit public reply commands. It has no model,
// tool, plugin, MCP, connector, credential, or other vendor frame.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	sessionID                 = "fixture-claude-session"
	qualificationMarkerPrefix = "HETEROGENEOUS_QUALIFIED:qualification-"
)

type providerFrame struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

type coordinationContent struct {
	Kind            string            `json:"kind"`
	Authority       string            `json:"authority"`
	MessageRef      string            `json:"messageRef"`
	ConversationRef string            `json:"conversationRef"`
	ReplyTo         string            `json:"replyTo,omitempty"`
	Source          map[string]string `json:"source"`
	Target          any               `json:"target"`
	Payload         string            `json:"payload"`
	SourceNotice    string            `json:"sourceNotice"`
	ReplyAction     string            `json:"replyAction"`
}

func main() {
	if err := run(); err != nil {
		if root, _, openErr := fixturePaths(); openErr == nil {
			_ = atomicWrite(root, "fixture-error", []byte(err.Error()+"\n"))
			_ = root.Close()
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// fixturePaths confines every artifact to the existing E2E-owned directory.
// The executable is supplied by the local test harness, never model input.
func fixturePaths() (*os.Root, string, error) {
	statePath, binary := os.Getenv("PROJMUX_FAKE_CLAUDE_STATE"), os.Getenv("PROJMUX_FAKE_CLAUDE_BIN")
	if !filepath.IsAbs(statePath) || filepath.Clean(statePath) != statePath || !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		return nil, "", errors.New("fake Claude requires absolute owned state and product binary")
	}
	info, err := os.Lstat(statePath) // #nosec G703 -- read-only validation before os.OpenRoot confinement.
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, "", errors.New("fake Claude state is not a private directory")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(owner.Uid) != int64(os.Getuid()) {
		return nil, "", errors.New("fake Claude state owner differs")
	}
	info, err = os.Lstat(binary) // #nosec G703 -- read-only validation of the harness-selected executable.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, "", errors.New("fake Claude product binary is not a fixed executable")
	}
	root, err := os.OpenRoot(statePath)
	return root, binary, err
}

func run() error {
	root, binary, err := fixturePaths()
	if err != nil {
		return err
	}
	defer root.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	socketPath := filepath.Join(root.Name(), "provider.sock")
	// A stale file must not be reused or silently removed.
	if _, err := root.Lstat("provider.sock"); !errors.Is(err, os.ErrNotExist) {
		return errors.New("fixture provider socket already exists")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(false)
	// Cancellation must unblock an accept before any qualification arrives.
	// The fixture owns this listener; no shared process or socket is signaled.
	go func() { <-ctx.Done(); _ = listener.Close() }()
	defer func() { _ = listener.Close(); _ = root.Remove("provider.sock") }()
	if err := root.Chmod("provider.sock", 0o600); err != nil {
		return err
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return errors.New("fixture token unavailable")
	}
	token := hex.EncodeToString(tokenBytes)
	environment := append(os.Environ(), "CLAUDE_CODE_MESSAGING_SOCKET="+socketPath, "CLAUDE_CODE_MESSAGING_TOKEN="+token, "PMX_INTERNAL_CLAUDE_REPLY_GUARD=1")
	publicProfile := os.Getenv("PMX_INTERNAL_CLAUDE_DIALOGUE_PROFILE") != ""
	if publicProfile {
		if err := publicProfileStartup(ctx, binary, environment); err != nil {
			return err
		}
	} else {
		if err := runHook(ctx, binary, environment, "claude-endpoint-register", map[string]any{
			"hook_event_name": "SessionStart", "session_id": sessionID,
		}); err != nil {
			return fmt.Errorf("registration hook: %w", err)
		}
	}
	if err := atomicWrite(root, "registration-ready", []byte("ready\n")); err != nil {
		return err
	}

	qualification, err := receiveFrame(listener, token)
	if err != nil {
		return fmt.Errorf("qualification push: %w", err)
	}
	var challenge coordinationContent
	if decodeExact([]byte(qualification.Message.Content), &challenge) != nil || !strings.HasPrefix(challenge.MessageRef, "qualification-") {
		return errors.New("qualification broker challenge missing")
	}
	if err := atomicWrite(root, "qualification.json", []byte(`{"state":"frame-received"}`+"\n")); err != nil {
		return err
	}
	if err := runExplicitReply(ctx, binary, environment, challenge, "HETEROGENEOUS_QUALIFIED:"+challenge.MessageRef); err != nil {
		return fmt.Errorf("qualification explicit reply: %w", err)
	}

	message, err := receiveFrame(listener, token)
	if err != nil {
		return fmt.Errorf("coordination push: %w", err)
	}
	var content coordinationContent
	if decodeExact([]byte(message.Message.Content), &content) != nil || content.Kind != "projmux-coordination" ||
		content.Authority != "untrusted-coordination-only" || content.MessageRef == "" || content.ConversationRef == "" {
		return errors.New("invalid untrusted coordination content")
	}
	frameBytes, _ := json.Marshal(content)
	if err := atomicWrite(root, "frame.json", append(frameBytes, '\n')); err != nil {
		return err
	}
	if err := runExplicitReply(ctx, binary, environment, content, "HETEROGENEOUS_REPLY:"+content.MessageRef); err != nil {
		return fmt.Errorf("explicit reply: %w", err)
	}

	if err := atomicWrite(root, "round-trip-complete", []byte("ready\n")); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func receiveFrame(listener *net.UnixListener, token string) (providerFrame, error) {
	connection, err := listener.AcceptUnix()
	if err != nil {
		return providerFrame{}, err
	}
	defer connection.Close()
	reader := bufio.NewReader(io.LimitReader(connection, 16<<10))
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	authLine, err := reader.ReadBytes('\n')
	if err != nil || decodeExact(authLine, &auth) != nil || auth.Type != "auth" || auth.Token != token {
		return providerFrame{}, errors.New("invalid auth line")
	}
	messageLine, err := reader.ReadBytes('\n')
	var message providerFrame
	if err != nil || decodeExact(messageLine, &message) != nil || message.Type != "user" ||
		message.Message.Role != "user" || message.Message.Content == "" {
		return providerFrame{}, errors.New("invalid frozen user frame")
	}
	if trailing, err := reader.ReadByte(); err != io.EOF || trailing != 0 {
		return providerFrame{}, errors.New("unexpected provider frame")
	}
	return message, nil
}

func decodeExact(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON value")
	}
	return nil
}

func runHook(ctx context.Context, binary string, environment []string, route string, payload map[string]any) error {
	input, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if route != "claude-endpoint-register" && route != "claude-message-reply" {
		return errors.New("fixture hook route is unsupported")
	}
	// #nosec G204 G702 -- harness-owned executable validated by fixturePaths;
	// fixed internal command and allowlisted hook route, with no shell.
	command := exec.CommandContext(ctx, binary, "internal", route)
	command.Env, command.Stdin, command.Stdout, command.Stderr = environment, strings.NewReader(string(input)), io.Discard, io.Discard
	return command.Run()
}

func runExplicitReply(ctx context.Context, binary string, environment []string, content coordinationContent, payload string) error {
	if content.Source["agentUID"] == "" || content.MessageRef == "" {
		return errors.New("fixture reply context missing")
	}
	var cleanEnvironment []string
	for _, value := range environment {
		if !strings.HasPrefix(value, "CLAUDE_CODE_MESSAGING_SOCKET=") && !strings.HasPrefix(value, "CLAUDE_CODE_MESSAGING_TOKEN=") {
			cleanEnvironment = append(cleanEnvironment, value)
		}
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	text := quote(binary) + " agent message send " + quote("uid:"+content.Source["agentUID"]) + " --reply-to " + quote(content.MessageRef) + " -- " + quote(payload)
	input, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": sessionID, "cwd": directory, "tool_name": "Bash", "tool_use_id": "tool-" + content.MessageRef, "tool_input": map[string]any{"command": text}})
	if err != nil {
		return err
	}
	publicProfile := os.Getenv("PMX_INTERNAL_CLAUDE_DIALOGUE_PROFILE") != ""
	toolID := "tool-" + content.MessageRef
	if publicProfile {
		if err := publicEvent(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": toolID, "name": "Bash", "input": map[string]any{"command": text}}}}}); err != nil {
			return err
		}
		if err := publicHookEvent("hook_started", "PreToolUse", toolID, ""); err != nil {
			return err
		}
	}
	// #nosec G204 G702 -- harness-pinned product binary, fixed internal route;
	// the synthetic official tool input is data on stdin, never evaluated here.
	prepare := exec.CommandContext(ctx, binary, "internal", "claude-reply-tool", "prepare")
	prepare.Env, prepare.Stdin, prepare.Stderr = cleanEnvironment, bytes.NewReader(input), io.Discard
	output, err := prepare.Output()
	if err != nil {
		return errors.New("fixture tool preparation failed")
	}
	var decision struct {
		Output struct {
			Decision string `json:"permissionDecision"`
			Input    struct {
				Command string `json:"command"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(output, &decision) != nil || decision.Output.Decision != "allow" || decision.Output.Input.Command == "" {
		return errors.New("fixture explicit tool was refused")
	}
	carrier := "fixture opaque carrier '" + decision.Output.Input.Command + "'"
	// #nosec G204 G702 -- exact harness-pinned executable and fixed internal route; opaque ticket is data, never a shell program.
	consume := exec.CommandContext(ctx, binary, "internal", "claude-reply-tool", "execute", carrier)
	if publicProfile {
		if err := publicHookEvent("hook_response", "PreToolUse", toolID, string(output)); err != nil {
			return err
		}
		alias := os.Getenv("CLAUDE_CODE_SHELL_PREFIX")
		if alias != filepath.Join(os.Getenv("PMX_INTERNAL_CLAUDE_DIALOGUE_PROFILE"), "projmux-claude-reply-prefix") {
			return errors.New("fixture public prefix differs from owned profile")
		}
		// #nosec G204 G702 -- exact profile alias points at the harness candidate; exercises public argv0 dispatch with one opaque argument, no eval.
		consume = exec.CommandContext(ctx, alias, carrier)
	}
	consume.Env, consume.Stderr = cleanEnvironment, io.Discard
	receipt, err := consume.Output()
	if err != nil {
		return err
	}
	if publicProfile {
		if err := publicEvent(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": string(receipt), "is_error": false}}}, "tool_use_result": map[string]any{"stdout": string(receipt), "stderr": "", "interrupted": false}}); err != nil {
			return err
		}
		return publicEvent(map[string]any{"type": "result", "subtype": "success", "is_error": false})
	}
	return nil
}

func atomicWrite(root *os.Root, name string, data []byte) error {
	temporary := name + ".tmp"
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}

// These are synthetic public CLI events, not model-execution evidence. They
// exercise the same production observer and ordinary public activation path.
func publicEvent(event map[string]any) error {
	event["session_id"] = sessionID
	return json.NewEncoder(os.Stdout).Encode(event)
}
func publicHookEvent(subtype, event, id, output string) error {
	name := event
	if event == "SessionStart" {
		name = "SessionStart:startup"
	}
	value := map[string]any{"type": "system", "subtype": subtype, "hook_id": id, "hook_event": event, "hook_name": name}
	if subtype == "hook_response" {
		value["exit_code"] = 0
		value["outcome"] = "success"
		value["stdout"] = output
		value["stderr"] = ""
		value["output"] = ""
	}
	return publicEvent(value)
}
func publicProfileStartup(ctx context.Context, binary string, environment []string) error {
	// The wrapper must have kept same-session persistence while restricting the
	// actual provider argv. The fixture never reads arbitrary terminal bytes.
	for key, want := range map[string]string{"--tools": "Bash", "--permission-mode": "dontAsk", "--setting-sources": "", "--input-format": "stream-json", "--output-format": "stream-json"} {
		found := false
		for i, arg := range os.Args {
			if arg == key && i+1 < len(os.Args) && os.Args[i+1] == want {
				found = true
			}
		}
		if !found {
			return errors.New("fixture public isolation argv missing")
		}
	}
	var initial providerFrame
	line, err := bufio.NewReader(io.LimitReader(os.Stdin, 4096)).ReadBytes('\n')
	if err != nil || decodeExact(line, &initial) != nil || initial.Type != "user" || initial.Message.Role != "user" || initial.Message.Content != "Reply READY." {
		return errors.New("fixture public readiness input differs")
	}
	for i := range 2 {
		id := fmt.Sprintf("fixture-startup-%d", i)
		if err := publicHookEvent("hook_started", "SessionStart", id, ""); err != nil {
			return err
		}
		if i == 0 {
			payload, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "session_id": sessionID})
			// #nosec G204 G702 -- fixed existing lifecycle callback on harness-owned candidate, no shell or model argv.
			state := exec.CommandContext(ctx, binary, "internal", "agent-hook", "ingest", "claude-hook", "--pane="+os.Getenv("PMX_INTERNAL_ACTIVATION_PANE_UID"))
			state.Env, state.Stdin, state.Stdout, state.Stderr = environment, bytes.NewReader(payload), io.Discard, io.Discard
			if err := state.Run(); err != nil {
				return errors.New("fixture public lifecycle callback failed")
			}
		} else if err := runHook(ctx, binary, environment, "claude-endpoint-register", map[string]any{"hook_event_name": "SessionStart", "session_id": sessionID}); err != nil {
			return errors.New("fixture public endpoint callback failed")
		}
		if err := publicHookEvent("hook_response", "SessionStart", id, ""); err != nil {
			return err
		}
	}
	if err := publicEvent(map[string]any{"type": "system", "subtype": "init", "tools": []string{"Bash"}, "mcp_servers": []any{}, "plugins": []any{}, "permissionMode": "dontAsk", "claude_code_version": "2.1.263"}); err != nil {
		return err
	}
	return publicEvent(map[string]any{"type": "result", "subtype": "success", "is_error": false})
}
