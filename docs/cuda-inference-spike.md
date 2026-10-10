# CUDA inference evaluation: #23 spike and #52 product wrapper

## Current #52 product wrapper result (2026-10-10)

Issue #52 connects explicit CPU/CUDA selection to the shared inference Engine.
The product-wrapper measurements below use the same Engine, request construction,
token counting, segmented Core path and configured defaults on both backends.
The original #23 spike report follows as historical evidence from before this
integration; its harness limits and results are not the #52 product results.

### Selection and effective context

[`goinfer.OpenWithOptions`](../internal/inference/goinfer/engine.go) accepts an
explicit `cpu` or `cuda` backend, `int4` compute quantization and a positive
configured context. Invalid options are rejected before the model snapshot is
read. The compatibility `Open(ctx, modelPath, computeQuant)` entry point remains
CPU-only and retains the model-declared context behavior. There is no automatic
backend selection, CPU fallback, or generation retry.

CUDA registration is compiled only for `cuda && linux && amd64`; ordinary CPU
builds do not import the CUDA module, including the existing Linux arm64 CPU
build. The runtime requires an NVIDIA device and host driver (`libcuda.so.1`); the build
does not install or bundle a driver. An explicit CUDA load fails with the
concrete setup reason if CUDA is not built, the driver is unavailable, the
upstream loader selects CPU, or resident context is inactive or unknown.

For the new options entry point, effective context is the minimum of model
context, configured context and (for CUDA) active resident context. The tested
metadata reported model context 262,144, configured and resident cap 4,096,
effective context 4,096, effective backend `cuda`, resident active, compute
quantization `int4`, and resident KV precision `f32`. `ModelInfo` also exposes
requested/effective backend, backend pins and a residency decline reason. It
does not implement the canonical #15 SourceIdentity/Profile/TM key; the backend
is not added to immutable request identity. The existing module pins are
`goinfer@v0.22.0` and `goinfer/cuda@v0.22.0`.

Both backends use the same `DefaultLimits` values from
[`internal/config/limits.go`](../internal/config/limits.go): artifact 1,495 B,
76 units, 980 B per unit, 1,036 B total text, 76 segments, context 4,096,
maximum output 2,048 tokens, 30 s request timeout, 300 s generation timeout,
1 s database operation and cleanup timeouts, and zero retries. The CUDA matrix
uses these defaults without the earlier spike's 2 MiB artifact or extended
experiment timeouts.

### Reproduction and environment

Run from the repository root on Linux amd64 with Docker, Python 3, at least 16
allowed CPUs, the existing model file, an NVIDIA driver for the actual CUDA
run, and the official Go `1.27.1` build image. The model is
`IndexTeam/Index-Translate-2B-GGUF`'s `Index-Translate-2B.Q4_K_M.gguf`; the GGUF
quantization is `Q4_K_M` and the Engine compute quantization is `int4`. The
already-present local model is used; this procedure does not fetch model
weights. An immutable model-repository revision is not pinned.

The recorded host was Linux amd64 with an NVIDIA GeForce RTX 4070, driver
`615.78.08`, and 12,282 MiB reported GPU memory. This is the only CUDA
configuration exercised here. The tagged image used official Go `1.27.1` with
`CGO_ENABLED=0`; its compiler/toolkit absence check is captured in the
[tagged CUDA build log](evidence/cuda-inference/product-wrapper/tagged-cuda-build.log).
The accompanying [host driver and model capture](evidence/cuda-inference/product-wrapper/environment/host-driver-model.log)
records the device/driver sample, `libcuda.so.1` presence and local model file.
The runner used the first 16 allowed CPUs, `GOMAXPROCS=16`,
`GOMEMLIMIT=14336MiB`, a sampled RSS ceiling of 16,384 MiB and a host available
memory floor of 1,024 MiB. Those memory bounds are monitored; no hard cgroup
memory bound was present. Inherited `GOINFER_*` settings were cleared. Matrix
requests used the product's 30 s/300 s request/generation budgets; the separate
runner watchdog is not a product timeout.

