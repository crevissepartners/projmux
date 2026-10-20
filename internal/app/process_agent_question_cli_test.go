package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
)

// Initial create payload is the only input trigger. All provider processes,
// configuration, Registry and socket paths belong to these protocol fixtures.
func TestExactProcessQuestionsCreatePayloadActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied product binary required")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, annotation := range []string{"", coremetadata.QuestionChannelOn, "off"} {
			t.Run(provider+"/"+annotation, func(t *testing.T) {
				var f processCreateCLI
				var args []string
				if provider == aiModeClaude {
					f = newProcessCreateCLI(t)
					args = f.args("--", "question")
				}
				if provider == aiModeCodex {
					cf := newProcessCodexCreateCLI(t)
					f = cf.processCreateCLI
					args = cf.args("--", "controls")
				}
				paths, _ := config.DefaultPathsFromEnv()
				if err := config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), config.AgentQuestionAnsweringClaude); err != nil {
					t.Fatal(err)
				}
				// Emit three distinct requests in one live turn, on each exact answer.
				scriptPath := filepath.Join(f.root, "provider.py")
				if provider == aiModeCodex {
					scriptPath = filepath.Join(f.root, "codex-provider.py")
				}
				raw, err := os.ReadFile(scriptPath)
				if err != nil {
					t.Fatal(err)
				}
				script := string(raw)
				if provider == aiModeClaude {
					script = strings.Replace(script, "emit({'type':'result','subtype':'success','session_id':'process-session'})\n elif frame['type']=='control_request':", "if request_count<3:\n   request_count+=1;request['request_id']='question-'+str(request_count);emit(request)\n  else:emit({'type':'result','subtype':'success','session_id':'process-session'})\n elif frame['type']=='control_request':", 1)
				} else {
					script = strings.Replace(script, "'question':'Color?','options'", "'question':'Color?','isOther':True,'options'", 1)
					script = strings.Replace(script, "if responses==2:complete()", "if 'answers' in n['result'] and responses<3:\n   q['id']='next-'+str(responses);emit(q)", 1)
				}
				if err = os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, f.binary, args...)
				in, _ := cmd.StdinPipe()
				out, _ := cmd.StdoutPipe()
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
				line, err := bufio.NewReader(out).ReadString('\n')
				if err != nil {
					t.Fatal(err, stderr.String())
				}
				uid := strings.TrimPrefix(strings.Fields(line)[1], "uid:")
				if _, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					a, _ := reg.Agent(uid)
					if annotation != "" {
						if a.Metadata.Annotations == nil {
							a.Metadata.Annotations = map[string]string{}
						}
						a.Metadata.Annotations[coremetadata.AnnotationAgentQuestionChannel] = annotation
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				d := ProcessQuestionDispatcher{RegistryPath: f.store.Path()}
				previous := ""
				for i, want := range []string{"process-exact-question", "held-question", "process-exact-question"} {
					var views []ExactProcessQuestion
					processCLIUntil(t, ctx, func() bool {
						views, err = d.ReadExactProcessQuestions(ctx, uid)
						return err == nil && len(views) == 1 && views[0].QuestionID != previous
					})
					v := views[0]
					if v.Delivery != want || !v.CanAnswer || len(v.Prompts) != 1 || (v.Deadline == nil) != (i != 1) {
						t.Fatalf("delivery %+v", v)
					}
					mode := config.AgentQuestionAnsweringProjmux
					if i == 1 {
						mode = config.AgentQuestionAnsweringClaude
					}
					if err = config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), mode); err != nil {
						t.Fatal(err)
					}
					unchanged, err := d.ReadExactProcessQuestions(ctx, uid)
					if err != nil || len(unchanged) != 1 || !reflect.DeepEqual(v, unchanged[0]) {
						t.Fatal("setting switched existing request", err)
					}
					for _, action := range []string{"enable", "disable"} {
						beforeRaw, _ := os.ReadFile(f.store.Path())
						globalBefore, _ := os.ReadFile(paths.AgentQuestionAnsweringFile())
						storeBefore, _ := os.ReadFile(agentquestion.NewStore(paths.StateDir).Path())
						before, _ := f.store.LoadReadOnly()
						answer, err := exec.CommandContext(ctx, f.binary, "agent", "question", action, "uid:"+uid).CombinedOutput()
						afterRaw, _ := os.ReadFile(f.store.Path())
						globalAfter, _ := os.ReadFile(paths.AgentQuestionAnsweringFile())
						storeAfter, _ := os.ReadFile(agentquestion.NewStore(paths.StateDir).Path())
						after, _ := f.store.LoadReadOnly()
						if !bytes.Equal(beforeRaw, afterRaw) {
							var left, right any
							_ = json.Unmarshal(beforeRaw, &left)
							_ = json.Unmarshal(afterRaw, &right)
							var fields []string
							var delta func(string, any, any)
							delta = func(path string, a, b any) {
								if reflect.DeepEqual(a, b) {
									return
								}
								if m, ok := a.(map[string]any); ok {
									if n, ok := b.(map[string]any); ok {
										for k, v := range m {
											delta(path+"."+k, v, n[k])
										}
										for k := range n {
											if _, ok := m[k]; !ok {
												fields = append(fields, path+"."+k)
											}
										}
										return
									}
								}
								if m, ok := a.([]any); ok {
									if n, ok := b.([]any); ok && len(m) == len(n) {
										for i := range m {
											delta(fmt.Sprintf("%s[%d]", path, i), m[i], n[i])
										}
										return
									}
								}
								fields = append(fields, path)
							}
							delta("Registry", left, right)
							t.Logf("host concurrent Registry delta fields: %v", fields)
						}
						if !bytes.Equal(globalBefore, globalAfter) || !bytes.Equal(storeBefore, storeAfter) {
							t.Fatal("legacy policy/store mutation")
						}
						beforeAgent, _ := before.Agent(uid)
						afterAgent, _ := after.Agent(uid)
						if err != nil || !reflect.DeepEqual(beforeAgent.Metadata, afterAgent.Metadata) || !bytes.Contains(answer, []byte("deprecated; no effect")) {
							t.Fatal("legacy mutation", err)
						}
					}
					selection := agentquestion.Selection{Labels: []string{"blue"}}
					if i == 2 {
						selection = agentquestion.Selection{Text: "custom answer", HasText: true}
					}
					if err = d.AnswerExactProcessQuestion(ctx, uid, v.QuestionID, map[int]agentquestion.Selection{0: selection}); err != nil {
						t.Fatal(err)
					}
					if err = d.AnswerExactProcessQuestion(ctx, uid, v.QuestionID, map[int]agentquestion.Selection{0: {Labels: []string{"red"}}}); err == nil {
						t.Fatal("duplicate accepted")
					}
					previous = v.QuestionID
				}
				records, err := agentquestion.NewStore(paths.StateDir).List(uid)
				if err != nil || len(records) != 1 || records[0].Disposition != "answered-direct" || len(records[0].Answers) != 0 {
					t.Fatal("redacted held completion missing", records, err)
				}
				wire, _ := os.ReadFile(f.trace)
				if bytes.Count(wire, []byte("control_response"))+bytes.Count(wire, []byte(`"answers"`)) < 3 {
					t.Fatal("missing actual responses")
				}
			})
		}
	}
}

func TestExactProcessSecretTypedActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied product binary required")
	}
	for _, mode := range []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux} {
		t.Run(string(mode), func(t *testing.T) {
			f := newProcessCodexCreateCLI(t)
			paths, _ := config.DefaultPathsFromEnv()
			if err := config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), mode); err != nil {
				t.Fatal(err)
			}
			scriptPath := filepath.Join(f.root, "codex-provider.py")
			raw, _ := os.ReadFile(scriptPath)
			script := string(raw)
			script = strings.Replace(script, "with open(log,'a') as f:f.write(line)", "with open(log,'a') as f:f.write('[redacted answer]\\n' if 'answers' in json.loads(line).get('result',{}) else line)", 1)
			script = strings.Replace(script, "'question':'Color?','options':[{'label':'blue'},{'label':'red'}]", "'question':'Secret?','isSecret':True,'isOther':True,'options':[]", 1)
			script = strings.Replace(script, "responses+=1", "responses+=1\n  if 'answers' in n['result']:open(os.path.join(os.environ['HOME'],'secret-received'),'w').write(str(n['result']['answers']['q']['answers']==['private-fixture-value']))", 1)
			if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, f.binary, f.args("--", "controls")...)
			in, _ := cmd.StdinPipe()
			out, _ := cmd.StdoutPipe()
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			line, err := bufio.NewReader(out).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			uid := strings.TrimPrefix(strings.Fields(line)[1], "uid:")
			d := ProcessQuestionDispatcher{RegistryPath: f.store.Path()}
			var views []ExactProcessQuestion
			processCLIUntil(t, ctx, func() bool { views, err = d.ReadExactProcessQuestions(ctx, uid); return err == nil && len(views) == 1 })
			if !views[0].Prompts[0].IsSecret {
				t.Fatal("secret flag missing")
			}
			refused, err := exec.CommandContext(ctx, f.binary, "agent", "question", "answer", "uid:"+uid, views[0].QuestionID, "--text", "1=refused").CombinedOutput()
			if err == nil || !bytes.Contains(refused, []byte("question-secret-native-only")) {
				t.Fatal("secret argv admitted")
			}
			if err = d.AnswerExactProcessQuestion(ctx, uid, views[0].QuestionID, map[int]agentquestion.Selection{0: {Text: "private-fixture-value", HasText: true}}); err != nil {
				t.Fatal(err)
			}
			processCLIUntil(t, ctx, func() bool {
				raw, _ := os.ReadFile(filepath.Join(f.root, "secret-received"))
				return string(raw) == "True"
			})
			for _, path := range []string{f.trace, f.store.Path(), agentquestion.NewStore(paths.StateDir).Path()} {
				raw, _ := os.ReadFile(path)
				if bytes.Contains(raw, []byte("private-fixture-value")) {
					t.Fatal("secret persisted", path)
				}
			}
			if bytes.Contains(stderr.Bytes(), []byte("private-fixture-value")) {
				t.Fatal("secret stderr")
			}
			records, _ := agentquestion.NewStore(paths.StateDir).List(uid)
			if mode == config.AgentQuestionAnsweringProjmux && (len(records) != 1 || records[0].Disposition != "answered-direct" || len(records[0].Answers) != 0) {
				t.Fatal(fmt.Sprint(records))
			}
		})
	}
}

