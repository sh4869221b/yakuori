# W3 acceptance fixtures and criteria

The current selection is `fixtures.json` (`fixture-candidates-v2`) and
`criteria.json` (`criteria-candidates-v4`). On 2026-11-05 the user directed
memory ceilings to match the main PC: 32 GB RAM and an RTX 4070 with 12 GB
VRAM. The resulting comparison ceilings are **24 GiB worker RSS and 10 GiB
sampled worker VRAM**, with one active model/worker. The 60-second ceiling,
fixture selection and human quality rules are unchanged.

The original Issue #85 decision and v3 criteria are preserved in
[`criteria-v3.json`](criteria-v3.json). Neither a criteria decision nor this
memory revision establishes a v1 acceptance or translation-quality result.
The checker reads local originals and reports readiness; it does not translate
or judge quality. #86, #99 and #87 own the GPU run, performance measurement
and human review respectively.

The [2026-11-03 shared GPU run](gpu-run-2026-11-03.md) records the first
approved fixture measurement and its remaining acceptance limits.
The [separate follow-up GPU run](gpu-db-run-2026-11-03.md) additionally checks
the committed SQLite rows against the accepted and published translations.

## Recommended fixture set

Select all 19 BetterKeybinds entries, all 30 MonsterOfTheWeek entries and the
seven project-authored English entries: **56 units, 927 decoded UTF-8 source
bytes and 3,134 binary bytes** across three separate artifacts. The fixed
source revisions, original pins, licenses and per-fixture counts are in
`fixtures.json`:

| Fixture | Units | Source UTF-8 bytes | Max unit bytes | Binary bytes | Source and use |
| --- | ---: | ---: | ---: | ---: | --- |
| `better-keybinds-en` | 19 | 259 | 19 | 952 | MIT MOD, release 1-1.12; retain its bundled notice. |
| `monster-of-the-week-en` | 30 | 223 | 19 | 1,122 | MIT MOD, release 1.0.2; retain its bundled notice. |
| `representative-authored-en` | 7 | 445 | 238 | 1,060 | Project-authored, fixed to the commit in the manifest; MIT, retain the repository LICENSE notice. |

