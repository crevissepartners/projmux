package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ManagedInstructions is one lifecycle boundary's managed base. Warm records
// the upstream limitation: resume overrides do not change an already loaded
// thread. Legacy appends a supersession context for threads previously created
// with permanent developer instructions; it never deletes their history.
// Callers read Warm only after the synchronous preparation returns.
type ManagedInstructions struct {
	Base     string
	Previous string
	Fallback bool
	Legacy   bool
	Shared   bool
	Warm     bool
	Accepted bool
}

func (c *Client) finishManagedResume(ctx context.Context, thread string, managed *ManagedInstructions) error {
	if managed == nil {
		return nil
	}
	if !managed.Legacy {
		managed.Accepted = true
		return nil
	}
	params := struct {
		ThreadID string            `json:"threadId"`
		Items    []peerHistoryItem `json:"items"`
	}{thread, []peerHistoryItem{{"message", "developer", []peerHistoryText{{"input_text", "Current Projmux managed instructions follow. These supersede previous Projmux managed instructions in this conversation. Preserve the conversation history.\n\n" + managed.Base}}}}}
	var receipt json.RawMessage
	if err := c.Request(ctx, "thread/inject_items", params, &receipt); err != nil {
		return fmt.Errorf("managed Codex legacy context was not confirmed: %w", err)
	}
	if trimmed := bytes.TrimSpace(receipt); len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("%w: legacy managed context returned no object receipt", ErrProtocol)
	}
	managed.Accepted = true
	return nil
}

// recoverManagedBase retires only this connection's subscription. It never
// stops a daemon, interrupts another subscriber, or substitutes a new thread.
// If retirement cannot be proven, the failure remains explicit and the old
// snapshot stays confirmed; an uncertain partial update is never called applied.
func (c *Client) recoverManagedBase(ctx context.Context, thread, cwd string, roots []string, settings ThreadSettings, cause error) (ThreadBinding, error) {
	managed := settings.ManagedInstructions
	if managed == nil || managed.Previous == "" || managed.Fallback || ctx.Err() != nil || errors.Is(cause, ErrPolicyMismatch) || errors.Is(cause, ErrSettingsNotApplied) {
		return ThreadBinding{}, cause
	}
	diagnostic := Diagnostic(cause)
	// Only a definite rejection of the managed context request permits recovery.
	// Generic resume, transport, policy and cancellation failures keep their
	// original error and must never cause a second resume.
	if diagnostic.Method != "thread/inject_items" || diagnostic.RPCCode == nil ||
		(*diagnostic.RPCCode != -32601 && *diagnostic.RPCCode != -32602) {
		return ThreadBinding{}, cause
	}
	retirement, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := UnsubscribeThread(retirement, c, thread); err != nil {
		return ThreadBinding{}, errors.Join(cause, fmt.Errorf("managed base fallback subscription could not retire: %w", err))
	}
	if err := AwaitThreadRetirement(retirement, c, thread, func() error { return retirement.Err() }); err != nil {
		return ThreadBinding{}, errors.Join(cause, fmt.Errorf("managed base fallback could not prove the existing thread unloaded: %w", err))
	}
	// The last confirmed context is already in the preserved history. A refused
	// new append must not duplicate it, including on unsupported endpoints.
	managed.Base, managed.Fallback, managed.Accepted, managed.Legacy = managed.Previous, true, false, false
	binding, err := c.ResumeThreadWithSettings(ctx, thread, cwd, roots, settings)
	if err != nil {
		return ThreadBinding{}, errors.Join(cause, fmt.Errorf("last confirmed managed base could not resume: %w", err))
	}
	return binding, nil
}
