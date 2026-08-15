package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// MapMetadataError converts a resource metadata failure into the CLI error
// channel.
//
// Metadata operations classify their own failures: invalid operator input
// (explicit name collision, rebind root collision, invalid name, missing or
// non-absolute root) is a usage error and reaches exit code 2 through the
// existing UsageError path in cmd/projmux/main.go. Everything else (missing
// uid, inconsistent persisted registry, exhausted suffix space) stays a
// runtime error and reaches exit code 1.
//
// The metadata layer cannot import internal/app, so this is the single seam
// where the core's typed errors meet the CLI exit-code contract. No public
// route is wired to it in this phase; the resource routes adopt it when they
// move.
func MapMetadataError(err error) error {
	if err == nil {
		return nil
	}
	if coremetadata.IsUsageError(err) {
		return usageError(err.Error())
	}
	return err
}