```sh
docker build --progress=plain -f ci/cuda-spike.Dockerfile -t yakuori-issue52-cuda .
container=$(docker create yakuori-issue52-cuda /bin/true)
docker cp "$container:/cuda-evaluation.test" /tmp/yakuori-issue52-cuda.test
docker rm "$container"

model=/home/sh4869/.cache/yakuori/models/Index-Translate-2B.Q4_K_M.gguf
out=$(mktemp -d)
python3 ci/run-cuda-evaluation.py --binary /tmp/yakuori-issue52-cuda.test \
  --model "$model" --output-dir "$out/contracts-cpu" \
  --mode contracts --backend cpu
python3 ci/run-cuda-evaluation.py --binary /tmp/yakuori-issue52-cuda.test \
  --model "$model" --output-dir "$out/contracts-cuda" \
  --mode contracts --backend cuda
python3 ci/run-cuda-evaluation.py --binary /tmp/yakuori-issue52-cuda.test \
  --model "$model" --output-dir "$out/matrix" \
  --mode matrix --backend both
```

The captured host outputs are retained under
[`docs/evidence/cuda-inference/product-wrapper/`](evidence/cuda-inference/product-wrapper/);
the commands above write a fresh reproduction under `$out` and do not replace
those captured reports.
Contract mode's `policy_scope` records that cancellation/deadline/lifecycle
probes use explicit test budgets; the matrix is the evidence for product
defaults. The driver-free checks mount only the read-only model and no NVIDIA
runtime/device. These are the recorded invocations; each result is also kept in
the `driver-free/` evidence directory.

```sh
docker run --rm --network none --memory 16g --memory-swap 16g --cpus 16 \
  -v /home/sh4869/.cache/yakuori/models/Index-Translate-2B.Q4_K_M.gguf:/model.gguf:ro \
  -v /home/sh4869/.codex/worktrees/issue-52-cuda-engine/yakuori/.omo/evidence/issue-52-cuda-engine:/evidence \
  -e GOMAXPROCS=16 -e YAKUORI_CUDA_MODEL=/model.gguf \
  -e YAKUORI_CUDA_BACKEND=cpu -e YAKUORI_CUDA_REPORT=/evidence/driver-free-cpu.json \
  --entrypoint /cuda-evaluation.test yakuori-issue52-cuda:latest \
  -test.run '^TestCUDAEvaluationContracts$' -test.v -test.count=1 -test.timeout=1800s \
  > .omo/evidence/issue-52-cuda-engine/driver-free-cpu.log 2>&1

docker run --rm --network none --memory 4g --memory-swap 4g --cpus 2 \
  -v /home/sh4869/.cache/yakuori/models/Index-Translate-2B.Q4_K_M.gguf:/model.gguf:ro \
  -v /home/sh4869/.codex/worktrees/issue-52-cuda-engine/yakuori/.omo/evidence/issue-52-cuda-engine:/evidence \
  -e YAKUORI_CUDA_MODEL=/model.gguf -e YAKUORI_CUDA_BACKEND=cuda \
  -e YAKUORI_CUDA_REPORT=/evidence/driver-free-cuda.json \
  --entrypoint /cuda-evaluation.test yakuori-issue52-cuda:latest \
  -test.run '^TestCUDAEvaluationContracts$' -test.v -test.count=1 -test.timeout=1800s \
  > .omo/evidence/issue-52-cuda-engine/driver-free-cuda.log 2>&1
```

The [CPU contract invocation](evidence/cuda-inference/product-wrapper/driver-free/driver-free-cpu.log)
returned zero with all 11 scenarios passed. The explicit CUDA invocation
returned nonzero as expected: `libcuda.so.1` was unavailable, effective backend
was empty, residency was inactive and cap was zero. The recorded
[CUDA rejection report](evidence/cuda-inference/product-wrapper/driver-free/driver-free-cuda.json)
shows the reason. CPU success in this driver-free control is not CUDA evidence.

### Product-wrapper contract and paired matrix results

The actual CUDA [contract report](evidence/cuda-inference/product-wrapper/contracts/cuda/cuda.json)
and CPU [contract report](evidence/cuda-inference/product-wrapper/contracts/cpu/cpu.json)
each show all 11 scenarios passed. These cover exact request counting and
markers, natural Stop, output limit, context boundary equality/overflow,
cancellation and deadline drain/reuse, close while generating, and idempotent
Close. These are cooperative cancellation/deadline checks, not hard real-time
interruption guarantees. CUDA reported requested/effective `cuda`, active
residency, cap 4,096 and KV precision `f32`; CPU reported effective `cpu` and
nonresident. The two contract reports record the shared rendered request,
spans, token IDs, identity, policy and count fields.

