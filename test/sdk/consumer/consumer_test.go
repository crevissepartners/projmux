package consumer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalInstructionsConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "config")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var written projmux.InstructionWriteResult
	written, err = c.WriteInstructions(ctx, "reviewer", []byte("exact\r\nbytes"))
	if err != nil {
		t.Fatal(err)
	}
	var list []projmux.InstructionInfo
	list, err = c.ListInstructions(ctx)
	if err != nil || len(list) != 1 || list[0].Digest != written.Digest {
		t.Fatalf("list %+v %v", list, err)
	}
	var content projmux.InstructionContent
	content, err = c.ReadInstructions(ctx, "reviewer")
	if err != nil || string(content.Content) != "exact\r\nbytes" {
		t.Fatalf("read %+v %v", content, err)
	}
	if err = c.DeleteInstructions(ctx, "reviewer"); err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadInstructions(ctx, "reviewer")
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeNotFound {
		t.Fatalf("typed refusal %T %v", err, err)
	}
}

func TestExternalResourceConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), SocketPath: filepath.Join(root, "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if c.Version() == "" {
		t.Fatal("empty version")
	}
	for _, kind := range []core.Kind{core.KindProject, core.KindWindow, core.KindPane, core.KindAgent} {
		var list core.ResourceList
		list, err = c.ListResources(context.Background(), kind, projmux.ResourceSelector{})
		if err != nil || list.Items == nil || len(list.Items) != 0 {
			t.Fatalf("%s: %+v %v", kind, list, err)
		}
		_, err = c.GetResource(context.Background(), core.Ref{Kind: kind, Value: "uid:missing"})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	// Concrete public fields remain usable without importing an internal package.
	_ = core.Agent{Metadata: core.ObjectMeta{UID: "agent-example"}, Status: core.AgentStatus{SessionRef: &core.AgentSessionRef{Codex: &core.CodexSessionRef{ThreadID: "thread"}}}}
}

func TestExternalRuntimeConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []core.RuntimeObjectKind{core.RuntimeSessions, core.RuntimeWindows, core.RuntimePanes} {
		var report core.RuntimeReport
		report, err = c.ObserveRuntime(context.Background(), projmux.RuntimeRequest{Kind: kind})
		if err != nil || report.Transport.Kind != "none" || report.Items == nil || len(report.Unavailable) == 0 {
			t.Fatalf("%+v %v", report, err)
		}
	}
	_, err = c.ObserveRuntime(context.Background(), projmux.RuntimeRequest{Kind: core.RuntimeSessions, SocketPath: "relative"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRuntimeRequestInvalid {
		t.Fatal(err)
	}
	_ = core.RuntimeRow{Resource: &core.RuntimeResource{UID: "pane-example"}, Conflicts: []core.RuntimeConflict{{Targets: []string{"%1"}}}}
}

// The consumer runs with GOWORK=off and -mod=readonly. Its module files must
// describe a library consumer without CLI framework requirements or checksums.
func TestExternalConsumerModuleExcludesCLI(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, dependency := range []string{"github.com/spf13/cobra", "github.com/spf13/pflag"} {
			if strings.Contains(string(content), dependency+" ") {
				t.Errorf("%s contains CLI dependency %s", name, dependency)
			}
		}
	}
}

func TestExternalAgentCatalogConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{CodexModelLister: func(context.Context) ([]string, error) { return []string{"fixture-model"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	var capabilities core.Capabilities
	capabilities, err = c.Capabilities(context.Background(), projmux.CapabilitiesRequest{Provider: "claude"})
	if err != nil || capabilities.Provider != "claude" || len(capabilities.Capabilities) == 0 {
		t.Fatalf("%+v %v", capabilities, err)
	}
	var models core.ModelSuggestions
	models, err = c.Models(context.Background(), "codex")
	if err != nil || len(models.Models) != 1 || models.Models[0] != "fixture-model" || !models.AcceptsUnlisted {
		t.Fatalf("%+v %v", models, err)
	}
	_, err = c.Capabilities(context.Background(), projmux.CapabilitiesRequest{Provider: "unknown"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProviderInvalid {
		t.Fatal(err)
	}
}

func TestExternalProfileConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "config")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var written core.ProfileWriteResult
	written, err = c.WriteProfile(ctx, "reader", []byte("model = \"opus\""))
	if err != nil {
		t.Fatal(err)
	}
	var list []core.ProfileInfo
	list, err = c.ListProfiles(ctx, "")
	if err != nil || len(list) != 1 || list[0].Digest != written.Digest || list[0].Provider != "-" {
		t.Fatalf("list %+v %v", list, err)
	}
	var content core.ProfileContent
	content, err = c.ReadProfile(ctx, "reader")
	if err != nil || string(content.Content) != "model = \"opus\"" {
		t.Fatalf("read %+v %v", content, err)
	}
	err = c.DeleteProfile(ctx, "reader", false)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProfileConfirmationRequired {
		t.Fatal(err)
	}
	if err = c.DeleteProfile(ctx, "reader", true); err != nil {
		t.Fatal(err)
	}
}

func TestExternalSettingsUsageNotificationsConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var value core.ConfigValue
	host := "process"
	value, err = c.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigAgentHost, DefaultHost: &host})
	if err != nil || value.DefaultHost != host {
		t.Fatalf("agent host: %+v %v", value, err)
	}
	value, err = c.GetConfig(ctx, core.ConfigAgentHost)
	if err != nil || value.DefaultHost != host {
		t.Fatalf("agent host read: %+v %v", value, err)
	}
	value, err = c.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigLocale, Locale: "ko-KR"})
	if err != nil || value.Locale != "ko-KR" {
		t.Fatalf("%+v %v", value, err)
	}
	value, err = c.GetConfig(ctx, core.ConfigLocale)
	if err != nil || value.Locale != "ko-KR" {
		t.Fatalf("%+v %v", value, err)
	}
	_, err = c.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigProviders, Provider: "not-a-provider"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeConfigValueInvalid {
		t.Fatal(err)
	}
	var cache core.UsageCache
	cache, err = c.GetUsage(ctx)
	if err != nil || len(cache.Snapshots) != 0 {
		t.Fatalf("%+v %v", cache, err)
	}
	var points []core.UsageMetricPoint
	points, err = c.GetUsageHistory(ctx, core.UsageHistoryFilter{})
	if err != nil || points == nil || len(points) != 0 {
		t.Fatalf("%+v %v", points, err)
	}
	var notifications []core.Notification
	notifications, err = c.ListNotifications(ctx, core.NotificationFilter{})
	if err != nil || notifications == nil || len(notifications) != 0 {
		t.Fatalf("%+v %v", notifications, err)
	}
	_, err = c.AckNotifications(ctx, core.NotificationAck{ID: "missing"})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeNotificationNotFound {
		t.Fatal(err)
	}
	count, err := c.AckNotifications(ctx, core.NotificationAck{All: true})
	if err != nil || count != 0 {
		t.Fatalf("%d %v", count, err)
	}
}

func TestExternalQuestionConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "config"), StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	var list core.QuestionList
	list, err = c.ListQuestions(context.Background(), "absent")
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound || len(list.Questions) != 0 {
		t.Fatalf("%+v %v", list, err)
	}
	option := "yes"
	var answer core.QuestionAnswerResult
	answer, err = c.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "absent", QuestionID: "question-0000000000000001", Selections: []core.QuestionSelection{{Question: 1, Option: &option}}})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound || answer.QuestionID != "" {
		t.Fatalf("%+v %v", answer, err)
	}
}

func TestExternalApprovalConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "config"), StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	var list core.ApprovalList
	list, err = c.ListApprovals(context.Background(), "absent")
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound || len(list.Requests) != 0 {
		t.Fatal(list, err)
	}
	var receipt core.ApprovalReceipt
	receipt, err = c.AnswerApproval(context.Background(), projmux.AnswerApprovalRequest{Agent: "absent", RequestID: "7", Allow: true})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound || receipt.RequestID != "" {
		t.Fatal(receipt, err)
	}
	receipt, err = c.ReviewApproval(context.Background(), projmux.ReviewApprovalRequest{Agent: "absent", RequestID: "7", Decision: core.OfferedDecision("accept")})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound || receipt.RequestID != "" {
		t.Fatal(receipt, err)
	}
}

