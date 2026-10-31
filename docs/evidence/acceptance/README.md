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
