package runtime

import (
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"os"
	"strings"
	"testing"
)

func TestManagedCodexBaseFirstFailureRefusesDefaultsAndFallbackKeepsWholeRecipe(t *testing.T) {
	paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	failure := errors.New("current guidance unreadable")
	if _, err := PrepareManagedCodexBase(paths, "agent-test", "partial new guidance", nil, failure); err == nil {
		t.Fatal("first failure silently selected provider base")
	}
	old, err := PrepareManagedCodexBase(paths, "agent-test", "whole old managed recipe", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]string{CodexManagedBaseDigest: old.Digest, CodexManagedBaseMode: "base", metadata.AnnotationAgentPersona: "old-role"}
	fallback, err := PrepareManagedCodexBase(paths, "agent-test", "partial new guidance", recorded, failure)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback.Fallback || fallback.Instructions.Base != "whole old managed recipe" || fallback.Annotations[metadata.AnnotationAgentPersona] != "old-role" || fallback.Notice() == "" {
		t.Fatalf("mixed fallback: %+v", fallback)
	}
	path, err := managedBaseStore(paths).RecordedSnapshotPath(old.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareManagedCodexBase(paths, "agent-test", "", recorded, failure); err == nil {
		t.Fatal("damaged last confirmed instructions accepted")
	}
}

func TestManagedCodexBaseAToBAndLegacyContext(t *testing.T) {
	paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	a, err := PrepareManagedCodexBase(paths, "agent-test", "A", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := PrepareManagedCodexBase(paths, "agent-test", "B", map[string]string{CodexManagedBaseMode: "base", CodexManagedBaseDigest: a.Digest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest == b.Digest || b.Instructions.Base != "B" || b.Instructions.Legacy {
		t.Fatalf("managed restart did not replace base only: %+v", b)
	}
	legacy, err := PrepareManagedCodexBase(paths, "agent-test", "B", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Instructions.Legacy {
		t.Fatal("historical developer instructions lost compatibility context")
	}
	again, err := PrepareManagedCodexBase(paths, "agent-test", "B", map[string]string{CodexManagedBaseDigest: legacy.Digest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Instructions.Legacy {
		t.Fatal("unchanged legacy context would be appended again")
	}
	empty, err := PrepareManagedCodexBase(paths, "agent-test", "", nil, nil)
	if err != nil || empty.Instructions.Base == "" {
		t.Fatalf("empty composition would resurrect provider base: %+v %v", empty, err)
	}
}

func TestManagedCodexBasePreparationDoesNotRecordSuccessfulApplication(t *testing.T) {
	paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	reg := metadata.Registry{Agents: []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent-test", Annotations: map[string]string{"existing": "retained"}}}}}
	base, err := PrepareManagedCodexBase(paths, "agent-test", "B", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = base.Record(&reg, "agent-test", true); err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent("agent-test")
	if a.Metadata.Annotations[CodexManagedBaseDigest] != "" {
		t.Fatal("prepared snapshot recorded as applied")
	}
	base.Instructions.Accepted = true
	if err = base.Record(&reg, "agent-test", true); err != nil {
		t.Fatal(err)
	}
	if a.Metadata.Annotations[CodexManagedBaseDigest] != base.Digest || a.Metadata.Annotations[CodexManagedBaseMode] != "base" || a.Metadata.Annotations["existing"] != "retained" {
		t.Fatalf("confirmed recipe lost or unrelated metadata changed: %v", a.Metadata.Annotations)
	}
}

func TestSharedCodexBaseNeverPromotesUnconfirmedCompositionToFallback(t *testing.T) {
	paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	a, err := PrepareManagedCodexBase(paths, "agent-test", "A", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[string]string{CodexManagedBaseDigest: a.Digest, CodexManagedBaseMode: "base", metadata.AnnotationAgentPersona: "role-A"}
	b, err := PrepareManagedCodexBase(paths, "agent-test", "B", previous, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.Annotations = map[string]string{metadata.AnnotationAgentPersona: "role-B"}
	b.Instructions.Shared, b.Instructions.Accepted = true, true
	if b.Instructions.Warm || !strings.Contains(b.Notice(), "unconfirmed") || strings.Contains(b.Notice(), "retained") || strings.Contains(b.Notice(), "loaded thread") {
		t.Fatalf("shared notice claimed an unobserved loaded base: %q", b.Notice())
	}
	reg := metadata.Registry{Agents: []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent-test", Annotations: previous}}}}
	if err = b.Record(&reg, "agent-test", false); err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent("agent-test")
	if agent.Metadata.Annotations[CodexManagedBaseDigest] != a.Digest || agent.Metadata.Annotations[metadata.AnnotationAgentPersona] != "role-A" || agent.Metadata.Annotations[CodexManagedBaseStatus] != "base-unconfirmed" {
		t.Fatalf("unconfirmed composition promoted: %v", agent.Metadata.Annotations)
	}
	fallback, err := PrepareManagedCodexBase(paths, "agent-test", "partial C", agent.Metadata.Annotations, errors.New("failed C"))
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Instructions.Base != "A" {
		t.Fatalf("fallback used unconfirmed B: %+v", fallback)
	}
}

func TestPlainCodexBaseFilePreservesLanePrivacyCompositionAndOff(t *testing.T) {
	paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	for _, mode := range []string{"assembled", "off", "guidance-off-with-role-and-project"} {
		t.Run(mode, func(t *testing.T) {
			off := mode != "assembled"
			guidance := AgentGuidanceLaunch{Active: true}
			links := ProjectLinksLaunch{}
			role := ""
			if !off {
				guidance.Digest = "present"
				guidance.Text = []byte("COMMON-AND-USER-PRIVATE")
				links = ProjectLinksLaunch{Active: true, Digest: "present", Text: []byte("PROJECT-LINK-PRIVATE"), Project: ProjectGuidanceLaunch{Active: true, Digest: "present", Text: []byte("PROJECT-GUIDANCE-PRIVATE")}}
				role = "ROLE-PRIVATE"
			}
			if mode == "guidance-off-with-role-and-project" {
				links = ProjectLinksLaunch{Active: true, Digest: "present", Text: []byte("PROJECT-LINK-PRIVATE"), Project: ProjectGuidanceLaunch{Active: true, Digest: "present", Text: []byte("PROJECT-GUIDANCE-PRIVATE")}}
				role = "ROLE-PRIVATE"
			}
			guidance = guidance.WithCodexBaseFile(paths, links, role)
			if guidance.Unavailable != nil || !guidance.CodexBaseFile || guidance.SystemPromptFile == "" {
				t.Fatalf("plain base preparation %+v", guidance)
			}
			content, err := os.ReadFile(guidance.SystemPromptFile)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "off" && string(content) != "\n" {
				t.Fatalf("off resurrected provider base: %q", content)
			}
			if mode == "guidance-off-with-role-and-project" {
				if strings.Contains(string(content), "COMMON-AND-USER") {
					t.Fatal("off guidance was resurrected")
				}
				for _, part := range []string{"ROLE-PRIVATE", "PROJECT-GUIDANCE-PRIVATE", "PROJECT-LINK-PRIVATE"} {
					if !strings.Contains(string(content), part) {
						t.Fatalf("off guidance removed %s", part)
					}
				}
			}
			if !off {
				for _, part := range []string{"COMMON-AND-USER-PRIVATE", "ROLE-PRIVATE", "PROJECT-GUIDANCE-PRIVATE", "PROJECT-LINK-PRIVATE"} {
					if !strings.Contains(string(content), part) {
						t.Fatalf("composition lost %s: %q", part, content)
					}
				}
			}
			service := AgentLaunchService{Plan: func(provider, cwd string, args []string) (string, []string, error) {
				if provider != "codex" {
					t.Fatal(provider)
				}
				return "Codex", append([]string{"codex"}, args...), nil
			}}
			_, argv, err := service.PlanCodexAgentLaunchWithBaseFile(metadata.AgentWorkspace{CWD: "/work"}, []string{"operand"}, "model", "high", codexappserver.ThreadPolicy{Sandbox: "read-only", ApprovalPolicy: "never"}, guidance.SystemPromptFile)
			if err != nil {
				t.Fatal(err)
			}
			rendered := strings.Join(argv, " ")
			if strings.Contains(rendered, "PRIVATE") || !strings.Contains(rendered, "model_instructions_file=") || !strings.Contains(rendered, "read-only") || argv[len(argv)-1] != "operand" {
				t.Fatalf("plain lane leaked instructions or lost grammar/policy: %v", argv)
			}
			annotations := guidance.WithCreateAnnotation(nil)
			if annotations[CodexManagedBaseDigest] != "" || annotations[CodexManagedBaseStatus] != "base-file-requested" || annotations[CodexManagedBaseMode] != "base" {
				t.Fatalf("plain request promoted to confirmation: %v", annotations)
			}
			if err = os.WriteFile(guidance.SystemPromptFile, []byte("tampered"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err = service.PlanCodexAgentLaunchWithBaseFile(metadata.AgentWorkspace{}, nil, "", "", codexappserver.ThreadPolicy{}, guidance.SystemPromptFile); err == nil {
				t.Fatal("invalid base snapshot launched")
			}
		})
	}
}
