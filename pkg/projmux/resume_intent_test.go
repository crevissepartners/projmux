package projmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// resumeFixtureCatalog is a Codex app-server catalog of fixed threads.
type resumeFixtureCatalog struct {
	threads []codexappserver.CatalogThread
}

func (c resumeFixtureCatalog) List(context.Context, codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	return codexappserver.CatalogPage{Threads: c.threads}, nil
}

func (c resumeFixtureCatalog) Read(_ context.Context, id string) (codexappserver.CatalogThread, error) {
	for _, thread := range c.threads {
		if thread.ID == id {
			return thread, nil
		}
	}
	return codexappserver.CatalogThread{}, errors.New("no thread " + id)
}

func (resumeFixtureCatalog) Close() error { return nil }

// useResumeCatalog points the fixture's discoveries at catalog instead of the
// default Codex app-server, which the fixture never starts.
func (f windowIntentFixture) useResumeCatalog(catalog aisessions.CodexCatalog) {
	f.c.resumeSources = func(opts *mutationruntime.ResumeDiscoveryOptions) {
		opts.CodexNative = nil
		opts.OpenCodexCatalog = func(context.Context) (aisessions.CodexCatalog, error) {
			if catalog == nil {
				return nil, codexappserver.ErrUnsupported
			}
			return catalog, nil
		}
	}
}

