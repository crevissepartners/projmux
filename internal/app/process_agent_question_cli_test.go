package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
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
	// Actual Wait of both owned processes, and released owner election, are the
	// cleanup evidence; no fixture daemon survives its private HOME.
	release, owned, e := st.LockOwner()
	if e != nil || !owned {
		t.Fatalf("owner leak %v %v", owned, e)
	}
	release()
	_ = input.Close()
	if e = cmd.Wait(); e != nil {
		waited = true
		t.Fatal(e, stderr.String())
	}
	waited = true
	records, e := st.List(uid)
	if e != nil || len(records) != 3 {
		t.Fatal(records, e)
	}
	wire, err := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{seed.ID, asked.ID, declined.ID, "Owner seed?", "answered", "expired", "declined"} {
		if !bytes.Contains(wire, []byte(text)) {
			t.Fatalf("provider fixture did not receive result %q", text)
		}
	}
	for _, r := range records {
		if r.Pending() {
			t.Fatal("unsettled fixture outbox", r)
		}
	}
}
