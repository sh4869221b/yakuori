# Issue #79 GPU evidence assessment

## Current GPU status

The adopted GPU for #79 and #22 is AMD. The host check outside the sandbox
observed PCI ID `1002:7550`, revision `c0`, identified by libdrm as **AMD Radeon
RX 9070 XT**. The host uses the `amdgpu` kernel driver and exposes `/dev/kfd`
and `/dev/dri/renderD128`.

Current AMD real-GPU acceptance is **not run**. Yakuori at
`f4494a7eba432d69e5775a756a94dd7ec777f433` lacks an AMD inference backend. The
acceptance runner explicitly requests `cuda`, and its supervisor uses
`nvidia-smi` to collect GPU measurements. Device discovery is the extent of
the current hardware observation. AMD generation, end-to-end publication and
resource measurements remain open. See the [worker request](../../../tools/acceptance/runner_linux.go#L729)
and [measurement supervisor](../../../tools/acceptance/supervisor_linux.go#L112).

## AMD acceptance procedure

Before running, provide an AMD inference backend, enable the acceptance worker
to select it explicitly, and support measurement for the selected AMD device
and worker process. Confirm the AMD measurement conditions and
record the run revision, backend and model pins, fixture revisions and host
load. No AMD run command is available at the assessed revision.

On 2026-11-22, the user approved ceilings of 60 seconds wall time, 24 GiB
(25,769,803,776 bytes) worker RSS, and 12 GiB (12,884,901,888 bytes) sampled
worker VRAM for each fresh AMD fixture run. The CUDA v3/v4 ceilings and the
NVIDIA measurements below apply to the historical CUDA assessment.

On the AMD-capable revision, use the approved fixture set and verify each
fixture with a fresh process and fresh TM, followed by separate-process TM
reuse. Capture actual AMD generation and residency, common and final
validation, SQLite commit and row comparison, publication and independent
reading. Include Japanese-script, empty and long-output observations. Check
that reuse validates its TM hits and makes zero generation calls. Compare wall
time, worker RSS and sampled worker VRAM attributed to the selected device
against the AMD ceilings for each fresh fixture, including the largest selected
fixtures. Record measurement completeness, attribution and cleanup.

For a completed run, publish the revision, pins, run conditions, statuses,
counts and measurements. Keep the full `run.json`, translated artifacts,
review sheets, fixture texts, prompts and local paths in private evidence.

## Historical runs and applicability

On 2026-11-03, two clean revisions ran on an RTX 4070 12 GB with NVIDIA driver
615.78.08, GoInfer and CUDA v0.23.0, and the registered Index-Translate-2B
Q4_K_M GGUF. Both used `fixture-candidates-v2` and `criteria-candidates-v3`.
The model was `index-translate`, 1,312,164,352 bytes, with the existing SHA-256
pin `044b313d29342bd3b2c77cbb64023ca0d209bd9b3247763f9d162767ef2d746a`.
The source was IndexTeam/Index-Translate-2B-GGUF, with no upstream immutable
revision recorded. Both runs reported actual resident CUDA with int4 compute,
f32 KV and effective context 4,096. The dated reports record their host load,
cache conditions and policy. See [first run](gpu-run-2026-11-03.md) and
[SQLite follow-up](gpu-db-run-2026-11-03.md).

- `27d59b07ce2c774a5b170eba79d624d165f82b5f` reached final stage validation,
  SQLite commit, publication and independent reading in all three fresh and
  three separate-process TM reuse modes. The published artifacts covered
  Japanese-script output, empty text and the long authored entry. This run
  did not inspect committed rows against accepted translations before cleanup.
- `2e97bdb7b82d2c83c0360e537d6b4b6c8a21f6a2` repeated the six executions and
  matched the committed row for each accepted unit to its translation in the
  still-open SQLite store, 19/19, 30/30 and 7/7 in each mode. The independent
  reader matched the published files to the same accepted sets. Fresh generated
  counts were 19, 30 and 6, with the empty entry unchanged. Reuse recorded
  19, 30 and 7 validated TM hits and zero generation calls.

These runs establish historical NVIDIA coverage for R5, R7 and R9 at their
recorded revisions. The backend and its pin participate in the canonical TM
profile, and publication recovery and path checks changed after the dated
runs. The AMD run must record the actual backend, compute and KV precision,
effective context, generation policy and resident status of its worker. See
[profile identity](../../source-profile-identity.md).

## Fresh measurements against v3 and v4 ceilings

The approved fixture set and 60 s per-fresh wall time are common to v3 and v4.
V3 limits are 5,368,709,120 bytes worker RSS and 3,221,225,472 bytes sampled
worker VRAM. V4 raises these to 25,769,803,776 and 10,737,418,240 bytes
respectively. The table compares the complete historical fresh measurements
individually. The v4 column checks those same numbers against v4 ceilings.
Both execution records are bound to v3 on NVIDIA. See [v3 criteria](criteria-v3.json),
[v4 criteria](criteria.json), [first measurement](gpu-measurement-2026-11-03.json)
and [SQLite follow-up measurement](gpu-db-measurement-2026-11-03.json).

| Executed revision | Fixture | Wall (s) | RSS (bytes) | Sampled VRAM (bytes) | v3 | v4 numerical comparison |
| --- | --- | ---: | ---: | ---: | --- | --- |
| `27d59b0` | `better-keybinds-en` | 5.610937606 | 3,826,847,744 | 1,868,562,432 | Pass | Within ceilings |
| `27d59b0` | `monster-of-the-week-en` | 6.080824575 | 3,657,928,704 | 1,868,562,432 | Pass | Within ceilings |
| `27d59b0` | `representative-authored-en` | 5.155080798 | 3,803,111,424 | 1,879,048,192 | Pass | Within ceilings |
| `2e97bdb` | `better-keybinds-en` | 5.671780189 | 3,737,423,872 | 1,868,562,432 | Pass | Within ceilings |
| `2e97bdb` | `monster-of-the-week-en` | 6.056856791 | 3,875,074,048 | 1,868,562,432 | Pass | Within ceilings |
| `2e97bdb` | `representative-authored-en` | 5.364351981 | 3,941,343,232 | 1,879,048,192 | Pass | Within ceilings |

MonsterOfTheWeek has the most units and the largest binary in the selected set
(30 units, 1,122 bytes). The authored fixture has the most decoded UTF-8 bytes
and the longest single unit (445 and 238 bytes). BetterKeybinds adds 19 units.
The two real MODs supply 49 short units and the authored fixture supplies 7.
The measurements cover one repetition of this set. Human meaning and translation
quality are tracked after v1 in #87. See [fixture pins](fixtures.json) and
[approved fixture scope](README.md#recommended-fixture-set).

## Reproduce the historical NVIDIA CUDA run

Use a clean checkout of `2e97bdb7b82d2c83c0360e537d6b4b6c8a21f6a2`, where
`docs/evidence/acceptance/criteria.json` contains v3. Use Linux/amd64 with Go
1.27.3, an RTX 4070 with a working NVIDIA driver and `nvidia-smi`, and one active
translation worker/model. Check available RAM and VRAM and record concurrent
workload and cache conditions. Use all five local originals at their manifest
paths and a local Cargo cache for the runner's `cargo build --locked --offline`
of the separate reader. Use an empty private evidence directory outside the
checkout on a supported publication filesystem such as Btrfs. Register the
pinned local GGUF with `yakuori models import --id index-translate "$MODEL_GGUF"`.
Run `yakuori models use index-translate` and `yakuori models verify index-translate`
in the same isolated XDG registry used by the runner.

Set `PRIVATE_EVIDENCE` to that directory, `GPU_UUID` to the device UUID and
`RUN_CONDITIONS` to the observed workload and cache conditions. From the clean
checkout root, run the existing checker and measurement supervisor.

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go run ./tools/acceptance/cmd/check \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json --root . --require-ready
CGO_ENABLED=0 GOTOOLCHAIN=local go build -tags cuda \
  -o /tmp/yakuori-acceptance-cuda-history ./tools/acceptance/cmd/run
CUDA_VISIBLE_DEVICES="$GPU_UUID" /tmp/yakuori-acceptance-cuda-history --measure \
  --manifest docs/evidence/acceptance/fixtures.json \
  --criteria docs/evidence/acceptance/criteria.json \
  --root "$PWD" --repository "$PWD" --evidence "$PRIVATE_EVIDENCE" \
  --model-id index-translate \
  --model-sha256 044b313d29342bd3b2c77cbb64023ca0d209bd9b3247763f9d162767ef2d746a \
  --model-quant Q4_K_M \
  --model-quant-source 'IndexTeam/Index-Translate-2B-GGUF filename and dated acceptance records' \
  --gpu-uuid "$GPU_UUID" --conditions "$RUN_CONDITIONS" \
  --repeat-index 1 --repeat-total 1 --game witcher3 --artifact acceptance
```

Compare the recorded wall time, worker RSS and sampled worker VRAM for each
fresh run with v3. The [SQLite follow-up report](gpu-db-run-2026-11-03.md)
records the observed generation, TM reuse, independent reads and cleanup.

On assessed revision `f4494a7`, both GoInfer modules use fork revision
`994cbf5ee38dd8dcc612aad6e6803ac79f772f3b`. The CUDA dependency moved to
`github.com/townsendmerino/goinfer/backend/cuda`, but the measurement check
requires the legacy `github.com/townsendmerino/goinfer/cuda` build entry.
`go version -m` on the CUDA-tagged runner confirmed the new module path.
The measurement on `f4494a7` would be incomplete (`worker_metadata`) even
after a successful CUDA worker and reader. See the
[measurement metadata check](../../../tools/acceptance/runner_linux.go#L102)
and [build info collection](../../../tools/acceptance/worker_linux.go#L38).
