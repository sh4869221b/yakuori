# CGO-free SQLite / storage feasibility (#4)

## Result and scope

2026-10-02: adopt **modernc.org/sqlite v1.60.1** for the next TM implementation
step. Its actual engine reports **SQLite 3.53.4**. The original #4 PR supplied executable,
test-only storage experiments under `internal/sqliteprobe`, not a TM service,
accepted-translation constructor, production database opener, or CLI command.
No user database is opened or created by `yakuori`; every fixture is synthetic
and owned by `testing.T.TempDir` (private directory, database mode 0600).

Design reference: maintainer’s private Draft v0.2, sections 8/11/15.
The implementation evidence and limitations below are self-contained.
This records concrete implementation evidence without replacing that design.
#15 now supplies [artifact scope, canonical source/profile records and fixtures](source-profile-identity.md)
through the identity APIs; #16 now connects those records through the
[Core TM implementation below](#current-core-tm-integration-16). The probe does not invent
an artifact identity from a pathname or enable reuse across artifacts/games.

## Driver decision and provenance

| Candidate | Evidence / decision |
| --- | --- |
| [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite@v1.60.1) | Selected. Pure-Go generated SQLite, database/sql driver, pinned module and engine tested below. Canonical source is [GitLab cznic/sqlite](https://gitlab.com/cznic/sqlite/-/tree/v1.60.1). |
| [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) | Viable CGO-free database/sql alternative; current upstream translates SQLite Wasm with wasm2go and supplies its own Go VFS. Documentation-only comparison; no benchmark or correctness superiority claimed. Not added as a second runtime dependency because the selected driver passes this bounded gate. |
| [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3#installation) | Rejected for this project: its build requires CGO and a C compiler. Not installed/tested. |

- Module sum: `h1:/blz53O951KWFOso4QQvEs/Fq6cDBKLtMVrYNSeJVKw=`.
- Upstream tag commit: `b122d0417c01508beb55158faedeb64a0c5bfd8a`.
- `go.mod`/`go.sum` pin all selected dependencies. `go mod verify` passed.
- Keep `modernc.org/libc v1.77.1` aligned with this driver's go.mod; upstream
  explicitly warns about mismatching that dependency. Go 1.27.1 satisfies the
  driver's Go 1.26 requirement. Do not regenerate SQLite sources during build.
- Driver is BSD-3-Clause; SQLite is public domain. Binary distribution must carry
  applicable copyright/license/disclaimer notices and must not imply endorsement.
  See upstream [LICENSE](https://gitlab.com/cznic/sqlite/-/blob/v1.60.1/LICENSE),
  [LICENSE-3RD-PARTY.md](https://gitlab.com/cznic/sqlite/-/blob/v1.60.1/LICENSE-3RD-PARTY.md)
  and SBOM for bundled and transitive licenses. A release must package those
  notices for the actual linked dependency graph; this PR makes no release.

## Versioned schema contract

The executable schema constant is `tableSQL` in the probe and `internal/tm/schema.go`:

- SQLite `application_id = 1497451343` (`YAKO`) and `user_version = 1`.
- One `STRICT, WITHOUT ROWID` table `tm`.
- Unique primary key `(key_schema, source_identity, translation_profile)`.
- `key_schema` is explicitly 1; source/profile are nonempty BLOBs, compared by
  exact bytes. `translated_text` stores the restored parent unit text.
- No case folding, whitespace trimming, Unicode normalization or hash-only
  equality. Embedded NUL bytes in *identity blobs* remain distinct. That test
  does not permit NUL in actual translations; validation remains #7/#16.
- Source/profile blob contents are defined by the versioned canonical records
  from #15, with content hashes verified by its identity tests. Production storage
  now uses these records (#16). This probe schema proves unique exact
  byte comparison, not canonical serialization, SourceIdentity or acceptance.
- SQLite constraints alone never confer `AcceptedTranslation` status. A row is
  only a candidate and must pass the current unit/profile/manifest validation.

### Conflict and idempotency

After current validation, preserve an existing valid row for an exact key.
`INSERT ... ON CONFLICT(key_schema, source_identity, translation_profile) DO
NOTHING` retains the first committed valid row. Repeating the same key with a
different independently accepted translation is storage-idempotent: it neither
replaces that row nor promises identical newly generated output. Both sequential
and two-connection concurrent fixtures prove preservation. Do not use REPLACE,
a generic `INSERT OR IGNORE`, or unconditional upsert: unrelated constraint
failures must remain failures, and a valid translation must not be overwritten.

#16's production implementation distinguishes valid conflicts from invalid-row repair.
Validate an observed
row outside the write transaction. A repair may conditionally replace *that exact
observed invalid value* after final artifact validation; if the row changed,
abort and revalidate the new candidate outside the transaction. Never overwrite
an unexamined conflict, hold a writer during generation, or retry generation
implicitly. The original #4 probe implements neither repair nor validation.

### Open, corruption and migration

- Missing files are not silently created. An empty/foreign/corrupt/unknown database
  fails closed. New-store initialization must be an explicit separate path using
  exclusive creation, with no truncation or reset fallback (#16).
- The read-only probe checks integrity, application ID, version, exact schema SQL
  and absence of additional user schema objects before any write. Known-version
  but changed layout and an extra trigger are rejected, not trusted by version alone.
- Rejected fixture bytes are compared before/after; missing files stay missing.
  Quiescent `-journal`, `-wal` or `-shm` sidecars cause the probe to stop before
  SQLite opens anything; main and sidecar bytes are preserved. Never use
  `immutable=1` to ignore potentially committed WAL contents.
- This probe is a **quiescent-fixture proof**, not race-safe production opening.
  The current Store adds cooperative ownership, actual-connection schema checks
  and explicit journal recovery as described below. Do not copy the probe
  preflight as a TOCTOU-safe Open API.
- Fresh probe stores use rollback-journal `DELETE` mode. WAL migration, network
  filesystems, concurrent replacement of the database path and hostile directory
  mutation are not supported or proven by this spike.
- Every future migration must name exact supported source and target schemas,
  verify source layout, and apply schema, row changes and `user_version` in one
  short transaction. No best-effort partial migration, destructive reset or
  implicit downgrade. The fixture-only v1→v2 migration adds a column and bumps
  version; injected SQL failure rolls both back and preserves the existing row.
  Reopening verifies each result. **There is no released v2 schema/migration.**
- Integrity errors are not auto-repaired; retain all assets and report failure.
  Rollback guarantees logical transactional consistency, not byte-for-byte
  identity after a successful write/migration. Power-loss durability is not claimed.

## Finite waits and measured adoption values

Use one dedicated `sql.Conn` for the short `BEGIN IMMEDIATE` → work → `COMMIT`,
with `busy_timeout=100` milliseconds on every connection and a **1 second context
budget** starting before connection acquisition. No busy retry loop. Use context
on COMMIT itself: the pinned driver's `sql.Tx.Commit()` calls its SQLite commit
with `context.Background()`, so merely calling `BeginTx(ctx)` is insufficient to
claim that context bounds an in-flight commit. The experiment uses SQL transaction
control through database/sql on a reserved connection instead.

On error/cancellation, do not publish. Attempt ROLLBACK with an independent finite
1 second cleanup context; if cleanup fails, discard the connection via
`driver.ErrBadConn`. Never put an uncertain active transaction back into the pool.
COMMIT can leave a transaction active after SQLITE_BUSY, which the commit-contention
test specifically exercises. No automatic commit retry is performed.

Important measured limitation: **context cancellation does not immediately interrupt
SQLite's busy wait**. A 20 ms deadline during locked COMMIT returned after about
101 ms, with deadline error and no persisted row. Budget cancellation may therefore
overshoot by a busy interval plus scheduler/I/O delay. These are finite SQLite
lock waits and cancellation budgets, not hard wall-clock guarantees against a
hung kernel/filesystem. The 1.5 second test ceiling is regression tolerance, not
a promised service deadline. A requirement for hard 1 second process termination
would require a different isolation design; it is not claimed here.

Five consecutive runs on Linux amd64, Go 1.27.1, AMD EPYC 9V74, container reporting
9.7 GiB RAM, overlayfs, CGO=0:

| Fixture | Samples | Observed range |
| --- | ---: | ---: |
| Writer / commit contention, 100 ms busy wait | 10 | 101.050–103.834 ms |
| Recursive-query cancellation, 20 ms context | 5 | 20.278–20.462 ms |
| Cancellation during busy COMMIT, 20 ms context | 5 | 100.971–101.256 ms |
| 1,000 short synthetic rows including transaction/commit | 5 | 10.112–13.446 ms |
| Adopted 1 second context expires, inserted row rolled back | 5 | 1000.315–1000.927 ms |

100 ms gives brief competing short commits room to finish while preventing an
unbounded writer queue; 1 second gives substantial headroom over this ~13 ms
synthetic batch and was itself exercised at expiry. These measured spike values
are adopted for #16's initial bounded storage implementation, **not** maximum
MVP workload/performance acceptance. #14/#16 must measure permitted maximum
artifact/unit counts, storage sizes and slow supported local filesystems before
calling the final limit/performance gate complete. Values are constants in the
probe; no user configuration schema is prematurely added.

## Reproduce and evidence

```sh
export PATH=/path/to/verified/go1.27.1/bin:$PATH
export CGO_ENABLED=0 GOTOOLCHAIN=local
# Use writable GOPATH/GOCACHE if the host defaults are not writable.
go mod verify
go test -count=5 -v ./internal/sqliteprobe
sh ci/verify.sh
# Clean no-C/C++/CMake/SQLite-header gate (runs in PR CI):
docker build --progress=plain -f ci/Dockerfile .
```

Local results: PASS module verification, all tests (five spike runs), CGO-free
dependency checks, build, vet, Linux arm64 CLI cross-build and explicit arm64
SQLite-bearing test binary compilation. The expected negative cgo-guard fixture
prints `exit status 1` inside an intentionally caught failure; the script succeeds.
Docker was unavailable on that initial local executor; the PR's exact-head CI provides the
clean-image result. The tests run in the existing required Linux CI, and the
arm64 probe compilation was added because the ordinary CLI does not yet link SQLite.

Not executed/claimed by the original #4 spike: arm64 runtime, real game files/translation/acceptance,
maximum-workload benchmarks, WAL/concurrent file replacement, process-kill recovery,
power-loss tests, TM integration, security audit, release or deployment. Later
failures must not erase user assets, publish output or turn into a cache miss.

## Natural translation batch experiment (#14)

`TestMeasuredTranslationBatches` imports successful warm parent translations from
`YAKUORI_MEASURED_TRANSLATIONS`, opens a fresh test-owned SQLite fixture for each
batch, and inserts those actual UTF-8 outputs through the existing dedicated
connection transaction probe. It verifies the committed row count and reports
rows, translated bytes and transaction/commit elapsed time. It does not use
backend partial output from failed/interrupted translation and does not implement
production TM, source/profile serialization or cache lookup.

The trial conditions remain `busy_timeout=100ms`, operation context 1s (including
connection acquisition and COMMIT), separate cleanup context 1s, retry=0. These
are initial measurement conditions, not a new product-budget adoption. Existing
busy/rollback tests remain the contention and cleanup evidence. Translation CPU
measurement and DB measurement run sequentially to avoid load contamination.
The test-owned databases are under this host's `/tmp` (`tmpfs`,
`rw,noatime,inode64,huge=advise`); these commit timings do not demonstrate slow
persistent-filesystem performance.

```sh
YAKUORI_MEASURED_TRANSLATIONS="$PWD/docs/evidence/cpu-long-context/index-translate/results" \
  CGO_ENABLED=0 go test ./internal/sqliteprobe ./tools/cpuspike -count=1 -v
```

The [measured batch results](evidence/cpu-long-context/index-translate/sqlite-batches.json)
contain 27 successful warm batches (three contexts × three inputs × three pairs).
The full SQLite and CLI-probe test invocation above passed, including existing
writer/commit busy, cancellation, rollback and operation-budget checks. Each
measured database committed exactly its expected rows; failed/interrupted CPU
reports supply no batch.

| Rows per batch | Measured batches | UTF-8 translated bytes per batch | Transaction + commit range (ms) |
| ---: | ---: | ---: | ---: |
| 1 | 9 | 21 | 0.052148–0.069541 |
| 19 | 9 | 440 | 0.143651–0.159791 |
| 76 | 9 | 1760 | 0.431723–0.523306 |

All observed transaction/commit times fit the initial 1s operation budget on
this tmpfs host fixture. These values do not establish a persistent-disk SLO,
production TM capacity, serialization cost, cache-hit behavior or the cost of
larger unmeasured batches. No new database budget is adopted by this experiment.

The owner-approved task8 output-reservation experiment on the existing 5150-byte
single unit produced no accepted output: its first cold run reached MaxTokens
at context4096/output2048, and warm/later pairs were unrun. Therefore it supplied
**no additional successful batch** to the opt-in SQLite measurement. The
27 measured task2 batches above remain the available database evidence; task8
did not run a database measurement or establish a new transaction bound. See the
[additional CPU summary](evidence/cpu-long-context/index-translate/reserve2048/summary.json).

For task9's nonrepeated prose, the existing probe measured all nine successful
warm outputs after CPU measurement ended:

```sh
YAKUORI_MEASURED_TRANSLATIONS="$PWD/docs/evidence/cpu-long-context/index-translate/prose/results" \
  CGO_ENABLED=0 go test ./internal/sqliteprobe \
  -run '^TestMeasuredTranslationBatches$' -count=1 -v
```

All nine fresh transactions passed and committed one row each. Output sizes were
54 bytes (short),342 bytes (medium),1146 bytes (long). Maximum observed
transaction+commit time was **0.066195ms**, with the unchanged initial busy100ms,
operation1000ms and cleanup1000ms budgets, no retry. The
[individual measurements](evidence/cpu-long-context/index-translate/prose/sqlite-batches.json)
record each batch. Database files were on host `/tmp` tmpfs
(`rw,noatime,inode64,huge=advise`), as in task2; these observations do not establish
a persistent-disk SLO or production TM performance. No database job ran alongside
model inference. The opt-in experiment passed; unchanged full CI was not rerun.

## Adopted operation and cleanup budgets (#14)

The owner's candidate A adopts `DBOperationTimeout=1s` and
`DBCleanupTimeout=1s` in `config.DefaultLimits()`, with retry0. Core finalization
passes a fresh operation child context from the caller to TM.Commit, covering
connection acquisition and work performed by the implementation. It does not
reuse the generation child context. A cancelled/failed commit prevents later
publication/writing; the generic TM interface cannot perform SQLite rollback.

The Store implementation in #16 now applies busy100ms on each connection and
implements context-bound commit, transaction rollback and independent cleanup.
Core supplies the operation budget without configuring SQLite itself. The
27 label/sentence and nine prose batch measurements above used fresh test-owned
databases on `/tmp` tmpfs. They inform these budgets without establishing
persistent-disk performance, production TM throughput or a hard wall when an
implementation ignores context. Existing busy/cancel/rollback and finalization
boundary tests remain the behavioral evidence.

If a TM implementation returns nil after its operation context expires, Core
retains `TMCommitted=true` to report that actual return honestly. The expired
operation still returns an error and prevents subsequent write/publication.
This does not assert rollback of a commit that may already have happened; the
existing finalization test covers this distinction.

## Current Core TM integration (#16)

`internal/tm` implements the existing v1 schema using the pinned CGO-free driver.
`Create(ctx,path,limits)` exclusively creates a new store; `Open` opens only an
existing supported store and refuses sidecars. `Recover` is an explicit separate
entry for one interrupted DELETE journal: it first copies main/journal into a
private temporary directory and asks SQLite to recover and validate the copy,
then asks SQLite to recover the original. Unknown/corrupt databases and WAL/SHM
are retained and rejected. No product schema migration or reset fallback is added.

A Store owns `path+".lock"` with a nonblocking Linux flock until Close; it does
not unlink the lockfile. This coordinates cooperative Store owners on one local
database. It does not protect against noncooperative writers, concurrent path
replacement or hostile directory mutation. Independent Runs share a Store while
keeping their observations and staging separate.

Core's additive `TextWithTM` / `FileWithTM` take an explicit Store and
`tm.Scope{GameNamespace, ArtifactNamespace, Keys}`. Nil Store and noncanonical
profiles are errors. All units still pass limits/protection/request/profile
preflight before lookup or generation. Each exact hit passes current Validation
and receives fresh import binding; restored hit text is not restored again.
Only candidate-content rejection returns to the existing generation path;
source/profile or lookup I/O errors stop. Only complete accepted parent rows are
staged. Final export/observation/comparison and scope-to-manifest key relations,
including empty keys and duplicates, must all pass before commit.

`Run.Commit` uses a short reserved-connection `BEGIN IMMEDIATE`/row-work/`COMMIT`
with context on each SQL operation. An observed valid exact text is retained;
an observed invalid row is repaired only if its exact observed text remains.
Changed, missing or unobserved conflicting values stop the run and roll back the
whole batch without retry. Generation/Validation do not run in the transaction.
The existing operation and independent cleanup budgets are 1s each, with
busy_timeout=100ms per connection. Cleanup failure discards the connection.
The busy interval plus scheduling/I/O can overshoot a context deadline; these
are cooperative budgets, not hard wall-clock termination guarantees.

Commit failure prevents output/publication. Commit success followed by publication
failure retains saved rows and reports `TMCommitted=true` with the actual
publication state. `TestTMCommitPublishFailureRerun` preserves the failed target
and stage, then reimports the same source/scope and publishes to a fresh output
parent with zero generation. This does not prove automatic recovery in the same
destination: existing publication recovery conservatively stops on missing or
conflicting records. The normal CLI still connects neither Engine nor TM.

### Persistent-filesystem capacity observations

`TestTMAdoptedCapacity` used Linux amd64, Go `1.27.1-X:nodwarf5`, Btrfs, fresh
test-owned databases, warm OS caches without cache eviction, and no concurrent
inference. Count-bound and byte-bound fixtures separately satisfy current
DefaultLimits; they do not require impossible simultaneous maxima. Each row has
2048 restored ASCII bytes with a declared 2048-token request ceiling. This is a
synthetic storage fixture, not measured model/tokenizer output equivalence.

| Fixture | Rows | Artifact / source bytes | Largest unit | Source / profile BLOB bytes | Restored / DB bytes | Conn acquisition + commit, two runs (ms) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Count-bound | 76 | 1495 / 988 | 13 | 31084 / 115216 | 155648 / 368640 | 3.104 / 4.297 |
| Byte-bound | 2 | 1495 / 1036 | 980 | 818 / 3032 | 4096 / 16384 | 1.895 / 2.560 |

Both runs fit the unchanged 1s operation budget. Closing/reopening the Store and
looking up every exact row verified restored text bytes. Actual maximum output
bytes under the real tokenizer and latency on slow supported filesystems remain
**UNVERIFIED**. These observations do not complete a general performance gate or
establish a persistent-disk SLO.

Reproduce using a test-owned ext4/Btrfs TMPDIR; unsupported filesystems skip the
publication/capacity cases and are not passing persistence evidence:

```sh
TMPDIR=/path/to/test-owned-persistent-dir CGO_ENABLED=0 GOTOOLCHAIN=local \
  go test -count=1 -v ./internal/localize -run '^TestTM'
TMPDIR=/path/to/test-owned-persistent-dir CGO_ENABLED=0 GOTOOLCHAIN=local \
  sh ci/verify.sh
```

The focused tests cover exact-hit revalidation, observed-invalid repair, atomic
failure, competing Runs, reader/writer deadlines, parent-only segmented rows and
commit/publication reruns. `TestMigrationAtomic` remains a probe-only migration
fixture; production rejects unknown versions. The verification script also
cross-compiles `internal/tm` and the retained probe test binaries for Linux arm64;
arm64 execution, real-game acceptance, release and deployment remain unverified.
