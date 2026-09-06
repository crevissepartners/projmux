// Command claudefixture is an offline Claude process-shape fixture used only by
// the deterministic heterogeneous-dialogue E2E. It owns a private Unix socket,
// accepts the documented auth line plus exactly one frozen user frame per
// connection, and invokes official-hook-shaped Stop children. It has no model,
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
	Kind            string `json:"kind"`
	Authority       string `json:"authority"`
	MessageRef      string `json:"messageRef"`
	ConversationRef string `json:"conversationRef"`
	ReplyTo         string `json:"replyTo,omitempty"`
	Source          any    `json:"source"`
	Target          any    `json:"target"`
	Payload         string `json:"payload"`
}

func main() {
	if err := run(); err != nil {
		root := os.Getenv("PROJMUX_FAKE_CLAUDE_STATE")
		if filepath.IsAbs(root) {
			_ = os.WriteFile(filepath.Join(root, "fixture-error"), []byte(err.Error()+"\n"), 0o600)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root, binary := os.Getenv("PROJMUX_FAKE_CLAUDE_STATE"), os.Getenv("PROJMUX_FAKE_CLAUDE_BIN")
	if !filepath.IsAbs(root) || !filepath.IsAbs(binary) {
		return errors.New("fake Claude requires absolute owned state and product binary")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	socketPath := filepath.Join(root, "provider.sock")
	_ = os.Remove(socketPath)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(false)
	defer func() { _ = listener.Close(); _ = os.Remove(socketPath) }()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return err
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return errors.New("fixture token unavailable")
	}
	token := hex.EncodeToString(tokenBytes)
	environment := append(os.Environ(), "CLAUDE_CODE_MESSAGING_SOCKET="+socketPath, "CLAUDE_CODE_MESSAGING_TOKEN="+token)
	if err := runHook(ctx, binary, environment, "claude-endpoint-register", map[string]any{
		"hook_event_name": "SessionStart", "session_id": sessionID,
	}); err != nil {
		return fmt.Errorf("registration hook: %w", err)
	}
	if err := atomicWrite(filepath.Join(root, "registration-ready"), []byte("ready\n")); err != nil {
		return err
	}

	qualification, err := receiveFrame(listener, token)
	if err != nil {
		return fmt.Errorf("qualification push: %w", err)
	}
	marker := qualification.Message.Content[strings.LastIndex(qualification.Message.Content, " ")+1:]
	if !strings.HasPrefix(marker, qualificationMarkerPrefix) {
		return errors.New("qualification marker missing")
	}
	if err := atomicWrite(filepath.Join(root, "qualification.json"), []byte(`{"state":"frame-received"}`+"\n")); err != nil {
		return err
	}
	if err := runHook(ctx, binary, environment, "claude-message-reply", map[string]any{
		"hook_event_name": "Stop", "session_id": sessionID, "stop_hook_active": false, "last_assistant_message": marker,
	}); err != nil {
		return fmt.Errorf("qualification Stop: %w", err)
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
	if err := atomicWrite(filepath.Join(root, "frame.json"), append(frameBytes, '\n')); err != nil {
		return err
	}
	if err := runHook(ctx, binary, environment, "claude-message-reply", map[string]any{
		"hook_event_name": "Stop", "session_id": sessionID, "stop_hook_active": false,
		"last_assistant_message": "HETEROGENEOUS_REPLY:" + content.MessageRef,
	}); err != nil {
		return fmt.Errorf("reply Stop: %w", err)
	}
	if err := atomicWrite(filepath.Join(root, "round-trip-complete"), []byte("ready\n")); err != nil {
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
	command := exec.CommandContext(ctx, binary, "internal", route)
	command.Env, command.Stdin, command.Stdout, command.Stderr = environment, strings.NewReader(string(input)), io.Discard, io.Discard
	return command.Run()
}

func atomicWrite(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
