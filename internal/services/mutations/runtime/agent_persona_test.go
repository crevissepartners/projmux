package runtime

import (
 "errors"
 "fmt"
 "os"
 "reflect"
 "strings"
 "testing"

 "github.com/crevissepartners/projmux/internal/config"
 m "github.com/crevissepartners/projmux/internal/core/metadata"
 "github.com/crevissepartners/projmux/internal/core/persona"
)

// Each operation owns its Registry and snapshot directories. Execution callbacks
// model the state left by a stop, rather than treating its error as liveness.
func TestPersonaServiceSequenceAndRecovery(t *testing.T) {
 for _,tc:=range []struct{
  name,action string
  phase m.AgentPhase
  stopError,resumeError bool
  live AgentRestartLiveness
 }{
  {name:"attach-running",action:"attach",phase:m.PhaseRunning},
  {name:"detach-running",action:"detach",phase:m.PhaseRunning},
  {name:"attach-offline",action:"attach",phase:m.PhaseOffline},
  {name:"attach-failed",action:"attach",phase:m.PhaseFailed},
  {name:"stop-alive",action:"attach",phase:m.PhaseRunning,stopError:true,live:AgentRestartAlive},
  {name:"stop-unknown",action:"detach",phase:m.PhaseRunning,stopError:true,live:AgentRestartUnknown},
  {name:"stop-closed",action:"attach",phase:m.PhaseRunning,stopError:true,live:AgentRestartClosed},
  {name:"resume-failed",action:"attach",phase:m.PhaseRunning,resumeError:true},
  {name:"stop-closed-resume-failed",action:"detach",phase:m.PhaseRunning,stopError:true,live:AgentRestartClosed,resumeError:true},
 } {t.Run(tc.name,func(t *testing.T){
  storage:=persona.NewDefaultStore(config.Paths{ConfigDir:t.TempDir(),StateDir:t.TempDir()})
  content:=[]byte("Review the current change.\n")
  if _,err:=storage.Write("reviewer",content);err!=nil{t.Fatal(err)}
  old:=m.AgentPersonaAnnotations{Persona:"old",PersonaDigest:persona.Digest([]byte("old")),SystemPromptSnapshot:m.SystemPromptSnapshotOff,InstructionsSource:m.SettingSourceProfile}
  agent:=m.Agent{Metadata:m.ObjectMeta{UID:"agent",Name:"worker"}}
  agent.Spec.Provider="claude"
  agent.Status.Phase=tc.phase
  agent.Status.SessionRef=&m.AgentSessionRef{Provider:"claude",Claude:&m.ClaudeSessionRef{SessionID:"conversation"}}
  if tc.phase==m.PhaseRunning{agent.Status.PaneRef="old-pane"}
  reg:=m.Registry{Agents:[]m.Agent{agent},Panes:[]m.Pane{{Metadata:m.ObjectMeta{UID:"old-pane",Name:"custom-pane",OwnerRef:&m.OwnerRef{Kind:m.KindAgent,UID:"agent"}}}}}
  mut:=m.Mutator{}
  if _,err:=mut.SetAgentPersona(&reg,"agent",old);err!=nil{t.Fatal(err)}
  target:=reg.Agents[0].Clone()
  stopCause,resumeCause:=errors.New("stop failed"),errors.New("resume failed")
  var events []string
  calls:=0
  result,err:=RunAgentPersona(AgentPersonaDependencies{
   Request:AgentPersonaRequest{Action:tc.action,Spelling:"agent persona "+tc.action,Noun:"persona",Persona:"reviewer",Confirm:true,SocketPath:"/fixture/socket"},
   Registry:reg,Target:target,Interaction:m.InteractionIdle,
   OpenStore:func()(persona.Store,error){return storage,nil},
   Refuse:func(reason,message string,cause error)error{return fmt.Errorf("%s: %s: %w",reason,message,cause)},
   Plan:func(p *AgentRestartPlan,q AgentSettingsRequest,_ func(string,string)error)error{
    if q.Source!=m.SettingSourceAttach||q.Instructions==nil{t.Fatalf("wrong source %+v",q)}
    events=append(events,"plan");return nil
   },
   Notice:func(s string){if !strings.HasSuffix(s,"\n"){t.Fatal("CLI notice lost newline")};events=append(events,"notice")},
   SetPersona:func(a m.Agent,want m.AgentPersonaAnnotations)error{
    events=append(events,"record")
    return SetPlannedAgentPersona("persona",&reg,mut,a,want)
   },
   Execution:AgentRestartExecution{
    ValidateTransport:func()error{events=append(events,"transport");return nil},
    Stop:func()error{
     calls++;events=append(events,"stop")
     if !tc.stopError||tc.live==AgentRestartClosed{reg.Agents[0].Status.Phase=m.PhaseOffline;reg.Agents[0].Status.PaneRef="";reg.Panes=nil}
     if tc.stopError{return stopCause};return nil
    },
    Observe:func()(AgentRestartLiveness,error){events=append(events,"observe");return tc.live,errors.New("observation")},
    Resume:func(stopped StoppedAgentPane)error{
     events=append(events,"resume")
     if tc.phase==m.PhaseRunning&&(stopped.UID!="old-pane"||stopped.Name!="custom-pane"){t.Fatalf("pane identity %+v",stopped)}
     if reg.Agents[0].Status.SessionRef.ConversationID()!="conversation"{t.Fatal("conversation changed")}
     if tc.resumeError{return resumeCause}
     reg.Agents[0].Status.Phase=m.PhaseRunning;reg.Agents[0].Status.PaneRef="new-pane";return nil
    },
    NewPane:func()string{return reg.Agents[0].Status.PaneRef},
   },
  })
  restored:=tc.stopError&&tc.live!=AgentRestartClosed
  if restored {
   if !errors.Is(err,stopCause)||!reflect.DeepEqual(m.PersonaAnnotationsOf(reg.Agents[0]),old){t.Fatalf("rollback result %+v err %v events %v",result,err,events)}
   if strings.Contains(strings.Join(events,","),"resume"){t.Fatal("failed stop replayed")}
   if tc.live==AgentRestartUnknown&&result.Recovery==""{t.Fatal("unknown liveness lost recovery")}
  }else{
   annotations:=m.PersonaAnnotationsOf(reg.Agents[0])
   if annotations.InstructionsSource!=m.SettingSourceAttach||annotations.SystemPromptSnapshot!=m.SystemPromptSnapshotOff{t.Fatalf("annotations %+v",annotations)}
   if tc.action=="attach"&&annotations.PersonaDigest!=persona.Digest(content){t.Fatal("digest mismatch")}
   if tc.action=="detach"&&(annotations.Persona!=""||annotations.PersonaDigest!=""){t.Fatal("detach retained persona")}
   if tc.resumeError{
    if !errors.Is(err,resumeCause)||reg.Agents[0].Status.Phase!=m.PhaseOffline||!strings.HasPrefix(result.Recovery,"projmux agent resume uid:agent"){t.Fatalf("lost partial recovery %+v %v",result,err)}
   }else if err!=nil||result.NewPaneUID!="new-pane"{t.Fatalf("result %+v err %v",result,err)}
   if tc.stopError&&tc.live==AgentRestartClosed{
    sequence:=strings.Join(events,",")
    if !strings.Contains(sequence,"stop,observe,notice,resume"){t.Fatalf("warning was delayed beyond resume: %v",events)}
   }
  }
  wantStops:=0;if tc.phase==m.PhaseRunning{wantStops=1};if calls!=wantStops{t.Fatalf("stop calls %d",calls)}
  if tc.action=="attach"{path,e:=storage.SnapshotPath(persona.Digest(content));if e!=nil{t.Fatal(e)};bytes,e:=os.ReadFile(path);if e!=nil||string(bytes)!=string(content){t.Fatalf("snapshot %q %v",bytes,e)}}
 })}
}

