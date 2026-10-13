package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func sdkSettingMessageMismatch(sdkErr, cliErr error) bool {
	// Only CLI input prefixes change. Preserve all remaining detail, including
	// quoted caller data; a locale value error with no CLI flag stays identical.
	cli := cliErr.Error()
	for _, p := range []struct{ prefix, field string }{
		{"config agent-host --default", "ConfigUpdate.DefaultHost"},
		{"config providers --disable", "ConfigUpdate.Provider"},
		{"config providers --enable", "ConfigUpdate.Provider"},
		{"config locale --set", "ConfigUpdate.Locale"},
		{"config agent-questions --answering", "ConfigUpdate.Answering"},
		{"config agent-approvals --answering", "ConfigUpdate.Answering"},
		{"config agent-questions --window", "ConfigUpdate.Window"},
		{"config agent-approvals --window", "ConfigUpdate.Window"},
	} {
		if detail, ok := strings.CutPrefix(cli, p.prefix); ok {
			return sdkErr.Error() != p.field+detail
		}
	}
	return sdkErr.Error() != cli
}

func TestResourceSDKInputFieldRefusals(t *testing.T) {
	_, c := sdkResourceFixture(t)
	for _, tc := range []struct {
		kind    core.Kind
		input   projmux.ResourceSelector
		cli     []string
		message string
	}{
		{core.KindProject, projmux.ResourceSelector{Windows: []string{"main"}}, []string{"projects", "--window", "main"}, "ResourceSelector.Windows is not supported for Project lists"},
		{core.KindAgent, projmux.ResourceSelector{Panes: []string{"zsh"}}, []string{"agents", "--pane", "zsh"}, "ResourceSelector.Panes is supported only for Pane lists"},
	} {
		_, err := c.ListResources(context.Background(), tc.kind, tc.input)
		var r *projmux.Refusal
		var out, stderr bytes.Buffer
		cliErr := newGetCommand().Run(tc.cli, &out, &stderr)
		if !errors.As(err, &r) || r.Code != projmux.CodeSelectorInvalid || r.Error() != tc.message || cliErr == nil || !strings.Contains(cliErr.Error(), "flag provided but not defined") || !IsUsageError(cliErr) {
			t.Fatalf("SDK=%v CLI=%v", err, cliErr)
		}
	}
}

func TestQuestionSDKInputFieldRefusals(t *testing.T) {
	f := newSDKQuestionFixture(t, false)
	for _, tc := range []struct {
		sel     []core.QuestionSelection
		cli     []string
		message string
	}{
		{nil, nil, "AnswerQuestionRequest.Selections requires at least one selection with Option, Index, or Text"},
		{[]core.QuestionSelection{{Question: 0, Text: ptr("secret must stay hidden")}}, []string{"--text", "0=secret must stay hidden"}, "AnswerQuestionRequest.Selections: Text requires a positive Question number"},
		{[]core.QuestionSelection{{Question: 99, Option: ptr("--index")}}, []string{"--option", "99=--index"}, "AnswerQuestionRequest.Selections: Option or Index names Question 99; this question set has 2"},
		{[]core.QuestionSelection{{Question: 1, Text: ptr("a")}, {Question: 1, Text: ptr("b")}}, []string{"--text", "1=a", "--text", "1=b"}, "AnswerQuestionRequest.Selections: Question 1 accepts one Text selection"},
	} {
		_, err := f.client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: f.record.ID, Selections: tc.sel})
		var r *projmux.Refusal
		var out, stderr bytes.Buffer
		cliErr := f.cmd.runQuestion(append([]string{"answer", "reader", f.record.ID}, tc.cli...), &out, &stderr)
		if !errors.As(err, &r) || r.Code != projmux.CodeQuestionInvalidAnswer || r.Error() != tc.message || cliErr == nil || exitCodeOf(cliErr) != 2 || strings.Contains(r.Error(), "--") {
			t.Fatalf("SDK=%v CLI=%v", err, cliErr)
		}
	}
}

func TestNotificationSDKInputFieldRefusals(t *testing.T) {
	c, _, _ := settingsClient(t, t.TempDir())
	for _, input := range []core.NotificationAck{{All: true, ID: "existing"}, {}} {
		_, err := c.AckNotifications(context.Background(), input)
		var r *projmux.Refusal
		if !errors.As(err, &r) || r.Code != projmux.CodeNotificationAckInvalid || r.Error() != "NotificationAck requires a non-empty ID or All=true with an empty ID" {
			t.Fatal(err)
		}
	}
}

