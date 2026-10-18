package questions

import (
	"context"
	"fmt"
	"slices"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
)

// questionSource deliberately excludes progress, turn state and observation
// times: those can change during a read without changing its conversation.
type questionSource struct {
	agent, window, project, pane, provider string
	runtime                                coremetadata.RuntimeKind
	phase                                  coremetadata.AgentPhase
	activation                             coremetadata.PaneActivation
	binding                                coremetadata.ProcessBinding
	session, connection                    string
	recordSessionID, recordThreadID        string
}

func captureQuestionSource(reg coremetadata.Registry, agent coremetadata.Agent) (questionSource, bool) {
	if agent.Metadata.OwnerRef == nil {
		return questionSource{}, false
	}
	pane, ok := reg.Pane(agent.Status.PaneRef)
	window, hasWindow := reg.Window(agent.Metadata.OwnerUID())
	if !ok || !hasWindow || pane.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerUID() != agent.Metadata.UID {
		return questionSource{}, false
	}
	project, hasProject := reg.Project(window.Metadata.OwnerUID())
	if !hasProject || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject {
		return questionSource{}, false
	}
	out := questionSource{agent: agent.Metadata.UID, window: window.Metadata.UID, project: project.Metadata.UID, pane: pane.Metadata.UID, provider: agent.Spec.Provider, runtime: pane.Spec.Runtime.EffectiveKind(), phase: agent.Status.Phase}
	// Compare activation identity for every runtime; no runtime observation occurs.
	out.activation = coremetadata.PaneActivation{Kind: pane.Status.Activation.Kind, Generation: pane.Status.Activation.Generation, RuntimeID: pane.Status.Activation.RuntimeID, AgentUID: pane.Status.Activation.AgentUID, OperationID: pane.Status.Activation.OperationID}
	if pane.Status.Activation.Process != nil {
		v := *pane.Status.Activation.Process
		out.activation.Process = &v
	}
	if out.runtime != coremetadata.RuntimeProcess || out.phase != coremetadata.PhaseRunning {
		return out, true
	}
	if pane.Status.Activation.Process == nil || pane.Status.ProcessSession == nil {
		return questionSource{}, false
	}
	out.binding = pane.Status.Activation.Process.Binding
	record := pane.Status.ProcessSession
	if record.ConnectionID != record.Binding.OperationID {
		return questionSource{}, false
	}
	out.recordSessionID, out.recordThreadID = record.SessionID, record.ThreadID
	out.session, out.connection = record.SessionID, record.ConnectionID
	if agent.Spec.Provider == "codex" {
		out.session = record.ThreadID
	}
	activation, provider, valid := reg.CurrentProcessActivation(out.binding)
	if !valid || provider != out.provider || activation != *pane.Status.Activation.Process {
		return questionSource{}, false
	}
	b := out.binding
	binding := processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
	if !processquestions.SessionMatches(reg, binding, out.provider, out.session, out.connection) {
		return questionSource{}, false
	}
	return out, true
}

func sameQuestionSource(a, b questionSource) bool {
	// PaneActivation includes process pointers; compare their values separately.
	ap, bp := a.activation.Process, b.activation.Process
	a.activation.Process, b.activation.Process = nil, nil
	return a == b && (ap == nil && bp == nil || ap != nil && bp != nil && *ap == *bp)
}

// ListWithSource preserves partial process history while fencing the entire
// result, including custom dispatchers. It never starts or reconciles a runtime.
func (c Service) ListWithSource(ctx context.Context, agent coremetadata.Agent) (List, error) {
	if err := ctx.Err(); err != nil {
		return List{}, err
	}
	stale := func() (List, error) {
		return List{}, &Failure{Code: "question-stale", Message: "question-stale: question source changed or is unavailable"}
	}
	reg, err := c.LoadRegistry()
	if err != nil {
		return List{}, err
	}
	current, found := reg.Agent(agent.Metadata.UID)
	if !found || current.Status.PaneRef != agent.Status.PaneRef || current.Spec.Provider != agent.Spec.Provider || current.Metadata.OwnerUID() != agent.Metadata.OwnerUID() || current.Status.Phase != agent.Status.Phase {
		return stale()
	}
	source, valid := captureQuestionSource(reg, *current)
	if !valid {
		return stale()
	}
	process := source.runtime == coremetadata.RuntimeProcess
	st, readErr := c.OpenStore()
	if readErr != nil {
		st = nil
	}
	var records []agentquestion.Record
	var asyncRecords []agentquestion.Record
	if readErr == nil {
		records, readErr = st.List(agent.Metadata.UID)
		if readErr == nil {
			asyncRecords, readErr = st.AsyncStore().List(agent.Metadata.UID)
			records = append(records, asyncRecords...)
		}
	}
	if readErr != nil && !process {
		return List{}, fmt.Errorf("agent question list: %w", readErr)
	}
	list := List{AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Channel: "off", Policy: "native", Questions: []View{}}
	if c.Answering != nil && c.Answering() == config.AgentQuestionAnsweringProjmux {
		list.Channel, list.Policy = "on", "projmux"
	}
	if process && source.phase == coremetadata.PhaseRunning {
		if err := ctx.Err(); err != nil {
			return List{}, err
		}
		if c.Dispatcher == nil {
			return stale()
		}
		list.ExactQuestions, err = c.Dispatcher.ReadExactProcessQuestions(ctx, agent.Metadata.UID)
		if err != nil {
			if ctx.Err() != nil {
				return List{}, ctx.Err()
			}
			return stale()
		}
	}
	cliRecords := map[string]agentquestion.CLIAsyncRecord{}
	if st != nil {
		cli, err := st.CLIAsyncStore().List(agent.Metadata.UID)
		if err != nil {
			return List{}, fmt.Errorf("agent question list: %w", err)
		}
		for _, r := range cli {
			cliRecords[r.ID] = r
			records = append(records, r.Record)
		}
	}
	c.projectRecords(&list, agent, process, records, cliRecords, true)
	// Only records actually read from the async namespace supply opaque item IDs.
	ids := map[string]string{}
	for _, r := range asyncRecords {
		if r.AgentUID == agent.Metadata.UID && r.Delivery == "async" {
			ids[r.ID] = r.RequestID
		}
	}
	for i := range list.Questions {
		v := &list.Questions[i]
		if v.Delivery == "async" {
			v.ItemID = ids[v.ID]
		}
		if process && v.Delivery != "async-message" {
			v.CanAnswer, v.CanDecline = false, false
		}
	}
	out := list
	out.ExactQuestions = slices.Clone(list.ExactQuestions)
	out.Provider, out.Runtime, out.Phase, out.Session = coremetadata.NormalizeProvider(source.provider), source.runtime, source.phase, source.session
	for i := range out.ExactQuestions {
		exact := &out.ExactQuestions[i]
		exact.ItemID = ""
		q := *exact
		if q.Delivery == "async" && st != nil {
			// A legacy failure must not hide an independently readable async exact ID.
			r, found, e := st.AsyncStore().Get(q.QuestionID)
			if e == nil && found && r.AgentUID == agent.Metadata.UID && r.Delivery == "async" {
				exact.ItemID = r.RequestID
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return List{}, err
	}
	fresh, err := c.LoadRegistry()
	if err != nil {
		return stale()
	}
	next, found := fresh.Agent(agent.Metadata.UID)
	if !found {
		return stale()
	}
	after, valid := captureQuestionSource(fresh, *next)
	if !valid || !sameQuestionSource(source, after) {
		return stale()
	}
	return out, nil
}
