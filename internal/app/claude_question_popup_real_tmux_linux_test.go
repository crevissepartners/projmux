package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"golang.org/x/sys/unix"
)

// realTmuxQuestionServer is one isolated tmux server on a private socket with
// one shell Pane standing in for the Agent Pane.
type realTmuxQuestionServer struct {
	root, socket, paneID string
	environment          []string
}

func startRealTmuxQuestionServer(t *testing.T) realTmuxQuestionServer {
	t.Helper()
	requireRealTmux(t)
	root, err := os.MkdirTemp("/tmp", "pqa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root, "TERM=xterm-256color", "SHELL=/bin/sh", "LANG=en_US.UTF-8"}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TMUX", "TMUX_PANE", "TMUX_TMPDIR", "TERM", "SHELL", "LANG", "LC_ALL", runtimeMutationAnchorPaneEnv:
			continue
		}
		environment = append(environment, entry)
	}
	socketDir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	server := realTmuxQuestionServer{root: root, socket: filepath.Join(socketDir, "pqa"), environment: environment}
	t.Cleanup(server.killServer)
	if out, err := server.tmux("new-session", "-d", "-s", "qa", "-x", "160", "-y", "48", "/bin/sh"); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	if out, err := server.tmux("set-option", "-g", "default-shell", "/bin/sh"); err != nil {
		t.Fatalf("seed isolated tmux: %v: %s", err, out)
	}
	paneID, err := server.tmux("display-message", "-p", "-t", "qa", "#{pane_id}")
	if err != nil || !strings.HasPrefix(paneID, "%") {
		t.Fatalf("pane id = %q (%v)", paneID, err)
	}
	server.paneID = paneID
	return server
}

func (s realTmuxQuestionServer) tmux(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "tmux", append([]string{"-S", s.socket, "-f", "/dev/null"}, args...)...)
	command.Env = s.environment
	out, err := command.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s realTmuxQuestionServer) killServer() {
	_, _ = s.tmux("kill-server")
}

// Run is the hook's tmux runner, pinned to the isolated server.
func (s realTmuxQuestionServer) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" {
		return nil, fmt.Errorf("unexpected command %q", name)
	}
	command := exec.CommandContext(ctx, "tmux", append([]string{"-S", s.socket, "-f", "/dev/null"}, args...)...)
	command.Env = s.environment
	return command.CombinedOutput()
}

// pickerWrapper writes the executable the popup runs: the test binary, told to
// run the popup route.
func (s realTmuxQuestionServer) pickerWrapper(t *testing.T) string {
	t.Helper()
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.root, "projmux")
	script := "#!/bin/sh\nexec env " + claudeQuestionPickerChildEnv + "=1 " + tmuxShellQuote(image) + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// realTmuxQuestionClient is one terminal client attached through a
// pseudo-terminal, which a popup needs; a control-mode client cannot draw one.
//
// Its drawn output is kept only as a bounded tail, so a runaway redraw cannot
// grow the test's memory; marks count every byte ever drawn.
type realTmuxQuestionClient struct {
	master  *os.File
	mu      sync.Mutex
	screen  []byte
	dropped int
	command *exec.Cmd
}

// realTmuxQuestionScreenLimit bounds the retained terminal output.
const realTmuxQuestionScreenLimit = 256 << 10

// realTmuxPtyUnavailable ends a test that could not acquire the
// pseudo-terminal its terminal client needs. It skips, unless
// realTmuxStrictEnv is "1": then the test fails with the step and its cause,
// so a strict runner cannot pass the popup tests without drawing a popup.
func realTmuxPtyUnavailable(t testing.TB, step string, err error) {
	t.Helper()
	if os.Getenv(realTmuxStrictEnv) == "1" {
		t.Fatalf("%s: %v; %s=1 requires the real-tmux popup tests to attach a terminal client", step, err, realTmuxStrictEnv)
	}
	t.Skipf("no pseudo-terminal: %s: %v", step, err)
}