func TestExternalProjectConsumer(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	client, err := projmux.NewClient(projmux.Options{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	req := core.CreateProjectRequest{Root: t.TempDir(), Name: "library-project", Labels: []string{"team=consumer"}}
	var result core.CreateProjectResult
	result, err = client.CreateProject(context.Background(), req)
	if err != nil || !result.Created || result.Reused || result.Receipt.Operation != core.OperationCreateProject || len(result.Windows) != 1 || len(result.Panes) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	result.Project.Metadata.Labels["team"] = "caller-only"
	result, err = client.CreateProject(context.Background(), req)
	if err != nil || result.Created || !result.Reused || result.Project.Metadata.Labels["team"] != "consumer" {
		t.Fatalf("%+v %v", result, err)
	}
	_, err = client.CreateProject(context.Background(), core.CreateProjectRequest{Root: "relative"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProjectRootInvalid {
		t.Fatal(err)
	}
}

func TestProjectLinkRulesPublicContract(t *testing.T) {
	client, err := projmux.NewClient(projmux.Options{ConfigDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Empty isolated Registry: every entrypoint exposes the same typed missing-Project refusal.
	for _, call := range []func() error{
		func() error { _, e := client.ReadProjectLinkRules(context.Background(), "absent"); return e },
		func() error {
			_, e := client.WriteProjectLinkRules(context.Background(), "absent", core.ProjectLinkRules{URLs: map[string]string{}, Links: []core.ProjectLink{}}, true)
			return e
		},
		func() error { _, e := client.DeleteProjectLinkRules(context.Background(), "absent", true); return e },
	} {
		var refusal *projmux.Refusal
		if err := call(); !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound {
			t.Fatalf("missing Project: %v", err)
		}
	}
	var result core.ProjectLinkRulesResult
	var refusal *projmux.Refusal
	_ = result
	_ = refusal
}

func TestExternalProjectCandidatesConsumer(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repos")
	root := filepath.Join(repo, "sample")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJMUX_PROJDIR", repo)
	t.Setenv("PROJMUX_MANAGED_ROOTS", "")
	t.Setenv("TMUX_SESSIONIZER_ROOTS", "")
	client, err := projmux.NewClient(projmux.Options{HomeDir: home, ConfigDir: filepath.Join(home, "config"), StateDir: filepath.Join(home, "state")})
	if err != nil {
		t.Fatal(err)
	}
	var result core.ProjectCandidatesResult
	result, err = client.ListProjectCandidates(context.Background())
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Path != root || result.Candidates[0].Pinned || result.Candidates[0].ProjectUID != "" {
		t.Fatalf("%+v %v", result, err)
	}
	for _, path := range []string{filepath.Join(home, "config"), filepath.Join(home, "state")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read created %s: %v", path, err)
		}
	}
}

func TestExternalRenameConsumer(t *testing.T) {
	client, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: t.TempDir(), Name: "old"})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := client.Rename(context.Background(), core.RenameRequest{Kind: core.KindProject, Ref: "uid:" + created.Project.Metadata.UID, Name: "new"})
	if err != nil || !renamed.Committed || renamed.Receipt.Operation != core.OperationRenameProject {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	project, ok := renamed.Projection.(core.Project)
	if !ok || project.Metadata.Name != "new" {
		t.Fatalf("projection: %#v", renamed.Projection)
	}
}

func TestUnregisterProjectPublicConsumer(t *testing.T) {
	client, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateProject(context.Background(), core.CreateProjectRequest{Root: t.TempDir(), Name: "sdk-unregister"})
	if err != nil {
		t.Fatal(err)
	}
	req := core.UnregisterRequest{Ref: "uid:" + created.Project.Metadata.UID}
	plan, err := client.UnregisterProject(context.Background(), req)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeConfirmationRequired || len(plan.Plan.Targets) != 1 {
		t.Fatalf("%+v %v", plan, err)
	}
	req.Confirm = true
	result, err := client.UnregisterProject(context.Background(), req)
	if err != nil || !result.Committed || result.OperationID == "" || result.Receipt.Operation != core.OperationUnregisterProject {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestDeletePublicConsumer(t *testing.T) {
	client, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	req := core.DeleteRequest{Kind: core.KindPane, Ref: "uid:reserved", Project: "explicit", Window: "scope", SocketPath: "/tmp/explicit", DryRun: true, Confirm: true}
	result, err := client.Delete(context.Background(), req)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNotFound || result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	_ = core.DeletePane{PaneUID: "pane", PaneID: "%1", RootKind: core.KindProject}
	_ = core.DeleteRegistryOnly{ResourceUID: "agent", Evidence: "offline"}
	_ = core.DeleteWindow{UID: "window", WindowID: "@1", SessionID: "$1", RootKind: core.KindProject, EndsSession: true}
}

func TestExternalStopProjectConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result core.StopProjectResult
	result, err = c.StopProject(ctx, core.StopProjectRequest{Ref: "uid:project", SocketPath: "/explicit/socket"})
	if !errors.Is(err, context.Canceled) || result.Stopped {
		t.Fatalf("cancelled stop: %+v %v", result, err)
	}
	_, err = c.StopProject(context.Background(), core.StopProjectRequest{})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid {
		t.Fatalf("input refusal %v", err)
	}
}

func TestExternalShellCreateConsumer(t *testing.T) {
	client, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir(), ConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var window core.CreateWindowResult
	window, err = client.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "named", Name: "shell", Labels: []string{"team=one"}})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateExecutableInvalid || window.Window.Metadata.UID != "" {
		t.Fatalf("window %+v %v", window, err)
	}
	var pane core.CreatePaneResult
	pane, err = client.CreatePane(context.Background(), core.CreatePaneRequest{Project: "named", Window: "shell", Pane: "uid:anchor", Name: "child", Placement: "down", CwdFrom: "pane"})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateExecutableInvalid || pane.Pane.Metadata.UID != "" {
		t.Fatalf("pane %+v %v", pane, err)
	}
}

func TestExternalAgentCreateConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out core.CreateAgentResult
	out, err = c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:project", Window: "uid:window", Provider: "claude", Profile: "none", Instructions: "reviewer", Model: "sonnet", Effort: "high", Cwd: "/workspace", AddDirs: []string{"/another"}, InteractiveOnly: false, DialogueReplyOnly: false, Name: "worker", Labels: []string{"role=worker"}, Placement: "right", CwdFrom: "project", Payload: []string{"review"}, Creator: "uid:agent", SocketPath: "/explicit/socket"})
	if !errors.Is(err, context.Canceled) || out.Agent.Metadata.UID != "" {
		t.Fatalf("%+v %v", out, err)
	}
	// Native create uses only public request/result/error types from an external module.
	_, nativeErr := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:project", Window: "uid:window", Provider: "codex", Profile: "native", Instructions: "reviewer", Payload: []string{"hello"}})
	if !errors.Is(nativeErr, context.Canceled) {
		t.Fatalf("native cancelled create: %v", nativeErr)
	}
	var nativeCode projmux.Code = projmux.CodeCreateNativeCodexUnavailable
	if nativeCode == "" {
		t.Fatal("native refusal code missing")
	}
	_, err = c.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "named", Window: "window", Provider: "codex"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateExecutableInvalid {
		t.Fatalf("%v", err)
	}
}

func TestExternalResumeAgent(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir(), ConfigDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result core.ResumeAgentResult
	result, err = c.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:agent", Project: "uid:project", Window: "uid:window", Model: "sonnet", Effort: "high", DialogueReplyOnly: false, SocketPath: "/explicit/socket"})
	if !errors.Is(err, context.Canceled) || result.PaneID != "" {
		t.Fatalf("cancelled external resume: %+v %v", result, err)
	}
	// The old exported token remains source-compatible; native refusal uses the new token.
	var oldCode projmux.Code = projmux.CodeResumeNativeCodexUnsupported
	var nativeCode projmux.Code = projmux.CodeResumeNativeCodexUnavailable
	if oldCode != "resume-native-codex-unsupported" || nativeCode != "resume-native-codex-unavailable" {
		t.Fatal(oldCode, nativeCode)
	}

}

// This module deliberately imports only public packages, including the recorder.
func TestExternalCreateRecorderConsumer(t *testing.T) {
	calls := 0
	var recorder projmux.CreateRecorder = func(outcome projmux.CreateOutcome) error {
		calls++
		if outcome.Kind != projmux.CreateKindWindow || outcome.Result != projmux.CreateError || outcome.LockHeld != nil || outcome.SpawnToRelease != nil || outcome.Phases != (projmux.CreatePhases{}) {
			t.Fatalf("unexpected outcome: %+v", outcome)
		}
		return errors.New("journal is unavailable")
	}
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), SocketPath: filepath.Join(root, "absent.sock"), ProjmuxExecutable: "/bin/sh", CreateRecorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateWindow(context.Background(), core.CreateWindowRequest{})
	if err == nil || calls != 0 {
		t.Fatal("validation recorded")
	}
	_, err = c.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "missing"})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateRouteRefused || calls != 1 {
		t.Fatalf("outcome changed refusal: calls=%d err=%v", calls, err)
	}
	_ = []projmux.CreateKind{projmux.CreateKindPane, projmux.CreateKindAgent, projmux.CreateKindResume}
	_ = []projmux.CreatePhase{projmux.CreatePhaseGuard, projmux.CreatePhaseFirstReconcile, projmux.CreatePhaseOperation, projmux.CreatePhaseSecondReconcile, projmux.CreatePhaseReprove, projmux.CreatePhaseStoreWrite}
	_ = projmux.CreateSuccess
}

