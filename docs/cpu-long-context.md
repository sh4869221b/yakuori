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
- GOMEMLIMIT=2300MiB is a soft Go heap target, **not an RSS ceiling**.
- A supervisor checks `/proc` every 50 ms and kills the child if sampled RSS/HWM
  exceeds 3 GiB, host MemAvailable drops below 1 GiB, or process wall time exceeds
  360 seconds. This watchdog has scheduling/measurement overshoot, not a hard cap.
- Every normal generation gets a 300-second context deadline. Higher stages stop
  after any failed normal or deadline probe. Timeout/error evidence is preserved.
- CI additionally uses a hard Docker 4 GiB cgroup (no swap), 2 CPUs, network disabled and a
  20-minute job timeout. Its narrower smoke stages are 1,024 and 4,096 tokens.

Capacity was examined before the staged run. Loaded dimensions are 24 layers,
896 hidden, 14 attention heads, 2 KV heads, derived head width 64, and intermediate
width 4,864. The default f32 KV payload is approximately 24,576 bytes per position
(768 MiB at 32,768 positions). The ten main batched prefill arrays in pinned
`decoder/forwardn.go` total approximately 65,024 bytes per prompt token (2,032 MiB
at 32,768), before weights, quantization workspaces, attention scratch, allocator
headroom and runtime overhead. These are capacity estimates, not measured RSS or
an allocation upper bound. The actual attention implementation tiles its query
rows and uses fused scratch; it does not allocate a full 32K-by-32K score matrix.
A full-window run is therefore not assumed to fit the 3 GiB experiment envelope.

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

The largest successfully completed tested prompt is 8,192 tokens. The larger
100 ms deadline overrun there confirms the pinned upstream per-layer prefill
cancellation check is cooperative: a nominal timeout can return seconds later.
Different cancellation phase/host/load can give different overruns; 7.556 s is an
observation, not a worst-case bound. No partial candidate is accepted.

## Acceptance and next decision

- Actual token-counted CPU prefill/decode/Stop through 8K: passed for this synthetic
  fixture. Pins, offline clean inference, and short terminal semantics remain as
  documented by the original spike and exact-head CI.
- The 16K normal stage: failed the chosen 5-minute experimental deadline. This
  failure is retained, not skipped or converted into success.
- Full advertised 32K window: **not run**, therefore #3's whole-window gate stays
  open. Metadata/admission arithmetic remains a separate check.
- #14 still needs representative translation inputs, quality/validation results,
  and a target machine before choosing an effective operational cap. Do not adopt
  8K, 5 minutes, 3 GiB, or 100 ms as product defaults from this experiment.

Two bounded next paths are available: choose/measure a smaller operational window
under #14, or explicitly budget a larger, suitably provisioned 16K/32K experiment.
The capacity estimate above already warns against assuming 32K fits this 3 GiB
RSS envelope; increasing only the time limit is not enough evidence of safe fit.
Neither path changes pure-Go requirements. If a hard cancellation deadline is
required, process isolation or a finer-grained upstream cancellation improvement
must be evaluated separately; swapping to a C/C++ backend is not implied.
