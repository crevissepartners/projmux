package core

// UnregisterRequest names exactly one Project. Refs follows the CLI grammar:
// a name or uid:<uid>. Empty and multiple refs are refused before any write.
type UnregisterRequest struct {
	Refs    []string
	DryRun  bool
	Confirm bool
}

type DeleteResource struct {
	Kind Kind   `json:"kind"`
	UID  string `json:"uid"`
	Name string `json:"name"`
}
type DeleteTarget struct {
	DeleteResource
	Descendants []DeleteResource `json:"descendants"`
}
type DeletionAffected struct {
	Kind   string `json:"kind"`
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Action string `json:"action"`
}
type DeletePlan struct {
	Kind     Kind               `json:"kind"`
	Targets  []DeleteTarget     `json:"targets"`
	Cascades int                `json:"cascades"`
	Affected []DeletionAffected `json:"affected"`
}

// UnregisterResult returns the reviewed plan even when confirmation is missing.
// A committed change preserves runtime and external files. OperationID identifies
// the best-effort deletion record; an empty value means minting failed. Notice
// reports a record warning without failing the committed Registry transaction.
type UnregisterResult struct {
	Plan        DeletePlan
	Receipt     OperationReceipt
	OperationID string
	Committed   bool
	Notice      string
}