func TestExternalStopAgentConsumer(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "config"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result core.StopAgentResult
	result, err = c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:agent", Binding: core.ProcessBinding{Generation: "generation"}})
	if !errors.Is(err, context.Canceled) || result.Stopped {
		t.Fatalf("cancelled Stop %+v %v", result, err)
	}
	_, err = c.StopAgent(context.Background(), core.StopAgentRequest{})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid {
		t.Fatalf("typed Stop refusal: %v", err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("preflight created state: %v", err)
	}
}

// This consumer seeds a public resource fixture without importing internal code.
func TestExternalDeleteProcessAgentConsumer(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	path := filepath.Join(state, "metadata", "registry.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	meta := func(uid, name string, kind core.Kind, owner string) core.ObjectMeta {
		m := core.ObjectMeta{UID: uid, Name: name}
		if owner != "" {
			m.OwnerRef = &core.OwnerRef{Kind: kind, UID: owner}
		}
		return m
	}
	project := core.Project{APIVersion: "projmux.io/v1alpha1", Kind: core.KindProject, Metadata: meta("project", "project", "", ""), Spec: core.ProjectSpec{Root: root, PrimaryWindowRef: "window"}}
	window := core.Window{APIVersion: "projmux.io/v1alpha1", Kind: core.KindWindow, Metadata: meta("window", "window", core.KindProject, "project"), Spec: core.WindowSpec{AnchorPaneRef: "pane"}}
	agent := core.Agent{APIVersion: "projmux.io/v1alpha1", Kind: core.KindAgent, Metadata: meta("agent", "worker", core.KindWindow, "window"), Spec: core.AgentSpec{Provider: "codex"}, Status: core.AgentStatus{Phase: core.PhaseOffline, PaneRef: "pane"}}
	pane := core.Pane{APIVersion: "projmux.io/v1alpha1", Kind: core.KindPane, Metadata: meta("pane", "worker-pane", core.KindAgent, "agent"), Spec: core.PaneSpec{Runtime: core.PaneRuntimeSpec{Kind: core.RuntimeProcess}, Role: core.PaneRoleAgent}}
	data, err := json.Marshal(map[string]any{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": []core.Project{project}, "windows": []core.Window{window}, "agents": []core.Agent{agent}, "panes": []core.Pane{pane}, "nameReservations": []map[string]any{{"kind": "Project", "uid": "project", "name": "project"}, {"scope": "project", "kind": "Window", "uid": "window", "name": "window"}, {"scope": "project", "kind": "Agent", "uid": "agent", "name": "worker"}, {"scope": "project", "kind": "Pane", "uid": "pane", "name": "worker-pane"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: state, HomeDir: root})
	if err != nil {
		t.Fatal(err)
	}
	req := core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:agent", DryRun: true}
	preview, err := c.Delete(context.Background(), req)
	if err != nil || preview.Committed || len(preview.Plan.Targets) != 1 {
		t.Fatalf("external preview %+v %v", preview, err)
	}
	wantProcess := core.DeleteProcess{AgentUID: "agent", WindowUID: "window", RootKind: core.KindProject, RootUID: "project", Runtime: "unknown", Evidence: "activation-absent", HostPID: 0}
	if len(preview.Processes) != 1 || preview.Processes[0] != wantProcess {
		t.Fatalf("external process fields: %+v", preview.Processes)
	}
	preview.Processes[0].Runtime = "caller changed"
	preflight, err := c.PreflightDelete(context.Background(), req)
	if err != nil || len(preflight.Processes) != 0 {
		t.Fatalf("Registry preflight acquired runtime: %+v %v", preflight, err)
	}
	req.DryRun = false
	req.Confirm = true
	deleted, err := c.Delete(context.Background(), req)
	if err != nil || !deleted.Committed || deleted.Receipt.Target.UID != "agent" {
		t.Fatalf("external delete %+v %v", deleted, err)
	}
	if len(deleted.Processes) != 1 || deleted.Processes[0] != wantProcess {
		t.Fatalf("external final process fields: %+v", deleted.Processes)
	}
	for _, ref := range []core.Ref{{Kind: core.KindAgent, Value: "uid:agent"}, {Kind: core.KindPane, Value: "uid:pane"}} {
		_, err = c.GetResource(context.Background(), ref)
		var f *projmux.Refusal
		if !errors.As(err, &f) || f.Code != projmux.CodeResourceNotFound {
			t.Fatalf("external removal %v", err)
		}
	}
}

func TestExternalTurnConsumer(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{ConfigDir: t.TempDir(), StateDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	methods := []func(context.Context, core.TurnRequest) (core.TurnResult, error){c.StartTurn, c.SteerTurn, c.DeliverTurn, c.InterruptTurn}
	for _, call := range methods {
		_, err := call(context.Background(), core.TurnRequest{})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid {
			t.Fatalf("explicit ref: %v", err)
		}
	}
}

func externalStopIdentity(t *testing.T, pid int) core.ProcessIdentity {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+2:])
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	return core.ProcessIdentity{PID: pid, OwnerUID: uint32(os.Getuid()), Start: "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19]}
}

func externalStopSocket(path string) string {
	digest := sha256.Sum256([]byte(path + "\x00pane\x00generation"))
	return filepath.Join("/tmp", "pmx-ce-"+hex.EncodeToString(digest[:16]), "codex-host.sock")
}

// This isolated test-binary protocol fixture owns sleep and records actual Wait.
// It imports only public SDK types and never runs a provider or reads auth.
func TestExternalProjectStopOwnerFixture(t *testing.T) {
	root := os.Getenv("PROJMUX_TEST_PROJECT_STOP_FIXTURE")
	if root == "" {
		return
	}
	path := filepath.Join(root, "state", "metadata", "registry.json")
	socket := externalStopSocket(path)
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(socket))
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "30")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	identities := []core.ProcessIdentity{externalStopIdentity(t, os.Getpid()), externalStopIdentity(t, child.Process.Pid)}
	if err = json.NewEncoder(os.Stdout).Encode(identities); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var req struct {
		Foreground struct {
			Action    string
			Authority struct {
				Binding struct{ Agent, Pane, Generation string }
			}
		}
	}
	if err = json.NewDecoder(conn).Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req.Foreground.Action != "stop" || req.Foreground.Authority.Binding.Agent != "agent" || req.Foreground.Authority.Binding.Pane != "pane" || req.Foreground.Authority.Binding.Generation != "generation" {
		t.Fatal("wrong exact Stop request")
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err == nil {
		t.Fatal("fixture kill did not reach actual Wait")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]json.RawMessage
	if err = json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	var agents []core.Agent
	var panes []core.Pane
	if err = json.Unmarshal(reg["agents"], &agents); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(reg["panes"], &panes); err != nil {
		t.Fatal(err)
	}
	code := child.ProcessState.ExitCode()
	wait := &core.TerminationEvidence{Source: core.TerminationSourceSupervisor, Classification: core.TerminationNormal, ObservedAt: time.Now(), AgentUID: "agent", PaneUID: "pane", Generation: "generation", OperationID: "operation", ExitCode: &code}
	if code < 0 {
		wait.ExitCode = nil
		wait.Signal = "killed"
	}
	agents[0].Status.Phase = core.PhaseOffline
	agents[0].Status.LastTermination = wait
	panes[0].Status.LastTermination = wait
	panes[0].Status.Activation = core.PaneActivation{}
	panes[0].Status.ProcessSession.ResumeState = core.ProcessResumable
	reg["agents"], err = json.Marshal(agents)
	if err != nil {
		t.Fatal(err)
	}
	reg["panes"], err = json.Marshal(panes)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".fixture", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".fixture", path); err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(conn).Encode(map[string]bool{"Accepted": true}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalHeadlessProjectStopConsumer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel-birth protocol fixture uses Linux procfs")
	}
	for _, mode := range []string{"born", "partial", "claim"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), strings.Repeat("long-root-", 16))
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(root, "state")
			path := filepath.Join(state, "metadata", "registry.json")
			fixture := exec.Command(os.Args[0], "-test.run=^TestExternalProjectStopOwnerFixture$")
			fixture.Env = append(os.Environ(), "PROJMUX_TEST_PROJECT_STOP_FIXTURE="+root)
			out, err := fixture.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			fixture.Stderr = os.Stderr
			if err = fixture.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			reaped := false
			defer func() {
				_ = fixture.Process.Kill()
				if reaped {
					return
				}
				select {
				case <-done:
				case <-time.After(time.Second):
				}
			}()
			var identities []core.ProcessIdentity
			if err = json.NewDecoder(out).Decode(&identities); err != nil {
				t.Fatal(err)
			}
			go func() { done <- fixture.Wait() }()
			meta := func(uid, name string, kind core.Kind, owner string) core.ObjectMeta {
				m := core.ObjectMeta{UID: uid, Name: name}
				if owner != "" {
					m.OwnerRef = &core.OwnerRef{Kind: kind, UID: owner}
				}
				return m
			}
			project := core.Project{APIVersion: "projmux.io/v1alpha1", Kind: core.KindProject, Metadata: meta("project", "project", "", ""), Spec: core.ProjectSpec{Root: root, PrimaryWindowRef: "window"}}
			window := core.Window{APIVersion: "projmux.io/v1alpha1", Kind: core.KindWindow, Metadata: meta("window", "window", core.KindProject, "project"), Spec: core.WindowSpec{AnchorPaneRef: "pane"}}
			binding := core.ProcessBinding{HostInstanceID: "host", ProjectUID: "project", WindowUID: "window", AgentUID: "agent", PaneUID: "pane", Generation: "generation", OperationID: "operation"}
			agent := core.Agent{APIVersion: "projmux.io/v1alpha1", Kind: core.KindAgent, Metadata: meta("agent", "worker", core.KindWindow, "window"), Spec: core.AgentSpec{Provider: "codex"}, Status: core.AgentStatus{Phase: core.PhaseRunning, PaneRef: "pane"}}
			pane := core.Pane{APIVersion: "projmux.io/v1alpha1", Kind: core.KindPane, Metadata: meta("pane", "worker-pane", core.KindAgent, "agent"), Spec: core.PaneSpec{Runtime: core.PaneRuntimeSpec{Kind: core.RuntimeProcess}, Role: core.PaneRoleAgent}}
			pane.Status.ProcessSession = &core.ProcessSessionRecord{Provider: "codex", Binding: binding, ThreadID: "conversation", ConnectionID: "operation", ResumeState: core.ProcessResumeUnknown}
			pane.Status.Activation = core.PaneActivation{Kind: core.RuntimeProcess, Generation: binding.Generation, AgentUID: "agent", OperationID: binding.OperationID, Process: &core.ProcessActivation{Binding: binding, HostProcess: identities[0], Child: identities[1]}}
			if mode == "claim" {
				pane.Status.Activation = core.PaneActivation{}
			}
			agents := []core.Agent{agent}
			panes := []core.Pane{pane}
			reservations := []map[string]any{{"kind": "Project", "uid": "project", "name": "project"}, {"scope": "project", "kind": "Window", "uid": "window", "name": "window"}, {"scope": "project", "kind": "Agent", "uid": "agent", "name": "worker"}, {"scope": "project", "kind": "Pane", "uid": "pane", "name": "worker-pane"}}
			if mode == "partial" {
				failed := agent
				failed.Metadata = meta("failed", "failed", core.KindWindow, "window")
				failed.Status.PaneRef = "failed-pane"
				failedPane := pane
				failedPane.Metadata = meta("failed-pane", "failed-pane", core.KindAgent, "failed")
				failedPane.Status.Activation = core.PaneActivation{}
				session := *pane.Status.ProcessSession
				session.Binding.AgentUID = "failed"
				session.Binding.PaneUID = "failed-pane"
				failedPane.Status.ProcessSession = &session
				agents = append(agents, failed)
				panes = append(panes, failedPane)
				reservations = append(reservations, map[string]any{"scope": "project", "kind": "Agent", "uid": "failed", "name": "failed"}, map[string]any{"scope": "project", "kind": "Pane", "uid": "failed-pane", "name": "failed-pane"})
			}
			raw, err := json.Marshal(map[string]any{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": []core.Project{project}, "windows": []core.Window{window}, "agents": agents, "panes": panes, "nameReservations": reservations})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: state, HomeDir: root})
			if err != nil {
				t.Fatal(err)
			}
			poison := t.TempDir()
			for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PROJMUX_CONFIG_DIR", "PROJMUX_STATE_DIR", "PROJMUX_REGISTRY_PATH", "TMUX", "TMUX_PANE"} {
				t.Setenv(key, poison)
			}
			result, err := c.StopProject(context.Background(), core.StopProjectRequest{Ref: "uid:project", SocketPath: filepath.Join(filepath.Dir(externalStopSocket(path)), "missing-tmux.sock")})
			if mode == "claim" {
				var refusal *projmux.Refusal
				after, readErr := os.ReadFile(path)
				if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProjectStopExecutionFailed || result.Stopped || result.Receipt.Operation != "" || readErr != nil || string(after) != string(raw) {
					t.Fatalf("claim invented effects %+v %v", result, err)
				}
				conn, e := net.Dial("unix", externalStopSocket(path))
				if e != nil {
					t.Fatal(e)
				}
				cleanup := map[string]any{"Foreground": map[string]any{"Action": "stop", "Authority": map[string]any{"Binding": map[string]string{"Agent": "agent", "Pane": "pane", "Generation": "generation"}}}}
				if e = json.NewEncoder(conn).Encode(cleanup); e != nil {
					t.Fatal(e)
				}
				var reply map[string]bool
				if e = json.NewDecoder(conn).Decode(&reply); e != nil {
					t.Fatal(e)
				}
				_ = conn.Close()
				if e = <-done; e != nil {
					t.Fatal(e)
				}
				reaped = true

			} else {
				if result.Stopped != (mode == "born") || result.Receipt.Operation != core.OperationStopProject || len(result.Receipt.AffectedUIDs) != 2 || result.Receipt.AffectedUIDs[1].UID != "agent" {
					t.Fatalf("stop %+v %v", result, err)
				}
				if mode == "born" && err != nil {
					t.Fatal(err)
				}
				if mode == "partial" {
					var refusal *projmux.Refusal
					if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProjectStopExecutionFailed || !strings.Contains(err.Error(), "uid:failed") {
						t.Fatal(err)
					}
				}
				select {
				case err := <-done:
					reaped = true
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("fixture owner remains")
				}
				for _, id := range identities {
					if _, e := os.Stat(fmt.Sprintf("/proc/%d", id.PID)); !errors.Is(e, os.ErrNotExist) {
						t.Fatalf("fixture kernel process remains: %d", id.PID)
					}
				}
				retained, e := c.GetResource(context.Background(), core.Ref{Kind: core.KindAgent, Value: "uid:agent"})
				if e != nil || retained.(core.Agent).Status.Phase != core.PhaseOffline {
					t.Fatalf("retained Agent %+v %v", retained, e)
				}
				retained, e = c.GetResource(context.Background(), core.Ref{Kind: core.KindPane, Value: "uid:pane"})
				if e != nil || retained.(core.Pane).Status.ProcessSession.ThreadID != "conversation" || retained.(core.Pane).Status.LastTermination == nil {
					t.Fatalf("retained Pane %+v %v", retained, e)
				}
			}
			entries, e := os.ReadDir(poison)
			if e != nil || len(entries) != 0 {
				t.Fatal("ambient root written")
			}
		})
	}
}

