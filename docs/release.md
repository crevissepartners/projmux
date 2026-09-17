# Release

This document describes how a projmux version is released. For the npm
package layout, publish order, and trusted publishing, see
[npm-distribution.md](npm-distribution.md). For the PR title rules that feed
the release notes, see [pr-guideline.md](pr-guideline.md).

## Stable releases through release-please

`release-please-action` watches `main`, accumulates Conventional Commit
subjects, and opens or refreshes a "chore(main): release X.Y.Z" PR. That PR
contains the version bump (`internal/version/version.go` and
`.release-please-manifest.json`), the `CHANGELOG.md` updates, and the release
notes.

Merging the release PR creates the GitHub Release with auto-generated notes as
a **draft** (`draft` in `release-please-config.json`).

## Tag creation

The same package config sets `force-tag-creation`, so release-please creates
`refs/tags/vX.Y.Z` during its release pass, before it computes the next release
PR. That tag creation triggers the tag workflow through the release-please PAT.

Do not move tag creation into a post-action workflow step. release-please-action
creates releases before pull requests, and a delayed tag makes the same run
treat the just-released commits as unreleased.

## The tag workflow

`.github/workflows/release.yml` triggers on the tag push. It runs the shipped
E2E shard/suite matrix behind the fail-closed `Release E2E Tests` aggregate,
then builds the linux/darwin × amd64/arm64 matrix and uploads tarballs to the
drafted release (`gh release upload --clobber`). `Build Release` depends on the
aggregate rather than on individual shards.

Do not add hardcoded notes back to that workflow. release-please owns the
notes.

## Publish ordering

The release becomes visible only in the final `publish-release` job, after
`publish-npm` succeeds. That ordering is the contract:

- Users never see a GitHub release for a version npm cannot install yet.
- A failed npm publish leaves the release drafted and the workflow red instead
  of shipping a half-published version.

See [npm-distribution.md](npm-distribution.md#publish-order) for the npm side
of that order.

## Release candidates

Release candidates are cut **outside** release-please, by the
`workflow_dispatch`-only `.github/workflows/release-rc.yml`. It takes an
`X.Y.Z-rc.N` version, drafts a GitHub Release marked `--prerelease` against
`main` HEAD, then pushes `vX.Y.Z-rc.N` with the release-please PAT, so the same
`release.yml` builds, uploads, and publishes it.

Running it is a deliberate manual act: npm publishes cannot be recalled.

### Do not configure prerelease in release-please

Do **not** add `prerelease` or `prerelease-type` to `release-please-config.json`
to get release candidates. release-please gates the prerelease flag on
`config.prerelease && (version.preRelease || version.major === 0)`. This
repository is `0.x`, so the key would stamp *stable* releases as prereleases,
`releases/latest` would stop resolving, and default-channel updates would go
silently dead.

### The rc path never touches the manifest

The rc path writes exactly one ref: the tag. It never touches
`.release-please-manifest.json`, so release-please keeps measuring the next
stable release from the previous *stable* release.

release-please picks that boundary by matching the manifest value against a
tag, with no prerelease filter on that path. An rc holding the manifest slot
would silently trim the next stable release notes and its `compare/` link.

## Channel branching in release.yml

`release.yml` branches on the tag only where the channel is decided:

- A prerelease tag publishes npm with `--tag rc` and keeps `--prerelease` when
  the release is undrafted.
- A stable tag runs exactly as before.

Job names, order, and the `publish-npm` → `publish-release` gating are
identical for both. `test/release_workflow_contract_test.py` executes both step
scripts against a stable and an rc tag to hold that split.

## Commit subjects

Non-Conventional commit subjects on `main` are silently skipped by
release-please. Keep PR titles strict. Squash merge ensures the PR title is the
only subject that lands.
