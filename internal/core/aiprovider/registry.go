// Package aiprovider re-exports the shared AI agent provider registry.
//
// The registry itself lives in the leaf package internal/aiprovider so that
// internal/config (a leaf layer) can consume provider identifiers without
// importing internal/core. This compat facade keeps the historical
// internal/core/aiprovider import path working for app and adapter callers;
// new code should import internal/aiprovider directly.
package aiprovider

import "github.com/crevissepartners/projmux/internal/aiprovider"

type (
	ID              = aiprovider.ID
	Metadata        = aiprovider.Metadata
	SupportMetadata = aiprovider.SupportMetadata
)

const (
	Claude      = aiprovider.Claude
	Codex       = aiprovider.Codex
	Antigravity = aiprovider.Antigravity
)

var (
	All                     = aiprovider.All
	Lookup                  = aiprovider.Lookup
	SettingsVisible         = aiprovider.SettingsVisible
	PickerEligible          = aiprovider.PickerEligible
	UsageSupported          = aiprovider.UsageSupported
	HookDiagnosticSupported = aiprovider.HookDiagnosticSupported
	SessionStateSupported   = aiprovider.SessionStateSupported
)