func TestPersonaServiceReadOnlyDispositionAndBusy(t *testing.T){
 for _,mode:=range []string{"dry-run","busy","unchanged"}{t.Run(mode,func(t *testing.T){
  storage:=persona.NewDefaultStore(config.Paths{ConfigDir:t.TempDir(),StateDir:t.TempDir()})
  loaded,e:=storage.Write("reviewer",[]byte("review"));if e!=nil{t.Fatal(e)}
  a:=m.Agent{Metadata:m.ObjectMeta{UID:"agent",Name:"worker"}}
  a.Spec.Provider="claude";a.Status.Phase=m.PhaseRunning;a.Status.PaneRef="pane"
  a.Status.SessionRef=&m.AgentSessionRef{Provider:"claude",Claude:&m.ClaudeSessionRef{SessionID:"conversation"}}
  reg:=m.Registry{Agents:[]m.Agent{a},Panes:[]m.Pane{{Metadata:m.ObjectMeta{UID:"pane"}}}}
  if mode=="unchanged"{if _,e:=(m.Mutator{}).SetAgentPersona(&reg,"agent",m.AgentPersonaAnnotations{Persona:loaded.Name,PersonaDigest:loaded.Digest,SystemPromptSnapshot:m.SystemPromptSnapshotOff,InstructionsSource:m.SettingSourceAttach});e!=nil{t.Fatal(e)}}
  effects:=0
  result,e:=RunAgentPersona(AgentPersonaDependencies{
   Request:AgentPersonaRequest{Action:"attach",Spelling:"agent persona attach",Noun:"persona",Persona:"reviewer",DryRun:mode=="dry-run"},Registry:reg,Target:reg.Agents[0].Clone(),Interaction:m.InteractionUnknown,
   OpenStore:func()(persona.Store,error){return storage,nil},
   Refuse:func(reason,message string,_ error)error{return errors.New(reason+": "+message)},
   Plan:func(_ *AgentRestartPlan,q AgentSettingsRequest,_ func(string,string)error)error{if q.Source!=m.SettingSourceAttach{t.Fatal(q)};return nil},
   SetPersona:func(m.Agent,m.AgentPersonaAnnotations)error{effects++;return nil},
   Execution:AgentRestartExecution{BeforeStop:func()error{effects++;return nil},ValidateTransport:func()error{effects++;return nil}},
  })
  if mode=="busy"{if e==nil||!strings.Contains(e.Error(),personaReasonAgentBusy){t.Fatalf("%+v %v",result,e)}}else if e!=nil{t.Fatal(e)}
  if effects!=0{t.Fatal("read-only disposition caused effects")}
  if mode=="unchanged"&&(!result.Unchanged||result.ConfirmationRequired||result.NewPaneUID!="pane"){t.Fatalf("%+v",result)}
  if mode=="dry-run"&&(result.Outcome!="would-restart"||!result.ConfirmationRequired){t.Fatalf("%+v",result)}
  path,e:=storage.SnapshotPath(loaded.Digest);if e!=nil{t.Fatal(e)}
  if _,e=os.Stat(path);!os.IsNotExist(e){t.Fatalf("snapshot written on %s: %v",mode,e)}
 })}
}
