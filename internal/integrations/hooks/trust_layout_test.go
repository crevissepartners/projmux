package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

func TestAuthorizeProjectLayoutArtifactRequiresTrustWithHooksOffAndNoConfig(t *testing.T) {
	t.Setenv("PROJMUX_PROJECT_HOOKS", "off")

	repo := t.TempDir()
	contents := []byte("schema_version = 1\ncommand = \"make watch\"\n")
	var request ProjectHookPromptRequest
	runner := &Runner{
		TrustStorePath: testTrustStorePath(t),
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			request = req
			return ProjectHookAllowOnce
		},
	}
	ok, err := runner.AuthorizeProjectLayoutArtifact(
		repo,
		".projmux/layouts/team.toml",
		repo+"/.projmux/layouts/team.toml",
		contents,
		[]string{"window 0 pane 0: make watch"},
	)
	if err != nil {
		t.Fatalf("AuthorizeProjectLayoutArtifact() error = %v", err)
	}
	if !ok {
		t.Fatal("AuthorizeProjectLayoutArtifact() = false, want true after approval")
	}
	sum := sha256.Sum256(contents)
	if request.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("request SHA256 = %q, want exact content hash %q", request.SHA256, hex.EncodeToString(sum[:]))
	}
	if request.RelativePath != ".projmux/layouts/team.toml" ||
		request.ArtifactKind != "project layout" ||
		!strings.Contains(request.Preview, "make watch") {
		t.Fatalf("request = %#v", request)
	}
}

func TestAuthorizeProjectLayoutArtifactChangedBytesRequireApprovalAgain(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	trustPath := testTrustStorePath(t)
	promptCalls := 0
	var changedRequest ProjectHookPromptRequest
	runner := &Runner{
		TrustStorePath: trustPath,
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			promptCalls++
			if promptCalls == 1 {
				return ProjectHookAllowAlways
			}
			changedRequest = req
			return ProjectHookDeny
		},
	}
	path := repo + "/.projmux/layouts/team.toml"
	first := []byte("command = \"safe\"\n")
	ok, err := runner.AuthorizeProjectLayoutArtifact(repo, ".projmux/layouts/team.toml", path, first, []string{"window 0 pane 0: safe"})
	if err != nil || !ok {
		t.Fatalf("first authorization = %v, %v", ok, err)
	}
	changed := []byte("command = \"changed\"\n")
	ok, err = runner.AuthorizeProjectLayoutArtifact(repo, ".projmux/layouts/team.toml", path, changed, []string{"window 0 pane 0: changed"})
	if err != nil {
		t.Fatalf("changed authorization error = %v", err)
	}
	if ok {
		t.Fatal("changed bytes reused old approval")
	}
	if promptCalls != 2 || changedRequest.PreviousSHA256 == "" || changedRequest.PreviousSHA256 == changedRequest.SHA256 {
		t.Fatalf("changed request = %#v, prompt calls = %d", changedRequest, promptCalls)
	}
}

