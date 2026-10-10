# CUDA inference evaluation and adoption constraints (#23)

## Scope and current finding

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
declaration. Production GPU integration and any upper-limit change remain #52
work. Exact prefill/decode phase times and request-specific prefill path remain
unverified.

For #52, this measured configuration is a recommended implementation candidate:
it meets the observed request/lifecycle contracts and materially reduces
generation time. This recommendation does not declare product support or approve
translation quality automatically; product limits and quality acceptance remain
for their respective follow-up decisions.

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

### #52 supported scope and selection

The only CUDA configuration evidenced here is Linux amd64 on the observed RTX
4070/driver `615.78.08`, with the named Index Translate Q4_K_M GGUF loaded as
`int4`, effective CUDA backend, active residency and a 4,096-token resident
context cap. No other GPU, driver, model or quantization is validated. The
4,096 context, 2,048 output-token policy, 300-second request and 1,500-second
generation timeout are harness experiment bounds, not newly adopted product
limits. No production maximum or existing CPU limit is changed by this report.

For #52 implementation, explicit CUDA selection should fail with its concrete
setup/admission reason when CUDA, residency, or the required context is
unavailable; it should not retry or fall back during generation. Explicit CPU
selection remains available. If #52 later adopts automatic selection, any CPU
choice should occur before generation and expose its reason. The evidence here
does not establish production automatic selection, fallback behavior, or GPU
upper-limit expansion.

## Remaining evidence gaps and ownership

The exact prefill/decode phase times and request-specific prefill path remain
unverified. This run also did not exercise a real GPU OOM, other GPU/driver/model
configurations, or independent human translation review. The report supports
only the observed configuration and the limited injected rejection cases above.

Production GPU integration and any context/output/time/memory limit expansion
remain #52 work. The #15 identity and Translation Memory contract remains a
proposal for that issue to decide. This report does not complete GPU v1
acceptance or authorize closure of #23.
