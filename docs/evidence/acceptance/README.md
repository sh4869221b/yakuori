# W3 acceptance fixture and criteria candidates

These are candidates for Issue #74, not an accepted fixture set or a v1
acceptance result. The checker reads local originals, validates metadata and
reports readiness. It does not translate or evaluate quality. Issue #85 owns
the user's confirmation of the representative set and criteria; #86 and #87
own the actual GPU run and human review respectively.

## Fixture set

`fixtures.json` retains the fixed source revisions, original SHA-256 values,
bytes, decoded unit counts and UTF-8 text bytes. The two MOD binaries and
license notices are already under `tools/w3spike/testdata/`. The original
sources, release pins, notices, independent CSVs and acquisition details are in
[the round-trip record](../../research/w3strings-roundtrip.md#fixture-provenance-and-rights-boundary).
Retain the applicable notices when redistributing either MOD fixture. The
project-authored fixtures contain no game dialogue or MOD prose.

The proposed real-MOD set in `criteria.json` is all 19 BetterKeybinds entries
and all 30 MonsterOfTheWeek entries: 49 units and 482 decoded UTF-8 source
bytes across two original artifacts totalling 2,074 binary bytes. These are
small UI-oriented samples, not evidence of support for a
large MOD or dialogue. `short_names` has one recorded MOD example. Dialogue,
long text, terminology, tags/placeholders and empty text are not established
as representative MOD coverage. The authored fixtures contain the latter
three structural cases. Selecting synthetic examples does not establish a
real-MOD example in those categories; the missing real-MOD categories are
reported for the representative-set decision in #85. A selected mixture may
cover categories across real and synthetic fixtures, subject to that decision.

Both MOD fixtures use `config.DefaultLimits` and fit the current input budgets:
1,495 artifact bytes, 76 units, 980 UTF-8 bytes per unit, 1,036 total text
bytes and 76 segments. The authored en/jp binaries each exceed the product
artifact, unit text and total text budgets. Their explicit research-only input
limits are 65,536 artifact bytes, 4,095 units, 16,384 bytes per unit, 32,768
text bytes and 4,095 segments. These values enable fixture inspection; they do
not expand product support or establish translation, context or output limits.
The authored `en` fixture uses an English binary header but contains authored
Japanese test text, so it is a format fixture, not representative English MOD
translation input. The `ja` language value corresponds to the binary's `jp`
header.

Each category claim identifies existing unit IDs and includes a human-written
reason. The checker verifies references and coverage, not the semantic truth of
the category or the suitability of the samples. Do not infer a human quality
review from a valid category claim.

## Candidate criteria

The proposed mechanical and human criteria and their rationales/impacts are in
`criteria.json`. They require all selected units and the final artifact to pass
common validation, and propose human review for material mistranslation,
omission, readability and required terminology. Whether the proposed set and
these qualitative rules are adequate needs the user's confirmation.

The selected unit, decoded text byte and original artifact byte criteria are
positive integers equal to the exact totals of all selected fixtures. They
describe this candidate set, not measured maximum support; product artifact
limits apply to each file independently. If the set, criteria or their versions
change, assess what must be remeasured or reviewed before confirming the new
versions.

### Candidate performance thresholds

The candidate limits are **60 seconds wall time**, **5 GiB (5,368,709,120
bytes) process RSS high-water** and **3 GiB (3,221,225,472 bytes) sampled
process VRAM**, each per fixture. Apply them only to CUDA runs of the two
selected MOD originals. For each original, use a fresh process and fresh TM;
generate once through the production W3/Core/SQLite/publish pipeline and read
the published output with an independent reader. Both fixtures must meet each
limit individually; an average or combined result cannot hide a failure. A TM
reuse check is separate and does not count as GPU generation performance. The
CPU regression in #76 has its own criteria; do not apply these GPU time or VRAM
limits to it.

The prior wrapper evidence provides a reference for choosing the limits, not a
measurement of this end-to-end workload:

| Metric and reference | Recorded value | Candidate and manual margin | Impact |
| --- | --- | --- | --- |
| [`02-x4-pair1-cuda.supervisor.json`](../cuda-inference/product-wrapper/matrix/02-x4-pair1-cuda.supervisor.json), `process_wall_seconds` | 17.3824957649922 s for 76 labels, including load and two cold/warm generations | 60 s, about 3.45× | Unknown production pipeline, TM, publish, reader and v0.23 overhead may cause the candidate to fail. |
| [`01-fixture-pair3-cuda.json`](../cuda-inference/product-wrapper/matrix/01-fixture-pair3-cuda.json), `peak_rss_kib` | 3,761,148 KiB, about 3.587 GiB | 5 GiB, about 1.39× | Translation-process RSS only; it is not host capacity or RAM plus VRAM. |
| [`02-long-pair3-cuda.supervisor.json`](../cuda-inference/product-wrapper/matrix/02-long-pair3-cuda.supervisor.json), `process_vram_sampled_max_mib` | 1,838 MiB, about 1.795 GiB | 3 GiB, about 1.67× | Target-process samples only; this is neither the 12 GiB GPU's device-wide ceiling nor a minimum VRAM requirement. |

These margins are manually chosen candidate values, not statistical guarantees,
predictions or current measurements. The wall-time reference does not include
the product TM, publish or independent reader. Historical conditions were
Linux/amd64, official Go 1.27.1, GOMAXPROCS 16, RTX 4070 with driver 615.78.08,
Index-Translate-2B Q4_K_M GGUF, int4 compute, context 4096, output limit 2048,
KV f32 and goinfer/cuda v0.22. The [CUDA evidence and environment](../../cuda-inference-spike.md#current-52-product-wrapper-result-2026-10-10)
record the conditions and limitations. The present go.mod pins v0.23; its
supported CUDA KV default is f16, and the current GPU/model/production pipeline
performance has not been measured. The historical model source lacked an
immutable repository revision. Record the adopted model's actual identity,
driver and other effective conditions in subsequent runs and check that the
comparison is appropriate. These candidates are for similar reference
conditions only; they do not guarantee performance for another backend, model,
hardware or workload.

Measure wall time from runner start with the local originals and registered GGUF
already in place. Include model snapshot/load, import, planning, generation,
validation, TM commit, publish, independent reading and normal child reaping;
exclude downloads, builds and fixture placement. A fresh model process is a
cold process; without flushing the OS page cache it is not a cold disk run. The
60-second overall performance criterion is separate from the product's 30 s
request and 300 s generation cooperative deadlines; it does not implement a
new timeout.

Measure Linux translation-child RSS high-water using `wait4`/`getrusage`
`ru_maxrss`, converted from KiB to bytes. Container cumulative `memory.peak`,
host-wide usage and runner or independent-reader RSS are not substitutes.
Measure the target translation PID's sampled VRAM maximum with a nominal 100 ms
sampling interval plus query latency; this is not a continuous peak or the
device-wide total. A missing measurement is not zero and cannot pass.

The #75 runner and #98 supervisor prepare the subsequent #86 measurements.
An exceeded limit is a candidate failure requiring review; do not silently
retry or expand limits. #85 must present these candidate values, the evidence
and impacts to the user for actual confirmation. Confirmation and representative
category coverage remain pending. No GPU run or human quality acceptance is
claimed by this document.

## Check local files

From the repository root, with Go 1.27 and `CGO_ENABLED=0`:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go run ./tools/acceptance/cmd/check \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json --root .
```

Relative `local_path` values are resolved from `--root`; an absolute path can
refer to a private local fixture. Its manifest entry still needs source and
use-condition information appropriate to that fixture, an explicit original
SHA-256 and the other required metadata. A private file is read locally and
must not be copied into the repository or CI artifacts without permission. The
checker does not download files or print their contents.

The command verifies every listed local original, including unselected fixtures.
It checks selected category references and reports both missing categories in
all selected fixtures and those missing from selected real MODs. Its JSON also
reports each fixture's chosen input limits, whether its observed input fits
product budgets and the IDs of selected fixtures that exceed those budgets. Normal candidate validation exits zero for structurally valid
but incomplete candidates and reports `acceptance_ready: false`, missing
categories, any undetermined criteria and `confirmation: pending`. Invalid
metadata, original bytes, SHA-256, format, language, counts, limits or versions
exit nonzero. Validation of a research fixture under research limits is not a
product execution result.

To enforce the conditions before starting a formal acceptance run, add
`--require-ready` to the same command. It emits the status and exits nonzero
unless every selected fixture fits the product input budgets, the selection
contains at least one MOD fixture, every required category is covered across
the selected fixtures, all criteria have values and an `approved` confirmation
records the current manifest and criteria versions, reviewer and RFC 3339
date/time. Missing real-MOD categories remain visible for the #85
representativeness decision; synthetic coverage does not establish real-MOD
coverage. The current candidates
intentionally fail this readiness check. The confirmation record represents a
user decision obtained in #85; the checker does not solicit or make it. Neither
checker mode executes GPU or CPU inference, establishes quality, or completes
v1 acceptance.

## Explicit W3 acceptance runner

`tools/acceptance/cmd/run` performs the product W3/SQLite/publication path
with CUDA and compares each published file with the separately built research
oracle. It starts only when the checker reports `acceptance_ready: true`.
The current candidate is pending #85, so an ordinary invocation records
`not_run` and does not start a translation. The runner does not approve
criteria, download a model or retry a failed generation.

After the representative set and criteria are confirmed, register and select
the intended local GGUF with the normal `yakuori models` commands. Build the
runner with Go 1.27.1 and CUDA support; the oracle build needs Rust/Cargo and
its locked dependencies already available locally. The runner uses
`cargo build --locked --offline` in a fresh private target directory; the GPL
oracle remains a separate process. Build time is outside the fixture run.

For example, from the checkout root, substitute the selected model ID, its
expected SHA-256, and an empty private evidence path on a supported filesystem:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go build -tags cuda -o /tmp/yakuori-acceptance ./tools/acceptance/cmd/run
/tmp/yakuori-acceptance \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json \
  --root "$PWD" --repository "$PWD" \
  --evidence /path/to/private/empty-evidence-directory \
  --model-id MODEL_ID --model-sha256 MODEL_SHA256 \
  --game witcher3 --artifact acceptance
```

The existing selected model registry is used for model loading, but each
fixture gets an isolated new TM. Each fresh run is followed by a separate
process that reuses that TM and publishes a separate file. The runner saves
`run.json` and the published outputs under the private evidence directory;
original fixtures remain at their manifest paths. The JSON associates each
unit and category with the original and published output without copying their
text into the record. Temporary oracle records, the working TM and the oracle
build directory are removed after use. Keep the referenced original fixtures
and private outputs for later review, subject to their use conditions.

The oracle checks the output language, IDs, text and all decoded key relations
against actual accepted values and source key relations. Its length-delimited
records preserve literal newlines and distinguish a zero key from no key.
An input the independent reader cannot decode, including its known zero-key
table limitation, fails rather than being replaced with a structural test
result; see the existing W3 structural tests. A successful machine check is not a human
translation-quality judgment. A missing worker result, missing build pin,
failed oracle or unconfirmed CUDA resident path cannot be reported as success.
`run.json` uses `not_run`, `fail` and `pass` and leaves unobserved counters and
stages null rather than reporting them as zero or false.

The `worker` subcommand reads one JSON `WorkerRequest` from stdin and writes
one `WorkerResult` to inherited descriptor 3. Its result contains accepted text
and must be treated as private. It runs just the translation, allowing the
#98 supervisor to be its direct parent for process RSS and VRAM measurements;
`acceptance.RunWithLauncher` lets it use the same preparation, published-output
check, acceptance decision and record as the ordinary runner. The oracle
record distinguishes launch failure, process exit or signal, exit code, and
the independent comparison result.
It is not by itself a formal acceptance result.

The model-free runner contract test uses the project-authored
`tools/acceptance/testdata/small.w3strings`, generated from `small.csv` with
the research oracle's `encode` command. Its empty entry and long generated
Japanese translation exercise the runner contract; the synthetic test run is
not a real MOD or GPU acceptance result.

## Explicit measurement supervisor

The `--measure` option runs the same acceptance workflow with its translation
worker directly supervised. Formal fixture runs still require confirmed
criteria. The following is a template for a future confirmed run, not an
approval or a measurement of this candidate set:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go build -tags cuda -o /tmp/yakuori-acceptance ./tools/acceptance/cmd/run
/tmp/yakuori-acceptance --measure \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json \
  --root "$PWD" --repository "$PWD" \
  --evidence /path/to/private/empty-evidence-directory \
  --model-id MODEL_ID --model-sha256 MODEL_SHA256 \
  --model-quant GGUF_QUANT --model-quant-source QUANT_SOURCE \
  --gpu-uuid GPU_UUID --conditions 'OS page cache not flushed; external load recorded by operator' \
  --repeat-index 1 --repeat-total 1 \
  --game witcher3 --artifact acceptance
```

The selected device is identified by `--gpu-uuid`. The operator supplies the
GGUF storage quantization and its source; it is distinct from the effective
compute quantization obtained from the actual worker engine. The command does
not infer a missing value. Record external activity and cache conditions in
`--conditions`; a device sample cannot establish that the rest of the host is
idle. One invocation executes one fresh/reuse pair per selected fixture.
Repetitions use separate invocations and empty evidence directories. Fresh and
reuse each start a process and load a model; reuse refers to TM reuse, not a
warm resident model. Neither mode flushes the OS page cache.

The private `run.json` adds a top-level `measurement` object and a measurement
object to each reached run. It binds existing run IDs, manifest and criteria
versions, selected model and engine details to host/device conditions, input
counts and bytes, operator conditions and timings. It records the requested and
effective backend/context, effective policy and goinfer/CUDA pins from the
worker. GPU baseline samples contain device usage and other compute PIDs before
worker startup. The per-worker samples contain timestamps, query durations,
device and worker-PID memory, and other compute PIDs. A null value means the
value was unavailable, not zero. Worker results containing translation text are
not copied into the measurement record.

Load time covers the worker opener, including its model verification, snapshot
and load. Generation time is the sum of returned backend `Generate` call wall
times, including failed calls and any drain; the record distinguishes unreached and partial
calls. There is no inferred prefill/decode timing. Validation counts distinguish
normal unit validation and TM-candidate revalidation; a rejected TM candidate
followed by an accepted new translation is not a failed final unit. Final
artifact validation is recorded separately. An absent worker result leaves
these observations unavailable.

Timing separates shared preparation (including manifest fixture checks),
oracle build, each fresh/reuse interval and translation-worker wall time.
Oracle build, downloads and fixture placement are excluded from performance
execution. Each mode interval includes model load, the product pipeline,
independent reading and ordinary worker reaping. Shared TM cleanup follows the
pair or an abort and is recorded separately, as is oracle-build cleanup; reuse
time is not added to the fresh interval. Runner wall time excluding build is
also recorded. These intervals do not decide how a future confirmed criterion
will account for shared preparation, nor do they apply the candidate 60-second
limit to a differently scoped measurement.

Linux worker RSS high-water comes from its direct child's `wait` resource usage
(`ru_maxrss` KiB converted to bytes). It is not the runner's RSS, an oracle
process value, or a sampled `/proc` maximum. GPU samples use `nvidia-smi` at a
nominal 100 ms interval plus query time. The adopted UUID and worker PID must
both match; ambiguous attribution remains unavailable. A successful query with
the PID absent is distinct from query failure/N/A and from interrupted
monitoring. A usable series requires completed monitoring and at least one
valid attributed process and device value; query errors or early stop leave
any observed maximum as a partial reference. PID absence during startup or
teardown alone does not fail a series. These values are sampled maxima, not
continuous peaks, and no missing value is replaced by zero.
If a PID occurs on more than one GPU, its process sample is ambiguous and is
not attributed to the selected GPU; device-wide values remain separate.
`resource_status` describes resource collection only; it is not the product or
overall measurement result. Each row's `measurement_status` and
`measurement_missing` record the final measurement decision separately.
`wall_seconds` is null if the worker never starts.
If a GPU query's process or process-group cleanup cannot be confirmed, its
cleanup status is `unknown` and further GPU queries are stopped.

`--wall-limit` bounds each mode through its independent reader;
`--preparation-limit` bounds preparation and the oracle build. Defaults are
5 minutes and 10 minutes respectively; they are supervision parameters, not
performance criteria. `--sample-interval`, `--query-timeout`, `--stop-grace`,
and `--reap-timeout` control measurement and stop behavior. On timeout or
cancellation, the supervisor stops the owned process group, escalates after a
finite grace, and records actual exit/signal, result availability, wait and
group recovery. A process remaining in an uninterruptible state or an
unconfirmed group cleanup is not reported as success. This external stop is
separate from the product's cooperative deadline and drain behavior; existing
`internal/inference/goinfer/deadline_test.go`, `cancel_test.go`, and
`close_test.go` cover the latter behavior.
Preparation uses ordinary file inputs and context-aware loops. A filesystem
operation blocked uninterruptibly in the kernel may exceed the preparation
deadline; this does not provide hard cancellation of such I/O.
`preparation_status` distinguishes complete, partial and not-run preparation;
`preparation_seconds` is an elapsed partial value when preparation stops,
with the reason recorded in `preparation_stop_reason`.

Product, oracle, measurement and cleanup results are distinct. A published
product result and passing independent reader remain recorded if VRAM is
unavailable, but measurement is incomplete and the command exits nonzero.
Incomplete samples, missing metadata, failed cleanup, timeout and unstarted
runs cannot establish performance acceptance. The current #85 criteria remain
pending; the supervisor does not change that state or perform #99's formal
maximum-fixture comparison.

### Controlled supervisor checks

The following model-free tests use the Go test binary as an actual direct child
and substitute a short controlled GPU query command; fake samples verify
classification and are not real VRAM measurements:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./tools/acceptance -run '^TestSupervisor' -count=1 -v
```

The controlled cases exercise successful and failed worker exits, a missing
worker result, wall limit, cancellation, a result descriptor retained by a
descendant, actual child `ru_maxrss`, query hangs and PID-absent/error/interrupted
sample classifications. The supervisor tests passed on Linux/amd64 with Go
1.27.1 and `CGO_ENABLED=0`; they do not establish GPU performance or a formal
fixture result. Product publication and independent-reader integration are
verified separately on a supported filesystem.

One execution of the command above using the project-authored test binary as
the worker on Linux/amd64 and Go 1.27.1 produced the following values. GPU
queries were controlled shell helpers; durations and RSS are observations from
these short processes, not real model or GPU performance:

| Control | Wall seconds | Child RSS bytes | Exit / signal | Stop reason | FD 3 result | Reap / group |
| --- | ---: | ---: | --- | --- | --- | --- |
| success | 0.252038 | 14,024,704 | 0 | none | available | reaped / cleared |
| failure | 0.001480 | 16,138,240 | 7 | none | missing or invalid | reaped / cleared |
| missing | 0.001395 | 16,138,240 | 0 | none | missing or invalid | reaped / cleared |
| deadline | 2.041012 | 16,138,240 | killed | wall limit | missing or invalid | reaped / cleared |
| cancellation | 0.045557 | 16,138,240 | killed | canceled | missing or invalid | reaped / cleared |

Each row was written to a private `run.json` by the normal record writer and
read back by the test. The tests also verify that a query process leaving a
same-group child is cleaned up, and stop further queries when that cleanup
cannot be confirmed. These process checks do not assert recovery of a child
that escapes its owned group or a process stuck in uninterruptible kernel I/O.
The deadline case uses an actual `context.WithTimeout` expiration after the
worker signals readiness. Another controlled case retains a valid earlier GPU
sample when a later query's stop grace exceeds the supervisor's reap wait. At
worker exit the supervisor kills an active query group before its caller can
exit, then waits a finite time for query recovery; unconfirmed cleanup remains
unknown. A separate helper exits immediately after supervision returns, and
the parent test verifies that the TERM-ignoring query no longer exists.

## Human review sheets

`tools/acceptance/cmd/review` consumes the runner's `run.json`. It does not
perform a human evaluation. Keep the original fixtures, published outputs and
review sheets private under the applicable use conditions; a sheet contains
the complete source and translated text as well as human observations. Do not
upload it to CI artifacts. Generation creates a new file with mode `0600` and
will not overwrite an existing sheet.

For each selected fixture run, choose the run ID from `run.json` and generate
a sheet. Relative source paths are resolved from `--root`; relative output paths
are resolved from the directory containing `run.json`. The tool verifies the
existing fixture and output SHA-256 pins before displaying text.

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go build -o /tmp/yakuori-review ./tools/acceptance/cmd/review
/tmp/yakuori-review generate \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json \
  --root "$PWD" --run /path/to/private/evidence/run.json \
  --run-id fixture-000-fresh --sheet /path/to/private/review.json
```

The sheet includes the criteria and confirmation state, run/model/profile and
fixture/output bindings including the producer publication run ID, unit
categories, nonblocking review findings, and the
source/translation pairs. For each row, the human reviewer fills `reviewer`,
`date` (RFC 3339), `verdict` (`pass`, `fail`, or `hold`), and `rationale`.
Leave rows unfilled when they were not reviewed; do not add observations to a
row with unavailable output. `hold` remains unconfirmed. A pass or fail is
counted as reviewed only when all four fields are present and the date is
valid. Preserve the bindings and text when editing the sheet.

Summarize one or more sheets from the same run record, repeating `--sheet` for
each selected fixture run:

```sh
/tmp/yakuori-review summarize \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json \
  --root "$PWD" --run /path/to/private/evidence/run.json \
  --sheet /path/to/private/review.json \
  --sheet /path/to/private/another-review.json > summary.json
```

The command reconstructs each expected sheet and rejects altered bindings,
criteria, text, missing or duplicate units, and duplicate runs. The JSON
summary contains counts and category coverage without fixture text, private
paths, reviewer names or rationales. It identifies selected fixtures without a
completed review and counts missing output, unreviewed and held units.
`selected_scope` reports all selected source units and category memberships;
`unsubmitted_scope` counts the portion whose fixtures have no submitted sheet.
The main counts describe submitted runs, so multiple runs of the same fixture
are counted separately there, while the selected fixture scope is counted once. A summary can report human
`pass` only when all included rows and at least one run of every selected
fixture have been reviewed, and the existing fixture/criteria check is ready.
This is a human-review result for the supplied runs, not machine or formal
acceptance. Unconfirmed criteria and incomplete reviews remain `incomplete`;
CPU and GPU output need not be identical. Actual GPU execution and human
review remain separate work.

The fixed quality cases reuse these tests, with no automatic human judgment:

- Empty source/candidate and nonempty/empty mismatch: `internal/validate`,
  `TestValidateCurrentUnit` and `TestValidateRejectsInvalidCandidate`.
- Proper-name and identical-source review findings: `internal/validate`,
  `TestReviewFindings` (accepted, nonblocking).
- Added explanatory wrappers and their boundaries: `internal/validate`,
  `TestContentRejectsAdditions`, `TestContentSourceExemptions`, and
  `TestContentFiniteBoundaries`.
- Duplicated protected token and crossed tags: `internal/protect`,
  `TestProtectionRejectsInvalid` and `TestProtectionRejectsInvalidStructure`.
- Conflicting glossary matches: `internal/glossary`, `TestMatchConflicts`.

The model-free review tests use the project-authored
`tools/acceptance/testdata/small.w3strings` and synthetic run metadata,
criteria and human decisions. They exercise bindings and aggregation, not a
GPU execution or real human quality assessment:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./tools/acceptance/... ./internal/validate ./internal/protect ./internal/glossary
CGO_ENABLED=0 GOTOOLCHAIN=local go build ./tools/acceptance/cmd/review
CGO_ENABLED=0 GOTOOLCHAIN=local go vet ./tools/acceptance/... ./internal/validate ./internal/protect ./internal/glossary
```
