package projmux

import "github.com/crevissepartners/projmux/pkg/projmux/core"

// ResumeRecipeChangedError exposes recorded process-resume differences through
// errors.As, including detached process ResumeAgent failures.
type ResumeRecipeChangedError = core.ResumeRecipeChangedError

// ResumeRecipeChange is one safe recorded-to-current setting difference.
type ResumeRecipeChange = core.ResumeRecipeChange
