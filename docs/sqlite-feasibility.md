# CGO-free SQLite / storage feasibility (#4)

## Result and scope

2026-10-02: adopt **modernc.org/sqlite v1.60.1** for the next TM implementation
step. Its actual engine reports **SQLite 3.53.4**. This PR supplies executable,
test-only storage experiments under `internal/sqliteprobe`, not a TM service,
accepted-translation constructor, production database opener, or CLI command.
No user database is opened or created by `yakuori`; every fixture is synthetic
and owned by `testing.T.TempDir` (private directory, database mode 0600).

Canonical design: [Draft v0.2 §8/11/15](https://chatgpt.com/space/page_3565e1d53fa08191a7d8cb56e84af5a5).
This records concrete implementation evidence without replacing that design.
#15 still owns artifact scope, canonical serialization/hash fixtures and profile
fields; #16 owns validated TM integration. The probe deliberately does not invent
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

The executable schema constant is `tableSQL` in `probe_test.go`:

- SQLite `application_id = 1497451343` (`YAKO`) and `user_version = 1`.
- One `STRICT, WITHOUT ROWID` table `tm`.
- Unique primary key `(key_schema, source_identity, translation_profile)`.
- `key_schema` is explicitly 1; source/profile are nonempty BLOBs, compared by
  exact bytes. `translated_text` stores the restored parent unit text.
- No case folding, whitespace trimming, Unicode normalization or hash-only
  equality. Embedded NUL bytes in *identity blobs* remain distinct. That test
  does not permit NUL in actual translations; validation remains #7/#16.
- Source/profile blob contents will be the versioned canonical records from
  #15, with its content hashes verified there. This schema proves unique exact
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

#16 must distinguish valid conflicts from invalid-row repair. Validate an observed
row outside the write transaction. A repair may conditionally replace *that exact
observed invalid value* after final artifact validation; if the row changed,
abort and revalidate the new candidate outside the transaction. Never overwrite
an unexamined conflict, hold a writer during generation, or retry generation
implicitly. This PR intentionally implements neither repair nor validation.

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
- This is a **quiescent-fixture proof**, not race-safe production opening. #16/#21
  must coordinate lifecycle/exclusive access, recheck identity/schema on the
  actual write connection, and handle recoverable interrupted journals before
  allowing normal use. Do not copy this preflight as a TOCTOU-safe Open API.
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
Docker is unavailable on this local executor; the PR's exact-head CI provides the
clean-image result. The tests run in the existing required Linux CI, and the
arm64 probe compilation was added because the ordinary CLI does not yet link SQLite.

Not executed/claimed: arm64 runtime, real game files/translation/acceptance,
maximum-workload benchmarks, WAL/concurrent file replacement, process-kill recovery,
power-loss tests, TM integration, security audit, release or deployment. Later
failures must not erase user assets, publish output or turn into a cache miss.