func TestAuthorizeProjectLayoutArtifactRenameReplacementInvalidatesApproval(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	store := corelayout.NewStore(repo)
	preset := func(command string) corelayout.Preset {
		return corelayout.Preset{
			SchemaVersion: corelayout.SchemaVersion,
			Windows: []corelayout.Window{{
				Index:           0,
				ActivePaneIndex: 0,
				Panes: []corelayout.Pane{{
					Index:  0,
					CWD:    "${PROJMUX_CWD}",
					Recipe: sessionstate.StartupRecipe(command),
				}},
			}},
		}
	}
	if err := store.Save("team", preset("safe")); err != nil {
		t.Fatal(err)
	}
	first, err := store.LoadArtifact("team")
	if err != nil {
		t.Fatal(err)
	}

	promptCalls := 0
	var replacementRequest ProjectHookPromptRequest
	runner := &Runner{
		TrustStorePath: testTrustStorePath(t),
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			promptCalls++
			if promptCalls == 1 {
				return ProjectHookAllowAlways
			}
			replacementRequest = req
			return ProjectHookDeny
		},
	}
	ok, err := runner.AuthorizeProjectLayoutArtifact(repo, first.RelativePath, first.Path, first.Contents, first.ExecutableCommands())
	if err != nil || !ok {
		t.Fatalf("initial authorization = %v, %v", ok, err)
	}

	replacement := filepath.Join(store.Dir(), ".replacement.toml")
	if err := os.WriteFile(replacement, []byte(corelayout.Render(preset("changed"))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, first.Path); err != nil {
		t.Fatal(err)
	}
	second, err := store.LoadArtifact("team")
	if err != nil {
		t.Fatal(err)
	}
	ok, err = runner.AuthorizeProjectLayoutArtifact(repo, second.RelativePath, second.Path, second.Contents, second.ExecutableCommands())
	if err != nil {
		t.Fatalf("replacement authorization error = %v", err)
	}
	if ok || promptCalls != 2 {
		t.Fatalf("replacement authorization = %v, prompts = %d; want denied second prompt", ok, promptCalls)
	}
	if replacementRequest.PreviousSHA256 == "" || replacementRequest.PreviousSHA256 == replacementRequest.SHA256 {
		t.Fatalf("replacement request = %#v", replacementRequest)
	}
}

func TestAuthorizeProjectLayoutArtifactNonInteractiveFailsClosed(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	var warnings strings.Builder
	runner := &Runner{
		TrustStorePath: testTrustStorePath(t),
		PromptReader:   strings.NewReader("a\n"),
		Logger:         &warnings,
	}
	ok, err := runner.AuthorizeProjectLayoutArtifact(
		repo,
		".projmux/layouts/team.toml",
		repo+"/.projmux/layouts/team.toml",
		[]byte("command = \"unsafe\"\n"),
		[]string{"window 0 pane 0: unsafe"},
	)
	if err != nil {
		t.Fatalf("AuthorizeProjectLayoutArtifact() error = %v", err)
	}
	if ok {
		t.Fatal("non-interactive layout authorization succeeded")
	}
	if !strings.Contains(warnings.String(), "requires trust") || !strings.Contains(warnings.String(), "non-interactive") {
		t.Fatalf("warnings = %q", warnings.String())
	}
}

func TestAuthorizeProjectLayoutArtifactEscapesCommandPreview(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	var request ProjectHookPromptRequest
	runner := &Runner{
		TrustStorePath: testTrustStorePath(t),
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			request = req
			return ProjectHookDeny
		},
	}
	ok, err := runner.AuthorizeProjectLayoutArtifact(
		repo,
		".projmux/layouts/team.toml",
		repo+"/.projmux/layouts/team.toml",
		[]byte("command"),
		[]string{"window 0 pane 0: echo \x1b]52;c;secret\a"},
	)
	if err != nil {
		t.Fatalf("AuthorizeProjectLayoutArtifact() error = %v", err)
	}
	if ok {
		t.Fatal("denied authorization succeeded")
	}
	if strings.Contains(request.Preview, "\x1b") || strings.Contains(request.Preview, "\a") {
		t.Fatalf("preview contains raw controls: %q", request.Preview)
	}
	if !strings.Contains(request.Preview, `\x1b]52;c;secret\x07`) {
		t.Fatalf("preview = %q, want visible controls", request.Preview)
	}
}

func TestAuthorizeProjectLayoutArtifactCommandlessDoesNotPrompt(t *testing.T) {
	t.Parallel()

	runner := &Runner{
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("commandless artifact prompted")
			return ProjectHookDeny
		},
	}
	repo := t.TempDir()
	ok, err := runner.AuthorizeProjectLayoutArtifact(
		repo,
		".projmux/layouts/team.toml",
		repo+"/.projmux/layouts/team.toml",
		[]byte("schema_version = 1\n"),
		nil,
	)
	if err != nil || !ok {
		t.Fatalf("commandless authorization = %v, %v; want true, nil", ok, err)
	}
}
