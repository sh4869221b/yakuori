# Bounded CPU long-context experiment (#3)

This supplements [the short CPU spike](cpu-inference-spike.md). It uses exactly the
same checksum-pinned official 0.5B GGUF and goinfer v0.20.0. No model choice,
translation-quality claim, production wrapper, or #14 product limit is adopted.
The [canonical design](https://chatgpt.com/space/page_3565e1d53fa08191a7d8cb56e84af5a5)
is unchanged. In particular, a synthetic controlled Stop is not an accepted translation.

## Method and safety budget

Each stage is a fresh Linux amd64 process: load/verify the model, render ChatML once,
encode the entire request with its real tokenizer, assert its exact target length,
encode again to check equality, and pass those same IDs to Generate. The fixture is
`" hello"` repeated inside an ordinary user turn, followed by `"\nReply with only Hello."`.
The explicit system message is `"You are a helpful assistant."`. No user input,
control-marker injection, token-ID truncation, or implicit model download occurs.
Token-ID JSON and rendered text hashes make the complete input reproducible without
committing thousands of repeated tokens. Prompt length includes the whole template.

The generation budget is 2 tokens. A logit processor forces `Hello` after the real
prefill and a template Stop token after one real autoregressive decode. This probes
allocation/forward/control flow, **not natural language quality, recall, or accuracy**.
A separate fresh process sets a 100 ms context deadline during prefill and drains
until the backend closes. `deadline_overrun_ms` is elapsed minus the requested
100 ms, not a claim of hard real-time cancellation. The prior short probe covers
explicit cancellation after partial output and model reuse.

Experimental safeguards, not product settings:
- GOMAXPROCS=2; ambient GOINFER tuning is removed by the supervisor and refused by
  the probe. Pinned upstream fast/fused attention defaults are enabled. These can
  change numerical results versus exact-prefill mode; no equivalence is claimed.
- Supervisor defaults are GOMEMLIMIT=2300MiB (a soft Go heap target, **not an RSS
  ceiling**), a sampled RSS/HWM kill threshold of 3 GiB, a 1 GiB host MemAvailable
  floor, a 360-second process wall, and a 300-second request deadline. It checks
  `/proc` every 50 ms, so watchdog timing can overshoot. The existing CI keeps
  these defaults and runs only 1,024- and 4,096-token stages in a 4 GiB Docker
  cgroup (no swap), 2 CPUs, with networking disabled and a 20-minute job timeout.
  Any failed normal or deadline probe stops higher stages, with timeout/error
  evidence retained. The extended run below overrides these supervisor budgets
  explicitly.

Capacity was examined before the staged run. Loaded dimensions are 24 layers,
896 hidden, 14 attention heads, 2 KV heads, derived head width 64, and intermediate
width 4,864. The default f32 KV payload is approximately 24,576 bytes per position
(768 MiB at 32,768 positions). The ten main batched prefill arrays in pinned
`decoder/forwardn.go` total approximately 65,024 bytes per prompt token (2,032 MiB
at 32,768), before weights, quantization workspaces, attention scratch, allocator
headroom and runtime overhead. These are capacity estimates, not measured RSS or
an allocation upper bound. The actual attention implementation tiles its query
rows and uses fused scratch; it does not allocate a full 32K-by-32K score matrix.
The later synthetic full-window run below used a separate 8 GiB container budget;
it does not establish that the full request fits the default 3 GiB sampled RSS
envelope.

## Reproduce

Use the Go 1.27.1 toolchain and verified local model described in the short spike:

```sh
export CGO_ENABLED=0 GOTOOLCHAIN=local
go build -o bin/cpuspike ./tools/cpuspike
python3 ci/run-long-context.py "$PWD/bin/cpuspike" /absolute/path/to/model.gguf \
  /tmp/yakuori-long-results --stages 1024,4096,8192,16384,32766
```

The last stage reserves 2 output tokens: 32,766+2=32,768. Admission alone is not
success; only a completed real forward/Stop probe would count as runtime evidence.
A failed stage exits nonzero; no later stage or retry runs automatically. The
supervisor writes JSON for every attempted stage, stderr, and `supervisor.json`.
Use a fresh output directory for each execution to keep evidence unambiguous.

For a clean compiler-free, offline runtime on a suitable Docker host:

```sh
docker build -f ci/cpu-spike.Dockerfile -t yakuori-cpu-spike .
mkdir -p long-context-results
docker run --rm --network none --memory 4g --memory-swap 4g --cpus 2 \
  -v "$PWD/long-context-results:/evidence" --entrypoint python3 yakuori-cpu-spike \
  /src/ci/run-long-context.py /cpuspike /model.gguf /evidence --stages 1024,4096
```

Missing model, changed digest, unexpected tokenizer/context/quant, malformed target,
wrong terminal state, or a watchdog kill is a failure. Ordinary unit tests do not
load a model and are not inference evidence. Linux arm64 remains compile-only.

## Measured local results (2026-10-02)

Host: AMD EPYC 9V74, Linux amd64, 9.73 GiB RAM, no swap; Go 1.27.1, 2 Go workers.
These are single observations on a shared host, not a benchmark or stable SLO.
[Raw reports and supervisor evidence](evidence/cpu-long-context/) retain successful
and failed results. Peak RSS below is self-reported process high-water RSS, including
model loading; the supervisor's sampled HWM differs slightly and is also preserved.
Local binaries were built in a modified worktree based on `580a33a`; binary hashes
are recorded. Final changes add safety checks/report cleanup without changing the
forward workload. Exact-PR-head CI independently reruns the 1K/4K subset.

| Actual prompt tokens | Normal request | Request elapsed | Peak RSS KiB | 100 ms deadline elapsed / overrun |
| ---: | --- | ---: | ---: | ---: |
| 1,024 | Stop, one controlled `Hello` | 13,563 ms | 970,900 | 644 / 544 ms |
| 4,096 | Stop, one controlled `Hello` | 66,930 ms | 970,160 | 2,777 / 2,678 ms |
| 8,192 | Stop, one controlled `Hello` | 185,427 ms | 1,345,144 | 7,655 / 7,556 ms |
| 16,384 | **Timeout, zero output, failure** | 301,980 ms | 1,974,312 | Not run after failed normal stage |
| 32,766 + 2 reserved output | **Not run** | — | — | — |

The 16K request reached its 300-second deadline and drained 1,980 ms later with
`context deadline exceeded`; it exited nonzero. The process wall time including
verification/loading was 305.671 s. No memory watchdog or OOM kill occurred.
It did **not** complete prefill or emit any token within the budget. Higher stages
were not automatically attempted. This is a measured time-envelope failure on this
host, **not proof of a backend architectural 16K limit or impossibility at 32K**.

In this 2026-10-02 EPYC run, the largest successfully completed tested prompt was
8,192 tokens. The larger 100 ms deadline overrun there confirms the pinned
upstream per-layer prefill cancellation check is cooperative: a nominal timeout
can return seconds later. Different cancellation phase/host/load can give
different overruns; 7.556 s is an observation, not a worst-case bound. No partial
candidate is accepted.

## Extended local results (2026-10-10)

The additional run used an AMD Ryzen 7 7700X host with 31.04 GiB RAM, CachyOS and
kernel 7.3.0-rc6-2-cachyos-rc, Linux amd64, Docker 29.9.0 with cgroup v2, Go
1.27.1, and two Docker CPUs. Docker enforced an 8 GiB memory limit and
`memory.swap.max=0`; networking was disabled. Before the run, host
`MemAvailable` was 24,340,208 KiB. These are measurements from this host and
configuration, independent of the earlier EPYC results.

The extended supervisor overrides the defaults above with a 1,800-second normal
request deadline, 2,100-second process wall, 7,168 MiB sampled RSS kill threshold,
4,096 MiB host `MemAvailable` floor, and `GOMEMLIMIT=6144MiB` (still a soft Go
heap target). The hard 8 GiB cgroup limit and the supervisor's RSS/host guards are
separate controls: sampled RSS is not the container limit, and host
`MemAvailable` is not cgroup memory remaining. All four fresh processes exited 0
without a watchdog kill; all stderr files are empty.

The exact run command was:

```sh
mkdir -p docs/evidence/cpu-long-context/ryzen-7700x-extended
docker run --rm --name yakuori-issue3-task2-extended --network none --memory 8g --memory-swap 8g --cpus 2 \
  -v "$PWD/docs/evidence/cpu-long-context/ryzen-7700x-extended:/evidence" \
  --entrypoint python3 yakuori-cpu-spike \
  /src/ci/run-long-context.py /cpuspike /model.gguf /evidence \
  --stages 16384,32766 --request-timeout-seconds 1800 \
  --process-wall-seconds 2100 --rss-limit-mib 7168 \
  --host-available-min-mib 4096 --go-memory-limit-mib 6144
```

Each mode ran in its own fresh process. JSON elapsed time and process RSS are
reported separately from supervisor process wall time and sampled RSS/HWM:

| Actual prompt tokens | Mode and result | JSON elapsed / peak RSS | Supervisor process wall / sampled peak RSS |
| ---: | --- | ---: | ---: |
| 16,384 | Stop; one controlled `Hello`, no error | 364,699 ms / 2,160,432 KiB | 367.168 s / 2,160,812 KiB |
| 16,384 | 100 ms deadline; Timeout, zero tokens, deadline error | 15,416 ms / 1,716,052 KiB | 17.829 s / 1,717,120 KiB |
| 32,766 | Stop; one controlled `Hello`, no error | 1,283,741 ms / 4,019,000 KiB | 1,286.257 s / 4,019,400 KiB |
| 32,766 | 100 ms deadline; Timeout, zero tokens, deadline error | 52,316 ms / 2,863,820 KiB | 54.799 s / 2,864,960 KiB |

The deadline reports record overruns of 15,316 ms and 52,217 ms respectively.
For the 32,766-token case, elapsed time and overrun were sampled separately and
differ by 1 ms. The deadline is cooperative and does not guarantee a 100 ms wall
time. The 32,766-token prompt plus a two-token output budget reaches the model's
32,768-token admission boundary. This synthetic controlled Stop demonstrates a
full-context forward/Stop path after emitting one `Hello` token, not translation
quality or natural full-window generation.

[The per-process JSON, empty stderr, and supervisor reports](evidence/cpu-long-context/ryzen-7700x-extended/)
preserve the measurements.

## Acceptance and next decision

- The 2026-10-02 EPYC run passed actual token-counted CPU prefill/decode/Stop through
  8K for this synthetic fixture. Pins, offline clean inference, and short terminal
  semantics remain as documented by the original spike and exact-head CI.
- The original EPYC 16K normal stage failed its chosen 5-minute deadline, and its
  32,766+2 stage was not run. Those results remain historical and are not merged
  with the later Ryzen measurements.
- A synthetic controlled Stop at 32,766 prompt tokens with two output tokens
  reserved completed under the extended budget. This records one full-context
  runtime observation; it does not select an operational cap or establish
  translation quality, repeated performance, or a general memory ceiling.
- #14 still needs representative translation inputs, quality/validation results,
  and a target machine before choosing an effective operational cap. Do not adopt
  8K, the measured runtimes, 3 GiB, 7 GiB, or either deadline as product defaults
  from this experiment.

The [#12 production CPU boundary](cpu-inference-spike.md#production-wrapper-12)
is implemented separately; #14 must measure representative translation inputs,
quality, and finite operating limits. The extended run does not
change pure-Go requirements. If a hard cancellation deadline is required, process
isolation or a finer-grained upstream cancellation improvement must be evaluated
separately; swapping to a C/C++ backend is not implied.
