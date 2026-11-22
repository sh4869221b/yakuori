# W3 v1 acceptance evidence for issue #79

## Scope

**v1 acceptance is incomplete.** The evaluated product revision is
`f4494a7eba432d69e5775a756a94dd7ec777f433`. The adopted GPU is **AMD Radeon
RX 9070 XT**. Current AMD translation, published-output comparison, performance
and offline observation remain outstanding. The target product supports the
recorded CPU and NVIDIA/CUDA paths. An AMD inference path and the corresponding
acceptance runner and measurement path are still needed. See the
[GPU assessment](gpu-assessment-79.md) and [issue #79](https://github.com/sh4869221b/yakuori/issues/79).

The [evidence index](evidence-index.json) records each required check and its
execution result, reproduction command, revision, applicable pins and source.
A local execution points to the observations below. GitHub CI evidence is
from [push run 45010347210, attempt 1](https://github.com/sh4869221b/yakuori/actions/runs/45010347210/attempts/1)
at the evaluated revision. The verifier checks which requirements each
source covers and compares recorded pins locally. The evidence author
inspects CI logs and private artifacts. If a referenced log or artifact
expires, record the affected evidence as unavailable until an applicable
result can be inspected.

## R1–R10

| Requirement | Current evidence | Result |
| --- | --- | --- |
| R1 Types | External `Accepted` construction and session, unit, source and profile binding tests. | Pass in scope |
| R2 Translation memory | Source and effective profile identity, hit/miss and invalid-candidate Validation. On Btrfs, `TestW3StringsSQLite` reached committed row and publication checks. | Pass in scope |
| R3 Publication | Non-root CI filesystem, concurrency and no-clobber cases cover output refusal, explicit replacement, permissions, aliases, separate mounts and collisions. [#72 PR](https://github.com/sh4869221b/yakuori/pull/81) | Pass in scope |
| R4 Failure and state | Required CI fault tests reached their named boundaries, including final validation, TM transaction and publish failures, cancellation and process stop. The test events cover retained committed rows, destination conflicts and irreversible stdout bytes. [#21](https://github.com/sh4869221b/yakuori/issues/21) · [#94 PR](https://github.com/sh4869221b/yakuori/pull/104) | Pass in scope |
| R5 Integrity | Structural tests reject missing, swapped, extra and foreign values and metadata changes. Independent round-trip CI passed. Published AMD Japanese, empty and long-text comparisons await an AMD run. | Incomplete |
| R6 Segmentation | Budget, protected spans, ordered joining and separators, plus failed-segment integration and parent-only TM entries. | Pass in scope |
| R7 Feasibility | Clean CGO-free CI, pinned CPU wrapper smoke and explicit W3 CPU regression passed. AMD generation through stage validation, SQLite commit, publication and independent readback is pending. | Incomplete |
| R8 Fixed cases | Fixed structural, protection and Validation cases passed. The mixed fixture selection and AMD per-fresh limits are approved. | Pass in scope |
| R9 Bounds | Parser seeds and active fuzzing, malformed lengths, offsets and counts, product bounds, deadlines, cancel/drain/reuse and failure without an internal retry passed. AMD performance remains unmeasured. | Incomplete |
| R10 Offline | Model-free and controlled pipeline observers, acquisition safeguards and safe diagnostics passed. Both real-model CPU matrices passed under the strict observer at the evaluated revision. The AMD offline matrix remains open. [#95 PR](https://github.com/sh4869221b/yakuori/pull/105) | Incomplete |

The non-root job checks the required RUN/PASS subtest frames with
`ci/check-test-events.py`. The job log includes an intentional RED case on an
unsupported filesystem where later stages were not reached. The
[offline-pipeline artifact](https://github.com/sh4869221b/yakuori/actions/runs/45010347210/artifacts/14749781644)
contains observer results and test events. Its GitHub-provided SHA-256 is in
the index. The pinned CPU regression used Qwen2.5-Coder-0.5B-Instruct Q4_K_M,
explicit CPU/int4, effective context 4096 and the project-authored
`small.w3strings`. It made two completed generation calls, passed Validation
for all three units and observed final validation, committed SQLite rows and
published readback. Runtime ran with Docker `--network none`. The separate
strict observer runs below recorded connection attempts.
[CPU job and steps](https://github.com/sh4869221b/yakuori/actions/runs/45010347210/job/137171069798#step:7:1)
· [#76 PR](https://github.com/sh4869221b/yakuori/pull/108)

## Adopted fixture set and performance

The approved set uses `fixture-candidates-v2`. The v4 criteria and local
readiness result cover 56 units, 927 decoded source UTF-8 bytes and 3,134 binary
bytes in three files. MonsterOfTheWeek has the most units (30) and binary bytes
(1,122). The authored fixture has the most decoded source bytes (445) and the
longest unit (238 source bytes). These are the maxima of the selected set.
The real MODs contribute 49 short, UI-oriented units. The authored examples
provide the dialogue, long text, terminology, tags/placeholders and empty-text
cases. [Fixture and criteria decision](README.md#recommended-fixture-set)

On 2026-11-22 the user approved per-fresh AMD ceilings of **60 seconds wall
time, 24 GiB worker RSS and 12 GiB sampled worker VRAM**. The corresponding
measurements at `f4494a7` were not run. The
published NVIDIA runs are bound to the RTX 4070, GoInfer/CUDA v0.23.0 and
criteria v3. The second execution also observed equality between committed
SQLite values and the independently read published artifacts. The GPU
assessment records their individual results and the scope of the numerical v4
comparison. Record the actual AMD measurement method and conditions for a
future run. [Historical run and public summary](gpu-db-run-2026-11-03.md) ·
[GPU assessment](gpu-assessment-79.md) ·
[AMD implementation and approved ceilings](https://github.com/sh4869221b/yakuori/issues/118)

Issue #87 remains after v1. The historical follow-up reports 56 unreviewed
fresh rows. Later private trials recorded human failures and unreviewed
results. Their counts and individual verdicts are unavailable in the public
record. [Issue #87](https://github.com/sh4869221b/yakuori/issues/87) ·
[Issue #79 scope decision](https://github.com/sh4869221b/yakuori/issues/79)

## Local checks

These component results were observed on Linux/amd64 with CGO disabled and
Go `1.27.4-X:nodwarf5`. The product source under test was the evaluated
revision. Use the repository root for these commands.

- The named component tests for R1, R2, R5, R6, R8 and R9 all reported PASS.
  The full command is in the index under `component-tests`.
- `TestW3StringsSQLite` with `TMPDIR` on Btrfs reported five expected
  generated key relations and ten SQLite rows in the keys case. The candidate
  case reused two exact hits, repaired one invalid row and generated two
  misses. The observer saw zero committed rows before final validation and
  five after successful publication in the ordering case. The tampered stage
  and failed commit cases never published.
- `FuzzParse` passed four seed cases and a five-second active run with 12,248
  executions. `TestPull` and `TestPullLeftovers` passed with their loopback
  HTTPS test fixture enabled. `TestPipelineDiagnostics` passed all six cases.
- The fixture checker returned `acceptance_ready: true` and
  `confirmation: confirmed` for the v4 manifest and criteria pair.

```sh
# Set TMPDIR to a writable directory on ext4 or Btrfs for the SQLite test.
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 -v ./internal/localize -run '^TestW3StringsSQLite$'
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./internal/adapter/w3strings -run '^$' -fuzz '^FuzzParse$' -fuzztime=5s -parallel=2
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./internal/models -run '^(TestPull|TestPullLeftovers)$'
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./internal/cli -run '^TestPipelineDiagnostics$'
CGO_ENABLED=0 GOTOOLCHAIN=local go run ./tools/acceptance/cmd/check \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json --root . --require-ready
```

### Real-model CPU offline observation

At `f4494a7`, the product CLI and both `offlinesmoke` test binaries were
built with official Go 1.27.3 and CGO disabled. The runs used the pinned
Qwen2.5-Coder-0.5B-Instruct Q4_K_M GGUF and Btrfs temporary files. The
strict observer used strace 7.3 with the NVIDIA MPS exception disabled.

| Test | Observations | Forbidden connection attempts | Remaining child processes |
| --- | ---: | ---: | ---: |
| `TestOfflineCoreMatrix` | 1 clean, target exit 0 | 0 | 0 |
| `TestOfflineCLIMatrix/real-model` | 18 clean, 10 expected exits 0 and 8 expected exits 1 | 0 | 0 |

Both test matrices passed. Core exercised the real CPU engine and Core
translation paths. The CLI observations covered real-model registration and
selection, generation and publication, and missing or corrupt model refusals.
Two preceding CLI captures were invalid because strace ended with
`???( <unfinished ...>`. The table counts the subsequent complete CLI run.
Raw traces and outputs are private.

From a clean checkout at the evaluated revision, use official Go 1.27.3 and
strace 7.3 on `PATH`. Set `MODEL` to the existing checksum-pinned local
GGUF, `E` to an absolute private evidence directory, and `TMPDIR` to a
writable Btrfs directory.
[Pinned Qwen fixture](../../foundation.md#offline-observer-and-cuda-reproduction)

```sh
export CGO_ENABLED=0 GOTOOLCHAIN=local
mkdir -p "$E/cpu-core" "$E/cpu-cli-2cores"
go build -o "$E/yakuori" ./cmd/yakuori
go test -c -tags offlinesmoke -o "$E/cpu-core.test" ./internal/localize
go test -c -tags offlinesmoke -o "$E/cpu-cli.test" ./internal/cli

GOMAXPROCS=5 YAKUORI_OFFLINE_MODEL="$MODEL" YAKUORI_OFFLINE_BACKEND=cpu \
  python3 ci/offline.py --evidence "$E/cpu-core/trace-result" --expect-exit 0 \
  -- "$E/cpu-core.test" -test.run '^TestOfflineCoreMatrix$' -test.v -test.timeout 15m

(
  cd internal/cli
  GOMAXPROCS=2 YAKUORI_OFFLINE_MODEL="$MODEL" YAKUORI_OFFLINE_BACKEND=cpu \
    YAKUORI_PULL_BINARY="$E/yakuori" YAKUORI_PULL_EVIDENCE="$E/cpu-cli-2cores" \
    taskset -c 0,1 "$E/cpu-cli.test" -test.run '^TestOfflineCLIMatrix$/^real-model$' \
    -test.v -test.timeout 15m
)
```

## Verify the index

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go build ./tools/acceptance/...
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./tools/acceptance/...
CGO_ENABLED=0 GOTOOLCHAIN=local go vet ./tools/acceptance/...
CGO_ENABLED=0 GOTOOLCHAIN=local go run ./tools/acceptance/cmd/evidence-index
```

The checked-in index returns `incomplete` and exit status 1. The blockers
are AMD generation and publication, published-output independent reading,
performance, and offline observation. Reassess the applicable evidence when
the product target advances. [Acceptance parent #22](https://github.com/sh4869221b/yakuori/issues/22)
