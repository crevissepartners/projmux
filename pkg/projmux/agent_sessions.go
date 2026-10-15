package projmux

import (
 "context"
 "errors"

 metadata "github.com/crevissepartners/projmux/internal/core/metadata"
 "github.com/crevissepartners/projmux/internal/core/selector"
 "github.com/crevissepartners/projmux/internal/services/agentsessions"
 "github.com/crevissepartners/projmux/internal/services/resources"
 "github.com/crevissepartners/projmux/pkg/projmux/core"
)

const CodeSessionsProviderUnsupported Code = "sessions-provider-unsupported"

// ListAgentSessionsRequest requires an explicit Agent name or uid: ref.
// Project and Windows are the CLI scopes; Labels are AND filters, with each
// value following CLI key=value grammar. No ambient Pane or cwd is consulted.
type ListAgentSessionsRequest struct {
 Agent string
 Project string
 Windows []string
 Labels []string
}
// ListProjectSessionsRequest accepts only a Project name or uid: ref, as on
// agent sessions project. Removed Projects require an exact Project-shaped UID.
type ListProjectSessionsRequest struct { Project string }

func (c *Client) agentSessionsService() agentsessions.Service {
 return agentsessions.Service{
  LoadRegistry:func() (metadata.Registry,error) {
   if c.pathsErr != nil { return metadata.Registry{}, c.pathsErr }
   return resources.Load(c.paths)
  },
  StateDir:func() (string,error) { return c.paths.StateDir,nil },
 }
}

// ListAgentSessions reads current and historical conversation metadata using
// the Client's frozen paths. It never opens a transcript or writes state.
// Cancellation before admission performs no reads; results own their pointers
// and slices independently of this Client and later calls.
func (c *Client) ListAgentSessions(ctx context.Context, input ListAgentSessionsRequest) (core.AgentSessionsList,error) {
 if err := ctx.Err(); err != nil { return core.AgentSessionsList{},err }
 if input.Agent == "" { return core.AgentSessionsList{}, &Refusal{Code:CodeSelectorInvalid,message:"agent sessions list requires <agent-ref>"} }
 query,err := resourceQuery(ResourceSelector{Project:input.Project,Windows:input.Windows,Labels:input.Labels})
 if err != nil { return core.AgentSessionsList{},resourceRefusal(err) }
 ref,err := selector.ParseRef(metadata.KindAgent,input.Agent)
 if err != nil { return core.AgentSessionsList{},resourceRefusal(selectorInputError(err,"ListAgentSessionsRequest.Agent")) }
 query.Agents = []selector.Ref{ref}
 listed,err := c.agentSessionsService().ListAgent(ctx,query)
 if err != nil { return core.AgentSessionsList{},sessionReadRefusal(err) }
 result := core.AgentSessionsList{AgentUID:listed.AgentUID,AgentName:listed.AgentName,Sessions:make([]core.AgentSession,len(listed.Sessions)),CorruptLines:listed.CorruptLines}
 for i,row := range listed.Sessions {
  result.Sessions[i] = core.AgentSession{AgentUID:row.AgentUID,Provider:row.Provider,SessionID:row.SessionID,TranscriptPath:row.TranscriptPath,ObservedAt:row.ObservedAt,Source:core.SessionSource(row.Source),ProjectUID:row.ProjectUID,WindowUID:row.WindowUID,AgentName:row.AgentName,AffiliationBasis:core.SessionAffiliationBasis(row.AffiliationBasis)}
  if row.LastRecordAt != nil { last := *row.LastRecordAt;result.Sessions[i].LastRecordAt = &last }
 }
 return result,nil
}

// ListProjectSessions reads recorded affiliations and current Registry rows.
// Missing history is empty; unreadable lines are counted. No affiliation is
// inferred from transcripts, cwd, or deletion records. Results are caller-owned.
func (c *Client) ListProjectSessions(ctx context.Context,input ListProjectSessionsRequest) (core.ProjectSessionsList,error) {
 listed,err := c.agentSessionsService().ListProject(ctx,input.Project)
 if err != nil { return core.ProjectSessionsList{},sessionReadRefusal(err) }
 result := core.ProjectSessionsList{ProjectUID:listed.ProjectUID,ProjectName:listed.ProjectName,Sessions:make([]core.ProjectSession,len(listed.Sessions)),Unattributed:listed.Unattributed,Ambiguous:listed.Ambiguous,CorruptLines:listed.CorruptLines}
 for i,row := range listed.Sessions {
  out := core.ProjectSession{Provider:row.Provider,SessionID:row.SessionID,TranscriptPath:row.TranscriptPath,ObservedAt:row.ObservedAt,Source:core.SessionSource(row.Source),Agents:make([]core.ProjectSessionAgent,len(row.Agents))}
  if row.LastRecordAt != nil { last := *row.LastRecordAt;out.LastRecordAt = &last }
  for j,agent := range row.Agents { out.Agents[j] = core.ProjectSessionAgent{AgentUID:agent.AgentUID,AgentName:agent.AgentName,WindowUID:agent.WindowUID,InRegistry:agent.InRegistry,Basis:core.SessionAffiliationBasis(agent.Basis)} }
  result.Sessions[i] = out
 }
 return result,nil
}
func sessionReadRefusal(err error) error {
 var failed *agentsessions.Failure
 if errors.As(err,&failed) { return &Refusal{Code:Code(failed.Code),message:failed.Error()} }
 return resourceRefusal(err)
}
