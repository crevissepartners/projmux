package consumer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