The two MOD sources, release pins, notices, independent CSVs and acquisition
information are in [the round-trip record](../../research/w3strings-roundtrip.md#fixture-provenance-and-rights-boundary).
The project-authored fixture has no game dialogue or MOD prose; its CSV and
oracle-encoded binary are in `tools/acceptance/testdata/`. Retain the relevant
MIT copyright and permission notice when redistributing each fixture. The
manifest pins the actual fixture commit; use the local files without a download.

The category memberships are the following distinct selected unit IDs; all
other rows are also reviewed. A unit may belong to more than one category.

| Category | IDs | Count | Coverage |
| --- | --- | ---: | --- |
| Dialogue | authored 8501, 8502 | 2 | A question and response at a gate. |
| Short proper names | MonsterOfTheWeek 2119001011 | 1 | A short monster name in the real MOD. |
| Long text | authored 8505 | 1 | A 238-byte three-sentence passage with a sequence, prohibition and condition. |
| Terminology | authored 8503, 8504 | 2 | Both contain the same two terms. |
| Tags/placeholders | authored 8506 | 1 | A paired tag and a player placeholder. |
| Empty text | authored 8507 | 1 | Empty source entry. |

The MODs are 49 short, UI-oriented units; only `short_names` has a recorded
real-MOD example. Dialogue, long text, terminology, tags/placeholders and
empty text are supplied by authored examples, not established as real-MOD
coverage. The passage is long relative to these UI strings, not evidence about
large MODs or the longest supported input. The checker validates IDs and
category references, not the semantic category or representativeness.

All selected artifacts fit the checker's product input bounds per file: 1,495
binary bytes, 76 units, 980 UTF-8 bytes per unit and 1,036 total text bytes.
The product also limits each file to 76 segments. The checker does not establish
the actual segment count or whether translated output and inference will fit.
The checker does not measure product execution; the dated GPU records above
report the observed product runs. The older authored `en`/`jp` format fixtures remain unselected:
each exceeds the product artifact, per-unit and total-text budgets, and the
`en` header contains Japanese research text. Their explicit research limits
are for inspection only and do not expand product support.

The approved mixed real/synthetic scope bounds the first v1 evaluation. If
real MOD dialogue or long-text coverage is later required, propose a suitable
independently pinned, lawfully usable MOD with known source and redistribution
conditions. A private local reference may be used under its terms; do not copy
unlicensed full MOD text into the repository, logs or CI artifacts. Re-evaluate
the selection, totals, categories and affected evidence before changing it.

## Approved human quality criteria

Review every row of the three fresh published outputs: 56 rows across three
sheets, including all six category counts above. Do not count the separate TM
reuse outputs again. For each row, record the four axes in its existing
`rationale` along with reviewer, RFC 3339 date and `pass`, `fail` or `hold`:

- **Mistranslation:** no material change of subject, action, negation, number
  or condition.
- **Omission:** no material omission of a proposition, instruction or
  restriction.
- **Readability:** the Japanese is understandable in its available context;
  for the passage, its sequence and conditions can be followed. Minor
  meaning-preserving wording and politeness differences are acceptable.
- **Terminology:** in both 8503 and 8504, `signal lantern` is `信号灯` and
  `night watch` is `夜警`. Grammatical particles and word order may vary;
  a different translation or omission of either term fails the row. No
  required terminology is specified for the other rows.

A material defect in any applicable axis is a row failure. If context is
insufficient to decide, use `hold` and explain what is missing. For 8507,
verify empty output remains empty and mark meaning and terminology not
applicable. For 8506, judge its meaning and explicitly verify that `<b>`, `</b>` and
`{player}` each occur once with the original nesting; any missing, altered or
extra occurrence fails the row. The W3 adapter currently declares no protection
spans for this text, so common validation does not by itself establish that the
source literals survived. The independent reader checks the published artifact
against accepted output, not the tag spelling against the source. Common
validation and the final published artifact/independent-reader check must pass
for every fixture, in addition to human review.

The quality result passes only when all 56 rows pass and all six
category memberships are reviewed. A failed row is an outstanding failure;
unavailable output, missing review or `hold` makes the review incomplete, not
pass. When failures and incomplete rows coexist, the existing summary reports
`incomplete`: preserve and report both the fail and incomplete counts. Review
sheets contain source, translated text and rationale and must be kept private
under applicable terms. No quality review is claimed here.

## Approved performance criteria

For one recorded repetition (`--repeat-index 1 --repeat-total 1`), run each
selected fixture on the same adopted CUDA device, registered model and profile
under recorded host and workload conditions, with a fresh process and fresh TM.
Each fresh result must pass the product pipeline, publication and independent
reader. A separate process checks TM reuse for each fixture; reuse results are
functional checks and reference measurements, not added to the fresh time.
The same valid `--measure` run record may support #86 and #99 if it meets both
issues' requirements. No formal GPU or performance run is claimed here.

Each of the three fresh results is compared individually against all three
approved ceilings, with equality allowed:

| Criterion | Record and boundary | Impact |
| --- | --- | --- |
| 60 s wall time | Fixture run `measurement_wall_seconds`: fresh mode from fixture setup through model snapshot/load, production pipeline, validation, TM commit, publication, independent reader and normal reaping. | A breach is a criterion failure; no averaging or retry to hide it. |
| 24 GiB (25,769,803,776 bytes) RAM | `measurement.rss_bytes`: Linux translation-child `wait4`/`getrusage` RSS high-water, converted from KiB. | This is process RSS, not host-wide RAM capacity or combined RAM and VRAM. Record host RAM and other workload; no host-wide ceiling is proposed. |
| 10 GiB (10,737,418,240 bytes) VRAM | `measurement.process_vram_sampled_max_bytes`: selected GPU UUID and worker PID, nominal 100 ms interval plus query latency. | A sampled process maximum, not a continuous peak, device-wide total or minimum device capacity. |

The assistant selected 24/10 GiB to implement the user's main-PC sizing
request, reserving approximately 7-8 GiB of host RAM and 2 GiB of VRAM for
the OS, desktop and other processes. These are engineering choices, not
numeric limits directly quoted from the user or measured guarantees for a
candidate. Use one active model/worker, check actual available RAM and free
VRAM before each run, and record concurrent workload. These criteria do not
enforce a cgroup, allocator or GPU allocation cap.

The existing supervisor collects measurements but does **not** compare them to
these criteria: the later evaluator must compare the recorded values. Require
product and oracle pass, `measurement_status: complete`, valid resource
samples and successful cleanup. Missing, partial, ambiguous or interrupted
measurements are incomplete, never zero or pass. Record shared
`measurement.preparation_seconds`, `measurement.oracle_build_seconds` and
`measurement.pair_cleanup` separately; the approved criteria allocate none of
those shared intervals to the per-fixture time ceiling. `measurement_wall_seconds`
is not worker-only `measurement.wall_seconds`. Downloads, builds and fixture
placement are outside the criterion. The fresh process does not imply a cold
disk cache. A single repetition cannot establish a distribution or guarantee.
The 60 s criterion is distinct from product cooperative deadlines and the
supervisor's stop limit.

Measuring all three selected fixtures also covers each observed size maximum:
MonsterOfTheWeek has the most units (30) and largest binary (1,122 bytes);
the authored fixture has the most source text (445 bytes) and largest single
unit (238 bytes). These are maxima **within the selected set**, not product
capacity. The separate #76 CPU regression uses its own criteria.

The original v3 limits of 60 s / 5 GiB / 3 GiB, preserved in
[`criteria-v3.json`](criteria-v3.json), were derived from a **different
goinfer/cuda v0.22 workload**, not v0.23 product E2E measurements. The table
below explains that historical decision; its 5/3 GiB margins are not the
current v4 memory ceilings:

| Historical reference | Observed value | Manual candidate margin |
| --- | --- | --- |
| [`02-x4-pair1-cuda.supervisor.json`](../cuda-inference/product-wrapper/matrix/02-x4-pair1-cuda.supervisor.json), `process_wall_seconds` | 17.3824957649922 s for 76 labels, load plus two cold/warm generations | 60 s, about 3.45× |
| [`01-fixture-pair3-cuda.json`](../cuda-inference/product-wrapper/matrix/01-fixture-pair3-cuda.json), `peak_rss_kib` | 3,761,148 KiB, about 3.587 GiB | 5 GiB, about 1.39× |
| [`02-long-pair3-cuda.supervisor.json`](../cuda-inference/product-wrapper/matrix/02-long-pair3-cuda.supervisor.json), `process_vram_sampled_max_mib` | 1,838 MiB, about 1.795 GiB | 3 GiB, about 1.67× |

These hand-chosen margins are not predictions or statistical guarantees. The
wall-time reference omitted the product TM, publication and independent
reader. Historical conditions were Linux/amd64, official Go 1.27.1,
GOMAXPROCS 16, RTX 4070 with driver 615.78.08, Index-Translate-2B Q4_K_M
GGUF, int4 compute, context 4096, output limit 2048, KV f32 and goinfer/cuda
v0.22. See the [CUDA evidence](../../cuda-inference-spike.md#current-52-product-wrapper-result-2026-10-10).
The current go.mod pins v0.23 and its supported CUDA KV default is f16. The
historical model source lacked an immutable repository revision. Record the
actual adopted model identity, driver, host RAM, workload and effective engine
conditions and check comparability. Pipeline/model/hardware differences may
cause any criterion to fail; do not silently raise a limit after measurement.
Changes to approved limits require a new decision and re-evaluation of affected
evidence.

## Confirmation and re-evaluation

On 2026-11-03 at 10:08:41 +09:00, user `sh4869221b` approved the pinned
mixed set and its real-MOD coverage limits, 56-row/six-category quality rules
and required terms, and the three numerical ceilings, per-fresh measurement
boundaries, single repetition and reference conditions. The decision is
recorded as `confirmed` and `approved` in the archived
[`criteria-v3.json`](criteria-v3.json), bound to `fixture-candidates-v2` and
`criteria-candidates-v3`. This approval is a
selection decision, not evidence that any later GPU run, measurement or human
review has passed.

On 2026-11-05 the user requested: "メモリ使用量上限はメインpcに合わせて下さい".
The assistant implemented that request as the 24 GiB worker RSS / 10 GiB
sampled worker VRAM ceilings described above. The v4 confirmation records
this delegated sizing decision under the assistant's reviewer identity; it
does not claim that the user directly specified these two numbers. The fixture
manifest, 60-second value and human quality rules are unchanged. The new
memory budget does not establish that any new model fits or passes.

For each evaluation, reference the same confirmed manifest and criteria
versions and the relevant fixture/model/run/output. The completed #86/#99
records remain bound to v3. New comparisons for #87 use v4.
Fixture, model, pipeline or measurement-condition changes require the affected
generation, measurement and quality evidence to be reassessed and missing
observations obtained. A threshold-only change can recompare complete raw
measurements if conditions and boundaries remain comparable; a rubric or
terminology change requires the affected reviews to be re-evaluated. Recompute
summaries. Existing tools bind records and sheets to versions: do not rewrite
old records to present them as the new version; produce new bound runs/sheets
when the tool requires them. A candidate breach remains visible for a user
decision rather than being silently replaced with a measured value.

To generate or summarize reviews for original v3 runs, explicitly pass
`--criteria docs/evidence/acceptance/criteria-v3.json` (or use the original
pinned checkout). Keep the original run records, sheets, values and decisions.
Use the current `criteria.json` for new v4 runs and new bound review sheets;
do not edit a v3 run or sheet's version to relabel it as v4.

## Check local files

From the repository root, with Go 1.27.3 and `CGO_ENABLED=0`:

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
product budgets and the IDs of selected fixtures that exceed those budgets. For
the approved selection, normal validation reports `acceptance_ready: true`, no
missing set-wide categories, the real-MOD gaps, and `confirmation: confirmed`.
Invalid
metadata, original bytes, SHA-256, format, language, counts, limits or versions
exit nonzero. Validation of a research fixture under research limits is not a
product execution result.

To enforce the conditions before starting a formal acceptance run, add
`--require-ready` to the same command. It emits the status and exits nonzero
unless every selected fixture fits the product input budgets, the selection
contains at least one MOD fixture, every required category is covered across
the selected fixtures, all criteria have values and confirmation has `status: confirmed` and
`decision: approved`, with the current manifest and criteria versions, reviewer
and RFC 3339 date/time. Missing real-MOD categories remain visible for the #85
representativeness decision; synthetic coverage does not establish real-MOD
coverage. The approved selection passes this readiness check when the local
originals match their recorded metadata. The confirmation record represents the
original #85 selection decision plus the user-directed v4 memory revision
described above; the checker does not solicit or make those decisions. Neither
checker mode executes GPU or CPU inference, establishes quality, or completes
v1 acceptance.

## Explicit W3 acceptance runner

`tools/acceptance/cmd/run` performs the product W3/SQLite/publication path
with CUDA and compares each published file with the separately built research
oracle. It starts only when the checker reports `acceptance_ready: true`.
A pending or otherwise unready selection records `not_run` and does not start a
translation. The runner does not approve
criteria, download a model or retry a failed generation.

Register and select
the intended local GGUF with the normal `yakuori models` commands. Build the
runner with Go 1.27.3 and CUDA support; the oracle build needs Rust/Cargo and
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
also recorded. The approved criteria compare the fresh mode interval with the
60-second ceiling and allocate none of the separately recorded shared
preparation to it.

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
runs cannot establish performance acceptance. The supervisor does not
perform #99's formal maximum-fixture comparison.

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

The criteria file must match the run and sheet: use `criteria-v3.json` for
existing v3 evidence and `criteria.json` for new v4 evidence. Keep their
results separate; the examples below use the current v4 criteria.

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
