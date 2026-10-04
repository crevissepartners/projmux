<!--
Title: Conventional Commits, e.g. `fix(scope): imperative summary`.
Keep all six sections in order. Background and Measurements may be `N/A`
with a one-line reason. Keep in sync with docs/pr-guideline.md.
-->

## Summary
<!-- What changed, in 1–3 bullets. -->
-

## Background
<!-- Why this is needed: the problem, a reproduction, related issues or PRs.
Use `Closes #<n>` to auto-close issues. -->
-

## Changes
<!-- Behavior and code changes, grouped by area. -->
-
<!-- Breaking: what breaks and how to migrate (if any). Also mark the title
with `!` or add a `BREAKING CHANGE:` footer. -->

## Scope
- In scope:
- Out of scope (and follow-ups):

## Verification
- [ ] Local checks: `make fmt` → `make fix` → `make test`
- [ ] CI checks on the same head: required `Format`, `Unit Tests`, `NPM Packages`, `Integration Tests`, `E2E Tests`, and aggregate `Test` green
- [ ] Manual steps (if relevant):
- Globalization (check exactly one):
  - [ ] No user-facing string changes.
  - [ ] User-facing strings are behind `internal/i18n` catalog keys with tests.
  - [ ] Non-translated strings are classified as literal/data/debug-only.

## Measurements
<!-- (If relevant) before/after numbers, method, environment, run ids.
Mark each number as observed or inferred. -->
-
