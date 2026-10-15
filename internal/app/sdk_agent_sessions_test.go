package app

import (
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/json"
 "errors"
 "io/fs"
 "os"
 "path/filepath"
 "reflect"
 "testing"
 "time"

 "github.com/crevissepartners/projmux/internal/config"
 metadata "github.com/crevissepartners/projmux/internal/core/metadata"
 "github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
 storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
 "github.com/crevissepartners/projmux/pkg/projmux"
 "github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The fixture uses metadata only: no hooks, provider input, or transcript reads.
func TestSDKAgentSessionsCLIParityAndReadOnly(t *testing.T) {
 for _, provider := range []string{"claude","codex"} {
  t.Run(provider,func(t *testing.T) {
   h := newSessionRefHarness(t,provider)
   root := t.TempDir()
   paths := config.Paths{ConfigDir:filepath.Join(root,"config"),StateDir:filepath.Join(root,"state")}
   agent,_ := h.registry.Agent(h.agentUID)
   agent.Metadata.Labels = map[string]string{"team":"sdk"}
   agent.Status.SessionRef = &metadata.AgentSessionRef{Provider:provider,ObservedAt:sessionRefObservedAt}
   if provider == "claude" { agent.Status.SessionRef.Claude = &metadata.ClaudeSessionRef{SessionID:"current",TranscriptPath:filepath.Join(root,"never-open")}
   } else { agent.Status.SessionRef.Codex = &metadata.CodexSessionRef{ThreadID:"current"} }
   project := h.registry.Projects[0]
   last := sessionRefObservedAt.Add(-time.Minute)
   rows := []sessionhistory.Record{
    {AgentUID:h.agentUID,Provider:provider,SessionID:"estimated",ObservedAt:last,LastRecordAt:&last,Source:sessionhistory.SourceEstimated},
    {AgentUID:h.agentUID,Provider:provider,SessionID:"current",ObservedAt:last,Source:sessionhistory.SourceObserved,ProjectUID:project.Metadata.UID,WindowUID:agent.Metadata.OwnerUID(),AgentName:agent.Metadata.Name},
    {AgentUID:"agent-deleted",Provider:provider,SessionID:"removed-agent",ObservedAt:last,Source:sessionhistory.SourceObserved,ProjectUID:project.Metadata.UID,WindowUID:"win-gone",AgentName:"gone"},
    {AgentUID:"agent-deleted",Provider:provider,SessionID:"unattributed",ObservedAt:last,Source:sessionhistory.SourceObserved},
    {AgentUID:"agent-deleted",Provider:provider,SessionID:"ambiguous",ObservedAt:last,Source:sessionhistory.SourceObserved,ProjectUID:project.Metadata.UID},
    {AgentUID:"agent-deleted",Provider:provider,SessionID:"ambiguous",ObservedAt:last,Source:sessionhistory.SourceObserved,ProjectUID:"proj-other"},
   }
   for _,row := range rows { if err:=sessionhistory.Append(paths.StateDir,row);err!=nil {t.Fatal(err)} }
   f,err := os.OpenFile(sessionhistory.Path(paths.StateDir),os.O_APPEND|os.O_WRONLY,0600)
   if err!=nil {t.Fatal(err)}
   if _,err=f.WriteString("corrupt\n");err!=nil {t.Fatal(err)}
   if err=f.Close();err!=nil {t.Fatal(err)}
   store := storemetadata.NewDefaultStore(paths)
   if _,err=store.Update(func(reg *metadata.Registry) error { *reg=h.registry.Clone();return nil });err!=nil {t.Fatal(err)}
   client,err := projmux.NewClient(projmux.Options{HomeDir:root,ConfigDir:paths.ConfigDir,StateDir:paths.StateDir})
   if err!=nil {t.Fatal(err)}
   // Changing ambient roots after construction must not redirect either read.
   t.Setenv("HOME",filepath.Join(root,"other-home"))
   t.Setenv("XDG_CONFIG_HOME",filepath.Join(root,"other-config"))
   t.Setenv("XDG_STATE_HOME",filepath.Join(root,"other-state"))
   t.Setenv("PROJMUX_STATE_DIR",filepath.Join(root,"other-override"))
   before := sessionReadTree(t,root)
   command := sessionHistoryAgentCommand(h,paths.StateDir)
   compare := func(args []string, value any) {
    t.Helper()
    var stdout,stderr bytes.Buffer
    if err:=command.Run(args,&stdout,&stderr);err!=nil {t.Fatalf("CLI: %v %s",err,stderr.String())}
    if stderr.Len()!=0 {t.Fatalf("JSON warning: %s",stderr.String())}
    encoded,err:=json.Marshal(value);if err!=nil {t.Fatal(err)}
    var cliJSON,sdkJSON any
    if err=json.Unmarshal(stdout.Bytes(),&cliJSON);err!=nil {t.Fatal(err)}
    if err=json.Unmarshal(encoded,&sdkJSON);err!=nil {t.Fatal(err)}
    if !reflect.DeepEqual(cliJSON,sdkJSON) {t.Fatalf("CLI %s SDK %s",stdout.Bytes(),encoded)}
   }
   ctx:=context.Background()
   req:=projmux.ListAgentSessionsRequest{Agent:agent.Metadata.Name,Project:project.Metadata.Name,Windows:[]string{"uid:"+agent.Metadata.OwnerUID()},Labels:[]string{"team=sdk"}}
   listed,err:=client.ListAgentSessions(ctx,req);if err!=nil {t.Fatal(err)}
   compare([]string{"sessions","list",req.Agent,"--project",req.Project,"--window",req.Windows[0],"--selector",req.Labels[0],"-o","json"},listed)
   if listed.CorruptLines!=1 || len(listed.Sessions)!=2 || listed.Sessions[0].LastRecordAt==nil || listed.Sessions[1].Source!=core.SessionSourceCurrent {t.Fatalf("Agent join: %+v",listed)}
   if provider=="codex" && listed.Sessions[1].TranscriptPath!="" {t.Fatal("filled Codex transcript path")}
   *listed.Sessions[0].LastRecordAt=time.Time{}
   listed.Sessions[0].SessionID="changed"
   fresh,err:=client.ListAgentSessions(ctx,req)
   if err!=nil || fresh.Sessions[0].SessionID!="estimated" || fresh.Sessions[0].LastRecordAt.IsZero() {t.Fatalf("caller ownership: %+v %v",fresh,err)}
   pr:=projmux.ListProjectSessionsRequest{Project:"uid:"+project.Metadata.UID}
   projected,err:=client.ListProjectSessions(ctx,pr);if err!=nil {t.Fatal(err)}
   compare([]string{"sessions","project",pr.Project,"-o","json"},projected)
   if projected.Unattributed!=1 || projected.Ambiguous!=1 || projected.CorruptLines!=1 {t.Fatalf("Project counts: %+v",projected)}
   projected.Sessions[0].Agents[0].AgentName="changed"
   for i:=range projected.Sessions {if projected.Sessions[i].LastRecordAt!=nil {*projected.Sessions[i].LastRecordAt=time.Time{}}}
   again,err:=client.ListProjectSessions(ctx,pr);if err!=nil {t.Fatal(err)}
   compare([]string{"sessions","project",pr.Project,"-o","json"},again)
   for _,row:=range again.Sessions {for _,a:=range row.Agents {if a.AgentName=="changed" {t.Fatal("nested slice alias")}};if row.LastRecordAt!=nil && row.LastRecordAt.IsZero() {t.Fatal("time pointer alias")}}
   for _,bad:=range []projmux.ListAgentSessionsRequest{{},{Agent:"missing"},{Agent:req.Agent,Labels:[]string{"bad"}},{Agent:req.Agent,Project:"missing"}} {
    _,err=client.ListAgentSessions(ctx,bad)
    var refused *projmux.Refusal
    if !errors.As(err,&refused) {t.Fatalf("untyped Agent refusal: %v",err)}
   }
   for _,bad:=range []string{"","missing","uid:agent-wrong"} {
    _,err=client.ListProjectSessions(ctx,projmux.ListProjectSessionsRequest{Project:bad})
    var refused *projmux.Refusal
    if !errors.As(err,&refused) {t.Fatalf("untyped Project refusal %q: %v",bad,err)}
   }
   canceled,cancel:=context.WithCancel(ctx);cancel()
   if _,err=client.ListAgentSessions(canceled,req);!errors.Is(err,context.Canceled) {t.Fatal(err)}
   if _,err=client.ListProjectSessions(canceled,pr);!errors.Is(err,context.Canceled) {t.Fatal(err)}
   if !reflect.DeepEqual(before,sessionReadTree(t,root)) {t.Fatal("read/cancellation/refusal changed fixture tree")}
   // Removed Project retains only recorded affiliation and its empty name.
   goneUID,err:=metadata.NewUID(metadata.KindProject);if err!=nil {t.Fatal(err)}
   if err=sessionhistory.Append(paths.StateDir,sessionhistory.Record{AgentUID:"agent-deleted",Provider:provider,SessionID:"removed-project",ObservedAt:last,Source:sessionhistory.SourceObserved,ProjectUID:goneUID,AgentName:"gone"});err!=nil {t.Fatal(err)}
   removedBefore:=sessionReadTree(t,root)
   removedRef:=projmux.ListProjectSessionsRequest{Project:"uid:"+goneUID}
   removed,err:=client.ListProjectSessions(ctx,removedRef);if err!=nil {t.Fatal(err)}
   compare([]string{"sessions","project",removedRef.Project,"-o","json"},removed)
   if removed.ProjectName!="" || len(removed.Sessions)==0 {t.Fatalf("removed Project: %+v",removed)}
   if !reflect.DeepEqual(removedBefore,sessionReadTree(t,root)) {t.Fatal("removed Project read wrote state")}
  })
 }
}

func sessionReadTree(t *testing.T,root string) map[string][32]byte {
 t.Helper()
 result:=map[string][32]byte{}
 err:=filepath.WalkDir(root,func(path string,entry fs.DirEntry,err error) error {
  if err!=nil {return err}
  if entry.IsDir() {result[path]=[32]byte{};return nil}
  data,err:=os.ReadFile(path);if err!=nil {return err};result[path]=sha256.Sum256(data);return nil
 })
 if err!=nil {t.Fatal(err)}
 return result
}
