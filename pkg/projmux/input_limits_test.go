package projmux_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// escapedBodyLimit is the body a JSON transport must admit so that every
// spelling of an input one byte past limit reaches the validator: a JSON
// string escapes one byte to at most six, plus room for the envelope.
func escapedBodyLimit(limit int) int { return 6*(limit+1) + 4<<10 }

// The public limits are the validators' own: the SDK accepts an input of
// exactly the limit and refuses one byte more with the validator's code, and a
// maximally escaped JSON body carrying that one byte more still decodes to an
// input the validator, not the transport, refuses.
func TestPublicInputLimitsMatchValidators(t *testing.T) {
	if projmux.MaxProjectLinkRulesBytes != projectlinks.MaxFileSize || projmux.MaxAgentGuidanceBytes != agentguidance.MaxSize {
		t.Fatalf("public limits %d, %d differ from the validators' %d, %d", projmux.MaxProjectLinkRulesBytes, projmux.MaxAgentGuidanceBytes, projectlinks.MaxFileSize, agentguidance.MaxSize)
	}
	root := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
	var uid string
	_, err := storemetadata.NewDefaultStore(paths).Update(func(reg *metadata.Registry) error {
		p, e := (metadata.Mutator{DirExists: func(string) (bool, error) { return true, nil }}).RegisterProject(reg, metadata.RegisterProjectOptions{Root: root, Name: "sample"})
		uid = p.Project.Metadata.UID
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inputs, err := projmux.SettingsInputPaths(paths.ConfigDir, uid)
	if err != nil {
		t.Fatal(err)
	}
	refused := func(err error, code projmux.Code) bool {
		var refusal *projmux.Refusal
		return errors.As(err, &refusal) && refusal.Code == code
	}

	t.Run("agent guidance", func(t *testing.T) {
		limit := projmux.MaxAgentGuidanceBytes
		validators := map[string]func([]byte) error{
			"global": func(b []byte) error { _, err := c.ValidateAgentGuidance(ctx, b); return err },
			"project": func(b []byte) error {
				_, err := c.ValidateProjectGuidance(ctx, "uid:"+uid, b)
				return err
			},
		}
		// Enabled host-global guidance is also bounded once assembled: the
		// built-in text, the process note and the candidate together must fit
		// the same limit, so the largest candidate is the limit less that text.
		small, err := c.ValidateAgentGuidance(ctx, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		fixed := max(small.AssembledBytes, small.ProcessAssembledBytes) - 1
		if fixed <= 0 || fixed >= limit {
			t.Fatalf("assembled text around one byte is %d bytes", fixed)
		}
		if _, err := c.ValidateAgentGuidance(ctx, bytes.Repeat([]byte("x"), limit-fixed)); err != nil {
			t.Fatalf("enabled: %d bytes assembling to the limit refused: %v", limit-fixed, err)
		}
		if _, err := c.ValidateAgentGuidance(ctx, bytes.Repeat([]byte("x"), limit-fixed+1)); !refused(err, projmux.CodeAgentGuidanceTooLarge) {
			t.Fatalf("enabled: %d bytes assembling past the limit: %v, want %s", limit-fixed+1, err, projmux.CodeAgentGuidanceTooLarge)
		}
		// With guidance off only the candidate's own size is checked.
		if err := c.SetAgentGuidanceEnabled(ctx, "", false); err != nil {
			t.Fatal(err)
		}
		for name, validate := range validators {
			if err := validate(bytes.Repeat([]byte("x"), limit)); err != nil {
				t.Fatalf("%s: %d bytes refused: %v", name, limit, err)
			}
			if err := validate(bytes.Repeat([]byte("x"), limit+1)); !refused(err, projmux.CodeAgentGuidanceTooLarge) {
				t.Fatalf("%s: %d bytes: %v, want %s", name, limit+1, err, projmux.CodeAgentGuidanceTooLarge)
			}
			// Every byte of this content is a control character, which a JSON
			// string spells as \u00XX: the longest body for this input length.
			body, err := json.Marshal(struct {
				Content string `json:"content"`
			}{strings.Repeat("\x01", limit+1)})
			if err != nil {
				t.Fatal(err)
			}
			if len(body) <= 6*(limit+1) || len(body) > escapedBodyLimit(limit) {
				t.Fatalf("%s: escaped body is %d bytes, want over %d and at most %d", name, len(body), 6*(limit+1), escapedBodyLimit(limit))
			}
			var decoded struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if err := validate([]byte(decoded.Content)); !refused(err, projmux.CodeAgentGuidanceTooLarge) {
				t.Fatalf("%s: escaped body: %v, want %s", name, err, projmux.CodeAgentGuidanceTooLarge)
			}
		}
	})

	t.Run("link rules write", func(t *testing.T) {
		limit := projmux.MaxProjectLinkRulesBytes
		// Plain filler bytes encode one to one, so the stored size moves with
		// the filler. The first write measures it; the filler is then grown to
		// make the stored encoding exactly the limit.
		fill := make([]int, 40)
		for i := range fill {
			fill[i] = 1500
		}
		rules := func() core.ProjectLinkRules {
			var out core.ProjectLinkRules
			for i, n := range fill {
				out.Links = append(out.Links, core.ProjectLink{LabelKey: fmt.Sprintf("k%02d", i), Template: "https://example.com/" + strings.Repeat("x", n) + "/{value}"})
			}
			return out
		}
		size := func() int {
			info, err := os.Stat(inputs.ProjectLinkRulesFile)
			if err != nil {
				t.Fatal(err)
			}
			return int(info.Size())
		}
		if _, err := c.WriteProjectLinkRules(ctx, "uid:"+uid, rules(), false); err != nil {
			t.Fatal(err)
		}
		delta := limit - size()
		if delta < 0 {
			t.Fatalf("base rules already encode past the limit by %d bytes", -delta)
		}
		for i := range fill {
			fill[i] += delta / len(fill)
		}
		fill[len(fill)-1] += delta % len(fill)
		if _, err := c.WriteProjectLinkRules(ctx, "uid:"+uid, rules(), true); err != nil {
			t.Fatalf("rules encoding to the limit refused on dry run: %v", err)
		}
		if _, err := c.WriteProjectLinkRules(ctx, "uid:"+uid, rules(), false); err != nil {
			t.Fatalf("rules encoding to the limit refused: %v", err)
		}
		if got := size(); got != limit {
			t.Fatalf("stored rules are %d bytes, want exactly %d", got, limit)
		}
		before, err := os.ReadFile(inputs.ProjectLinkRulesFile)
		if err != nil {
			t.Fatal(err)
		}
		fill[len(fill)-1]++
		over := rules()
		for _, dryRun := range []bool{true, false} {
			_, err := c.WriteProjectLinkRules(ctx, "uid:"+uid, over, dryRun)
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeInvalidLinkRules || refusal.Field != "links" {
				t.Fatalf("rules one byte past the limit (dry run %t): %v", dryRun, err)
			}
		}
		if after, err := os.ReadFile(inputs.ProjectLinkRulesFile); err != nil || !bytes.Equal(before, after) {
			t.Fatalf("refused write changed the stored rules: %v", err)
		}
		body, err := json.Marshal(over)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > escapedBodyLimit(limit) {
			t.Fatalf("body of rules one byte past the limit is %d bytes, over %d", len(body), escapedBodyLimit(limit))
		}
		var decoded core.ProjectLinkRules
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err := c.WriteProjectLinkRules(ctx, "uid:"+uid, decoded, true); !refused(err, projmux.CodeInvalidLinkRules) {
			t.Fatalf("decoded body: %v, want %s", err, projmux.CodeInvalidLinkRules)
		}
	})

	t.Run("link rules read", func(t *testing.T) {
		limit := projmux.MaxProjectLinkRulesBytes
		if filepath.Dir(inputs.ProjectLinkRulesFile) != inputs.ProjectLinkRulesDir {
			t.Fatalf("rules file %q is not in %q", inputs.ProjectLinkRulesFile, inputs.ProjectLinkRulesDir)
		}
		if err := os.MkdirAll(inputs.ProjectLinkRulesDir, 0700); err != nil {
			t.Fatal(err)
		}
		stored := `{"links":[]}`
		for _, tt := range []struct {
			size int
			code projmux.Code
		}{{limit, ""}, {limit + 1, projmux.CodeLinkRulesUnreadable}} {
			content := stored + strings.Repeat(" ", tt.size-len(stored))
			if err := os.WriteFile(inputs.ProjectLinkRulesFile, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := c.ReadProjectLinkRulesByUID(ctx, uid)
			if (tt.code == "" && err != nil) || (tt.code != "" && !refused(err, tt.code)) {
				t.Fatalf("%d-byte rule file: %v, want %q", tt.size, err, tt.code)
			}
		}
	})
}
