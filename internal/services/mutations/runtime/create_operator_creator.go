package runtime

import (
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
)

// CreatorDeclinedOperator is the declaration token a valid declared creator
// gets when an operator creator is recorded in its place.
const CreatorDeclinedOperator = "operator-client"

// creatorDeclinedNoticeFormat is the one line, without its newline, a create
// owes for a declared creator it did not record.
const creatorDeclinedNoticeFormat = "creator declaration not recorded: %s (--creator uid:%s)"

// CreatorDeclinedNotice is the line the CLI prints to stderr and the SDK
// returns in Notices for a declaration that lost to stronger evidence.
func CreatorDeclinedNotice(declined, declared string) string {
	return fmt.Sprintf(creatorDeclinedNoticeFormat, declined, declared)
}

// OperatorAgentCreator is the operator creator record for client, or no record
// for a name outside the operator client rule. Its producers are in-process
// seams only; TestNoArgvPathBuildsAnOperatorCreator holds the list.
func OperatorAgentCreator(client string) AgentCreatorRecord {
	if operatorclient.Validate(client) != nil {
		return AgentCreatorRecord{}
	}
	return AgentCreatorRecord{Annotations: coremetadata.OperatorCreatorAnnotations(client)}
}

// OperatorCreatorDecision is the operator branch of creator precedence: the
// record an operator create writes, and the declaration it replaced.
type OperatorCreatorDecision struct {
	AgentCreatorRecord
	// Declared is the bare Agent UID of a declaration the operator record
	// replaced, and Declined its token; both are empty without a declaration.
	Declared, Declined string
}

// DecideOperatorCreator is the operator precedence the CLI and the SDK share.
// An operator client is recorded in place of any observation or declaration;
// a declaration, already checked against the Registry by DeclaredAgentCreator,
// is reported as declined.
func DecideOperatorCreator(client, declared string) OperatorCreatorDecision {
	decision := OperatorCreatorDecision{AgentCreatorRecord: OperatorAgentCreator(client)}
	if declared != "" {
		decision.Declared, decision.Declined = declared, CreatorDeclinedOperator
	}
	return decision
}