The [matrix schedule](evidence/cuda-inference/product-wrapper/matrix/schedule.json)
contains 26 passing processes: four representative fixtures across three CPU /
CUDA pairs, plus one pair for the 76-label fixture. Each process records cold
and warm results, for 52 passing runs total. The planned request objects match
exactly across all 13 CPU/CUDA pairs. Each backend accepted 284 of 284 units
with zero mechanical `Validation` failures. The maximum fixture contains 76
labels, 1,495 artifact bytes and 1,036 total text bytes; the prose fixture has
one 980-byte text unit and 985 artifact bytes.

The table shows medians across the three pairs (the 76-label fixture has one
pair). Load includes model snapshot, decoder and tokenizer setup. Generation is
the sum of Engine `Generate` time for that fixture's units. TTFT is measured
from the `Generate` call to its first stream token and summarized across cold
and warm requests; it is not exact prefill time.

| Fixture | Pairs | Load CPU / CUDA (ms) | Cold generation CPU / CUDA (s) | Warm generation CPU / CUDA (s) | TTFT CPU / CUDA (ms) |
| --- | ---: | ---: | ---: | ---: | ---: |
| Short prose | 3 | 3,195.9 / 5,645.4 | 3.82 / 0.11 | 3.75 / 0.10 | 3,337.8 / 53.8 |
| 19 labels | 3 | 3,354.2 / 5,761.4 | 61.35 / 1.38 | 61.17 / 1.36 | 3,030.9 / 47.6 |
| Medium prose | 3 | 3,267.2 / 5,631.8 | 7.62 / 0.39 | 7.31 / 0.39 | 4,542.1 / 70.0 |
| Long prose | 3 | 3,280.8 / 5,694.0 | 20.15 / 1.41 | 19.84 / 1.35 | 8,881.9 / 130.4 |
| 76 labels | 1 | 3,372.8 / 5,701.3 | 246.60 / 5.79 | 244.30 / 5.59 | 3,010.5 / 48.1 |

The 76-label CPU generations completed within the 300 s generation budget. Its
[CPU report](evidence/cuda-inference/product-wrapper/matrix/02-x4-pair1-cpu.json)
records 246.599 s cold and 244.302 s warm generation; the paired
[CUDA report](evidence/cuda-inference/product-wrapper/matrix/02-x4-pair1-cuda.json)
records 5.789 s and 5.589 s. The 980-byte prose case completed in both modes
under the same product limits.

| Fixture | Process RSS high-water CPU / CUDA (KiB) | CUDA sampled process VRAM maximum (MiB) |
| --- | ---: | ---: |
| Short prose | 3,815,732 / 3,736,400 | 1,782 |
| 19 labels | 3,754,256 / 3,761,148 | 1,782 |
| Medium prose | 3,813,260 / 3,712,800 | 1,792 |
| Long prose | 3,754,020 / 3,723,560 | 1,838 |
| 76 labels | 3,823,580 / 3,633,048 | 1,782 |

RSS is the process `getrusage` high-water value. VRAM is a sampled child-process
maximum (nominal 100 ms interval plus query latency), not a continuous peak;
device-wide samples include the existing display/device baseline. The raw
per-process JSON and stdout captures, supervisor resource samples and schedule
are available in the linked `matrix/` directory. The RSS values are the maximum
process high-water among each fixture's pairs.

The measurements show speed and mechanical acceptance, not language quality.
The short fixture is identical on both backends (`北の門のそばに灯りを置いてください。`).
The 19-label outputs differ (`ヤルデンをキャストする` vs `Yrdenをキャストする`,
and `カスト・イグニ` vs `Igniをキャストする`). Both long-prose outputs include
the malformed `物資を船で送し` and `狭い水路をクルーを案内`; CUDA also has the
wording `手を振う`, while CPU has `手を振る`. Full current-run outputs are in
the [CPU long report](evidence/cuda-inference/product-wrapper/matrix/02-long-pair1-cpu.json)
and [CUDA long report](evidence/cuda-inference/product-wrapper/matrix/02-long-pair1-cuda.json).
`Validation` is structural/mechanical and is not a human translation review or
a quality score.

