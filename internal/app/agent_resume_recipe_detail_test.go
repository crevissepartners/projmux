package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestRecordedResumeRecipeChanges(t *testing.T) {
	a := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-example", Annotations: map[string]string{
		coremetadata.AnnotationAgentPersonaDigest: "old-instructions", coremetadata.AnnotationAgentGuidanceDigest: "old-guidance", coremetadata.AnnotationAgentProjectGuidanceDigest: "old-project", coremetadata.AnnotationAgentProjectLinkRulesDigest: "old-links",
		"secret-environment": "ENV-SENTINEL", "secret-auth": "AUTH-SENTINEL",
	}}}
	r := processRelaunchRecipe{restart: &agentRestart{provider: aiModeClaude}}
	s := &r.restart.settings.resolution
	s.Current.Profile.Name, s.New.Profile.Name = "old-profile", "new-profile"
	s.Current.Profile.Digest, s.New.Profile.Digest = "old-profile-digest", "new-profile-digest"
	s.Current.Instructions.Value, s.New.Instructions.Value = "old-instructions-name", "new-instructions-name"
	s.InstructionsDigest = "new-instructions"
	s.Current.Model.Value, s.New.Model.Value = "old-model", "new-model"
	s.Current.Effort.Value, s.New.Effort.Value = "high", "medium"
	r.guidance.digest, r.guidance.text = "new-guidance", []byte("GUIDANCE-BODY-SENTINEL")
	r.links.digest, r.links.text = "new-links", []byte("https://SECRET-URL-SENTINEL")
	r.links.project.digest, r.links.project.text = "new-project", []byte("PROJECT-BODY-SENTINEL")
	cases := []struct{ reason, field, old, current string }{
		{agentsettings.ReasonProfileChanged, "profile", "old-profile", "new-profile"},
		{agentsettings.ReasonInstructionsChanged, "instructions", "old-instructions-name", "new-instructions-name"},
		{agentsettings.ReasonInstructionsContentChanged, "instructionsDigest", "old-instructions", "new-instructions"},
		{agentsettings.ReasonModelChanged, "model", "old-model", "new-model"},
		{agentsettings.ReasonEffortChanged, "effort", "high", "medium"},
		{agentsettings.ReasonGuidanceChanged, "guidanceDigest", "old-guidance", "new-guidance"},
		{agentsettings.ReasonProjectGuidanceChanged, "projectGuidanceDigest", "old-project", "new-project"},
		{agentsettings.ReasonLinkRulesChanged, "linkRulesDigest", "old-links", "new-links"},
		{"future-reason", "unknown", "<unknown>", "<unknown>"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			s.Reasons = []string{tc.reason}
			got := recordedResumeRecipeError(a, r)
			want := core.ResumeRecipeChange{Field: tc.field, Reason: tc.reason, Recorded: []string{tc.old}, Current: []string{tc.current}}
			if got.Changes[0].Field != tc.field || !reflect.DeepEqual(got.Changes[0], want) {
				t.Fatalf("changes=%+v want=%+v", got.Changes, want)
			}
			raw, _ := json.Marshal(got)
			for _, secret := range []string{"ENV-SENTINEL", "AUTH-SENTINEL", "GUIDANCE-BODY-SENTINEL", "SECRET-URL-SENTINEL", "PROJECT-BODY-SENTINEL"} {
				if strings.Contains(string(raw)+got.Error(), secret) {
					t.Fatal("secret exposed")
				}
			}
		})
	}
	// Multiple reasons are deterministic regardless of producer order, with all
	// reasons, a profile switch and workspace differences retained.
	s.Reasons = nil
	for _, tc := range cases {
		s.Reasons = append(s.Reasons, tc.reason)
	}
	slices.Reverse(s.Reasons)
	s.ProfileSwitched = true
	a.Spec.Workspace = coremetadata.AgentWorkspace{CWD: "/old", AdditionalWritableRoots: []string{"/old-root"}}
	r.workspace = coremetadata.AgentWorkspace{CWD: "/new"}
	got := recordedResumeRecipeError(a, r)
	if len(got.Changes) != 12 || got.Changes[0].Field != "profile" || got.Changes[11].Field != "workspace.additionalWritableRoots" || got.Changes[11].Current == nil {
		t.Fatalf("incomplete changes: %+v", got.Changes)
	}
	s.Reasons = []string{agentsettings.ReasonModelChanged}
	s.ProfileSwitched = false
	s.New.Model.Value = ""
	if got := recordedResumeRecipeError(a, r); !reflect.DeepEqual(got.Changes[0].Current, []string{""}) {
		t.Fatalf("empty scalar: %+v", got)
	}
}

