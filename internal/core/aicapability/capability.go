// Package aicapability defines the provider-neutral capability projection used
// by Agent actions. Provider protocol values are normalized before they reach
// this package.
package aicapability

import "errors"

var ErrUnavailable = errors.New("AI provider capability unavailable")

type ReviewCapability struct {
	Available bool
	Reason    string
}

type ReviewTargetKind string

const (
	ReviewUncommitted ReviewTargetKind = "uncommitted-changes"
	ReviewBaseBranch  ReviewTargetKind = "base-branch"
	ReviewCommit      ReviewTargetKind = "commit"
	ReviewCustom      ReviewTargetKind = "custom"
)

type ReviewTarget struct {
	Kind  ReviewTargetKind
	Value string
}

type ReviewStatus string

const (
	ReviewInProgress  ReviewStatus = "in-progress"
	ReviewCompleted   ReviewStatus = "completed"
	ReviewFailed      ReviewStatus = "failed"
	ReviewInterrupted ReviewStatus = "interrupted"
	ReviewUnknown     ReviewStatus = "unknown"
)

type ReviewResult struct {
	ThreadID string
	TurnID   string
	Status   ReviewStatus
}