Exact prefill/decode phase times and the path taken by an individual request
remain unavailable; TTFT and static backend capability do not prove either.
The test did not trigger a real GPU OOM, compare other GPUs/drivers/models or
quantizations, exercise TM/commit/publication, or establish broad support beyond
the one Linux amd64 RTX 4070 configuration. #15 still owns canonical
SourceIdentity/Profile/TM identity and must decide how the reported backend,
pin, effective context, KV precision and template/tokenizer facts should inform
it. The latest [#23 owner decision](https://github.com/sh4869221b/yakuori/issues/23)
accepts the unavailable phase/path data as documented limitations and does not
make them or an intentional real OOM completion blockers; #23 is closed. They
remain unverified and are not counted as passing observations.

## Historical #23 spike report (captured before #52 product integration)

The sections below preserve the original #23 evaluation, its measurements and
acceptance status as they were recorded before the shared product wrapper was
connected. Those results use the historical test-only harness and experiment
limits. Current #52 runtime behavior and product-limit measurements are in the
section above.

## Historical scope and finding

This report records an opt-in evaluation of the existing Index Translate GGUF
through the test-only `cuda && cudasmoke` harness. The reports show a resident
CUDA backend on the tested machine and passing CPU/CUDA request and lifecycle
contracts. The complete paired matrix has 24 passing backend processes and 48
passing cold/warm runs; cold-generation medians were 14.3–43.6 times faster and
warm-generation medians 15.0–44.7 times faster than CPU across the four
fixtures. The long-prose examples also expose shared
translation errors and a CUDA-specific typo, so speed and mechanical Validation
do not establish language-quality approval. This is evidence for the
configuration below, not a production GPU integration or a general CUDA support
declaration. At the time of this #23 report, production GPU integration and any
upper-limit change remained #52 work. Exact prefill/decode phase times and
request-specific prefill path remain unverified.

At the time of the original #23 report, this measured configuration was a
recommended implementation candidate; that recommendation is superseded by
the current #52 results above. It did not declare product support or approve
translation quality automatically.

## Evaluated configuration

| Item | Observed value |
| --- | --- |
| Host | CachyOS Linux, kernel `7.3.0-rc6-2-cachyos-rc`, Linux amd64 |
| GPU | NVIDIA GeForce RTX 4070, 12,282 MiB reported total memory; 10,411 MiB free in the captured `nvidia-smi` sample |
| NVIDIA driver | `615.78.08`; `libcuda.so.1` is present on the host |
| Go | Official Go `1.27.1`, `CGO_ENABLED=0`, `linux/amd64` |
| Inference pins | goinfer and goinfer/cuda `v0.22.0`; aikit `v1.57.0`; aikit/gpu `v0.33.5`; gocudrv `v0.3.2`; purego `v0.10.1` |
| Model | Official `IndexTeam/Index-Translate-2B-GGUF`, file `Index-Translate-2B.Q4_K_M.gguf`, local path `/home/sh4869/.cache/yakuori/models/Index-Translate-2B.Q4_K_M.gguf` |
| Quantization | GGUF file is `Q4_K_M`; goinfer compute quantization is `int4` |
| CUDA setup | Requested/effective backend `cuda`; resident active; resident context cap 4,096; model metadata context 262,144; effective Engine context 4,096; resident KV precision `f32` |
| Tuning | Upstream defaults, inherited `GOINFER_*` cleared, resident context pinned to 4,096 |

The recorded model source is the Hugging Face repository name
`IndexTeam/Index-Translate-2B-GGUF` and the file above, as described in the
[Index Translate CPU compatibility record](cpu-long-context.md#index-translate-2b-cpu-compatibility-14-2026-10-10).
This CUDA evaluation uses that already-present local model; it does not bundle
or download model weights. This spike does not pin a separate immutable
model-repository revision. The host capture ran
`nvidia-smi --query-gpu=name,driver_version,memory.total,memory.free --format=csv`
and reported the RTX 4070, driver `615.78.08`, 12,282 MiB total and 10,411 MiB
free. The captured host-library listing included `libcuda.so.1` and
`libcudart.so.13`.

The host capture also lists `libcudart.so.13` and CUDA library paths. The
compiler-free build is established by the tagged Docker image build and its
compiler-absence guard; this host run does not establish absence of CUDA runtime
or toolkit files on the host.

The runner selects the first 16 allowed CPUs, sets `GOMAXPROCS=16` and
`GOMEMLIMIT=14336MiB`, applies a 16,384 MiB sampled RSS ceiling and a 1,024 MiB
host-available-memory floor, and uses 300-second request, 1,500-second
generation, and 1,800-second process timeouts. No hard cgroup memory bound was
available (`memory.max=max`); these are monitored limits, not cgroup-enforced
limits. `peak_rss_kib` is the `getrusage` high-water value; supervisor RSS and
device/process VRAM are sampled. VRAM sampling is nominally 100 ms plus query
latency, is not continuous and has no pre-process per-process baseline. “Cold”
and “warm” mean first and subsequent generation in the same loaded process; the
page cache was not flushed.

## Reproduction

Run from the repository root on Linux amd64 with the required host NVIDIA driver,
the model already present at the path above, Docker, Python 3, and at least 16
allowed CPUs. The compiler-free official Go image builds the tagged test binary;
the runtime still needs the host driver and `libcuda.so.1`.

```sh
docker build --progress=plain -f ci/cuda-spike.Dockerfile -t yakuori-cuda-evaluation .
container=$(docker create yakuori-cuda-evaluation /bin/true)
docker cp "$container:/cuda-evaluation.test" /tmp/yakuori-cuda-evaluation.test
docker rm "$container"

model=/home/sh4869/.cache/yakuori/models/Index-Translate-2B.Q4_K_M.gguf
out=$(mktemp -d)
python3 ci/run-cuda-evaluation.py \
  --binary /tmp/yakuori-cuda-evaluation.test --model "$model" \
  --output-dir "$out/contracts-cpu" --mode contracts --backend cpu
python3 ci/run-cuda-evaluation.py \
  --binary /tmp/yakuori-cuda-evaluation.test --model "$model" \
  --output-dir "$out/contracts-cuda" --mode contracts --backend cuda
python3 ci/run-cuda-evaluation.py \
  --binary /tmp/yakuori-cuda-evaluation.test --model "$model" \
  --output-dir "$out/matrix" --mode matrix --backend both
```

The matrix runner serializes four existing fixtures across three paired runs,
CPU then CUDA for each pair. Each process records cold and warm generation. It
fails when a fixture, model, executable, or minimum CPU allowance is missing;
outputs are written to a fresh directory and inherited `GOINFER_*` variables
are removed. The exported binary is copied to `/tmp/yakuori-cuda-evaluation.test`;
the captured runner JSON, stdout/stderr, supervisor reports and schedules are
retained under [`docs/evidence/cuda-inference/contracts/`](evidence/cuda-inference/contracts/)
and [`docs/evidence/cuda-inference/matrix/`](evidence/cuda-inference/matrix/).

## CPU/CUDA request and lifecycle contracts

The [CPU raw contract report](evidence/cuda-inference/contracts/cpu/cpu.json)
and [current CUDA raw contract report](evidence/cuda-inference/contracts/cuda-current/cuda.json)
each record all 11 scenarios as `passed`. These are real Engine runs on the
respective backend. Exact scenario outcomes and request fields are preserved in
those JSON files; the earlier CUDA report is omitted because it predates the
current implementation run.

| Contract | CPU | CUDA |
| --- | --- | --- |
| Rendered request, literal control markers, exact token count | Passed | Passed |
| Natural Stop and one-token `MaxTokens` finish | Passed | Passed |
| Context-boundary equality accepted; one-token overflow rejected before decoder call | Passed | Passed |
| Partial output on cancellation; drain and healthy reuse | Passed | Passed |
| Deadline outcome; drain and healthy reuse | Passed | Passed |
| Close during generation drains; repeated Close remains safe | Passed | Passed |
| Requested/effective backend and residency | CPU effective, nonresident | CUDA effective, resident active, cap 4,096 |

Comparing the CPU and current CUDA contract report request fields shows identical
rendered prompt, spans, token IDs, identity, effective policy including StopIDs,
and `CountTokens` result (115). For the CUDA profile,
the model advertises 262,144 positions but the resident cap is 4,096; the Engine
uses the configured/resident intersection of 4,096. The captured static
`static_prefill_batched=true` value describes an upstream capability/policy; it
does not establish the per-request prefill path. No exact phase timer or
request-level prefill-path proof is available.

Final tagged compiler-free validation passed with this invocation:

```sh
docker build --progress=plain -f ci/cuda-spike.Dockerfile -t yakuori-cuda-evaluation .
docker run --rm --name yakuori-issue23-final-focused yakuori-cuda-evaluation sh -c \
  "go test -count=1 -tags 'cuda,cudasmoke' ./internal/inference/goinfer -run '^TestCUDAEvaluation(Context|Observer)$' && go test -count=1 ./internal/inference/goinfer ./internal/localize"
```

Both commands returned exit code 0. The final build used official Go `1.27.1`
with `CGO_ENABLED=0` and no compiler or SQLite headers in the image; the focused
tests covered declined/unknown-cap rejection, observer partial outcome/error
forwarding and token notification, plus existing inference/localization tests.
They are injected tests, not real GPU OOM or incompatibility evidence. The
separate final-source `docker build --progress=plain -f ci/Dockerfile -t
yakuori-issue23-qa-foundation .` / `ci/verify.sh` run also returned 0 in 29.0 s
with normal build/test/vet, cgo guard and ARM64 executable/SQLite test
cross-build. The existing CPU smoke was reused because the CPU path and wrapper
were unchanged: `docker run --rm --name yakuori-issue23-qa-probe --network none
--memory 4g --cpus 2 yakuori-issue23-qa-cpu` returned 0 for all six cases, and
`docker run --rm --name yakuori-issue23-qa-wrapper --network none --memory 4g
--memory-swap 4g --cpus 2 -e YAKUORI_CPU_MODEL=/model.gguf --entrypoint
/cpu-wrapper-smoke.test yakuori-issue23-qa-cpu -test.run '^TestCPUWrapperSmoke$'
-test.v -test.timeout 180s` returned 0 in 3.17 s. They remain separate from the
actual CUDA contract and matrix runs.

## Paired matrix results and translation quality

The matrix command in [Reproduction](#reproduction) completed with exit code 0.
All 24 process entries in the [matrix schedule](evidence/cuda-inference/matrix/schedule.json)
passed; all 24 supervisor reports have return code 0 and no watchdog kill; all
48 cold/warm run records passed, with no unrun or failed case. The raw CPU and
CUDA request objects match before generation in all 12 fixture/pair process
pairs. Within each fixture/backend, the output is identical across three pairs
and both cold/warm runs. Each backend has 132/132 mechanically accepted units,
with zero `Validation` failures. The [summary](evidence/cuda-inference/matrix/summary.json)
contains the aggregated medians and counts.

The table reports per-fixture medians. Generation is `Generate` call-to-return
time, excluding load, planning and validation; the speedup ratio is CPU divided
by CUDA. Cold means the first generation in a newly loaded process, without
flushing the OS page cache. Load includes snapshot, decoder and tokenizer setup.

| Fixture | Median load CPU / CUDA (ms) | Cold generation CPU / CUDA (ms; speedup) | Warm generation CPU / CUDA (ms; speedup) |
| --- | ---: | ---: | ---: |
| Labels (`01-fixture`) | 3,297.1 / 5,745.2 | 61,943.9 / 1,419.9 (43.6×) | 61,143.9 / 1,367.8 (44.7×) |
| Short prose (`00-short`) | 3,316.3 / 5,566.4 | 3,701.5 / 131.9 (28.1×) | 3,632.2 / 105.7 (34.4×) |
| Medium prose (`01-medium`) | 3,289.6 / 5,614.3 | 7,342.7 / 392.0 (18.7×) | 7,143.2 / 376.4 (19.0×) |
| Long prose (`02-long`) | 3,361.3 / 5,576.8 | 19,921.6 / 1,393.4 (14.3×) | 20,065.4 / 1,336.6 (15.0×) |

Memory observations from the paired processes are below. `peak_rss_kib` is the
process `getrusage(RUSAGE_SELF).ru_maxrss` high-water value; the supervisor's
`observed_peak_rss_kib` is sampled. CUDA process and device VRAM are sampled
maxima, not continuous peaks. CPU processes have no per-process GPU allocation;
their device-wide readings reflect the already-used display/device baseline.

| Fixture | Process RSS high-water CPU / CUDA (KiB) | CUDA process / device VRAM sampled max (MiB) |
| --- | ---: | ---: |
| Labels (`01-fixture`) | 3,755,364 / 3,805,732 | 1,782 / 3,274 |
| Short prose (`00-short`) | 3,686,816 / 3,788,540 | 1,782 / 3,247 |
| Medium prose (`01-medium`) | 3,622,308 / 3,798,560 | 1,792 / 3,264 |
| Long prose (`02-long`) | 3,762,716 / 3,808,144 | 1,838 / 3,359 |

The test-only observer measures `TTFT` from the Engine `Generate` call to the
first observed stream token; `post_first_token_stream_ms` measures that token
through stream drain. These observable intervals do not provide exact
prefill/decode phase times. The
request-specific prefill path is also unavailable and remains unmet in #23. The
100 ms nominal VRAM sampling interval includes query latency. The runner used
monitored RSS and host available-memory limits, but no hard cgroup memory bound
was available (`memory.max=max`).

The paired outputs show language quality separately from `Validation`. For the
short fixture, both CPU and CUDA returned the same natural translation,
`北の門のそばに灯りを置いてください。`, for “Please leave the lantern beside
the northern gate.” The complete long-fixture outputs are in the [CPU pair 1 report](evidence/cuda-inference/matrix/02-long-pair1-cpu.json)
and [CUDA pair 1 report](evidence/cuda-inference/matrix/02-long-pair1-cuda.json).
Both outputs preserve event order and most content, but share errors:

| Source | CPU output | CUDA output | Observation |
| --- | --- | --- | --- |
| “They agreed to send supplies by boat and ask the lighthouse keeper to guide the crew through the narrow channel.” | `彼らは物資を船で送し、灯台の管理人に狭い水路をクルーを案内するよう頼むことにした。` | Same as CPU | `送し` is ungrammatical; `狭い水路をクルーを案内` has incorrect case marking. |
| “A young carpenter stayed behind to repair the village cart.” | `若い大工が村の荷車を修理するために残された。` | `若い木職人が村の荷車を修理するために残された。` | “Stayed behind” becomes “was left behind,” changing agency in both; `木職人` is awkward here. |
| “From the deck, Mara watched him wave until the harbor disappeared into the morning mist.” | `甲板から、マーラは彼が手を振るのを眺め、港が朝の霧に消えていくまで見続けた。` | `甲板から、マーラは彼が手を振うのを眺めていたが、港が朝の霧に消えていくまで見続けていた。` | The CPU sentence is natural; CUDA has the typo `手を振う` and an unnecessary adversative `が`. |
| “her sister” | Both render it as `姉` | Both render it as `姉` | Japanese specifies an older sister, while the English source gives no age. |

The [full paired comparison](evidence/cuda-inference/matrix/comparison.md)
also records that label outputs leave `Cast Aard`, `Cast Axii` and `Sheathe Auto`
in English. For “Cancel Aiming,” CPU says `エイメイをキャンセルする` while
CUDA says `エイミングをキャンセル`; for `Cast Igni`, CPU uses `カスト・イグニ`
and CUDA uses `Igniをキャストする`. Both render `Draw Auto Sword` as
`自動剣を引く`, which is unnatural. The medium fixture retains its clauses in
both outputs; CPU uses `閉鎖されたままである` and CUDA uses `閉鎖されたままです`,
mainly a style difference. Both translate “old mill” as `古い水車場`, adding an
unprovided water-mill interpretation.

The matrix shows strong generation-time gains on this machine, with CUDA load
taking about 2.2–2.5 seconds longer per process. It does not establish quality
parity: the sampled long translation contains shared grammar/agency issues, and
CUDA has a small additional wording defect. `Validation` counts mechanical
acceptance/failure outcomes only; it is not a translation-quality score or
human review.

## #23 acceptance clause status

Statuses distinguish evidence that has been observed from criteria that remain
open. `PARTIAL` means the named limited cases were tested but do not establish
broader support. `UNVERIFIED` means the required observation was not available.

| #23 acceptance clause | Status | Evidence and remaining condition |
| --- | --- | --- |
| Record host/GPU/driver/VRAM, Go and dependency pins, model source/quantization, and reproduction command | PASS | Environment and command are recorded above; driver and backend setup are in the raw reports. Model source is identified by official repository and file; no immutable source-revision pin is claimed. |
| Confirm actual resident GPU, equal rendered request/token IDs/counts, and effective context versus resident cap | PASS | CPU/CUDA contract request fields match; the current CUDA contract report shows effective CUDA, active residency and cap 4,096 against model context 262,144. The 12-pair matrix also confirms exact CPU/CUDA request equality before generation. |
| Check Stop, MaxTokens/ContextLimit, partial result/error, cancellation/deadline drain, reuse and Close | PASS | All 11 real CPU and CUDA contract scenarios passed; injected observer behavior is recorded separately above. |
| Compare same-fixture CPU/CUDA speed, memory, Validation and representative translation quality | PASS | Complete medians, process RSS high-water, sampled VRAM, 132/132 mechanical Validation per backend and paired translation examples are recorded above. This does not meet the separate exact prefill/decode phase or request-specific path requirements, which remain unverified. |
| Record exact prefill/decode phase times and request-specific prefill path | UNVERIFIED | The test-only observer measures TTFT and post-first-token stream intervals; the pinned public API does not expose exact phase clocks or the path used for an individual request. Static prefill capability is not per-request proof. |
| Record #15 identity/policy and #52 support/limit/selection/fallback contracts for downstream use | PASS | Proposed contracts are recorded below; this report does not implement them in production or change defaults. |
| Record unsupported model/GPU configurations and reasons | PARTIAL | The injected setup test rejects a declined resident backend and an active backend with unknown resident cap. Real GPU OOM and other GPU/driver/model configurations were not run. Missing-model rejection is a harness setup failure, not evidence of a GPU support boundary. |
| Keep CGO=0 build/test/vet and existing CPU smoke green, distinguishing real GPU evidence | PASS | Final official Go 1.27.1 `CGO_ENABLED=0` tagged build/focused tests and normal `ci/verify.sh` passed; the existing six-case CPU probe and wrapper smoke remain passing with no related source changes. These CPU/tagged checks are separate from the real CUDA contract and matrix runs. |

## Proposed downstream contracts

### #15 identity and Translation Memory

The #15-facing proposal is to distinguish the observed effective backend and
the generation-relevant implementation/policy, including goinfer release,
on-disk model quantization, compute quantization, effective context, CUDA KV
precision, tokenizer/template and any proven prefill policy that can affect
output. Requested backend is not a substitute for effective backend. The
reported static prefill capability must not be presented as a request-specific
path. This is a proposal for #15 to decide; this spike does not define or
implement a profile key or a new identity scheme.

The matrix invokes `Core.Generate` directly with no Translation Memory lookup,
commit or publication mutation. TM identity/reuse behavior was not evaluated.
The #15 issue remains the authority for a future exact SourceIdentity/Profile
contract; a TM hit must not skip current-unit validation. These are downstream
requirements, not claims that #15's contract has been implemented here.

### #52 proposal in the historical #23 report

This subsection records what the original #23 report proposed for the then
future #52 work. The CUDA configuration observed in that spike was Linux amd64
on the RTX 4070/driver `615.78.08`, with the Index Translate Q4_K_M GGUF loaded
as `int4`, effective CUDA backend, active residency and a 4,096-token resident
context cap. That spike's 4,096 context, 2,048 output-token policy, 300-second
request and 1,500-second generation timeout were experiment bounds. The current
#52 implementation uses the shared defaults documented at the start of this
file, including 30 s request and 300 s generation timeouts. Other GPUs, drivers,
models and quantizations remain unevaluated.

The proposal recommended explicit CPU/CUDA selection and a concrete failure
when CUDA residency or context was unavailable. The current #52 implementation
and its driver-free rejection are described above. Automatic selection, mid-run
fallback, retries and GPU upper-limit expansion are outside #52 scope.

## Remaining evidence gaps and ownership

The exact prefill/decode phase times and request-specific prefill path remain
unverified. This run also did not exercise a real GPU OOM, other GPU/driver/model
configurations, or independent human translation review. The report supports
only the observed configuration and the limited injected rejection cases above.

The product-wrapper integration is documented at the start of this file. The
old statement that this report did not authorize #23 closure predates the
owner's completion decision above; #23 is now closed. The #15 identity and
Translation Memory contract remains for that issue to decide.