func TestRecordedResumeRecipeRecoveryProviders(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			a := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-exact"}}
			e := recordedResumeRecipeError(a, processRelaunchRecipe{restart: &agentRestart{provider: provider}})
			want := "projmux agent relaunch uid:agent-exact --detach"
			if provider == aiModeClaude {
				want += " -- <prompt>"
			}
			if e.RelaunchCommand != want || !strings.Contains(e.Guidance, "preserves Agent UID, Pane UID, and the recorded conversation") {
				t.Fatalf("guidance: %+v", e)
			}
			var public *projmux.ResumeRecipeChangedError
			if !errors.As(fmt.Errorf("%s: %w", processResumeRefused, usageErrorFor(e.Error(), e)), &public) || public != e {
				t.Fatal("lost public cause")
			}
		})
	}
}

func recipeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHostLostRecipeRefusalDetailsBeforeEffects(t *testing.T) {
	for _, field := range []string{"guidanceDigest", "workspace.cwd", "workspace.additionalWritableRoots"} {
		t.Run(field, func(t *testing.T) {
			c, opts, store, a := hostLostPreparedFixture(t)
			root := os.Getenv("HOME")
			// Any provider invocation would add a file and fail the byte comparison.
			if err := os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\nprintf invoked > "+fmt.Sprintf("%q", filepath.Join(root, "provider-invoked"))+"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if field == "guidanceDigest" {
				if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					agent, _ := reg.Agent(a.Binding.AgentUID)
					agent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest] = "previous-guidance"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				c.resolveWorkspace = func(_ string, _ coremetadata.Registry, _ coremetadata.Project, _ string, cwd string, roots []string) (coremetadata.AgentWorkspace, error) {
					workspace := coremetadata.AgentWorkspace{CWD: cwd, AdditionalWritableRoots: slices.Clone(roots)}
					if field == "workspace.cwd" {
						workspace.CWD = filepath.Join(root, "next")
					} else {
						workspace.AdditionalWritableRoots = []string{filepath.Join(root, "writable")}
					}
					return workspace, nil
				}
			}
			before := recipeFiles(t, root)
			_, err := c.prepareOwnedClaudeResume(context.Background(), opts)
			var detail *projmux.ResumeRecipeChangedError
			var usage *UsageError
			if !errors.As(err, &detail) || !errors.As(err, &usage) || detail.Code != processResumeRefused || detail.Reason != relaunchReasonNoConversation || !strings.HasPrefix(err.Error(), processResumeRefused+":") {
				t.Fatalf("typed refusal lost: %v", err)
			}
			if len(detail.Changes) != 1 || detail.Changes[0].Field != field {
				t.Fatalf("changes: %+v", detail.Changes)
			}
			previous := fmt.Errorf("%s: %w", processResumeRefused, usageError("recorded resume recipe changed (relaunch-no-conversation); nothing was changed"))
			if got, want := cli.ClassifyFailure(err, IsUsageError(err)), cli.ClassifyFailure(previous, IsUsageError(previous)); got != want {
				t.Fatalf("CLI exit changed: %+v vs %+v", got, want)
			}
			after := recipeFiles(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed fixture bytes, registry, launch, or provider invocation state")
			}
		})
	}
}