// writeClaudeResumeSession records a Claude transcript of cwd in the fixture
// home with mtime at and returns its path.
func (f windowIntentFixture) writeClaudeResumeSession(t *testing.T, id, cwd string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(f.c.discoveryHome, ".claude", "projects", aisessions.EncodeClaudeProjectPath(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	body := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"gitBranch":"main","message":{"content":"Session %s"}}`+"\n", id, cwd, id)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestListResumeCandidatesMatchesPicker holds ListResumeCandidates to the
// discovery the CLI resume picker runs (`ai_resume_live.go` builds the same
// service ResumeDiscovery): the rows of every enabled provider, a Codex
// app-server row included, in the picker's order, and one provider alone.
func TestListResumeCandidatesMatchesPicker(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	if err := config.SaveAIEnabledAgentsFile(f.c.paths.AIEnabledAgentsFile(), []config.AIAgentProvider{config.AIAgentCodex, config.AIAgentClaude}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.writeClaudeResumeSession(t, "11111111-2222-4333-8444-000000000001", f.workspace, base)
	f.writeClaudeResumeSession(t, "11111111-2222-4333-8444-000000000002", f.workspace, base.Add(-time.Hour))
	f.useResumeCatalog(resumeFixtureCatalog{threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000003", CWD: f.workspace, Name: "native", UpdatedAt: base.Add(time.Minute), RecencyAt: base.Add(time.Minute)}}})

	home := func() (string, error) { return f.c.discoveryHome, nil }
	lookup := func(key string) string { return f.c.discoveryEnv[key] }
	opts := mutationruntime.ResumeDiscoveryOptions{
		Ctx: ctx, Cwd: f.workspace, Home: f.c.discoveryHome, ProviderEnabled: map[string]bool{"codex": true, "claude": true},
		Depth:             mutationruntime.ResolveResumeScanDepth(home, lookup, f.workspace).Depth,
		Limit:             mutationruntime.ResolveResumePickerLimit(home, lookup, f.workspace).Limit,
		PopulationTimeout: mutationruntime.ResumeSummaryPopulationBudget, PopulationContext: context.WithTimeout,
		Budgets:  mutationruntime.DefaultResumeBudgets(),
		Discover: aisessions.DiscoverResumeSummariesContext,
	}
	f.c.resumeSources(&opts)
	want := []string{}
	for _, summary := range mutationruntime.NewResumeDiscovery(opts).Populate().Summaries {
		want = append(want, summary.Provider+" "+summary.ResumeID+" "+summary.Source)
	}

	rows := func(query core.ResumeCandidateQuery) []string {
		t.Helper()
		candidates, err := f.c.ListResumeCandidates(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, candidate := range candidates {
			if candidate.Cwd != f.workspace {
				t.Fatalf("candidate cwd %q, want %q", candidate.Cwd, f.workspace)
			}
			out = append(out, candidate.Provider+" "+candidate.Conversation+" "+candidate.Source)
		}
		return out
	}
	got := rows(core.ResumeCandidateQuery{Project: "uid:" + f.project})
	if strings.Join(got, "\n") != strings.Join(want, "\n") || len(got) != 3 {
		t.Fatalf("candidates\n%s\nwant picker rows\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	sources := map[string]bool{}
	for _, row := range got {
		sources[row[strings.LastIndex(row, " ")+1:]] = true
	}
	if !sources[aisessions.SourceCodexAppServer] || !sources[aisessions.SourceClaudeTranscript] {
		t.Fatalf("candidates miss a source: %v", got)
	}
	claude := rows(core.ResumeCandidateQuery{Project: "uid:" + f.project, Provider: "claude", Cwd: f.workspace})
	if len(claude) != 2 || !strings.HasPrefix(claude[0], "claude 11111111-2222-4333-8444-000000000001 ") {
		t.Fatalf("claude candidates %v", claude)
	}
	for name, query := range map[string]core.ResumeCandidateQuery{
		"relative-cwd": {Project: "uid:" + f.project, Cwd: "sub"},
		"provider":     {Project: "uid:" + f.project, Provider: "nonesuch"},
	} {
		var refusal *Refusal
		if _, err := f.c.ListResumeCandidates(ctx, query); !errors.As(err, &refusal) || refusal.Code != CodeCreateRequestInvalid {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var refusal *Refusal
	if _, err := f.c.ListResumeCandidates(ctx, core.ResumeCandidateQuery{Project: "uid:proj-absent"}); !errors.As(err, &refusal) || refusal.Code != CodeResourceNotFound {
		t.Fatalf("absent Project: %v", err)
	}
}

// TestResumeCandidateSelectionStaleBeforeEffects proves a selection that is
// no longer a candidate, or one the create cannot run, is refused with no
// Window, Agent or tmux Window, and that a current one commits its Window and
// Agent in one transaction with the conversation as the Agent's session.
func TestResumeCandidateSelectionStaleBeforeEffects(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	f.useResumeCatalog(nil)
	id := "11111111-2222-4333-8444-000000000010"
	path := f.writeClaudeResumeSession(t, id, f.workspace, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	selection := &core.ResumeSelection{Provider: "claude", Conversation: id, Source: aisessions.SourceClaudeTranscript, Cwd: f.workspace}
	intent := f.intent(&core.WindowIntentAgent{Resume: selection})

	before, tmuxBefore := f.registry(t), f.tmuxWindows(t)
	unchanged := func(t *testing.T) {
		t.Helper()
		after := f.registry(t)
		if len(after.Windows) != len(before.Windows) || len(after.Agents) != len(before.Agents) || len(after.Panes) != len(before.Panes) || f.tmuxWindows(t) != tmuxBefore {
			t.Fatal("refused resume changed the Registry or tmux")
		}
	}
	for name, tc := range map[string]struct {
		intent core.WindowCreateIntent
		want   string
	}{
		"process":         {core.WindowCreateIntent{Project: "uid:" + f.project, Host: "process", Detach: true, Agent: &core.WindowIntentAgent{Resume: selection}}, "process-resume-unsupported"},
		"provider":        {f.intent(&core.WindowIntentAgent{Provider: "codex", Resume: selection}), "does not match Resume.Provider"},
		"profile":         {f.intent(&core.WindowIntentAgent{Profile: "coder", Resume: selection}), "inherits its launch settings"},
		"pane-cwd":        {f.intent(&core.WindowIntentAgent{CwdFrom: "pane", CwdPane: "uid:pane-x", Resume: selection}), "CwdFrom pane"},
		"identity":        {f.intent(&core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "claude", Source: aisessions.SourceClaudeTranscript}}), "requires Provider, Conversation and Source"},
		"codex-source":    {f.intent(&core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "codex", Conversation: "019f", Source: "elsewhere"}}), "not a resumable codex source"},
		"not-a-candidate": {f.intent(&core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "claude", Conversation: "absent", Source: aisessions.SourceClaudeTranscript}}), "resume-candidate-stale"},
	} {
		t.Run(name, func(t *testing.T) {
			_, preflight := f.c.PreflightWindowIntent(ctx, tc.intent)
			_, created := f.c.CreateWindowFromIntent(ctx, tc.intent)
			for _, err := range []error{preflight, created} {
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Code != CodeCreateRequestInvalid || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %s refusal", err, tc.want)
				}
			}
			unchanged(t)
		})
	}

	// A Codex app-server selection is checked only at commit: the preview
	// passes, and the create refuses a row the catalog no longer lists, or a
	// row with no endpoint to resume on, before any effect.
	if err := os.WriteFile(filepath.Join(f.c.agentEnvironment["PATH"], "codex"), []byte("#!/bin/sh\nexec /bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	native := f.intent(&core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "codex", Conversation: "019f0000-0000-7000-8000-000000000060", Source: aisessions.SourceCodexAppServer, Cwd: f.workspace}})
	if preview, err := f.c.PreflightWindowIntent(ctx, native); err != nil || preview.Provider != "codex" {
		t.Fatalf("app-server preview %+v %v", preview, err)
	}
	if _, err := f.c.CreateWindowFromIntent(ctx, native); err == nil || !strings.Contains(err.Error(), "resume-candidate-stale") {
		t.Fatalf("app-server create without its row: %v", err)
	}
	unchanged(t)
	f.useResumeCatalog(resumeFixtureCatalog{threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000060", CWD: f.workspace, Name: "native", UpdatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}}})
	if _, err := f.c.CreateWindowFromIntent(ctx, native); err == nil || !strings.Contains(err.Error(), "cannot be resumed natively") || !strings.Contains(err.Error(), mutationruntime.NativeCodexReasonLegacyEndpointMissing) {
		t.Fatalf("app-server create without an endpoint: %v", err)
	}
	unchanged(t)
	f.useResumeCatalog(nil)

	// The selection was a candidate when previewed; the transcript is gone
	// before the create, so the create's own revalidation refuses it.
	if preview, err := f.c.PreflightWindowIntent(ctx, intent); err != nil || preview.Provider != "claude" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.CreateWindowFromIntent(ctx, intent); err == nil || !strings.Contains(err.Error(), "resume-candidate-stale") {
		t.Fatalf("stale create: %v", err)
	}
	unchanged(t)

	if err := os.Rename(path+".moved", path); err != nil {
		t.Fatal(err)
	}
	// A history line that cannot be appended leaves the create standing
	// with the CLI's one notice line.
	if err := os.MkdirAll(sessionhistory.Path(f.state), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := f.c.CreateWindowFromIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	notice := strings.TrimSuffix(fmt.Sprintf(mutationruntime.SessionHistoryNotRecordedFmt, "append-failed"), "\n")
	if !slices.Contains(result.Notices, notice) {
		t.Fatalf("notices %q, want %q", result.Notices, notice)
	}
	reg := f.registry(t)
	stored := requireAgentOnlyWindow(t, reg, result)
	ref := stored.Status.SessionRef
	if stored.Spec.Provider != "claude" || ref == nil || ref.Claude == nil || ref.Claude.SessionID != id || stored.Spec.Workspace.CWD != f.workspace {
		t.Fatalf("resumed Agent %+v session %+v", stored.Spec, ref)
	}
	if len(reg.Windows) != len(before.Windows)+1 || len(reg.Agents) != len(before.Agents)+1 {
		t.Fatal("resume did not commit exactly one Window and one Agent")
	}
	if _, ok := reg.Window(result.Window.Metadata.UID); !ok || result.Agent.Status.SessionRef == nil {
		t.Fatalf("result %+v", result.Agent)
	}
}

// resumeSpy counts every read of the default Codex app-server a discovery
// makes: its route, a connection to it and its catalog. clientSession records,
// per discovery, whether the Client had set those reads up through its own
// captured native session before the spy replaced them.
type resumeSpy struct {
	routes, opens, catalogs atomic.Int32
	clientSession           []bool
}

func (f windowIntentFixture) spyResumeCatalog(spy *resumeSpy) {
	route := mutationruntime.NativeCodexRoute{Endpoint: metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "codex-0.160.1"}, State: metadata.CodexGenerationCurrent, TUIExecutable: "/fixture/codex", Default: true}
	f.c.resumeSources = func(opts *mutationruntime.ResumeDiscoveryOptions) {
		_, native := opts.CodexNative.(sdkCodexCatalog)
		spy.clientSession = append(spy.clientSession, native && opts.OpenRoute != nil && opts.OpenCodexCatalog != nil)
		opts.CodexNative = sdkCodexCatalog{current: func(context.Context) (mutationruntime.NativeCodexRoute, error) {
			spy.routes.Add(1)
			return route, nil
		}}
		opts.OpenRoute = func(context.Context, mutationruntime.NativeCodexRoute, bool) (*codexappserver.Client, error) {
			spy.opens.Add(1)
			return nil, codexappserver.ErrUnsupported
		}
		opts.OpenCodexCatalog = func(context.Context) (aisessions.CodexCatalog, error) {
			spy.catalogs.Add(1)
			return nil, codexappserver.ErrUnsupported
		}
	}
}

// writeCodexRollout records a Codex rollout of cwd in the fixture home.
func (f windowIntentFixture) writeCodexRollout(t *testing.T, id, cwd string) string {
	t.Helper()
	dir := filepath.Join(f.c.discoveryHome, ".codex", "sessions", "2026", "10", "01")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-10-01T12-00-00-"+id+".jsonl")
	body := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n", id, cwd)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResumeCandidatesReadCodexThroughClientSession proves the Codex catalog
// ListResumeCandidates reads is set up through the Client's captured native
// session, and that the discovery connects to a route through that opener
// rather than the process's ambient default endpoint.
func TestResumeCandidatesReadCodexThroughClientSession(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	id := "019f0000-0000-7000-8000-000000000030"
	f.writeCodexRollout(t, id, f.workspace)
	spy := &resumeSpy{}
	f.spyResumeCatalog(spy)
	candidates, err := f.c.ListResumeCandidates(ctx, core.ResumeCandidateQuery{Project: "uid:" + f.project, Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spy.clientSession, []bool{true}) || spy.routes.Load() == 0 || spy.opens.Load() != 1 {
		t.Fatalf("client session %v, routes %d, opens %d", spy.clientSession, spy.routes.Load(), spy.opens.Load())
	}
	// The route read failed, so the row comes from the rollout scan.
	if len(candidates) != 1 || candidates[0].Conversation != id || candidates[0].Source != aisessions.SourceCodexRollout {
		t.Fatalf("candidates %+v", candidates)
	}
}

// TestResumeIntentChecksStartNoCodexProcess proves the candidate check of a
// preview and of a create reads no Codex route, connection or catalog, so it
// can start no app-server process, while ListResumeCandidates does read them.
// The preview of a Codex app-server selection reads none of them either.
func TestResumeIntentChecksStartNoCodexProcess(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(f.c.agentEnvironment["PATH"], "codex"), []byte("#!/bin/sh\nexec /bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	id := "019f0000-0000-7000-8000-000000000031"
	path := f.writeCodexRollout(t, id, f.workspace)
	spy := &resumeSpy{}
	f.spyResumeCatalog(spy)
	intent := f.intent(&core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "codex", Conversation: id, Source: aisessions.SourceCodexRollout, Cwd: f.workspace}})
	if preview, err := f.c.PreflightWindowIntent(ctx, intent); err != nil || preview.Provider != "codex" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.CreateWindowFromIntent(ctx, intent); err == nil || !strings.Contains(err.Error(), "resume-candidate-stale") {
		t.Fatalf("stale create: %v", err)
	}
	// An app-server selection is not checked by the preview at all.
	native := f.intent(&core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "codex", Conversation: id, Source: aisessions.SourceCodexAppServer, Cwd: f.workspace}})
	if preview, err := f.c.PreflightWindowIntent(ctx, native); err != nil || preview.Provider != "codex" {
		t.Fatalf("app-server preview %+v %v", preview, err)
	}
	if read := spy.routes.Load() + spy.opens.Load() + spy.catalogs.Load(); read != 0 {
		t.Fatalf("candidate checks read the Codex app-server %d times", read)
	}
	if _, err := f.c.ListResumeCandidates(ctx, core.ResumeCandidateQuery{Project: "uid:" + f.project, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if spy.routes.Load() == 0 || spy.opens.Load() == 0 {
		t.Fatal("ListResumeCandidates read no Codex route")
	}
}

// TestResumeIntentResolvesNoFreshProfile proves a Resume does not resolve
// the profile a fresh create would take from its role label: the resume
// inherits its launch settings, so a role mapped to another provider's
// profile neither refuses it nor applies to it.
func TestResumeIntentResolvesNoFreshProfile(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	f.useResumeCatalog(nil)
	if _, err := f.c.WriteProfile(ctx, "coder", []byte("provider = \"codex\"\nroles = [\"worker\"]\n")); err != nil {
		t.Fatal(err)
	}
	id := "11111111-2222-4333-8444-000000000040"
	f.writeClaudeResumeSession(t, id, f.workspace, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	intent := f.intent(&core.WindowIntentAgent{Labels: []string{"role=worker"}, Resume: &core.ResumeSelection{Provider: "claude", Conversation: id, Source: aisessions.SourceClaudeTranscript, Cwd: f.workspace}})
	if preview, err := f.c.PreflightWindowIntent(ctx, intent); err != nil || preview.Profile != "" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	result, err := f.c.CreateWindowFromIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	stored := requireAgentOnlyWindow(t, f.registry(t), result)
	if stored.Metadata.Labels["role"] != "worker" || stored.Metadata.Annotations[metadata.AnnotationAgentProfile] != "" || result.Receipt.Profile != nil {
		t.Fatalf("resumed Agent %+v receipt profile %+v", stored.Metadata, result.Receipt.Profile)
	}
}

// TestResumeIntentShowsInheritedProfile proves the preview and the receipt of
// a Resume name the profile it inherits from the Agent recording the
// conversation, and that holders recording different profiles refuse it.
func TestResumeIntentShowsInheritedProfile(t *testing.T) {
	f := newWindowIntentFixture(t)
	ctx := context.Background()
	f.useResumeCatalog(nil)
	if _, err := f.c.WriteProfile(ctx, "rev", []byte("provider = \"claude\"\n")); err != nil {
		t.Fatal(err)
	}
	id := "11111111-2222-4333-8444-000000000050"
	f.writeClaudeResumeSession(t, id, f.workspace, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	hold := func(name, profile string) {
		t.Helper()
		if _, err := store.NewStore(store.PathFor(f.state)).Update(func(reg *metadata.Registry) error {
			windows := reg.WindowsOf(f.project)
			if len(windows) == 0 {
				return errors.New("fixture project has no Window")
			}
			a, err := store.DefaultMutator().CreateAgent(reg, windows[0].Metadata.UID, metadata.CreateAgentOptions{Name: name, Provider: "claude", Workspace: metadata.AgentWorkspace{CWD: f.workspace}})
			if err != nil {
				return err
			}
			row, _ := reg.Agent(a.Metadata.UID)
			row.Status.Phase = metadata.PhaseOffline // fixture seed, before the operation
			row.Metadata.Annotations = map[string]string{metadata.AnnotationAgentProfile: profile}
			_, _, err = store.DefaultMutator().RecordAgentSessionRef(reg, a.Metadata.UID, metadata.AgentSessionObservation{Provider: "claude", SessionID: id})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	hold("holder", "rev")
	intent := f.intent(&core.WindowIntentAgent{Resume: &core.ResumeSelection{Provider: "claude", Conversation: id, Source: aisessions.SourceClaudeTranscript, Cwd: f.workspace}})
	if preview, err := f.c.PreflightWindowIntent(ctx, intent); err != nil || preview.Profile != "rev" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	result, err := f.c.CreateWindowFromIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	stored := requireAgentOnlyWindow(t, f.registry(t), result)
	if profile := result.Receipt.Profile; profile == nil || profile.Name != "rev" || profile.Digest == "" || stored.Metadata.Annotations[metadata.AnnotationAgentProfile] != "rev" {
		t.Fatalf("receipt profile %+v, Agent annotations %v", profile, stored.Metadata.Annotations)
	}

	hold("other", "")
	before := f.registry(t)
	for _, check := range []func() error{
		func() error { _, err := f.c.PreflightWindowIntent(ctx, intent); return err },
		func() error { _, err := f.c.CreateWindowFromIntent(ctx, intent); return err },
	} {
		if err := check(); err == nil || !strings.Contains(err.Error(), "different profiles") {
			t.Fatalf("disagreeing holders: %v", err)
		}
	}
	if after := f.registry(t); len(after.Windows) != len(before.Windows) || len(after.Agents) != len(before.Agents) {
		t.Fatal("refused resume changed the Registry")
	}
}
