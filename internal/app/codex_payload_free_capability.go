package app

import "github.com/crevissepartners/projmux/internal/integrations/agents/codexgeneration"

// projectCodexPayloadFree is the single Doctor/create semantic projection.
// A nil source is the production Phase-1 state until an exact live tuple is
// supplied; invalid evidence is reduced by the owner to unknown/plain.
func projectCodexPayloadFree(source func() codexgeneration.Record) codexgeneration.Projection {
	record := codexgeneration.Record{}
	if source != nil {
		record = source()
	}
	return codexgeneration.Project(record)
}