// The copied CLI and protocol fixture prove immediate return, unblocked peer
// delivery, durable expiry, and a single outbox owner. This is not an actual
// provider acceptance run.
func TestCLIAsyncQuestionActualCLIIsolated(t *testing.T) {
	f := newProcessCreateCLI(t)
	// This fixture completes each peer-triggered turn. The shared default
	// fixture deliberately opens a synchronous AskUserQuestion on every peer;
	// that target hold is covered separately and would block this result test.
	providerPath := filepath.Join(f.root, "provider.py")
	provider, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	provider = bytes.Replace(provider, []byte("emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()"), []byte("emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close();emit({'type':'result','subtype':'success','session_id':'process-session'});continue"), 1)
	if err = os.WriteFile(providerPath, provider, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, f.args("--", "initial task")...)
	input, _ := cmd.StdinPipe()
	output, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, e := bufio.NewReader(output).ReadString('\n')
	if e != nil {
		t.Fatal(e, stderr.String())
	}
	ref := strings.Fields(line)[1]
	uid := strings.TrimPrefix(ref, "uid:")
	paths, _ := config.DefaultPathsFromEnv()
	st := agentquestion.NewStore(paths.StateDir).CLIAsyncStore()
	// Seed a waiting record so the independently owned helper can be waited for
	// by this fixture; ask's short-lived duplicate must not create another timer.
	seed, e := st.Create(uid, json.RawMessage(`[{"question":"Owner seed?","options":[{"label":"yes"}]}]`), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	readyR, readyW, _ := os.Pipe()
	defer readyR.Close()
	owner := exec.CommandContext(ctx, f.binary, "internal", "agent-question-outbox")
	owner.ExtraFiles = []*os.File{readyW}
	owner.Env = questionOutboxEnvironment(os.Environ())
	var ownerErr bytes.Buffer
	owner.Stderr = &ownerErr
	if e = owner.Start(); e != nil {
		t.Fatal(e)
	}
	_ = readyW.Close()
	ownerWaited := false
	defer func() {
		if !ownerWaited {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	}()
	var ready [1]byte
	if _, e = readyR.Read(ready[:]); e != nil || ready[0] != '1' {
		t.Fatal(e)
	}
	// A waiting seed keeps this owner live for the Linux/macOS full-argv
	// discovery control. Missing/truncated ps output must not mean absence.
	ownerIdentity, _, e := localipc.Process(owner.Process.Pid)
	if e != nil {
		t.Fatal(e)
	}
	initial, e := asyncQuestionOutboxIdentities(ctx, f.binary)
	if e != nil {
		t.Fatal(e)
	}
	ownerFound := false
	for _, identity := range initial {
		ownerFound = ownerFound || identity == ownerIdentity
	}
	if !ownerFound {
		t.Fatal("ready outbox owner missing from full-argv discovery", ownerIdentity)
	}
	t.Logf("ready outbox full-argv control identity=%+v", ownerIdentity)
	ask := exec.CommandContext(ctx, f.binary, "agent", "question", "ask", ref, "--questions", `[{"question":"Continue work?","options":[{"label":"yes (Recommended)"}]}]`, "--expires-in", "10s", "-o", "json")
	raw, e := ask.CombinedOutput()
	if e != nil {
		t.Fatal(string(raw), e)
	}
	var asked agentquestion.Record
	if e = json.Unmarshal(raw, &asked); e != nil || asked.Delivery != "async-message" {
		t.Fatalf("ask %s %v", raw, e)
	}
	// Peer delivery proceeds while the question is waiting, with no answer.
	var peer []byte
	processCLIUntil(t, ctx, func() bool {
		peer, e = exec.CommandContext(ctx, f.binary, "agent", "message", "send", ref, "--source", ref, "--message-ref", "message-open-question-peer", "--", "peer while question open").CombinedOutput()
		return e == nil
	})
	processCLIUntil(t, ctx, func() bool {
		peer, e = exec.CommandContext(ctx, f.binary, "agent", "message", "status", "message-open-question-peer", "-o", "json").CombinedOutput()
		return e == nil && bytes.Contains(peer, []byte(`"state":"delivered"`))
	})
	if waiting, _, e := st.Get(asked.ID); e != nil || waiting.State != agentquestion.StateWaiting {
		t.Fatal("peer did not arrive during open question", waiting, e)
	}
	expired := false
	defer func() {
		if t.Failed() {
			r, _, err := st.Get(asked.ID)
			t.Logf("expiry record=%+v error=%v owner=%s provider=%s", r, err, ownerErr.String(), stderr.String())
		}
	}()
	processCLIUntil(t, ctx, func() bool {
		r, found, e := st.Get(asked.ID)
		expired = found && r.State == agentquestion.StateExpired && r.ResultReason == "expired" && r.Outcome == "delivered"
		return e == nil && expired
	})
	declineRaw, err := exec.CommandContext(ctx, f.binary, "agent", "question", "ask", ref, "--questions", `[{"question":"Decline this?","options":[{"label":"yes"}]}]`).CombinedOutput()
	if err != nil {
		t.Fatal(string(declineRaw), err)
	}
	declined := agentquestion.Record{ID: strings.TrimSpace(string(declineRaw))}
	if !agentquestion.ValidID(declined.ID) {
		t.Fatalf("plain ask output is not one ID: %q", declineRaw)
	}
	saved, found, err := st.Get(declined.ID)
	if err != nil || !found || saved.Deadline.Sub(saved.CreatedAt) != 24*time.Hour {
		t.Fatal("default lifetime", saved, err)
	}
	if raw, e = exec.CommandContext(ctx, f.binary, "agent", "question", "answer", ref, seed.ID, "--index", "1=1").CombinedOutput(); e != nil {
		t.Fatal(string(raw), e)
	}
	if raw, e = exec.CommandContext(ctx, f.binary, "agent", "question", "decline", ref, declined.ID).CombinedOutput(); e != nil {
		t.Fatal(string(raw), e)
	}
	if e = owner.Wait(); e != nil {
		ownerWaited = true
		t.Fatal(e, ownerErr.String())
	}
	ownerWaited = true
	// Close the last spawn-capable caller, including its return-time recovery.
	// All short-lived question commands have also actually Waited; each helper
	// launch returns only after admission. No new launch remains behind this
	// fixture boundary. Non-owned helpers below are observed, never Waited.
	_ = input.Close()
	if e = cmd.Wait(); e != nil {
		waited = true
		t.Fatal(e, stderr.String())
	}
	waited = true
	late := asyncQuestionLateHolder(t, paths.StateDir, st.Path())
	known := append([]coremetadata.ProcessIdentity(nil), initial...)
	if late != nil {
		known = append(known, *late)
	}
	release, owned, e := asyncQuestionWaitOutboxRetirement(t, ctx, st, f.binary, known, late)
	if e != nil || !owned {
		t.Fatalf("owner leak %v %v", owned, e)
	}
	release()
	records, e := st.List(uid)
	if e != nil || len(records) != 3 {
		t.Fatal(records, e)
	}
	wire, err := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{seed.ID, asked.ID, declined.ID, "answered", "expired", "declined"} {
		if !bytes.Contains(wire, []byte(text)) {
			t.Fatalf("provider fixture did not receive result %q", text)
		}
	}
	// Decode both JSON string layers before checking the seed's compact answer.
	// The peer envelope remains a string inside the provider's message frame.
	seedAnswers := 0
	for line := range bytes.SplitSeq(bytes.TrimSpace(wire), []byte("\n")) {
		var frame struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(frame.Message.Content), &envelope); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(envelope.Payload, "{") {
			if envelope.Payload != "peer while question open" {
				t.Fatalf("provider fixture received unexpected plain peer payload: %q", envelope.Payload)
			}
			continue
		}
		var result struct {
			QuestionID string            `json:"questionId"`
			Answers    map[string]string `json:"answers"`
		}
		if err := json.Unmarshal([]byte(envelope.Payload), &result); err != nil {
			t.Fatal(err)
		}
		if result.QuestionID == seed.ID {
			if len(result.Answers) != 1 || result.Answers["1"] != "yes" {
				t.Fatalf("provider fixture received wrong compact seed answer: %+v", result)
			}
			seedAnswers++
		}
	}
	if seedAnswers != 1 {
		t.Fatalf("provider fixture received %d compact seed answers, want exactly one", seedAnswers)
	}

	for _, r := range records {
		if r.Pending() {
			t.Fatal("unsettled fixture outbox", r)
		}
	}
}

type asyncQuestionHolderReady struct {
	Identity  coremetadata.ProcessIdentity `json:"identity"`
	StorePath string                       `json:"storePath"`
	LockPath  string                       `json:"lockPath"`
}

// This controlled holder models the admitted late helper's lock interval. It
// does not prove that any historical CI failure followed this exact sequence.
func TestCLIAsyncQuestionLateOwnerRetirement(t *testing.T) {
	if root := os.Getenv("PMX_TEST_QUESTION_HOLDER"); root != "" {
		// TestMain restores liveguard's default XDG paths on every re-exec.
		// Use the explicit fixture input instead of those default locations.
		stateDir := os.Getenv("PMX_TEST_QUESTION_STATE_DIR")
		if stateDir == "" || !filepath.IsAbs(stateDir) {
			t.Fatal("controlled holder requires an absolute fixture state directory")
		}
		store := agentquestion.NewStore(stateDir).CLIAsyncStore()
		release, owned, err := store.LockOwner()
		if err != nil || !owned {
			t.Fatalf("late holder lock %v %v", owned, err)
		}
		held := true
		defer func() {
			if held {
				release()
			}
		}()
		identity, _, err := localipc.Process(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(asyncQuestionHolderReady{Identity: identity, StorePath: store.Path(), LockPath: store.Path() + ".owner"})
		if err != nil {
			t.Fatal(err)
		}
		asyncQuestionMarker(t, root, "ready", raw)
		ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
		defer cancel()
		processCLIUntil(t, ctx, func() bool {
			_, err := os.Stat(filepath.Join(root, "release"))
			return err == nil
		})
		release()
		held = false
		asyncQuestionMarker(t, root, "unlocked", nil)
		processCLIUntil(t, ctx, func() bool {
			_, err := os.Stat(filepath.Join(root, "retire"))
			return err == nil
		})
		return
	}
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied product binary required")
	}
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprintf("stuck=%v", stuck), func(t *testing.T) {
			root := t.TempDir()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run=^TestCLIAsyncQuestionActualCLIIsolated$", "-test.v")
			child.WaitDelay = time.Second
			child.Env = append(os.Environ(), "PMX_TEST_QUESTION_RETIREMENT="+root)
			type result struct {
				out []byte
				err error
			}
			done := make(chan result, 1)
			go func() {
				out, err := child.CombinedOutput() // actual Wait of the original body
				done <- result{out, err}
			}()
			waited := false
			// Run before defer cancel: a parent Fatal must release the fixture
			// barriers and consume the one actual Wait result, too.
			defer func() {
				if !waited {
					for _, name := range []string{"release", "retire"} {
						if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
							t.Errorf("parent cleanup %s: %v", name, err)
						}
					}
					select {
					case r := <-done:
						waited = true
						t.Logf("parent cleanup original body actual Wait=%v\n%s", r.err, r.out)
					case <-time.After(5 * time.Second):
						cancel()
						select {
						case r := <-done:
							waited = true
							t.Errorf("parent cleanup required cancellation; actual Wait=%v\n%s", r.err, r.out)
						case <-time.After(5 * time.Second):
							t.Error("parent cleanup original body actual Wait not observed")
						}
					}
				}
				// This grandchild is not ours to Wait. Observe its recorded birth
				// retiring in the kernel; never turn a read error into absence.
				raw, err := os.ReadFile(filepath.Join(root, "ready"))
				if os.IsNotExist(err) {
					t.Log("holder identity was not published; no grandchild absence claim")
					return
				}
				var ready asyncQuestionHolderReady
				decodeErr := json.Unmarshal(raw, &ready)
				identity := ready.Identity
				if err != nil || decodeErr != nil || identity.PID <= 0 || identity.Start == "" {
					t.Errorf("parent cleanup holder identity unreadable: %v", err)
					return
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					current, _, err := localipc.Process(identity.PID)
					if err == localipc.ErrProcessAbsent || err == nil && current != identity {
						t.Logf("parent cleanup holder kernel retirement=%+v (not Wait)", identity)
						return
					}
					if err != nil || !time.Now().Before(deadline) {
						t.Errorf("parent cleanup holder retirement unproven: identity=%+v current=%+v error=%v", identity, current, err)
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			observed := false
			for {
				select {
				case r := <-done:
					waited = true
					if raw, err := os.ReadFile(filepath.Join(root, "observed")); err == nil {
						observed = string(raw) == "false <nil>"
					}
					// The child also performs release/Wait on its owned holder.
					t.Logf("original body actual Wait=%v\n%s", r.err, r.out)
					if !observed || !bytes.Contains(r.out, []byte("late holder identity=")) {
						t.Fatal("late lock ordering was not observed")
					}
					if stuck {
						if r.err == nil || !bytes.Contains(r.out, []byte("owner leak false <nil>")) {
							t.Fatal("persistent owner lock was not rejected")
						}
					} else {
						if r.err != nil {
							t.Fatal("late owner retirement rejected by original body")
						}
						if _, err := os.Stat(filepath.Join(root, "retirement-blocked")); err != nil {
							t.Fatal("lock release alone bypassed helper retirement")
						}
					}
					return
				case <-time.After(10 * time.Millisecond):
					if raw, err := os.ReadFile(filepath.Join(root, "observed")); err == nil {
						observed = string(raw) == "false <nil>"
						if !stuck {
							asyncQuestionMarker(t, root, "release", nil)
							// The holder then stays alive without the lock. Only an
							// observed retirement barrier may let it exit.
							if _, err := os.Stat(filepath.Join(root, "retirement-blocked")); err == nil {
								asyncQuestionMarker(t, root, "retire", nil)
							}
						}
					}
				case <-ctx.Done():
					t.Fatal("original body did not finish before parent deadline")
				}
			}
		})
	}
}

func asyncQuestionMarker(t *testing.T, root, name string, data []byte) {
	t.Helper()
	file, err := os.CreateTemp(root, name+"-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file.Name(), filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
}

func asyncQuestionLateHolder(t *testing.T, stateDir, storePath string) *coremetadata.ProcessIdentity {
	t.Helper()
	root := os.Getenv("PMX_TEST_QUESTION_RETIREMENT")
	if root == "" {
		return nil
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestCLIAsyncQuestionLateOwnerRetirement$", "-test.v")
	if stateDir == "" || !filepath.IsAbs(stateDir) || !filepath.IsAbs(storePath) {
		t.Fatal("late holder requires absolute fixture state/store paths")
	}
	cmd.Env = append(os.Environ(), "PMX_TEST_QUESTION_HOLDER="+root, "PMX_TEST_QUESTION_STATE_DIR="+stateDir)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Own this child, release it on all failure paths, and actually reap it.
	t.Cleanup(func() {
		for _, name := range []string{"release", "retire"} {
			if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
				t.Errorf("controlled holder cleanup %s: %v", name, err)
			}
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			t.Logf("controlled holder actual Wait=%v\n%s", err, output.String())
			if err != nil {
				t.Error("controlled holder failed")
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			err := <-done
			t.Errorf("controlled holder cleanup required kill; actual Wait=%v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ready asyncQuestionHolderReady
	processCLIUntil(t, ctx, func() bool {
		raw, err := os.ReadFile(filepath.Join(root, "ready"))
		return err == nil && json.Unmarshal(raw, &ready) == nil
	})
	if ready.StorePath != storePath || ready.LockPath != storePath+".owner" {
		t.Fatalf("late holder namespace mismatch: ready=%+v expected store=%s lock=%s", ready, storePath, storePath+".owner")
	}
	identity := ready.Identity
	current, _, err := localipc.Process(cmd.Process.Pid)
	if err != nil || current != identity || identity.PID != cmd.Process.Pid {
		t.Fatalf("late holder identity mismatch %+v %+v %v", identity, current, err)
	}
	t.Logf("late holder identity=%+v store=%s lock=%s after first owner actual Wait", identity, ready.StorePath, ready.LockPath)
	return &identity
}

func asyncQuestionLockObserved(t *testing.T, owned bool, err error) {
	t.Helper()
	if root := os.Getenv("PMX_TEST_QUESTION_RETIREMENT"); root != "" && !owned && err == nil {
		asyncQuestionMarker(t, root, "observed", fmt.Appendf(nil, "%v %v", owned, err))
	}
}

// asyncQuestionOutboxPIDs reads the same wide command column on Linux/macOS.
// The live seed-owner control verifies the fixed full argv on the host. This
// inventory is scoped to a unique copied binary and closed launch boundary;
// it makes no claim about unrelated helpers or their historical existence.
func asyncQuestionOutboxPIDs(ctx context.Context, binary string) (map[int]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ps", "-axww", "-o", "pid=", "-o", "command=")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("outbox full-argv inventory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf("outbox full-argv inventory empty or incomplete")
	}
	expected := binary + " internal agent-question-outbox"
	found := map[int]bool{}
	for row := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		row = strings.TrimSpace(row)
		fields := strings.Fields(row)
		if len(fields) < 2 {
			return nil, fmt.Errorf("outbox inventory row lacks PID/command")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("outbox inventory PID invalid")
		}
		command := strings.TrimSpace(row[len(fields[0]):])
		if command == expected {
			if found[pid] {
				return nil, fmt.Errorf("outbox inventory duplicate PID %d", pid)
			}
			found[pid] = true
		} else if strings.HasPrefix(expected, command) || strings.HasPrefix(command, expected) {
			return nil, fmt.Errorf("outbox argv incomplete or ambiguous for PID %d", pid)
		}
	}
	return found, nil
}

// Confirm argv around kernel birth reads. A terminal/zombie identity reported
// absent by localipc is kernel inactivity, not PID removal or an actual Wait.
func asyncQuestionOutboxIdentities(ctx context.Context, binary string) ([]coremetadata.ProcessIdentity, error) {
	for {
		first, err := asyncQuestionOutboxPIDs(ctx, binary)
		if err != nil {
			return nil, err
		}
		identities := map[int]coremetadata.ProcessIdentity{}
		for pid := range first {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			identity, _, err := localipc.Process(pid)
			if err == localipc.ErrProcessAbsent {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("outbox identity PID %d: %w", pid, err)
			}
			identities[pid] = identity
		}
		second, err := asyncQuestionOutboxPIDs(ctx, binary)
		if err != nil {
			return nil, err
		}
		changed := false
		for pid := range second {
			changed = changed || !first[pid]
		}
		if changed {
			continue // discover the new candidate within the same original ctx
		}
		var result []coremetadata.ProcessIdentity
		for pid, identity := range identities {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			current, _, err := localipc.Process(pid)
			if err == localipc.ErrProcessAbsent {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("outbox identity recheck PID %d: %w", pid, err)
			}
			if current != identity || !second[pid] {
				return nil, fmt.Errorf("outbox argv/birth changed for PID %d", pid)
			}
			result = append(result, identity)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return result, nil
	}
}

func asyncQuestionIdentityActive(ctx context.Context, identity coremetadata.ProcessIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	current, _, err := localipc.Process(identity.PID)
	if ended := ctx.Err(); ended != nil {
		return false, ended
	}
	if err == localipc.ErrProcessAbsent {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return current == identity, nil
}

func asyncQuestionWaitOutboxRetirement(t *testing.T, ctx context.Context, st *agentquestion.CLIAsyncStore, binary string, known []coremetadata.ProcessIdentity, late *coremetadata.ProcessIdentity) (func(), bool, error) {
	t.Helper()
	identities, err := asyncQuestionOutboxIdentities(ctx, binary)
	if err != nil {
		return nil, false, err
	}
	known = append(known, identities...)
	t.Logf("closed launch boundary outbox identities=%+v; known=%+v", identities, known)
	active := func() (bool, error) {
		for _, identity := range known {
			alive, err := asyncQuestionIdentityActive(ctx, identity)
			if err != nil || alive {
				return alive, err
			}
		}
		return false, nil
	}
	deadline := func() (func(), bool, error) {
		t.Logf("outbox retirement deadline: %v; known=%+v", ctx.Err(), known)
		return nil, false, nil // preserve the original owner leak false,nil FAIL
	}
	for {
		if ctx.Err() != nil {
			return deadline()
		}
		release, owned, err := st.LockOwner()
		asyncQuestionLockObserved(t, owned, err)
		if err != nil {
			return nil, false, err
		}
		if owned {
			alive, err := active()
			if err != nil {
				release()
				if ctx.Err() != nil {
					return deadline()
				}
				return nil, false, err
			}
			if !alive {
				// A completion candidate must pass one more full discovery. Add
				// newly seen identities and wait again instead of assuming empty.
				latest, err := asyncQuestionOutboxIdentities(ctx, binary)
				if err != nil {
					release()
					if ctx.Err() != nil {
						return deadline()
					}
					return nil, false, err
				}
				for _, identity := range latest {
					found := false
					for _, previous := range known {
						found = found || identity == previous
					}
					if !found {
						known = append(known, identity)
						t.Logf("final scan discovered outbox identity=%+v", identity)
					}
				}
				alive, err = active()
				if err != nil || ctx.Err() != nil {
					release()
					if ctx.Err() != nil {
						return deadline()
					}
					return nil, false, err
				}
				if !alive {
					t.Logf("known outbox identities kernel-inactive (not Wait): %+v", known)
					return release, true, nil
				}
			}
			retirementBlocked := false
			if late != nil {
				alive, err := asyncQuestionIdentityActive(ctx, *late)
				if err != nil {
					release()
					if ctx.Err() != nil {
						return deadline()
					}
					return nil, false, err
				}
				retirementBlocked = alive
			}
			release()
			if retirementBlocked {
				// The probe succeeded while this exact holder was alive. Release
				// before publishing: even a marker Fatal must not retain our lock.
				asyncQuestionMarker(t, os.Getenv("PMX_TEST_QUESTION_RETIREMENT"), "retirement-blocked", nil)
			}
		}
		select {
		case <-ctx.Done():
			return deadline()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// This subprocess freezes the pre-compact owner's strict ref check. It is a
// protocol-behavior fixture, not an actual previously released binary.
func TestCLIAsyncQuestionOwnerUpgrade(t *testing.T) {
	if stateDir := os.Getenv("PMX_TEST_QUESTION_OLD_OWNER"); stateDir != "" {
		st := agentquestion.NewStore(stateDir).CLIAsyncStore()
		release, owned, err := st.LockOwner()
		if err != nil || !owned {
			t.Fatal("legacy primary admission", owned, err)
		}
		defer release()
		ready := os.NewFile(3, "legacy-ready")
		if _, err = ready.Write([]byte("1")); err != nil {
			t.Fatal(err)
		}
		_ = ready.Close()
		var barrier [1]byte
		if _, err = io.ReadFull(os.Stdin, barrier[:]); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(st.Path())
		if err != nil {
			t.Fatal(err)
		}
		var disk struct {
			Records []agentquestion.CLIAsyncRecord `json:"records"`
		}
		if err = json.Unmarshal(raw, &disk); err != nil {
			t.Fatal(err)
		}
		for _, r := range disk.Records {
			for i, p := range r.Parts {
				ref := fmt.Sprintf("message-%s-%d", r.ID, i+1)
				if p.Attempt != 0 {
					ref += fmt.Sprintf("-retry-%d", p.Attempt)
				}
				if p.MessageRef != ref {
					t.Fatalf("frozen legacy reader: %v", agentquestion.ErrMalformedStore)
				}
			}
		}
		t.Fatal("frozen legacy reader unexpectedly accepted compact result")
	}
	f := newProcessCreateCLI(t)
	providerPath := filepath.Join(f.root, "provider.py")
	provider, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	provider = bytes.Replace(provider, []byte("emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()"), []byte("emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close();emit({'type':'result','subtype':'success','session_id':'process-session'});continue"), 1)
	if err = os.WriteFile(providerPath, provider, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	agent := exec.CommandContext(ctx, f.binary, f.args("--", "initial task")...)
	input, err := agent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := agent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var agentErr bytes.Buffer
	agent.Stderr = &agentErr
	if err = agent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = agent.Process.Kill(); _ = agent.Wait() }()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err, agentErr.String())
	}
	ref := strings.Fields(line)[1]
	uid := strings.TrimPrefix(ref, "uid:")
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	st := agentquestion.NewStore(paths.StateDir).CLIAsyncStore()
	// Existing unknown/accepted parts are independent of the new question.
	raw, err := os.ReadFile("../integrations/agents/agentquestion/testdata/cli_async_legacy_results.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen []struct {
		Record agentquestion.CLIAsyncRecord
		Bodies []string
	}
	if err = json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	uncertain := frozen[0].Record
	uncertain.AgentUID = uid
	uncertain.CreatedAt = now.Add(-time.Minute)
	uncertain.UpdatedAt = now
	uncertain.Deadline = now.Add(time.Hour)
	uncertain.ResultDeadline = now.Add(24 * time.Hour)
	for i := range uncertain.Parts {
		var b bytes.Buffer
		if err = json.Compact(&b, uncertain.Parts[i].Receipt); err != nil {
			t.Fatal(err)
		}
		uncertain.Parts[i].Receipt = append(json.RawMessage(nil), b.Bytes()...)
	}
	accepted := uncertain
	accepted.ID = "question-0000000000000003"
	accepted.Outcome = "pending"
	accepted.Parts = []agentquestion.CLIAsyncPart{{MessageRef: "message-" + accepted.ID + "-1", State: "accepted"}}
	bodies, err := accepted.Messages()
	if err != nil {
		t.Fatal(err)
	}
	var route message.Route
	var lastRouteReason string
	defer func() {
		if t.Failed() {
			t.Logf("upgrade route preparation: %s", lastRouteReason)
		}
	}()
	processCLIUntil(t, ctx, func() bool {
		reg, err := f.store.Load()
		if err != nil {
			return false
		}
		// The public metadata resolver requires a tmux runtime. This process
		// fixture needs the same live registration proof as process peer sends.
		resolved, reason := resolveClaudePeerRoute(intmetadata.PathFor(paths.StateDir), reg, uid)
		lastRouteReason = reason
		if reason != "" {
			return false
		}
		route = publicMessageRoute(resolved)
		return route.Incarnation != ""
	})
	envelope := message.Envelope{Version: message.Version, MessageRef: accepted.Parts[0].MessageRef, ConversationRef: "conversation-upgrade-accepted", Source: route, Target: route, Authority: message.PeerAuthority(), Payload: bodies[0], AcceptedAt: time.Now().UTC(), Deadline: accepted.ResultDeadline}
	messages := messagestore.NewStore(paths.StateDir)
	canonical, _, err := messages.PutAccepted(envelope, "claude-coordination")
	if err != nil {
		t.Fatal(err)
	}
	accepted.Parts[0].Receipt = questionResultReceipt(agentmessages.ReceiptFor(canonical), "")
	disk, _ := json.Marshal(struct {
		Version int                            `json:"version"`
		Records []agentquestion.CLIAsyncRecord `json:"records"`
	}{1, []agentquestion.CLIAsyncRecord{uncertain, accepted}})
	if err = os.MkdirAll(filepath.Dir(st.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(st.Path(), disk, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rs, _ := st.List("")
		for _, r := range rs {
			_ = st.TargetDeleted(r.ID)
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		release, owned, err := asyncQuestionWaitOutboxRetirement(t, cleanup, st, f.binary, nil, nil)
		if err != nil || !owned {
			t.Errorf("upgrade fixture owner retirement: %v %v", owned, err)
		} else {
			release()
		}
	})
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := exec.CommandContext(ctx, self, "-test.run=^TestCLIAsyncQuestionOwnerUpgrade$", "-test.v")
	old.Env = append(os.Environ(), "PMX_TEST_QUESTION_OLD_OWNER="+paths.StateDir)
	barrier, err := old.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyR.Close()
	old.ExtraFiles = []*os.File{readyW}
	var oldOut bytes.Buffer
	old.Stdout = &oldOut
	old.Stderr = &oldOut
	if err = old.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyW.Close()
	oldWaited := false
	defer func() {
		_ = barrier.Close()
		if !oldWaited {
			_ = old.Process.Kill()
			_ = old.Wait()
		}
	}()
	var ready [1]byte
	if _, err = io.ReadFull(readyR, ready[:]); err != nil || ready[0] != '1' {
		t.Fatal("legacy owner readiness", err)
	}
	seed, err := st.Create(uid, json.RawMessage(`[{"question":"Upgrade?","options":[{"label":"yes"}]}]`), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expiry, err := st.Create(uid, json.RawMessage(`[{"question":"Expire?","options":[{"label":"yes"}]}]`), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, f.binary, "agent", "question", "answer", ref, seed.ID, "--index", "1=1").CombinedOutput(); err != nil {
		t.Fatal("new Resolve/recovery", err, string(out))
	}
	// Additional recovery admissions must not leave additional waiting children.
	for range 3 {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.CommandContext(ctx, f.binary, "internal", "agent-question-outbox")
		child.Env = questionOutboxEnvironment(os.Environ())
		child.ExtraFiles = []*os.File{w}
		if err = child.Start(); err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
		_, readErr := io.ReadFull(r, ready[:])
		_ = r.Close()
		waitErr := child.Wait()
		if readErr != nil || waitErr != nil || ready[0] != '1' {
			t.Fatal("duplicate admission", readErr, waitErr)
		}
	}
	candidates, err := asyncQuestionOutboxIdentities(ctx, f.binary)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Errorf("compatible handoff candidate count=%d, want exactly one", len(candidates))
	}
	release, owned, err := st.LockOwner()
	if owned {
		release()
	}
	if err != nil || owned {
		t.Fatal("legacy primary released before barrier", owned, err)
	}
	if _, err = barrier.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	_ = barrier.Close()
	err = old.Wait()
	oldWaited = true
	if err == nil || !strings.Contains(oldOut.String(), "frozen legacy reader: "+agentquestion.ErrMalformedStore.Error()) {
		t.Fatal("old owner did not retire on strict read", err, oldOut.String())
	}
	// No CLI/recovery mutation after this barrier: only the admitted successor
	// can deliver the answer and expire the separate waiting card.
	deadline := time.Now().Add(15 * time.Second)
	for {
		answer, _, e1 := st.Get(seed.ID)
		expired, _, e2 := st.Get(expiry.ID)
		if e1 != nil || e2 != nil {
			t.Fatal(e1, e2)
		}
		if answer.Outcome == "delivered" && expired.State == agentquestion.StateExpired && expired.Outcome == "delivered" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("handoff did not continue delivery and expiry without recovery")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _, err := st.Get(uncertain.ID)
	if err != nil || got.Outcome != "unknown" || got.Parts[0].MessageRef != uncertain.Parts[0].MessageRef || !bytes.Equal(got.Parts[0].Receipt, uncertain.Parts[0].Receipt) {
		t.Fatal("upgrade changed unknown evidence", err)
	}
	got, _, err = st.Get(accepted.ID)
	if err != nil || got.Parts[0].State != "accepted" || got.Parts[0].MessageRef != accepted.Parts[0].MessageRef || !bytes.Equal(got.Parts[0].Receipt, accepted.Parts[0].Receipt) {
		t.Fatal("upgrade changed accepted evidence", err)
	}
	wire, err := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(seed.ID)) || !bytes.Contains(wire, []byte(expiry.ID)) || bytes.Contains(wire, []byte(uncertain.ID)) || bytes.Contains(wire, []byte(accepted.ID)) {
		t.Fatal("upgrade lost new result or replayed old evidence")
	}
	if _, _, err = messages.Apply(envelope.MessageRef, message.Event{Kind: message.EventDeliver, MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, Reason: "fixture-delivered", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