func TestExternalResumeRecipeDetail(t *testing.T) {
	original := &core.ResumeRecipeChangedError{Code: "process-resume-refused", Reason: "relaunch-no-conversation", AgentUID: "agent-example", Changes: []core.ResumeRecipeChange{{Field: "model", Reason: "model-changed", Recorded: []string{"old"}, Current: []string{"new"}}}, RelaunchCommand: "projmux agent relaunch uid:agent-example --detach", Guidance: "preserves Agent UID, Pane UID, and the recorded conversation"}
	var detail *projmux.ResumeRecipeChangedError
	if !errors.As(fmt.Errorf("wrapped: %w", original), &detail) || detail != original {
		t.Fatal("public aliases lost errors.As identity")
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var decoded projmux.ResumeRecipeChangedError
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Code != original.Code || decoded.Reason != original.Reason || len(decoded.Changes) != 1 || decoded.Changes[0].Current[0] != "new" || decoded.RelaunchCommand != original.RelaunchCommand {
		t.Fatalf("public JSON %s: %v", raw, err)
	}
}

// The public CLI creates this detached fixture; SDK CreateAgent remains tmux-only.
func TestExternalClaudeDetachedFirstInput(t *testing.T) {
	binary := os.Getenv("PMX_TEST_CLI")
	if binary == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, text := range []string{"first input"} {
		t.Run(fmt.Sprintf("payload=%d", len(text)), func(t *testing.T) {
			root, err := os.MkdirTemp(os.TempDir(), "pc-sdk-")
			if err != nil {
				t.Fatal(err)
			}
			cleanupConfirmed := true // No detached creation has been attempted yet.
			defer func() {
				if cleanupConfirmed {
					_ = os.RemoveAll(root)
				} else {
					t.Logf("preserving unresolved first-input fixture root=%s", root)
				}
			}()
			script := `import os,sys,json,socket,subprocess
root=os.path.dirname(__file__)
if os.path.exists(root+'/provider.sock'):os.unlink(root+'/provider.sock')
s=socket.socket(socket.AF_UNIX);s.bind(root+'/provider.sock');os.chmod(root+'/provider.sock',0o600);s.listen()
os.environ['CLAUDE_CODE_MESSAGING_SOCKET']=root+'/provider.sock'
os.environ['CLAUDE_CODE_MESSAGING_TOKEN']='isolated-fixture-token'
p=subprocess.run([os.environ['PMX_TEST_PROCESS_BINARY'],'internal','claude-endpoint-register'],input=json.dumps({'hook_event_name':'SessionStart','session_id':'first-session'}).encode(),stdout=subprocess.PIPE,stderr=subprocess.PIPE)
if p.returncode or p.stdout or p.stderr: raise Exception((p.returncode,p.stdout,p.stderr))
open(root+'/hook-done','w').write('observed')
for line in sys.stdin:
 f=json.loads(line);assert f['type']=='user'
 with open(root+'/wire.jsonl','a') as out: out.write(line)
 print(json.dumps({'type':'system','subtype':'init','session_id':'first-session'}),flush=True)
 print(json.dumps({'type':'result','subtype':'success','session_id':'first-session'}),flush=True)
s.close()
`
			provider := filepath.Join(root, "provider.py")
			if err = os.WriteFile(provider, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			wrapper := fmt.Sprintf("#!/bin/sh\nexport PMX_TEST_PROCESS_BINARY='%s'\nexec python3 -u '%s'\n", binary, provider)
			if err = os.WriteFile(filepath.Join(root, "claude"), []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			env := []string{}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key == "HOME" || key == "PATH" || key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == "CLAUDE_CONFIG_DIR" || key == "CODEX_HOME" || key == "CODEX_API_KEY" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") || strings.HasPrefix(key, "PMX_") || strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "OPENAI_") {
					continue
				}
				env = append(env, entry)
			}
			tmuxDir := filepath.Join(root, "tmux")
			if err = os.MkdirAll(tmuxDir, 0700); err != nil {
				t.Fatal(err)
			}
			env = append(env, "TMUX_TMPDIR="+tmuxDir)
			env = append(env, "HOME="+root, "PATH="+root+":"+os.Getenv("PATH"), "XDG_CONFIG_HOME="+root+"/config", "XDG_STATE_HOME="+root+"/state", "XDG_CACHE_HOME="+root+"/cache", "CLAUDE_CONFIG_DIR="+root+"/.claude", "CODEX_HOME="+root+"/.codex")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cli := func(args ...string) ([]byte, error) {
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Env = env
				return cmd.CombinedOutput()
			}
			out, err := cli("create", "project", "--root", root, "--name", "first-input", "-o", "uid")
			if err != nil {
				t.Fatalf("project %v %s", err, out)
			}
			project := strings.TrimPrefix(strings.TrimSpace(string(out)), "uid:")
			c, err := projmux.NewClient(projmux.Options{ConfigDir: root + "/config/projmux", StateDir: root + "/state/projmux", HomeDir: root})
			if err != nil {
				t.Fatal(err)
			}
			cleanupConfirmed = false
			out, err = cli("create", "agent", "--host", "process", "--detach", "--provider", "claude", "--project", "uid:"+project, "--create-window", "--window", "first-window", "--name", "probe-first-input")
			if err != nil {
				t.Fatalf("create %v %s", err, out)
			}
			fields := strings.Fields(string(out))
			if len(fields) < 2 || fields[0] != "agent" {
				t.Fatalf("receipt %s", out)
			}
			ref := fields[1]
			var owned core.ProcessActivation
			defer func() {
				cancel()
				if ctx.Err() == nil {
					t.Error("caller context was not canceled before cleanup")
				}
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cleanupCancel()
				stopped, err := c.StopAgent(cleanupCtx, core.StopAgentRequest{Ref: ref, Binding: owned.Binding})
				if err != nil {
					t.Errorf("fixture Stop ref=%s root=%s: %v", ref, root, err)
					return
				}
				wait := stopped.Termination
				if owned.Binding.AgentUID == "" || stopped.Binding != owned.Binding || !stopped.Stopped || stopped.ClaimReleased ||
					wait == nil || wait.Source != core.TerminationSourceSupervisor || wait.ObservedAt.IsZero() ||
					wait.AgentUID != owned.Binding.AgentUID || wait.PaneUID != owned.Binding.PaneUID ||
					wait.Generation != owned.Binding.Generation || wait.OperationID != owned.Binding.OperationID || (wait.ExitCode == nil && wait.Signal == "") {
					t.Errorf("fixture exact Wait/Stop retirement not confirmed ref=%s root=%s", ref, root)
					return
				}
				resource, err := c.GetResource(cleanupCtx, core.Ref{Kind: core.KindPane, Value: "uid:" + owned.Binding.PaneUID})
				if err != nil || !resource.(core.Pane).Status.Activation.IsZero() {
					t.Errorf("fixture activation not retired ref=%s root=%s: %v", ref, root, err)
					return
				}
				cleanupConfirmed = true
				t.Log("canceled caller; exact supervisor Wait and public Stop retirement confirmed")
			}()
			resource, err := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: ref})
			if err != nil {
				t.Fatal(err)
			}
			resource, err = c.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + resource.(core.Agent).Status.PaneRef})
			if err != nil || resource.(core.Pane).Status.Activation.Process == nil {
				t.Fatalf("fixture activation: %v", err)
			}
			owned = *resource.(core.Pane).Status.Activation.Process
			for {
				if _, err = os.Stat(filepath.Join(root, "hook-done")); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("SessionStart barrier", ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
			refused, err := c.DeliverTurn(ctx, core.TurnRequest{Ref: ref, Text: "unsupported"})
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeTurnProviderUnsupported || refused.Accepted || refused.OutcomeUnknown {
				t.Fatalf("DeliverTurn %+v %v", refused, err)
			}
			if raw, _ := os.ReadFile(filepath.Join(root, "wire.jsonl")); len(raw) != 0 {
				t.Fatal("unsupported call wrote input")
			}
			accepted, err := c.StartTurn(ctx, core.TurnRequest{Ref: ref, Text: text})
			if err != nil || !accepted.Accepted || accepted.OutcomeUnknown || accepted.Operation == "" {
				t.Fatalf("StartTurn %+v %v", accepted, err)
			}
			for {
				raw, _ := os.ReadFile(filepath.Join(root, "wire.jsonl"))
				if len(raw) > 0 {
					var f struct{ Message struct{ Content string } }
					if err = json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &f); err != nil || f.Message.Content != text {
						t.Fatalf("one exact frame bytes=%d err=%v", len(raw), err)
					}
					t.Logf("public first input payload=%d actual frame=%d", len(text), len(raw))
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("frame consumption", ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
		})
	}
}

// A separate public-only caller exits after CreateAgent. The parent retains
// only public resources and proves the independent owner's lifetime and Wait.
func TestExternalSDKDetachedProcessCreateCallerExit(t *testing.T) {
	if root := os.Getenv("PMX_SDK_CREATE_CHILD"); root != "" {
		provider := os.Getenv("PMX_SDK_CREATE_PROVIDER")
		c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: root + "/deep/custom-settings", StateDir: root + "/deep/custom-records", ProjmuxExecutable: os.Getenv("PMX_SDK_CREATE_BINARY")})
		if err != nil {
			t.Fatal(err)
		}
		project, err := c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "sdk-process"})
		if err != nil {
			t.Fatal(err)
		}
		// Change only this fixture caller's ambient inputs after construction.
		for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PATH", "CODEX_HOME"} {
			if err = os.Setenv(key, root+"/poison"); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + project.Project.Metadata.UID, Window: "uid:" + project.Windows[0].Metadata.UID, Provider: provider, Profile: "none", Model: "stub-model", Effort: "low", Host: "process", Detach: true, Name: "public-only", Payload: []string{"first input"}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		result.Projection = core.ResourceList{}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(root+"/result.json", raw, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	binary := os.Getenv("PMX_TEST_CLI")
	if binary == "" {
		t.Skip("copied product binary required")
	}
	if runtime.GOOS != "linux" {
		t.Skip("caller-exit birth observation uses procfs")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			// Own a short hash lease even under the CI runner's long TMPDIR.
			lease := filepath.Dir(externalStopSocket(filepath.Join(t.TempDir(), "sdk-create")))
			if err := os.Mkdir(lease, 0700); err != nil {
				t.Fatal(err)
			}
			t.Logf("owned fixture root=%s", lease)
			for _, dir := range []string{lease + "/.codex", lease + "/.claude"} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			c, err := projmux.NewClient(projmux.Options{HomeDir: lease, ConfigDir: lease + "/deep/custom-settings", StateDir: lease + "/deep/custom-records"})
			if err != nil {
				t.Fatal(err)
			}
			var result core.CreateAgentResult
			cleaned := false
			stopAttempted := false
			defer func() {
				if result.Agent.Metadata.UID != "" && !cleaned && !stopAttempted {
					stopAttempted = true
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if _, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + result.Agent.Metadata.UID, Binding: result.Binding}); err != nil {
						t.Errorf("cleanup: %v; preserving %s", err, lease)
						return
					}
				}
				if cleaned {
					_ = os.RemoveAll(lease)
				}
			}()
			script := `import os,sys,json,socket,subprocess
root=os.environ['HOME']
if os.path.exists(root+'/provider.sock'):os.unlink(root+'/provider.sock')
s=socket.socket(socket.AF_UNIX);s.bind(root+'/provider.sock');os.chmod(root+'/provider.sock',0o600);s.listen()
os.environ['CLAUDE_CODE_MESSAGING_SOCKET']=root+'/provider.sock'
os.environ['CLAUDE_CODE_MESSAGING_TOKEN']='sdk-create-fixture-token'
p=subprocess.Popen([os.environ['PMX_SDK_CREATE_BINARY'],'internal','claude-endpoint-register'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
open(root+'/hook-pid','w').write(str(p.pid))
out,err=p.communicate(json.dumps({'hook_event_name':'SessionStart','session_id':'sdk-create-session'}).encode())
open(root+'/hook-status','w').write(str((p.returncode,len(out),len(err))))
if p.returncode or out or err: raise Exception('fixture registration failed')
for line in sys.stdin:
 f=json.loads(line);assert f['type']=='user'
 open(root+'/input-observed','w').write(f['message']['content'])
 print(json.dumps({'type':'system','subtype':'init','session_id':'sdk-create-session'}),flush=True)
 print(json.dumps({'type':'result','subtype':'success','session_id':'sdk-create-session'}),flush=True)
s.close()
`
			if provider == "codex" {
				script = `import os,sys,json
root=os.environ['HOME']
def emit(n):print(json.dumps(n),flush=True)
for line in sys.stdin:
 n=json.loads(line);m=n.get('method','')
 if m=='initialize':emit({'id':n['id'],'result':{'userAgent':'projmux/0.160.0'}})
 elif m in ('thread/start','thread/resume'):emit({'id':n['id'],'result':{'thread':{'id':'sdk-create-thread'},'model':'stub-model','reasoningEffort':'low','sandbox':{'type':'readOnly'},'approvalPolicy':'on-request'}})
 elif m=='turn/start':
  open(root+'/input-observed','w').write(n['params']['input'][0]['text'])
  emit({'method':'turn/started','params':{'threadId':'sdk-create-thread','turn':{'id':'turn-1'}}})
  emit({'id':n['id'],'result':{'turn':{'id':'turn-1'}}})
  emit({'method':'turn/completed','params':{'threadId':'sdk-create-thread','turn':{'id':'turn-1','status':'completed'}}})
`
			}
			script = strings.ReplaceAll(script, "os.environ['PMX_SDK_CREATE_BINARY']", fmt.Sprintf("%q", binary))
			if err = os.WriteFile(lease+"/provider.py", []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			wrapper := fmt.Sprintf("#!/bin/sh\nexec %q -u %q\n", python, lease+"/provider.py")
			if err = os.WriteFile(lease+"/"+provider, []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(lease+"/tmux", 0700); err != nil {
				t.Fatal(err)
			}
			env := []string{}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key == "HOME" || key == "PATH" || key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == "CODEX_HOME" || key == "CLAUDE_CONFIG_DIR" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") || strings.HasPrefix(key, "PMX_") || strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "ANTHROPIC_") {
					continue
				}
				env = append(env, entry)
			}
			env = append(env, "HOME="+lease, "PATH="+lease+":"+os.Getenv("PATH"), "CODEX_HOME="+lease+"/.codex", "CLAUDE_CONFIG_DIR="+lease+"/.claude", "TMUX_TMPDIR="+lease+"/tmux", "XDG_CONFIG_HOME="+lease+"/ambient-config", "XDG_STATE_HOME="+lease+"/ambient-state", "PMX_SDK_CREATE_CHILD="+lease, "PMX_SDK_CREATE_PROVIDER="+provider, "PMX_SDK_CREATE_BINARY="+binary)
			caller := exec.Command(os.Args[0], "-test.run=^TestExternalSDKDetachedProcessCreateCallerExit$")
			caller.Env = env
			if out, err := caller.CombinedOutput(); err != nil {
				t.Fatalf("caller: %v %s", err, out)
			}
			raw, err := os.ReadFile(lease + "/result.json")
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.Binding.AgentUID != result.Agent.Metadata.UID || result.Binding.PaneUID != result.Agent.Status.PaneRef {
				t.Fatal("binding lost")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			paneResource, err := c.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + result.Binding.PaneUID})
			if err != nil {
				t.Fatal(err)
			}
			pane := paneResource.(core.Pane)
			if pane.Status.Activation.Process == nil {
				t.Fatal("missing born owner")
			}
			activation := *pane.Status.Activation.Process
			if externalStopIdentity(t, activation.HostProcess.PID) != activation.HostProcess || externalStopIdentity(t, activation.Child.PID) != activation.Child {
				t.Fatal("caller exit retired owner/provider")
			}
			for {
				data, e := os.ReadFile(lease + "/input-observed")
				if e == nil && string(data) == "first input" {
					break
				}
				select {
				case <-ctx.Done():
					status, _ := os.ReadFile(lease + "/hook-status")
					pid, _ := os.ReadFile(lease + "/hook-pid")
					wchan, _ := os.ReadFile("/proc/" + string(pid) + "/wchan")
					t.Fatalf("initial input not observed; registration rc/stdout-bytes/stderr-bytes=%s pid=%s wait=%s", status, pid, wchan)
				case <-time.After(10 * time.Millisecond):
				}
			}
			sessionID := func(agent core.Agent) string {
				if agent.Status.SessionRef == nil {
					return ""
				}
				if provider == "claude" && agent.Status.SessionRef.Claude != nil {
					return agent.Status.SessionRef.Claude.SessionID
				}
				if provider == "codex" && agent.Status.SessionRef.Codex != nil {
					return agent.Status.SessionRef.Codex.ThreadID
				}
				return ""
			}
			var beforeCID string
			for beforeCID == "" {
				resource, err := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "uid:" + result.Agent.Metadata.UID})
				if err != nil {
					t.Fatal(err)
				}
				beforeCID = sessionID(resource.(core.Agent))
				if beforeCID != "" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("initial session identity not confirmed")
				case <-time.After(10 * time.Millisecond):
				}
			}
			stopAttempted = true
			stopped, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + result.Agent.Metadata.UID, Binding: result.Binding})
			if err != nil || !stopped.Stopped || stopped.Termination == nil || stopped.Binding != result.Binding {
				t.Fatalf("actual Wait: %+v %v", stopped, err)
			}
			preserved, err := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "uid:" + result.Agent.Metadata.UID})
			if err != nil {
				t.Fatal(err)
			}
			agent := preserved.(core.Agent)
			if agent.Metadata.UID != result.Agent.Metadata.UID || agent.Status.PaneRef != result.Binding.PaneUID || sessionID(agent) != beforeCID {
				t.Fatal("UID/Pane/conversation lost")
			}
			t.Run("SDK detached resume after caller exit", func(t *testing.T) {
				caller := exec.Command(os.Args[0], "-test.run=^TestExternalSDKDetachedProcessResumeCallerExit$")
				caller.Env = env
				if out, err := caller.CombinedOutput(); err != nil {
					t.Fatalf("resume caller: %v %s", err, out)
				}
				raw, err := os.ReadFile(lease + "/resume-result.json")
				if err != nil {
					t.Fatal(err)
				}
				var resumed core.ResumeAgentResult
				if err = json.Unmarshal(raw, &resumed); err != nil {
					t.Fatal(err)
				}
				if resumed.Binding.AgentUID != result.Binding.AgentUID || resumed.Binding.PaneUID != result.Binding.PaneUID || resumed.Binding.Generation == result.Binding.Generation || resumed.ClaimWaiting || resumed.PaneID != "" || sessionID(resumed.Agent) != beforeCID {
					t.Fatalf("resume identity: %+v", resumed)
				}
				result.Binding = resumed.Binding
				stopAttempted = false
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				resource, err := c.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + resumed.Binding.PaneUID})
				if err != nil {
					t.Fatal(err)
				}
				activation := resource.(core.Pane).Status.Activation.Process
				if activation == nil || externalStopIdentity(t, activation.HostProcess.PID) != activation.HostProcess || externalStopIdentity(t, activation.Child.PID) != activation.Child {
					t.Fatal("resume caller exit retired owner")
				}
				stopped, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + agent.Metadata.UID, Binding: resumed.Binding})
				stopAttempted = true
				if err != nil || !stopped.Stopped || stopped.Termination == nil || stopped.Binding != resumed.Binding {
					t.Fatalf("resume Wait: %+v %v", stopped, err)
				}
			})
			t.Run("SDK detached relaunch after caller exit", func(t *testing.T) {
				caller := exec.Command(os.Args[0], "-test.run=^TestExternalSDKDetachedProcessRelaunchCallerExit$")
				caller.Env = env
				if out, err := caller.CombinedOutput(); err != nil {
					t.Fatalf("relaunch caller: %v %s", err, out)
				}
				raw, err := os.ReadFile(lease + "/relaunch-result.json")
				if err != nil {
					t.Fatal(err)
				}
				var relaunched core.RelaunchAgentResult
				if err = json.Unmarshal(raw, &relaunched); err != nil {
					t.Fatal(err)
				}
				if relaunched.Binding.AgentUID != result.Binding.AgentUID || relaunched.Binding.PaneUID != result.Binding.PaneUID || relaunched.Binding.Generation == result.Binding.Generation || relaunched.ClaimWaiting {
					t.Fatalf("relaunch identity: %+v", relaunched)
				}
				result.Binding = relaunched.Binding
				stopAttempted = false
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				resource, err := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "uid:" + agent.Metadata.UID})
				if err != nil {
					t.Fatal(err)
				}
				if sessionID(resource.(core.Agent)) != beforeCID {
					t.Fatal("relaunch replaced conversation")
				}
				pane, err := c.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + result.Binding.PaneUID})
				if err != nil {
					t.Fatal(err)
				}
				activation := pane.(core.Pane).Status.Activation.Process
				if activation == nil || externalStopIdentity(t, activation.HostProcess.PID) != activation.HostProcess || externalStopIdentity(t, activation.Child.PID) != activation.Child {
					t.Fatal("relaunch caller exit retired owner")
				}
				stopped, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + agent.Metadata.UID, Binding: result.Binding})
				stopAttempted = true
				if err != nil || !stopped.Stopped || stopped.Termination == nil {
					t.Fatalf("relaunch Wait: %+v %v", stopped, err)
				}
			})
			if _, err = c.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + agent.Metadata.UID, Confirm: true}); err != nil {
				t.Fatal(err)
			}
			cleaned = true
		})
	}
}

