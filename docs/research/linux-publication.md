# Linux publication / recovery contract (issue #8)

Status: bounded technical investigation, not the #9 publisher or #10 recovery implementation.
Canonical product design: [Draft v0.2 §§5,9,15](https://chatgpt.com/space/page_3565e1d53fa08191a7d8cb56e84af5a5).
The probe is deliberately unexported and is not called by the CLI. No new runtime dependency.

## Adopted API and environment gate

- Linux amd64/arm64, Go 1.27.1, CGO_ENABLED=0. Probe directly calls the Linux
  `renameat2` syscall (UAPI numbers 316 / 276); #9 should use a reviewed Linux wrapper,
  not copy a platform number to other architectures.
- New output, source-to-backup and backup restoration: `renameat2(...,
  RENAME_NOREPLACE)`. An existence check is never the no-clobber guarantee.
- Explicit replacement of a *different* existing output: `renameat2(..., 0)`
  (`renameat` semantics). Never unlink first. Never copy/delete on EXDEV,
  ENOSYS, EOPNOTSUPP, EINVAL or any other error. Errors are non-zero with
  old output/stage retained as evidence. No automatic retry of translation.
- Use opened directory FDs and single-component names. Source, backup and final
  live in the same directory in same-path mode; stage is inside a private 0700
  run directory beneath the output directory. Cross-mount publication is refused,
  even when device identifiers happen to match through a bind mount.
- Initial supported persistent filesystem is local ext4, with the CI mount
  matrix below as evidence; tmpfs is the tested volatile development target.
  Each target mount still needs the capability gate. Overlay is tested as a development
  filesystem only, **not certified for v1**. XFS/btrfs are documented kernel
  candidates but untested here. NFS/CIFS/FUSE/other distributed or unknown mounts
  are refused in v1; do not infer eligibility solely from syscall availability.
- #9 must enforce an allowlist of demonstrated filesystem types and perform
  disposable same-directory capability checks before touching real paths. A
  successful probe is evidence for that mounted environment, not proof of all
  kernels, mount options or hostile concurrent writers. ext4 is selected from
  measured evidence; other persistent filesystem families remain unqualified.

[Linux rename API](https://man7.org/linux/man-pages/man2/rename.2.html)
provides no-replace and single-rename replacement, not a transaction over two
renames. [Linux flock](https://man7.org/linux/man-pages/man2/flock.2.html)
is advisory and released on process exit. [Overlay documentation](https://www.kernel.org/doc/html/latest/filesystems/overlayfs.html)
explains why overlay results are not a certification of lower/upper configurations.

## Paths, ownership and concurrent writers

1. Open all parent components with directory/no-follow semantics; reject any
   symlink component, dangling symlink, non-regular leaf, and ambiguous aliases.
   `realpath` plus a later pathname open is insufficient against component swaps.
   Record absolute diagnostic paths **and** open-FD dev/inode identities.
2. Reject source/output hardlink aliases via dev/inode comparison, including
   different spellings of the same existing inode. Conservatively reject
   existing source/output with link count other than one. An explicitly
   same-path request uses one validated parent+basename, never an alias.
3. Acquire nonblocking exclusive `flock` on the **output parent directory inode**
   before recovery/preflight and hold through final reconciliation. This avoids
   stale PID and unlink/recreated-lockfile races. It intentionally serializes all
   Yakuori publications to a directory; busy exits clearly rather than waiting
   indefinitely. Acquire source-parent lock too when distinct, deduplicated and
   ordered by `(dev,ino)`; release all if any acquisition fails. All participating
   Yakuori commands must obey this protocol. Do not unlink a locked object.
4. Snapshot source bytes for import. Immediately before publication recheck source,
   output, parent and stage dev/inode, size, mode, link count and SHA-256. Any
   detected drift fails closed. For replacement record the preexisting output's
   identity/hash as well. Stage is owned exclusively, closed to writing after
   final validation, and rehashed before publication.
5. External writers must be closed. A check followed by rename is **not** a
   compare-and-swap of inode/content. An external writer can change bytes or
   insert a new file after a check; explicit replace cannot atomically condition
   replacement on the previously observed identity. No-replace still protects
   an arriving destination. No guarantee against hostile same-UID processes,
   directory replacement, already-open writable FDs, or uncooperative writers.
   The probe demonstrates changed-content detection, not elimination of this race.

## CLI mapping (engineering recommendation for #9)

Existing approved behavior maps to the following mutually exclusive modes; these
options are **not implemented** by this PR:

| Form | Behavior |
| --- | --- |
| `localize --output OUT SOURCE` | OUT must not exist; no-replace publish |
| `localize --output OUT --replace-output SOURCE` | explicit atomic replacement, OUT distinct from SOURCE; absent OUT may be created |
| `localize --in-place SOURCE` | same validated path; preserve original backup, then no-replace publish |

Reject `--in-place` together with `--output` or `--replace-output`; reject
source=output in ordinary mode and explain `--in-place`. These names make the
already-approved distinction explicit, without adding a force/delete bypass.
A separate automatic cleanup/backup deletion/recovery command is not introduced.
Recovery runs before new localization as the design requires. Any later proposal
to remove backup, follow symlinks or broaden replacement needs product review.

## Run record and backup naming

Run ID: 128 random bits as 32 lower-case hex characters from `crypto/rand`; fail
on randomness error. Private directory `.yakuori-run-<runID>` is mkdir-exclusive
0700 in the final parent. Backup basename `.yakuori-backup-<runID>` is fixed length
(to avoid NAME_MAX failures with long original names), **in the original directory**.
The run record supplies the original basename. Never reuse/overwrite a colliding
run directory or backup. A backup collision fails before source movement; keeping
it for inspection is preferable to silently choosing an unrecorded new name.
Backup stays after success. This is an ordinary renamed original, not a copy.

Record schema v1, mode, run ID, absolute source/output/stage/backup paths, parent
identities, source and stage identity+SHA-256+size, existing-output identity/hash
or explicit absence, and phase/last completed operation. No text or prompt.
Record the selected backup destination before any source rename. Use a bounded
JSON file with strict fields, valid schema and validated relative basenames.
Paths outside this run/recorded parent, mismatched IDs, duplicates, malformed or
truncated records are manual-no-mutation errors. Hashes establish content identity,
not authorship; also require inode/mode/link-count identity evidence.

Write each record snapshot into a fresh 0600 temp within the private run directory,
check full write, Sync and Close, rename over record, and sync the directory where
supported. Close the prepared record before touching the source. A failed record
write/close blocks the destructive transition. Filesystem evidence has priority
when a rename completed but the next phase record did not. Record updates are
single renames too. These precautions do **not** claim power-loss durability.

## Stop/recovery table

All recovery requires valid record, directory locks, trusted parent identities,
regular single-link files and the exact recorded hashes/identities. No old stage
is automatically published. If evidence is ambiguous: retain everything, non-zero,
print run ID, final/backup paths and the conflict reason without file bodies.

| Stop/error boundary | Evidence / action on next run |
| --- | --- |
| Preflight, import, generation, any unit failure/cancellation | No TM writes or output mutation; leave source/output intact |
| Stage create/write/flush/close | No TM commit or publish; partial stage cannot qualify as validated |
| Final validation | Failure means no TM writes/publish; stage may be inspected then discarded only under owned-file policy |
| Before/during TM commit | No publish until commit success; ambiguous commit is reconciled by DB contract, not assumed rolled back |
| TM committed, before prepared record close | Accepted TM rows may remain; source/output untouched; no source rename |
| Prepared record closed, before backup rename | Final is original, backup absent: unchanged; abandon old stage, new run may start after reconciliation |
| Backup rename failed | Source unchanged, backup collision retained; abort. If error outcome uncertain inspect actual paths |
| Backup rename succeeded, phase record stale | Final absent + backup original: restore original with NOREPLACE; never trust phase label alone |
| Before/during second rename, error/cancellation | Inspect paths first. Final absent + backup original: NOREPLACE restore. A newly appearing final blocks restoration |
| Second rename succeeded, record stale | Final matches recorded stage inode/hash, backup matches original, stage absent: published; keep backup |
| Published, diagnostic/stdout/record update fails | Report publication separately from later command failure; never delete or roll back published result |
| Final exists with other bytes, backup mismatch, extra links, corrupt/missing record | Manual-no-mutation; keep all evidence. Missing record never authorizes guessing |
| Restore succeeded, completion-record write fails | Original at final, backup absent: unchanged/restored; never infer publication solely from hash |

When original and translated hashes coincide, distinguish source/stage inode,
backup and stage presence. A matching hash alone is never sufficient. For ordinary
output, final matching the recorded stage inode/hash with stage absent establishes
publication; previous-output identity with intact stage means not yet published;
anything else is manual-no-mutation. Never replay explicit replacement over a
newly arrived unrelated output during recovery.

**The two same-path renames are not atomic together.** Between them final is
absent and original exists at backup. On safe restoration backup is moved back,
not deleted. Following confirmed publication the original backup remains.
Cancellation after source movement first reconciles/restores; after publication
it does not roll back. stdout write cannot retract delivered bytes and is outside
file atomicity. Power-off/reboot durability and arbitrary external-writer
coordination remain out of scope.

## Reproduction and observed results

Fixtures are generated ASCII strings in temporary directories; no external data
or license-dependent game files. Toolchain pin and digest are in `ci/Dockerfile`.
Base commit: `72fe380b3e2e84cc95e3c38da820991d1c309b53`.

```sh
export CGO_ENABLED=0 GOTOOLCHAIN=local
# Use Go 1.27.1 (not the unrelated /usr/bin/go executable).
go test -count=1 -v ./research/publication
# Run the same matrix on the target mount:
mkdir -p /path/on/target-fs/probe-tmp
TMPDIR=/path/on/target-fs/probe-tmp go test -count=1 -v ./research/publication
sh ci/verify.sh
go vet ./research/publication
GOOS=linux GOARCH=arm64 go test -c -o /tmp/publication-arm64.test ./research/publication
```

2026-10-02, Linux 6.18.44 amd64, Go 1.27.1, non-root, CGO=0:

- PASS on tmpfs (magic 0x1021994): syscall no-replace, existing output/backup
  collision preservation, atomic replacement/open old reader, 16 concurrent
  publishers with exactly one winner, symlink/hardlink rejection, content-change
  detection, permission-denied preservation, invalid-flag refusal, directory lock.
- PASS: real child process abrupt exits (no defers) immediately before backup,
  between renames and after publish; OS lock release and filesystem reconciliation.
  This tests those publication boundaries, not every pipeline fault injection.
- PASS: nine table fixtures including equal original/staged hashes, new writer,
  changed backup and leftover stage ambiguity; racing recovery destination remains
  untouched. This is a decision-table model, **not** product recovery validation.
- PASS: EXDEV across tmpfs temporary fixtures and overlay checkout; no copy fallback.
- PASS on overlay (0x794c7630): same probe matrix; cross-mount case skipped
  in this run because fixture mounts coincide (separate tmpfs run passes it).
- PASS: full `ci/verify.sh`, build/test/vet, cgo scan and arm64 probe cross-compile.
- Remaining actual #9/#10/#21 gates: pipeline/DB/stdout injection, SIGKILL at
  arbitrary instruction timing, corrupt-record parser tests, openat component
  traversal races, source-parent multi-lock ordering, power-loss (out of scope),
  arm64 runtime (cross-compile alone is not runtime evidence).

Do not mark the product R3/R4 gates passed from this spike. The filesystem support
matrix is deliberately narrower than Linux API documentation; unsupported or
unmeasured mounts cannot silently enter the v1 support claim.

### CI ext4 qualification

[Non-root filesystem run 36979000748](https://github.com/sh4869221b/yakuori/actions/runs/36979000748)
on head `7ec49f9c9490d4b410124439f9c2f806e8f6f63e` passed the matrix on
Linux 6.17.0-1022-azure, Ubuntu 24.04 runner, Go 1.27.1. `findmnt` identifies the
host mount as **ext4** (statfs magic 0xef53 alone cannot distinguish ext2/3/4).
Observed options: `rw,relatime,discard,journal_async_commit,nobarrier,errors=remount-ro,commit=30,data=writeback`.
All same-mount probe cases passed, including non-root EACCES. The host EXDEV case
was correctly skipped (same device); the second container-overlay-to-host run
passed EXDEV and preserved both files. Format/vet and arm64 probe cross-compile
also passed. [Foundation run 36979000658](https://github.com/sh4869221b/yakuori/actions/runs/36979000658)
passed the clean CGO-free build/test/vet/cgo guard. The PR workflow checks the
synthetic merge against unchanged base `72fe380b`; this is CI associated with the
stated head, not an arm64 execution or power-loss test. These options and results
do not imply power-loss durability, nor prescribe users' mount settings.
