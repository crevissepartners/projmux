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