func TestExternalRelaunchAgent(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{HomeDir: t.TempDir(), ConfigDir: t.TempDir(), StateDir: t.TempDir(), SocketPath: "/explicit/socket"})
	if err != nil {
		t.Fatal(err)
	}
	none := ""
	req := core.RelaunchAgentRequest{Ref: "uid:agent", Project: "uid:project", Window: "uid:window", Model: "sonnet", Effort: "high", Profile: &none, Instructions: nil, Reset: []string{"instructions"}, SocketPath: "/explicit/socket", DryRun: true, Confirm: false}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result core.RelaunchAgentResult
	result, err = c.RelaunchAgent(ctx, req)
	if !errors.Is(err, context.Canceled) || result.AgentUID != "" || result.Stop.Committed || result.Resume.PaneID != "" {
		t.Fatalf("cancelled external relaunch: %+v %v", result, err)
	}
	_, err = c.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{})
	var refused *projmux.Refusal
	if !errors.As(err, &refused) || refused.Code != projmux.CodeSelectorInvalid {
		t.Fatalf("explicit target refusal: %v", err)
	}
	_, err = c.RelaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:agent", SocketPath: "relative", DryRun: true})
	if !errors.As(err, &refused) || refused.Code != projmux.CodeRuntimeRequestInvalid {
		t.Fatalf("explicit socket refusal: %v", err)
	}
	// All plan/settings/partial-failure types are usable from a separate module.
	_ = core.RelaunchAgentResult{AgentUID: "agent", Phase: core.PhaseRunning, CurrentSettings: core.RelaunchAgentSettings{Profile: core.RelaunchAgentProfile{Name: "base", Digest: "digest", Source: "profile"}, Model: core.RelaunchAgentSetting{Value: "sonnet", Source: "relaunch", ProfileValue: "sonnet", Override: true}}, Recovery: "projmux agent resume uid:agent"}
	for _, code := range []projmux.Code{projmux.CodeRelaunchProcessUnsupported, projmux.CodeRelaunchSelfTarget, projmux.CodeRelaunchSourceStale, projmux.CodeRelaunchRequestInvalid, projmux.CodeRelaunchAgentBusy, projmux.CodeRelaunchNoConversation, projmux.CodeRelaunchProviderUnsupported, projmux.CodeRelaunchExecutionFailed, projmux.CodeRelaunchStopAlive, projmux.CodeRelaunchStopUnknown} {
		if code == "" {
			t.Fatal("empty public relaunch code")
		}
	}
}