func TestSDKSelectorParserFieldRefusals(t *testing.T) {
	_, c := sdkResourceFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		field string
		call  func() error
	}{
		{"ResourceSelector.Project", func() error {
			_, err := c.ListResources(ctx, core.KindWindow, projmux.ResourceSelector{Project: "uid:"})
			return err
		}},
		{"ResourceSelector.Windows", func() error {
			_, err := c.ListResources(ctx, core.KindWindow, projmux.ResourceSelector{Windows: []string{"uid:"}})
			return err
		}},
		{"ResourceSelector.Panes", func() error {
			_, err := c.ListResources(ctx, core.KindPane, projmux.ResourceSelector{Panes: []string{"uid:"}})
			return err
		}},
		{"core.Ref.Value", func() error { _, err := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: "uid:"}); return err }},
		{"ListProfiles projectRef", func() error { _, err := c.ListProfiles(ctx, "uid:"); return err }},
		{"CapabilitiesRequest.Agent", func() error { _, err := c.Capabilities(ctx, projmux.CapabilitiesRequest{Agent: "uid:"}); return err }},
		{"ListQuestions agentRef", func() error { _, err := c.ListQuestions(ctx, "uid:"); return err }},
		{"AnswerQuestionRequest.Agent", func() error {
			_, err := c.AnswerQuestion(ctx, projmux.AnswerQuestionRequest{Agent: "uid:"})
			return err
		}},
		{"ListApprovals agentRef", func() error { _, err := c.ListApprovals(ctx, "uid:"); return err }},
		{"AnswerApprovalRequest.Agent", func() error {
			_, err := c.AnswerApproval(ctx, projmux.AnswerApprovalRequest{Agent: "uid:"})
			return err
		}},
		{"ReviewApprovalRequest.Agent", func() error {
			_, err := c.ReviewApproval(ctx, projmux.ReviewApprovalRequest{Agent: "uid:"})
			return err
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			err := tc.call()
			var refusal *projmux.Refusal
			want := "parse selector: " + tc.field + ` "uid:" requires a uid after "uid:"`
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid || refusal.Error() != want {
				t.Fatalf("SDK=%v want=%s", err, want)
			}
		})
	}
	for _, tc := range []struct {
		kind        core.Kind
		input       projmux.ResourceSelector
		cli         []string
		sdk, detail string
	}{
		{core.KindWindow, projmux.ResourceSelector{Windows: []string{" "}}, []string{"windows", "--window", " "}, "ResourceSelector.Windows", "requires a value"},
		{core.KindAgent, projmux.ResourceSelector{Labels: []string{"broken"}}, []string{"agents", "--selector", "broken"}, "ResourceSelector.Labels", `"broken" must be key=value`},
		{core.KindWindow, projmux.ResourceSelector{Labels: []string{"--selector"}}, []string{"windows", "--selector", "--selector"}, "ResourceSelector.Labels", `"--selector" must be key=value`},
		{core.KindWindow, projmux.ResourceSelector{Labels: []string{"=--window"}}, []string{"windows", "--selector", "=--window"}, "ResourceSelector.Labels", `"=--window" has an empty key`},
	} {
		_, err := c.ListResources(ctx, tc.kind, tc.input)
		var refusal *projmux.Refusal
		var out, stderr bytes.Buffer
		cliErr := newGetCommand().Run(tc.cli, &out, &stderr)
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid || refusal.Error() != "parse selector: "+tc.sdk+" "+tc.detail || cliErr == nil || !IsUsageError(cliErr) || !strings.HasSuffix(cliErr.Error(), tc.detail) || !strings.Contains(cliErr.Error(), "--") {
			t.Fatalf("SDK=%v CLI=%v", err, cliErr)
		}
	}
}

// The input parser diagnostics are the Task 4 exception to CLI text parity.
// All non-parser text, including resolution recovery, remains byte identical.
func sdkSelectorMessageMismatch(sdkErr, cliErr error, flag, field string) bool {
	detail, parsed := strings.CutPrefix(cliErr.Error(), "parse selector: "+flag+" ")
	if parsed {
		return sdkErr.Error() != "parse selector: "+field+" "+detail
	}
	return sdkErr.Error() != cliErr.Error()
}