// TestRealTmuxPtyUnavailableSkipsOrFails pins both answers to a pseudo-terminal
// that cannot be acquired: a skip by default, a failure carrying the step, the
// cause, and the strict variable under strict mode.
func TestRealTmuxPtyUnavailableSkipsOrFails(t *testing.T) {
	cause := errors.New("no such device")
	for _, value := range []string{"", "0", "true"} {
		t.Setenv(realTmuxStrictEnv, value)
		recorder := recordRealTmuxHelper(func(tb testing.TB) { realTmuxPtyUnavailable(tb, "open /dev/ptmx", cause) })
		if !recorder.skipped || recorder.fatal != "" {
			t.Fatalf("%s=%q: skipped=%v fatal=%q, want a skip", realTmuxStrictEnv, value, recorder.skipped, recorder.fatal)
		}
	}

	t.Setenv(realTmuxStrictEnv, "1")
	recorder := recordRealTmuxHelper(func(tb testing.TB) { realTmuxPtyUnavailable(tb, "open /dev/ptmx", cause) })
	for _, want := range []string{"open /dev/ptmx", cause.Error(), realTmuxStrictEnv + "=1"} {
		if recorder.skipped || !strings.Contains(recorder.fatal, want) {
			t.Fatalf("strict: skipped=%v fatal=%q, want a failure containing %q", recorder.skipped, recorder.fatal, want)
		}
	}
}

func (s realTmuxQuestionServer) attach(t *testing.T) *realTmuxQuestionClient {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		realTmuxPtyUnavailable(t, "open /dev/ptmx", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		realTmuxPtyUnavailable(t, "unlock pseudo-terminal", err)
	}
	index, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		realTmuxPtyUnavailable(t, "name pseudo-terminal", err)
	}
	tty, err := os.OpenFile("/dev/pts/"+strconv.Itoa(index), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		realTmuxPtyUnavailable(t, "open /dev/pts/"+strconv.Itoa(index), err)
	}
	defer tty.Close()
	if err := unix.IoctlSetWinsize(int(tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 48, Col: 160}); err != nil {
		t.Fatal(err)
	}
	client := &realTmuxQuestionClient{master: master}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	command := exec.CommandContext(ctx, "tmux", "-S", s.socket, "-f", "/dev/null", "attach-session", "-t", "qa")
	command.Env = s.environment
	command.Stdin, command.Stdout, command.Stderr = tty, tty, tty
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		cancel()
		_ = master.Close()
		t.Fatalf("attach a terminal client: %v", err)
	}
	client.command = command
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				client.mu.Lock()
				client.screen = append(client.screen, buffer[:n]...)
				if extra := len(client.screen) - realTmuxQuestionScreenLimit; extra > 0 {
					client.screen = append(client.screen[:0:0], client.screen[extra:]...)
					client.dropped += extra
				}
				client.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		cancel()
		_ = master.Close()
	})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if out, err := s.tmux("list-clients", "-F", "#{client_name}"); err == nil && out != "" {
			return client
		}
		if time.Now().After(deadline) {
			t.Fatal("the terminal client never attached")
		}
	}
}

// waitFor waits until the client's terminal has drawn text since mark, and
// returns the new mark.
func (c *realTmuxQuestionClient) waitFor(t *testing.T, mark int, text string) int {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		c.mu.Lock()
		start := max(mark-c.dropped, 0)
		found := bytes.Contains(c.screen[start:], []byte(text))
		length := c.dropped + len(c.screen)
		tail := string(c.screen[max(len(c.screen)-4000, 0):])
		c.mu.Unlock()
		if found {
			return length
		}
		if time.Now().After(deadline) {
			t.Fatalf("the client never drew %q; it drew:\n%q", text, tail)
		}
	}
}

// drewSince reports whether the client's terminal drew text since mark.
func (c *realTmuxQuestionClient) drewSince(mark int, text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Contains(c.screen[max(mark-c.dropped, 0):], []byte(text))
}

func (c *realTmuxQuestionClient) mark() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped + len(c.screen)
}