// Session reads consume public types and fixture bytes, with no provider input.
func TestExternalAgentSessionsConsumer(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	registryPath := filepath.Join(state, "metadata", "registry.json")
	if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
		t.Fatal(err)
	}
	const projectUID = "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"
	project := core.Project{APIVersion: "projmux.io/v1alpha1", Kind: core.KindProject, Metadata: core.ObjectMeta{UID: projectUID, Name: "sample"}, Spec: core.ProjectSpec{Root: root, PrimaryWindowRef: "window"}}
	window := core.Window{APIVersion: "projmux.io/v1alpha1", Kind: core.KindWindow, Metadata: core.ObjectMeta{UID: "window", Name: "work", OwnerRef: &core.OwnerRef{Kind: core.KindProject, UID: projectUID}}, Spec: core.WindowSpec{AnchorPaneRef: "pane"}}
	observed := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	agent := core.Agent{APIVersion: "projmux.io/v1alpha1", Kind: core.KindAgent, Metadata: core.ObjectMeta{UID: "agent", Name: "worker", OwnerRef: &core.OwnerRef{Kind: core.KindWindow, UID: "window"}}, Spec: core.AgentSpec{Provider: "codex"}, Status: core.AgentStatus{Phase: core.PhaseOffline, PaneRef: "pane", SessionRef: &core.AgentSessionRef{Provider: "codex", ObservedAt: observed, Codex: &core.CodexSessionRef{ThreadID: "current"}}}}
	pane := core.Pane{APIVersion: "projmux.io/v1alpha1", Kind: core.KindPane, Metadata: core.ObjectMeta{UID: "pane", Name: "worker-pane", OwnerRef: &core.OwnerRef{Kind: core.KindAgent, UID: "agent"}}, Spec: core.PaneSpec{Role: core.PaneRoleAgent, Runtime: core.PaneRuntimeSpec{Kind: core.RuntimeProcess}}}
	registry, err := json.Marshal(map[string]any{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": []core.Project{project}, "windows": []core.Window{window}, "agents": []core.Agent{agent}, "panes": []core.Pane{pane}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registryPath, registry, 0600); err != nil {
		t.Fatal(err)
	}
	last := observed.Add(-time.Minute)
	row := core.AgentSession{AgentUID: "agent", Provider: "codex", SessionID: "previous", ObservedAt: last, LastRecordAt: &last, Source: core.SessionSourceEstimated, ProjectUID: projectUID, WindowUID: "window", AgentName: "worker"}
	history, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, '\n')
	historyPath := filepath.Join(state, "agent-session-history.jsonl")
	if err = os.WriteFile(historyPath, history, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := projmux.ListAgentSessionsRequest{Agent: "worker", Project: "sample", Windows: []string{"work"}}
	var listed core.AgentSessionsList
	listed, err = client.ListAgentSessions(ctx, request)
	if err != nil || len(listed.Sessions) != 2 || listed.Sessions[0].LastRecordAt == nil || listed.Sessions[1].TranscriptPath != "" || listed.Sessions[1].Source != core.SessionSourceCurrent {
		t.Fatalf("Agent sessions: %+v %v", listed, err)
	}
	*listed.Sessions[0].LastRecordAt = time.Time{}
	listed.Sessions[0].SessionID = "changed"
	listed, err = client.ListAgentSessions(ctx, request)
	if err != nil || listed.Sessions[0].SessionID != "previous" || listed.Sessions[0].LastRecordAt.IsZero() {
		t.Fatalf("Agent result ownership: %+v %v", listed, err)
	}
	projectRequest := projmux.ListProjectSessionsRequest{Project: "uid:" + projectUID}
	var projected core.ProjectSessionsList
	projected, err = client.ListProjectSessions(ctx, projectRequest)
	if err != nil || len(projected.Sessions) != 2 || len(projected.Sessions[0].Agents) != 1 || projected.Sessions[0].Agents[0].Basis != core.SessionAffiliationRecorded {
		t.Fatalf("Project sessions: %+v %v", projected, err)
	}
	projected.Sessions[0].Agents[0].AgentName = "changed"
	*projected.Sessions[0].LastRecordAt = time.Time{}
	projected, err = client.ListProjectSessions(ctx, projectRequest)
	if err != nil || projected.Sessions[0].Agents[0].AgentName != "worker" || projected.Sessions[0].LastRecordAt.IsZero() {
		t.Fatalf("Project result ownership: %+v %v", projected, err)
	}
	_, err = client.ListAgentSessions(ctx, projmux.ListAgentSessionsRequest{})
	var refused *projmux.Refusal
	if !errors.As(err, &refused) || refused.Code != projmux.CodeSelectorInvalid {
		t.Fatalf("explicit Agent refusal: %v", err)
	}
	_, err = client.ListProjectSessions(ctx, projmux.ListProjectSessionsRequest{})
	if !errors.As(err, &refused) || refused.Code != projmux.CodeSelectorInvalid {
		t.Fatalf("explicit Project refusal: %v", err)
	}
	_, err = client.ListProjectSessions(ctx, projmux.ListProjectSessionsRequest{Project: "missing"})
	if !errors.As(err, &refused) || refused.Code != projmux.CodeResourceNotFound {
		t.Fatalf("Project refusal: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = client.ListProjectSessions(canceled, projectRequest); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = client.ListAgentSessions(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for path, before := range map[string][]byte{registryPath: registry, historyPath: history} {
		after, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatalf("session read wrote %s: %v", filepath.Base(path), err)
		}
	}
	// A fixture change is setup for a refused read, not an SDK mutation.
	agent.Spec.Provider = "antigravity"
	agent.Status.SessionRef = nil
	unsupported, err := json.Marshal(map[string]any{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": []core.Project{project}, "windows": []core.Window{window}, "agents": []core.Agent{agent}, "panes": []core.Pane{pane}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registryPath, unsupported, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = client.ListAgentSessions(ctx, request)
	if !errors.As(err, &refused) || refused.Code != projmux.CodeSessionsProviderUnsupported {
		t.Fatalf("provider refusal: %v", err)
	}
	unchanged, err := os.ReadFile(registryPath)
	if err != nil || sha256.Sum256(unchanged) != sha256.Sum256(unsupported) {
		t.Fatalf("refused read changed Registry: %v", err)
	}

	// This frozen StateDir cannot be read as a Registry directory. Missing
	// target admission must still refuse before trying that read.
	badState := filepath.Join(root, "state-file")
	if err = os.WriteFile(badState, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	badClient, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: badState})
	if err != nil {
		t.Fatal(err)
	}
	_, err = badClient.ListProjectSessions(ctx, projmux.ListProjectSessionsRequest{})
	if !errors.As(err, &refused) || refused.Code != projmux.CodeSelectorInvalid {
		t.Fatalf("missing Project read frozen invalid state: %v", err)
	}
	_, err = badClient.ListProjectSessions(ctx, projectRequest)
	if !errors.As(err, &refused) || refused.Code != projmux.CodeRegistryUnavailable {
		t.Fatalf("invalid frozen state did not fail a real read: %v", err)
	}
	if _, err = badClient.ListProjectSessions(canceled, projmux.ListProjectSessionsRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	badAfter, err := os.ReadFile(badState)
	if err != nil || string(badAfter) != "not a directory" {
		t.Fatalf("invalid frozen state changed: %v", err)
	}

	if _, err = os.Stat(filepath.Join(root, "config")); !os.IsNotExist(err) {
		t.Fatalf("session read materialized config: %v", err)
	}
}

// Invoked in an independent caller by the two-provider public lifecycle fixture.
func TestExternalSDKDetachedProcessResumeCallerExit(t *testing.T) {
	root := os.Getenv("PMX_SDK_CREATE_CHILD")
	if root == "" {
		return
	}
	raw, err := os.ReadFile(root + "/result.json")
	if err != nil {
		t.Fatal(err)
	}
	var previous core.CreateAgentResult
	if err = json.Unmarshal(raw, &previous); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: root + "/deep/custom-settings", StateDir: root + "/deep/custom-records", ProjmuxExecutable: os.Getenv("PMX_SDK_CREATE_BINARY")})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if err = os.Setenv(key, root+"/poison"); err != nil {
			t.Fatal(err)
		}
	}
	request := core.ResumeAgentRequest{Ref: "uid:" + previous.Agent.Metadata.UID, Detach: true, Binding: previous.Binding}
	if os.Getenv("PMX_SDK_CREATE_PROVIDER") == "claude" {
		request.Prompt = "resumed input"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if os.Getenv("PMX_SDK_CREATE_PROVIDER") == "claude" {
		prepared, prepareErr := c.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: request.Ref, Binding: request.Binding})
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		valid, validateErr := c.ValidateAgentResumePreparation(ctx, core.ValidateAgentResumePreparationRequest{Ref: request.Ref, Preparation: prepared.Preparation})
		if validateErr != nil || valid != prepared.Preparation {
			t.Fatal("public preparation", valid, validateErr)
		}
		request.Preparation = &valid
	}
	result, err := c.ResumeAgent(ctx, request)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	result.Projection = core.ResourceList{}
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(root+"/resume-result.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExternalSDKDetachedProcessRelaunchCallerExit(t *testing.T) {
	root := os.Getenv("PMX_SDK_CREATE_CHILD")
	if root == "" {
		return
	}
	raw, err := os.ReadFile(root + "/resume-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var previous core.ResumeAgentResult
	if err = json.Unmarshal(raw, &previous); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: root + "/deep/custom-settings", StateDir: root + "/deep/custom-records", ProjmuxExecutable: os.Getenv("PMX_SDK_CREATE_BINARY")})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if err = os.Setenv(key, root+"/poison"); err != nil {
			t.Fatal(err)
		}
	}
	request := core.RelaunchAgentRequest{Ref: "uid:" + previous.Agent.Metadata.UID, Detach: true, Binding: previous.Binding}
	if os.Getenv("PMX_SDK_CREATE_PROVIDER") == "claude" {
		request.Prompt = "resumed input"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	result, err := c.RelaunchAgent(ctx, request)
	cancel()
	if err != nil {
		t.Fatal(err)
	}

	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(root+"/relaunch-result.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
}
