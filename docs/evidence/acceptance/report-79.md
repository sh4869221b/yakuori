# W3 v1 acceptance evidence for issue #79

## Scope

**The R1–R10 acceptance evidence passes for this product revision.**
The evaluated product revision is
`c0eeecea36f3c8d4faa9fce47c0bbbb494c3fa78`. The adopted GPU is **AMD Radeon
RX 9070 XT**. All six AMD fresh/reuse runs, published-output comparison,
performance measurements and strict desktop offline observations passed.
[AMD acceptance requirements](https://github.com/sh4869221b/yakuori/issues/118)

The [evidence index](evidence-index.json) records each required check and its
execution result, reproduction command, revision, applicable pins and source.
The target was checked out directly by `workflow_dispatch`. The verifier
checks which requirements each source covers and compares recorded pins
locally. [Target CI run 45014450662](https://github.com/sh4869221b/yakuori/actions/runs/45014450662)

The original `f4494a7` assessment, index and verifier are preserved at
[`fb17f0c`](https://github.com/sh4869221b/yakuori/tree/fb17f0ca787106c92d88a9c224e454b70f41ef2b/docs/evidence/acceptance).
The earlier clean RX 9070 XT measurement at `9feb720` met the performance
limits. The earlier strict desktop run observed 14 connection attempts with
a CLI built from `f4494a7` with `vcs.modified=true`. Four strict cases
passed in a manually prepared display-free environment with a CLI built
from `9feb720` with `vcs.modified=true`.
[AMD measurement report](amd-webgpu-run-2026-11-22.md)

## R1–R10

| Requirement | Current evidence | Result |
| --- | --- | --- |
| R1 Types | External `Accepted` construction and session, unit, source and profile binding tests. | Pass in scope |
| R2 Translation memory | Source and effective profile identity, hit/miss and invalid-candidate Validation. On Btrfs, `TestW3StringsSQLite` reached committed row and publication checks. | Pass in scope |
| R3 Publication | Non-root CI filesystem, concurrency and no-clobber cases cover output refusal, explicit replacement, permissions, aliases, separate mounts and collisions. [#72 PR](https://github.com/sh4869221b/yakuori/pull/81) | Pass in scope |
| R4 Failure and state | Required CI fault tests reached their named boundaries, including final validation, TM transaction and publish failures, cancellation and process stop. The test events cover retained committed rows, destination conflicts and irreversible stdout bytes. [#21](https://github.com/sh4869221b/yakuori/issues/21) · [#94 PR](https://github.com/sh4869221b/yakuori/pull/104) | Pass in scope |
| R5 Integrity | Structural tests and independent round-trip CI passed. All six published AMD outputs matched the independent reader, including Japanese-script, empty and long-text cases. | Pass in scope |
| R6 Segmentation | Budget, protected spans, ordered joining and separators, plus failed-segment integration and parent-only TM entries. | Pass in scope |
| R7 Feasibility | The CGO-free clean gate, pinned CPU smoke, explicit W3 CPU regression and AMD resident generation through SQLite commit, publication and independent readback passed. | Pass in scope |
| R8 Fixed cases | Fixed structural, protection and Validation cases passed. The mixed fixture selection and AMD per-fresh limits are approved. | Pass in scope |
| R9 Bounds | Parser seeds and active fuzzing, product bounds, deadlines, cancel/drain/reuse and failure without an internal retry passed. All three fresh AMD measurements met the approved limits. | Pass in scope |
| R10 Offline | Model-free and controlled pipeline observers, acquisition safeguards and safe diagnostics passed. Strict real-model CPU and normal-desktop AMD observations passed. [#95 PR](https://github.com/sh4869221b/yakuori/pull/105) | Pass in scope |

The non-root job checks the required RUN/PASS subtest frames with
`ci/check-test-events.py`. The job log includes an intentional RED case on an
unsupported filesystem where later stages were not reached. The
[offline-pipeline artifact](https://github.com/sh4869221b/yakuori/actions/runs/45014450662/artifacts/14751550211)
contains observer results and test events. Its GitHub-provided SHA-256 is in
the index. The pinned CPU regression used Qwen2.5-Coder-0.5B-Instruct Q4_K_M,
explicit CPU/int4, effective context 4096 and the project-authored
`small.w3strings`. It made two completed generation calls, passed Validation
for all three units and observed final validation, committed SQLite rows and
published readback. Runtime ran with Docker `--network none`. The separate
strict observer runs below recorded connection attempts.
[CPU job and steps](https://github.com/sh4869221b/yakuori/actions/runs/45014450662/job/137183645780#step:7:1)
· [#76 PR](https://github.com/sh4869221b/yakuori/pull/108)

In attempt 1, the CGO-free clean lane stopped on
`test_group_escaping_descendant_is_stopped_on_timeout`. The observer's
stopped-descendant list omitted the expected child. The independent reader,
non-root filesystem and CPU lanes passed. Attempt 2 checked out the same
`c0eeece` commit, completed the clean lane and passed the CI aggregate gate.
[Clean lane, attempt 1](https://github.com/sh4869221b/yakuori/actions/runs/45014450662/job/137183646089#step:4:1) ·
[Clean lane, attempt 2](https://github.com/sh4869221b/yakuori/actions/runs/45014450662/job/137184689674#step:4:1)

## Adopted fixture set and performance

The approved set uses `fixture-candidates-v2`. The AMD criteria and local
readiness result cover 56 units, 927 decoded source UTF-8 bytes and 3,134 binary
bytes in three files. MonsterOfTheWeek has the most units (30) and binary bytes
(1,122). The authored fixture has the most decoded source bytes (445) and the
longest unit (238 source bytes). These are the maxima of the selected set.
The real MODs contribute 49 short, UI-oriented units. The authored examples
provide the dialogue, long text, terminology, tags/placeholders and empty-text
cases. [Fixture and criteria decision](README.md#recommended-fixture-set)

All six runs used `webgpu:vulkan-resident (int4)`, f32 KV and a 4,096-token
effective context. The worker reported WebGPU module
`github.com/sh4869221b/goinfer/backend/webgpu@994cbf5ee38dd8dcc612aad6e6803ac79f772f3b`.
All 55 fresh generations advanced the worker's GPU activity counter on the
selected device. The empty fixture entry stayed empty. Reuse revalidated all
56 hits with zero generation calls. Final validation, SQLite commit and row
verification, publication, independent reader comparison and cleanup passed
in all six runs. [AMD run conditions and procedure](amd-webgpu-run-2026-11-22.md)

All three fresh runs met the approved **60 seconds, 24 GiB worker RSS and
12 GiB sampled worker VRAM** ceilings in `criteria-amd-v1`.

| Fixture | Fresh wall time (s) | Worker RSS (bytes) | Sampled worker VRAM (bytes) |
| --- | ---: | ---: | ---: |
| BetterKeybinds | 14.25 | 3,949,441,024 | 2,844,053,504 |
| MonsterOfTheWeek | 19.80 | 3,865,088,000 | 2,844,053,504 |
| Authored | 7.91 | 3,917,697,024 | 2,844,053,504 |

The existing review generator read all 56 published rows in each mode after
checking the recorded pins. In both fresh and reuse output, 16 BetterKeybinds
rows, 24 MonsterOfTheWeek rows and 6 authored rows contained Japanese-script
characters. The authored empty entry stayed empty, and the 238-byte authored
long-text input had a 343-byte output containing Japanese-script characters.
These are output shape observations. The separate w3strings 0.2.0 oracle
matched each published artifact to its accepted units. The historical NVIDIA
measurements retain their original revision, pins and v3 criteria.
[Historical CUDA run](gpu-db-run-2026-11-03.md)

## AMD desktop offline

The strict observer recorded zero connection attempts in seven CLI cases
launched with the normal Wayland desktop variables inherited. Doctor, fresh
text translation, repeat text translation and W3 localization returned 0.
Missing Vulkan ICD, missing model and corrupt model returned 1. Separate SIGINT
and SIGTERM runs exited 1 and left no output, TM commit or model snapshot.
[AMD run report](amd-webgpu-run-2026-11-22.md)

Issue #87 remains after v1. The historical follow-up reports 56 unreviewed
fresh rows. Later private trials recorded human failures and unreviewed
results. Their counts and individual verdicts are unavailable in the public
record. [Issue #87](https://github.com/sh4869221b/yakuori/issues/87) ·
[Issue #79 scope decision](https://github.com/sh4869221b/yakuori/issues/79)

## Local checks

These component results were observed on Linux/amd64 with CGO disabled and
official Go 1.27.3. The product source under test was the evaluated
revision. Use the repository root for these commands.

- The named component tests for R1, R2, R5, R6, R8 and R9 all reported PASS.
  The full command is in the index under `component-tests`.
- `TestW3StringsSQLite` with `TMPDIR` on Btrfs reported five expected
  generated key relations and ten SQLite rows in the keys case. The candidate
  case reused two exact hits, repaired one invalid row and generated two
  misses. The observer saw zero committed rows before final validation and
  five after successful publication in the ordering case. The tampered stage
  and failed commit cases never published.
- `FuzzParse` passed the seed cases and a five-second active run with 61,688
  executions. `TestPull` and `TestPullLeftovers` passed with their loopback
  HTTPS test fixture enabled. `TestPipelineDiagnostics` passed all six cases.
  `TestPullCLIFailure` and the `TestOfflineCLIMatrix/model-free` observer
  also passed on the evaluated product checkout.
- The fixture checker returned `acceptance_ready: true` and
  `confirmation: confirmed` for `fixture-candidates-v2` and `criteria-amd-v1`.

```sh
# Set TMPDIR to a writable directory on ext4 or Btrfs for the SQLite test.
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 -v ./internal/localize -run '^TestW3StringsSQLite$'
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./internal/adapter/w3strings -run '^$' -fuzz '^FuzzParse$' -fuzztime=5s -parallel=2
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./internal/models -run '^(TestPull|TestPullLeftovers)$'
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./internal/cli -run '^TestPipelineDiagnostics$'
CGO_ENABLED=0 GOTOOLCHAIN=local go run ./tools/acceptance/cmd/check \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria-amd.json --root . --require-ready
```

### Real-model CPU offline observation

At `c0eeece`, the product CLI and both `offlinesmoke` test binaries were
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
The first CLI capture was invalid because two strace records could not be
classified. The table counts the subsequent complete CLI run.
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

GOMAXPROCS=2 YAKUORI_OFFLINE_MODEL="$MODEL" YAKUORI_OFFLINE_BACKEND=cpu \
  taskset -c 2,3 python3 ci/offline.py --evidence "$E/cpu-core/trace-result" --expect-exit 0 \
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

The index returns `pass` with an empty blockers list for `c0eeece`. The
release acceptance decision is tracked in the parent issue.
[Acceptance parent #22](https://github.com/sh4869221b/yakuori/issues/22)
