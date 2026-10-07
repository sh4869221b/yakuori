# Public-readiness checklist

Review baseline: `5df2c9232c869aacb1489b67693482be3a72f651` (2026-10-07).
This document records the source/notice preparation and its validation scope.
Repository visibility, pull-request checks and merge status must be verified
against GitHub; this document is not a claim that a binary release exists.

## Prepared and verified

- Original Yakuori code/documentation now has an MIT LICENSE, with third-party
  fixture, dependency, model and GPL oracle terms explicitly preserved.
- Private design-page URLs and a machine-specific home/worktree path were removed
  from current documentation, without copying private design text.
- The development status remains explicit: `localize` has no production backend;
  no completed translation application, real model/TM integration or release is claimed.
- All 13 fixed Go modules were retrieved from the official proxy, checked against
  unchanged go.sum and verified by `go mod verify`. Root/nested notices and file
  headers were inspected, with target-specific package/file selection evidence.
- `sh ci/verify.sh` passed locally on Go 1.27.1/linux/amd64: cgo guard and its
  negative self-test, dependency cgo checks for amd64/arm64, formatting, CLI build,
  all 14 root-package tests, vet, arm64 CLI and SQLite-test cross-builds.
- This materialized source snapshot uses `GOFLAGS=-buildvcs=false` because its
  parent workspace has no usable Git checkout metadata. No product code was
  changed to satisfy tests. Downloads were disabled during tests.
- Exact built CLI metadata contains x/sys v0.48.0 as its sole external module.
  SQLite and inference libraries are only in separate tests/probes at this baseline.

## Publication and distribution controls

1. **Historical metadata retention accepted (2026-10-07).** The owner explicitly
   accepted leaving the known email identity, private design-page URLs/titles
   (not private page contents or access rights), and machine-specific worktree
   path in Git history and issue/PR text. These known items are no longer an
   unresolved publication-readiness decision. No history rewrite or remote
   discussion edit was requested or performed as part of that metadata decision.
   Metadata retention and repository publication remain separate decisions.
2. **Control CI costs before pushing.** Current workflows run Docker builds on
   PRs and main pushes. Even documentation edits can start three jobs; some
   dependency/probe changes add model download/inference. No hosted CI was run.
3. **Verify the approved publication revision.** Review the exact PR head, its
   required checks and the resulting main commit before changing visibility.
   Source publication does not also publish a binary release or research image.
4. **Keep distribution scope narrow.** This preparation covers original source,
   retained licensed fixtures and notices. It does not approve redistributing
   whole Go module caches, a SQLite test executable, the CPU probe, GPL oracle,
   model assets or research Docker images. Their additional notices/provenance
   and any source-delivery duties must be resolved for the exact output shipped.
5. **Do not overstate runtime coverage.** Clean Docker reproduction, arm64
   execution, independent Rust oracle execution, real-model inference and actual
   game acceptance were not repeated. Unit tests/cross-builds do not substitute
   for those separate checks.

## CLI archive contents

Use an explicit allowlist: the exact target CLI binary, root LICENSE,
THIRD_PARTY_NOTICES.md, applicable verbatim license/attribution files under
licenses/, a concise README and checksums. Verify `go version -m` against the
actual executable and retain an exact module manifest for that build. The Go
runtime includes file-level notices beyond the root BSD license; retain
`licenses/upstream/go-1.27.1/THIRD_PARTY_SOURCE_NOTICES.txt` as well.

Never package the entire working directory as a binary release. Exclude model
weights, Rust oracle executables, probe/test binaries, caches, databases, recovery
records, private paths and Docker layers. Include MOD fixtures only intentionally
with their adjacent MIT notices. A future import, backend, platform or dependency
change invalidates the current binary-closure assessment and requires review.

Do not add a release workflow or auto-publish job until those gates are met.
These are engineering risk checks, not a legal opinion or warranty of rights.
