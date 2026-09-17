// Package codexgeneration owns the pure, content-free lifecycle and consumer
// projection for a Codex app-server endpoint generation. It has no provider,
// process, filesystem, Registry-writer, or tmux dependency; callers supply the
// durable evidence and perform every write themselves.
package codexgeneration

import "github.com/crevissepartners/projmux/internal/core/metadata"

type GenerationState = metadata.CodexGenerationState

const (
	StatePreparing       = metadata.CodexGenerationPreparing
	StateCurrent         = metadata.CodexGenerationCurrent
	StateDraining        = metadata.CodexGenerationDraining
	StateHandoverPending = metadata.CodexGenerationHandoverPending
	StateRetired         = metadata.CodexGenerationRetired
	StateRecovering      = metadata.CodexGenerationRecovering
	StateBlocked         = metadata.CodexGenerationBlocked
)
