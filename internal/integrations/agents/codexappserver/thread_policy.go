package codexappserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Thread policy request values, spelled exactly as thread/start and
// thread/resume take them. They are the only values ThreadPolicy may carry:
// anything else is refused before the wire.
const (
	SandboxReadOnly         = "read-only"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxDangerFullAccess = "danger-full-access"

	ApprovalUntrusted = "untrusted"
	ApprovalOnRequest = "on-request"
	ApprovalNever     = "never"
)

// sandboxResponseTypes maps each request sandbox to the `type` the answer's
// sandbox object carries for it. The request spells the mode in kebab case
// and the answer in camel case; the fourth answer type, externalSandbox, has
// no request spelling and so can never match a requested sandbox.
var sandboxResponseTypes = map[string]string{
	SandboxReadOnly:         "readOnly",
	SandboxWorkspaceWrite:   "workspaceWrite",
	SandboxDangerFullAccess: "dangerFullAccess",
}

// approvalPolicies is the string approval vocabulary. Upstream also takes a
// granular object, which projmux never sends.
var approvalPolicies = []string{ApprovalUntrusted, ApprovalOnRequest, ApprovalNever}

// ThreadPolicy is the sandbox and approval policy one thread is started or
// resumed with. An empty field is not requested: the key stays off the wire
// and the upstream configuration decides it, and the answer is not checked
// for it. The zero value requests nothing, which is every thread projmux
// started or resumed before a profile could carry a policy.
type ThreadPolicy struct {
	Sandbox        string
	ApprovalPolicy string
}

// IsZero reports a policy that requests nothing.
func (p ThreadPolicy) IsZero() bool { return p.Sandbox == "" && p.ApprovalPolicy == "" }

// validate refuses a value outside the request vocabulary, so a caller's
// mistake never reaches the wire as a value upstream might read differently.
func (p ThreadPolicy) validate() error {
	if _, ok := sandboxResponseTypes[p.Sandbox]; p.Sandbox != "" && !ok {
		return fmt.Errorf("%w: unknown thread sandbox %q", ErrProtocol, p.Sandbox)
	}
	if p.ApprovalPolicy != "" && !slices.Contains(approvalPolicies, p.ApprovalPolicy) {
		return fmt.Errorf("%w: unknown thread approval policy %q", ErrProtocol, p.ApprovalPolicy)
	}
	return nil
}

// ErrPolicyMismatch is the sentinel of every PolicyMismatchError.
var ErrPolicyMismatch = errors.New("codex app-server thread policy mismatch")

// ReasonPolicyMismatch is the stable reason token of a thread whose effective
// policy is not the one requested.
const ReasonPolicyMismatch = "codex-thread-policy-mismatch"

// PolicyMismatchError is a thread/start or thread/resume whose answer reports
// an effective policy other than the one requested: a thread that was already
// loaded and kept its own policy, an endpoint that ignored the fields, or an
// answer that does not report the policy at all.
//
// It is deliberately not a ThreadActionError and never a safe fallback: the
// only launch it could fall back to is one without the requested policy, which
// is exactly the silent capability change a profile exists to prevent.
//
// Effective holds only closed-vocabulary words -- a known sandbox type or
// approval value, "granular", "unrecognized", or "missing" -- so the message
// never carries provider content.
type PolicyMismatchError struct {
	Method    string
	Requested ThreadPolicy
	Effective ThreadPolicy
}

func (e *PolicyMismatchError) Error() string {
	return fmt.Sprintf("%s: %s answered sandbox %s and approval %s, but sandbox %s and approval %s were requested",
		ReasonPolicyMismatch, e.Method,
		policyWord(e.Effective.Sandbox), policyWord(e.Effective.ApprovalPolicy),
		policyWord(e.Requested.Sandbox), policyWord(e.Requested.ApprovalPolicy))
}

func (e *PolicyMismatchError) Unwrap() error { return ErrPolicyMismatch }

func policyWord(value string) string {
	if value == "" {
		return "unrequested"
	}
	return value
}

// checkThreadPolicy compares the requested fields of policy with the
// effective policy the answer reports, and only those: a field that was not
// requested is upstream's to decide. A requested sandbox matches only the
// answer type it maps to; a requested approval matches only the identical
// string, so a granular object or a missing field is a mismatch.
func checkThreadPolicy(method string, policy ThreadPolicy, result threadResult) error {
	if policy.IsZero() {
		return nil
	}
	mismatch := false
	effective := ThreadPolicy{}
	if policy.Sandbox != "" {
		effective.Sandbox = effectiveSandbox(result.Sandbox)
		mismatch = mismatch || effective.Sandbox != sandboxResponseTypes[policy.Sandbox]
	}
	if policy.ApprovalPolicy != "" {
		effective.ApprovalPolicy = effectiveApproval(result.ApprovalPolicy)
		mismatch = mismatch || effective.ApprovalPolicy != policy.ApprovalPolicy
	}
	if mismatch {
		return &PolicyMismatchError{Method: method, Requested: policy, Effective: effective}
	}
	return nil
}

// effectiveSandbox is the answer's sandbox type when it is one of the known
// types, "missing" when the answer has none, and "unrecognized" otherwise.
func effectiveSandbox(raw json.RawMessage) string {
	if isAbsent(raw) {
		return "missing"
	}
	var sandbox struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &sandbox) != nil {
		return "unrecognized"
	}
	if sandbox.Type == "externalSandbox" {
		return sandbox.Type
	}
	for _, known := range sandboxResponseTypes {
		if sandbox.Type == known {
			return known
		}
	}
	return "unrecognized"
}

// effectiveApproval is the answer's approval policy when it is one of the
// string values, "granular" for an object, "missing" when the answer has
// none, and "unrecognized" otherwise.
func effectiveApproval(raw json.RawMessage) string {
	if isAbsent(raw) {
		return "missing"
	}
	var approval string
	if json.Unmarshal(raw, &approval) == nil {
		if slices.Contains(approvalPolicies, approval) {
			return approval
		}
		return "unrecognized"
	}
	var granular map[string]json.RawMessage
	if json.Unmarshal(raw, &granular) == nil {
		return "granular"
	}
	return "unrecognized"
}

func isAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}