func (c *realTmuxQuestionClient) keys(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, err := c.master.WriteString(key); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// realTmuxQuestionHook is a way-2 hook whose popup talks to the isolated
// server and runs the popup route from the test binary.
func (s realTmuxQuestionServer) questionFixture(t *testing.T) (*questionFixture, claudeQuestionHook) {
	t.Helper()
	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	fixture.command.questionAnswering = func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux }
	pane, _ := fixture.resources.registry.Pane(questionTestPane)
	pane.Status.Activation.RuntimeID = s.paneID
	wrapper := s.pickerWrapper(t)
	fixture.popup = tmuxClaudeQuestionPopup{
		runner:     s,
		executable: func() (string, error) { return wrapper, nil },
		lookupEnv: func(key string) string {
			if key == "TMUX" {
				return s.socket + ",1,0"
			}
			return ""
		},
	}
	hook := fixture.hook(time.Minute)
	hook.clientPoll = 100 * time.Millisecond
	return fixture, hook
}

// startRealTmuxQuestionHook runs one more hook and returns the id of the
// question it records.
func startRealTmuxQuestionHook(t *testing.T, fixture *questionFixture, hook claudeQuestionHook) (string, <-chan string) {
	t.Helper()
	before, _ := fixture.store.List(questionTestAgent)
	known := map[string]bool{}
	for _, record := range before {
		known[record.ID] = true
	}
	done := make(chan string, 1)
	go func() {
		var stdout bytes.Buffer
		hook.run(context.Background(), []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
		done <- stdout.String()
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		records, _ := fixture.store.List(questionTestAgent)
		for _, record := range records {
			if !known[record.ID] {
				return record.ID, done
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the hook never recorded its question")
		}
	}
}

// TestClaudeQuestionPopupRealTmuxAnswersFromThePicker drives the real popup on
// a real terminal client: the picker is answered with keys, and that answer
// is the hook's decision.
func TestClaudeQuestionPopupRealTmuxAnswersFromThePicker(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	client := server.attach(t)
	fixture, hook := server.questionFixture(t)
	mark := client.mark()
	id, done := startRealTmuxQuestionHook(t, fixture, hook)

	mark = client.waitFor(t, mark, "Which build tool?")
	// Multi-select: toggle "make", move to Done, and finish.
	client.keys(t, "\r")
	mark = client.waitFor(t, mark, "[x] make")
	client.keys(t, "\x0e", "\x0e", "\x0e", "\r")
	client.waitFor(t, mark, "Which branch?")
	// Single-select: the first option.
	client.keys(t, "\r")

	want := `"answers":{"Which branch?":"main","Which build tool?":"make"}`
	select {
	case got := <-done:
		if !strings.Contains(got, `"permissionDecision":"allow"`) || !strings.Contains(got, want) {
			t.Fatalf("decision = %q, want %s", got, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the hook did not return")
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateAnswered {
		t.Fatalf("state = %s, want answered", record.State)
	}
}

// TestCodexQuestionPopupRealTmuxAnswersThroughBinding uses the same isolated
// socket and terminal picker as Claude, then checks Codex's existing server
// request responder receives the ID-keyed answer array.
func TestCodexQuestionPopupRealTmuxAnswersThroughBinding(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	client := server.attach(t)
	fixture := newQuestionFixture(t, false)
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	agent.Spec.Provider = aiModeCodex
	pane, _ := fixture.resources.registry.Pane(questionTestPane)
	pane.Status.Activation.RuntimeID = server.paneID
	if err := fixture.resources.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	wrapper := server.pickerWrapper(t)
	popup := tmuxClaudeQuestionPopup{
		runner:     server,
		executable: func() (string, error) { return wrapper, nil },
		routed:     true,
	}
	channel := codexQuestionChannel{
		loadRegistry: fixture.resources.store().load,
		store:        func() (*agentquestion.Store, error) { return fixture.store, nil },
		popup:        popup,
		answering:    func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux },
		window:       func() time.Duration { return time.Minute },
		newID:        agentquestion.NewID,
		poll:         10 * time.Millisecond,
	}
	// Cleanups run before t.TempDir removes the store, and after
	// t.Context is canceled, so the waiter is joined first.
	t.Cleanup(channel.Wait)
	responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mark := client.mark()
	channel.Handle(ctx, codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: server.paneID, Generation: "gen-1", ThreadID: "thread-1"}, codexappserver.Notification{
		Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"choice","question":"Choose a build tool","options":[{"label":"make"},{"label":"task"}]}]}`),
	}, responder)
	client.waitFor(t, mark, "Choose a build tool")
	client.keys(t, "\r")
	select {
	case reply := <-responder.replies:
		if reply.id != "17" || len(reply.result.Answers) != 1 || len(reply.result.Answers["choice"].Answers) != 1 || reply.result.Answers["choice"].Answers[0] != "make" {
			t.Fatalf("Codex popup answer = %+v", reply)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("real tmux popup answer did not reach the Codex binding")
	}
	records, err := fixture.store.List(questionTestAgent)
	if err != nil || len(records) != 1 || records[0].State != agentquestion.StateAnswered {
		t.Fatalf("Codex popup record = %+v, err=%v", records, err)
	}
}

// TestClaudeQuestionPopupRealTmuxOpensForALateClientAndClosesOnACommandLineAnswer
// starts with nobody viewing the Pane: the hook waits, opens the popup once a
// client attaches, and when the command line answers first it closes that
// popup for real: keys typed afterwards reach the Pane's shell again.
func TestClaudeQuestionPopupRealTmuxOpensForALateClientAndClosesOnACommandLineAnswer(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	fixture, hook := server.questionFixture(t)
	id, done := startRealTmuxQuestionHook(t, fixture, hook)

	time.Sleep(500 * time.Millisecond)
	select {
	case got := <-done:
		t.Fatalf("the hook gave up with no client: %q", got)
	default:
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateWaiting {
		t.Fatalf("state = %s, want waiting with no client", record.State)
	}

	client := server.attach(t)
	client.waitFor(t, 0, "Which build tool?")
	if _, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, id, "--index", "1=2", "--text", "2=release"); err != nil {
		t.Fatalf("command-line answer: %v", err)
	}
	select {
	case got := <-done:
		if !strings.Contains(got, `"answers":{"Which branch?":"release","Which build tool?":"task"}`) {
			t.Fatalf("decision = %q, want the command-line answer", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the hook did not return")
	}

	marker := filepath.Join(server.root, "popup-closed")
	client.keys(t, "touch "+marker+"\r")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("keys never reached the Pane: the popup is still open")
		}
	}
}

// TestClaudeQuestionPopupRealTmuxEscGivesTheQuestionBack is Esc in the real
// popup: no decision, and the record is closed.
func TestClaudeQuestionPopupRealTmuxEscGivesTheQuestionBack(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	client := server.attach(t)
	fixture, hook := server.questionFixture(t)
	id, done := startRealTmuxQuestionHook(t, fixture, hook)

	client.waitFor(t, 0, "Which build tool?")
	client.keys(t, "\x1b")
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("hook printed %q after Esc", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the hook did not return")
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateClosed {
		t.Fatalf("state = %s, want closed", record.State)
	}
}

// answerRealTmuxQuestionPicker answers the fixture's two questions in the
// popup on client with keys: "make", then "main".
func answerRealTmuxQuestionPicker(t *testing.T, client *realTmuxQuestionClient, mark int) {
	t.Helper()
	mark = client.waitFor(t, mark, "Which build tool?")
	client.keys(t, "\r")
	mark = client.waitFor(t, mark, "[x] make")
	client.keys(t, "\x0e", "\x0e", "\x0e", "\r")
	client.waitFor(t, mark, "Which branch?")
	client.keys(t, "\r")
}

func waitRealTmuxQuestionHook(t *testing.T, done <-chan string) string {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(20 * time.Second):
		t.Fatal("the hook did not return")
		return ""
	}
}

// TestClaudeQuestionPopupRealTmuxOpensOnAClientViewingAnotherWindow is the
// operator looking at another Window: the popup opens on their client anyway,
// its title names the asking Agent and its Project/Window, the picker answers
// the question, and the popup does not come back once it closed.
func TestClaudeQuestionPopupRealTmuxOpensOnAClientViewingAnotherWindow(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	client := server.attach(t)
	if out, err := server.tmux("new-window", "-t", "qa", "/bin/sh"); err != nil {
		t.Fatalf("open a second Window: %v: %s", err, out)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		viewed, err := server.tmux("list-clients", "-F", "#{pane_id}")
		if err == nil && strings.HasPrefix(viewed, "%") && viewed != server.paneID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the client still views %q, want a Pane other than the Agent's %s", viewed, server.paneID)
		}
	}
	fixture, hook := server.questionFixture(t)
	title := claudeQuestionText{locale: settingsLocale()}.popupTitle(claudeQuestionAsker{Agent: "codex", Location: "alpha/main"})
	mark := client.mark()
	id, done := startRealTmuxQuestionHook(t, fixture, hook)

	client.waitFor(t, mark, title)
	answerRealTmuxQuestionPicker(t, client, mark)
	if got := waitRealTmuxQuestionHook(t, done); !strings.Contains(got, `"answers":{"Which branch?":"main","Which build tool?":"make"}`) {
		t.Fatalf("decision = %q, want the popup answer", got)
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateAnswered {
		t.Fatalf("state = %s, want answered", record.State)
	}

	// The popup is gone for good: keys reach the viewed Window's shell, and
	// the title is not drawn again.
	after := client.mark()
	marker := filepath.Join(server.root, "popup-closed")
	client.keys(t, "touch "+marker+"\r")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("keys never reached the shell: the popup is still open")
		}
	}
	time.Sleep(time.Second)
	if client.drewSince(after, title) {
		t.Fatal("the popup came back after it closed")
	}
}

// TestClaudeQuestionPopupRealTmuxQueuesASecondQuestionOnOneClient is two
// questions for one client: tmux will not draw a second popup over the first,
// so the second question keeps waiting, and its popup shows once the first is
// answered and closed.
func TestClaudeQuestionPopupRealTmuxQueuesASecondQuestionOnOneClient(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	client := server.attach(t)
	fixture, hook := server.questionFixture(t)
	mark := client.mark()
	first, firstDone := startRealTmuxQuestionHook(t, fixture, hook)
	client.waitFor(t, mark, "Which build tool?")

	second, secondDone := startRealTmuxQuestionHook(t, fixture, hook)
	// Several looks at 100ms each meet the first popup.
	time.Sleep(time.Second)
	select {
	case got := <-secondDone:
		t.Fatalf("the second hook returned %q while the first popup was open", got)
	default:
	}
	if record, _, _ := fixture.store.Get(second); record.State != agentquestion.StateWaiting {
		t.Fatalf("second state = %s, want waiting behind the first popup", record.State)
	}

	answerRealTmuxQuestionPicker(t, client, mark)
	if got := waitRealTmuxQuestionHook(t, firstDone); !strings.Contains(got, `"permissionDecision":"allow"`) {
		t.Fatalf("first decision = %q", got)
	}
	if record, _, _ := fixture.store.Get(first); record.State != agentquestion.StateAnswered {
		t.Fatalf("first state = %s, want answered", record.State)
	}

	answerRealTmuxQuestionPicker(t, client, client.mark())
	if got := waitRealTmuxQuestionHook(t, secondDone); !strings.Contains(got, `"answers":{"Which branch?":"main","Which build tool?":"make"}`) {
		t.Fatalf("second decision = %q, want the answer from its own popup", got)
	}
	if record, _, _ := fixture.store.Get(second); record.State != agentquestion.StateAnswered {
		t.Fatalf("second state = %s, want answered", record.State)
	}
}
