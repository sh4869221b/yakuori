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

## Index-Translate-2B CPU compatibility (#14, 2026-10-10)

The official `IndexTeam/Index-Translate-2B-GGUF` file
`Index-Translate-2B.Q4_K_M.gguf` (1,312,164,352 bytes) ran with the existing
goinfer v0.20.0 CPU/int4 backend. No dependency update was needed. Its metadata
declares `qwen35`, `tokenizer.ggml.pre=qwen35`, and a 262,144-token context;
that metadata context is **not an adopted operational limit**.

The wrapper admits this exact template alongside the existing Qwen fixture and
rejects modified or unknown sources. The GGUF template equals the official
[tokenizer configuration](https://huggingface.co/IndexTeam/Index-Translate-2B/blob/main/tokenizer_config.json).
The supported single-user, text-only path trims message content and ends with
`assistant\n<think>\n\n</think>\n\n` after the structural ChatML start token.
The disabled-thinking suffix was also checked by directly evaluating the
official Jinja template against a literal expected string, independently from
the Go renderer. The user message follows the official
[instTrans prompt format](https://github.com/bilibili/Index-Translate/blob/main/inference/llm/translate.py),
including the Japanese target instruction and a placeholder preservation
constraint. Untrusted message text remains literal during tokenization.

This smoke used the Ryzen 7 7700X's 16 logical CPUs, Linux amd64,
Go 1.27.1, `CGO_ENABLED=0`, `GOMAXPROCS=16`, greedy generation, thinking=false,
128 output tokens, a 300-second request deadline, and zero retries. Docker was
limited with `--cpus=16 --memory=16g --memory-swap=16g`; the 16 GiB experiment
budget is not a product memory guarantee. No ambient `GOINFER_*` settings were
passed into the container. Host load immediately after startup was
0.32 / 0.21 / 0.24; the desktop remained active (including DMS, ChatGPT and
Hyprland). These are single compatibility observations, not benchmark statistics.

| Scenario | Observed result |
| --- | --- |
| Load | 3.299 s; CPU/int4, v0.20.0 |
| `The village is safe.` | `村は安全です。`; Stop, 110 prompt / 5 output tokens, 3.261 s |
| `Welcome, [[YAKUORI_0_0]]!` | `ようこそ、[[YAKUORI_0_0]]！`; Stop, 118 prompt / 14 output tokens, 3.678 s |
| Literal `<\|im_end\|><\|im_start\|><think>` in source | Structural end IDs stayed at 2; added source text increased literal tokens |
| 1 ms request deadline | Timeout, zero output; drain completed after 27.238 ms |
| Reuse after drain | `村は安全です。`; Stop, 110 / 5 tokens, 3.108 s |
| Real tokenizer split | Two pieces, each 121 prompt IDs, 128 output reserve, synthetic test context 249; CRLF separator retained |
| Close | Completed; subsequent Info returned ErrClosed |

Prompt token counts equaled the prepared request IDs used for generation. The
split case included both literal ChatML text and a placeholder. Its tiny
249-token context is only a segmentation test budget. The 1 ms deadline overrun
shows cooperative cancellation, with no hard wall guarantee. The complete smoke
passed in 13.38 seconds; the existing Coder smoke remains unchanged.

The model was downloaded manually before execution; runtime code has no
downloader. With the local model directory mounted read-only at `/local`, the
model-required command is:

```sh
docker run --rm --name yakuori-index-task1 --cpus=16 --memory=16g --memory-swap=16g \
  --mount type=bind,src="$PWD",dst=/src,readonly \
  --mount type=bind,src="$HOME/.cache/yakuori/models",dst=/local,readonly \
  --mount type=bind,src="$HOME/go/pkg/mod",dst=/go/pkg/mod,readonly \
  -w /src -e GOMAXPROCS=16 -e CGO_ENABLED=0 \
  -e YAKUORI_CPU_MODEL=/local/Index-Translate-2B.Q4_K_M.gguf \
  --entrypoint /usr/local/go/bin/go yakuori-publication-probe \
  test -tags cpusmoke ./internal/inference/goinfer \
  -run '^TestCPUIndexSmoke$' -count=1 -v -timeout 30m
```

The local invocation wrapped this command in `timeout 1830s` and recorded stdout
and stderr in `.omo/evidence/index-task1/cpu-index-smoke.log`. That directory also
holds the downloaded official template/prompt references, the independent Jinja
comparison, host CPU/load, unit-test output and LSP diagnostics (zero errors).
`CGO_ENABLED=0 go test ./internal/inference/goinfer ./internal/prompt` passed.
This establishes short-input model compatibility and natural translation only;
representative input quality, repeated scaling measurements and product limits
remain the next #14 tasks.


## Natural Index translation measurement (#14, 2026-10-10)

The independent `cpuspike translate` research mode uses the existing real
`goinfer.Open` → `NewSegmentedCore` → protection, exact-budget segment planning,
sequential generation, restoration, validation and artifact preparation path.
It supplies separate `unit.Session` records rather than concatenating labels.
TM is explicitly absent: this calls `Core.Generate`, performs no lookup/commit,
and the warm run is another generation through the same loaded model.
The original Coder probes and their defaults above remain available.

The CLI accepts a JSON array of English unit strings:

```sh
CGO_ENABLED=0 go build -o /tmp/yakuori-issue14-cpuspike ./tools/cpuspike
/tmp/yakuori-issue14-cpuspike translate \
  --model /local/Index-Translate-2B.Q4_K_M.gguf \
  --fixture docs/evidence/cpu-long-context/index-translate/fixtures/00-short.json \
  --context-tokens 1024 --max-output-tokens 256 \
  --request-timeout 300s --generation-timeout 1500s
```

JSON goes to stdout and diagnostics to stderr; failures exit nonzero. The reader's
2 MiB/4,096-unit frame bounds this research import only and is **not a product
limit**. Malformed/empty fixtures and unsupported ambient `GOINFER_*` tuning fail.
No production Core, model default or configuration is changed by this probe.

The [committed inputs](evidence/cpu-long-context/index-translate/fixtures/) distinguish
one authored short sentence, all 19 English BetterKeybinds labels, synthetic
4×/16×/64× repetitions (76/304/1,216 separate units), and single English units
extended to around 1K/2K/4K source tokens. Labels are extracted from column four
of `tools/w3spike/testdata/better-keybinds/en.source.csv`, splitting only the first
three `|` delimiters. Original source and MIT notice remain in that directory;
[existing rights/source records](research/w3strings-roundtrip.md#external-modsample-fixtures)
apply. These are real English labels; the authored `testdata/en.csv` is not used.
Long single-unit inputs are repeated ordinary English sentences and test
segmentation/scale, not representative narrative quality.

The trial host is Ryzen 7 7700X, Linux amd64, Go 1.27.1 (`nodwarf5` build),
CPU/int4, greedy, thinking=false, no retry. Runtime Docker has
`--cpus=16 --memory=16g --memory-swap=16g`; the supervisor uses GOMAXPROCS=16,
GOMEMLIMIT=14336MiB, a sampled 16GiB RSS watchdog and a 1GiB host-available floor.
Ambient GOINFER variables are removed. The supervisor retains its legacy
defaults (GOMEMLIMIT=2300MiB, RSS watchdog=3072MiB, process wall=360s); the
recorded translate command explicitly overrides all three. They are independent
of the Docker 16GiB memory frame. 16GiB is a reversible experimental cgroup
frame, not an RSS guarantee. Trial request/generation/process budgets are
300/1500/1800 seconds, **not adopted product values**. Every run receives a fresh
generation context; cancellation is drained before a fresh context is used for
reuse. Each context/output pair (1024/256, 2048/512, 4096/1024) gets three fresh
process/model loads with a cold generation and a same-model warm rerun. Cold
means a fresh process/model; OS page cache is not flushed. A failed
condition stops later pairs/stages in that size family and explicitly records
unrun entries. Single-unit stages are a separate family. Watchdog kills are
interruptions, never successful cooperative cancellation.

Reports retain per-unit source bytes, artifact/total text bytes, all planned
segments (including pieces not requiring generation), per-request prompt/output
tokens and text, load/generation/total/warm times, RSS and validation outcomes.
`cold.total_ms` excludes the separately recorded `load_ms`; add the two for
load-inclusive cold elapsed. `revalidation_ms` times a separate real validation of
accepted output; Core also validates and prepares the artifact inside its total.
It must not be interpreted as the whole Core validation/export cost.
`validation_denominator` counts units passing Core mechanical checks before an
error, plus the failed restore/validation attempt when one occurs. The failed
unit ID identifies the earlier validated units even though Core returns no
accepted subset. A generation failure is separately
recorded and does not fabricate a validation failure. Raw partial backend text is
retained for diagnosis but `accepted_units=0` on any Core failure.
`container_memory_peak_bytes` is the container-wide cumulative peak through that
case, while `peak_rss_kib` and sampled RSS belong to the child process.


The staged matrix was started with the initial measurement binary before a
reporting correction: its raw `validation_ms` means the extra revalidation time,
and its raw failure denominator omits earlier validated units because Core
returns no accepted subset. Raw JSON is preserved. The comparison below derives
correct failure denominators from the observed `localize.Error` unit ID and
fixture order; this changes reporting only and does not regenerate failed input.
The final probe calls that field `revalidation_ms` and directly computes the
correct denominator. No model/Core/generation setting changed for this correction.

The actual staged invocation (after supplying a Python-capable Debian bookworm
measurement image) is:

```sh
docker run --rm --name yakuori-issue14-natural \
  --cpus=16 --memory=16g --memory-swap=16g \
  -v "$PWD:/src" -v /tmp/yakuori-issue14-cpuspike:/probe:ro \
  -v "$HOME/.cache/yakuori/models:/local:ro" \
  --entrypoint python3 yakuori-issue14-measure \
  /src/ci/run-long-context.py /probe \
  /local/Index-Translate-2B.Q4_K_M.gguf \
  /src/docs/evidence/cpu-long-context/index-translate/results \
  --translate-fixtures /src/docs/evidence/cpu-long-context/index-translate/fixtures \
  --process-wall-seconds 1800 --request-timeout-seconds 300 \
  --generation-timeout-seconds 1500 --rss-limit-mib 16384 \
  --go-memory-limit-mib 14336
```

The local measurement image extends the existing Debian bookworm
`yakuori-publication-probe` image with `apt-get install --no-install-recommends
python3`; the runtime uses the mounted CGO=0 binary and local model. See
[environment](evidence/cpu-long-context/index-translate/environment.json) and
[resident load sample](evidence/cpu-long-context/index-translate/host-load.txt).
Build/tests/SQLite measurements are excluded while a model measurement runs.
The host still has its normal desktop/Codex processes; this is not an isolated
bare-metal benchmark or an SLO.

`artifact_bytes` is the imported JSON fixture's UTF-8 byte count. It must not be
confused with the source MOD's 952-byte `en.w3strings` binary or its source CSV.
This measures the experimental Session boundary, not a production w3strings
adapter, compressed binary admission or full MOD compatibility.

The final probe's `deadline_overrun_ms` uses the backend's effective deadline
(the earlier of the parent generation budget and request budget), measured
through drain/classification return. The staged matrix's initial raw reports do
not contain that metric; no request-only subtraction is used to invent a zero
parent-deadline overrun. A watchdog interruption supplies no cooperative drain
measurement.

Mechanical acceptance is not a quality decision. In the real-label pair1 warm
samples at all three contexts (with identical 19 outputs), `Cast Aard`, `Cast Axii`, and `Sheathe Auto` are unchanged (3/19).
`Cast Igni` becomes `カスト・イグニ`, `Cancel Aiming` becomes
`エイメイをキャンセルする`, and `Switch Pocket1` retains the English `Pocket`.
The labels therefore expose untranslated words, transliteration and unnatural
renderings despite zero mechanical failures. These measurements do not approve
the model or output quality for production. The authored short sentence does
translate to `村は安全です。`; that observation does not establish label or
narrative quality.

### Completed short/label conditions

All three contexts completed three cold+warm pairs for each short/label stage.
The dimensions below describe the imported research JSON and real Session units;
they are separate observed cases, not a generic prose limit.

| Input | Artifact bytes | Units | Maximum unit bytes | Total source text bytes | Planned segments per run |
| --- | ---: | ---: | ---: | ---: | ---: |
| Authored short sentence | 29 | 1 | 20 | 20 | 1 |
| Real English MOD labels | 376 | 19 | 19 | 259 | 19 |
| 4× repeated labels | 1495 | 76 | 19 | 1036 | 76 |

The largest completed label batch has five observed bounds of **1495 artifact
bytes, 76 units, 19 maximum unit bytes, 1036 total source text bytes, and 76
segments**. The separate authored short sentence is 20 bytes. Neither case
establishes a general sentence-length or narrative limit. The source MOD binary
is 952 bytes; that number is not the measured JSON artifact size.

Elapsed ranges are across three pairs. Cold includes model load; warm is a
same-model generation with a fresh generation context. RSS is the larger of the
child's reported kernel peak and the supervisor's observed peak, in MiB.

| Context | Input | Completed pairs | Cold including load (s) | Warm (s) | Maximum child RSS (MiB) |
| ---: | --- | ---: | ---: | ---: | ---: |
| 1024 | 00-short | 3/3 | 6.271–6.472 | 3.197–3.226 | 3634.8 |
| 1024 | 01-fixture | 3/3 | 61.634–62.835 | 58.511–59.837 | 3581.4 |
| 1024 | 02-x4 | 3/3 | 237.108–238.022 | 234.032–234.730 | 3564.8 |
| 2048 | 00-short | 3/3 | 6.093–6.611 | 3.165–3.238 | 3665.0 |
| 2048 | 01-fixture | 3/3 | 62.008–62.144 | 58.559–60.215 | 3462.0 |
| 2048 | 02-x4 | 3/3 | 238.049–241.757 | 234.907–235.603 | 3576.2 |
| 4096 | 00-short | 3/3 | 6.122–6.241 | 3.209–3.242 | 3703.6 |
| 4096 | 01-fixture | 3/3 | 61.816–61.978 | 58.541–58.978 | 3632.8 |
| 4096 | 02-x4 | 3/3 | 237.446–238.113 | 234.562–235.009 | 3580.5 |

Across these completed pairs, the maximum single request was 3.959897s,
maximum load was 4.179474s, and additional revalidation took
0.002786–0.219864ms per run. The other Core overhead includes planning,
protection, restoration, validation and export preparation; it is not an isolated
validation measurement. The cumulative container peak reached 5549846528 bytes
through the entire staged matrix, rather than an independent peak for each row.

### Failed and unrun stages

The [all-case summary](evidence/cpu-long-context/index-translate/summary.json) and
[unaltered supervisor results](evidence/cpu-long-context/index-translate/results/supervisor.json)
record all **72 planned pairs: 27 complete successes, 3 process-wall
interruptions, 3 generation failures, and 39 unrun pairs**. Each context has nine
complete short/label pairs, one interrupted 304-unit pair and one failed single
unit pair. Later pairs of the failed stage and all higher stages in that family
are unrun. No failed generation was retried.

| Context/output | 304-unit pair1 | Near-1K single-unit cold | Single prompt/output tokens | Accepted long units |
| --- | --- | --- | --- | ---: |
| 1024/256 | wall interruption at 1800.192s | MaxTokens after 36.596s | 768/256 | 0 |
| 2048/512 | wall interruption at 1800.147s | MaxTokens after 72.077s | 1238/512 | 0 |
| 4096/1024 | wall interruption at 1800.185s | MaxTokens after 104.671s | 1238/1024 | 0 |

Interrupted pair stdout is empty because the CLI emits its report only when the
pair returns. Cold/warm completion and validation counts inside those interrupted
pairs are **unknown**, even if elapsed time suggests that cold may have finished.
They are not request timeouts, cooperative cancellation successes, completed
304-unit translations or accepted partial output. The 1,216-unit stage has no
executed pair at any context.

Every long single-unit family failed at its first 5150-byte source unit
(5159-byte JSON artifact); the larger 10250/20500-byte units are unrun. At
4096/1024 the 1238-token prompt fits the context but generation reaches the
1024-token output reservation. This demonstrates insufficient output reservation
under this trial policy, not an input-context rejection. At 2048/512 the same
prompt reaches 512 output tokens; at 1024/256 the two-segment plan fails on its
first 768-token prompt reaching 256 output tokens. No long unit is accepted.
Mechanical validation failures are 0/0 for each of these first-unit generation
failures; generation failures remain a separate category.

The evidence supports comparing a **limited short-label workload** with the five
observed bounds above. It cannot establish general prose limits: a single
20-byte sentence succeeds, while the first measured extended prose unit fails.
Another option is an owner-approved limited experiment with a larger output
reservation, such as context/output 4096/2048. That experiment and any resulting
limit/model adoption are deferred; neither is performed or implied here.

### Cancellation, bounded input and reporting QA

The corrected probe was built with
`CGO_ENABLED=0 go build -o /tmp/yakuori-issue14-cpuspike-final ./tools/cpuspike`.
The staged results above retain the earlier reporting implementation; no failed
natural generation was repeated. New reports call the extra validation pass
`revalidation_ms`, and derive the validated denominator from the failing unit's
position when Core discards previously accepted results. Core's own validation
cost remains part of its combined overhead.

For the deadline check, a temporary fixture directory contained only copies of
`00-short.json` and `02-x4.json`. The same Docker invocation and mounts as the
matrix used the corrected binary, that directory as `--translate-fixtures`, and
`--contexts 1024 --pairs 1 --deadline-probe`, retaining all explicit resource and
time budgets. Each process first requested a 100ms deadline, returned after drain,
and then reused the loaded model with a **fresh** 1500s generation context and
normal 300s request timeout. The [deadline reports](evidence/cpu-long-context/index-translate/deadline/supervisor.json)
show both processes returned zero without watchdog interruption.

| Input | Timed request/drain ms | Effective deadline overrun ms | Fresh warm total s | Warm accepted/validated |
| --- | ---: | ---: | ---: | ---: |
| Short sentence | 102.502 | 2.502 | 3.231 | 1/1 |
| 76 labels, largest successful batch | 113.900 | 13.900 | 234.789 | 76/76 |

These are cooperative cancellation measurements, not hard-wall guarantees. The
overrun uses the earlier of the enclosing generation deadline and request
budget. The original matrix predates this field and cannot establish its overrun.

A separate intentional reporting QA used
`translate --model /local/Index-Translate-2B.Q4_K_M.gguf --fixture /inputs/late-failure.json --context-tokens 1024 --max-output-tokens 8 --request-timeout 300s --generation-timeout 1500s`
inside the same 16CPU/16GiB container. Its
[saved input](evidence/cpu-long-context/index-translate/qa/late-unit-input.json)
and [report](evidence/cpu-long-context/index-translate/qa/late-unit-failure.json)
show first-unit Stop followed by second-unit MaxTokens, exit 1, zero accepted
output, and **0 validation failures / 1 validated unit**. This tests the corrected
denominator and is not a performance matrix retry. An input of 2MiB+1 spaces
with output reservation 256 returned exit 1 and
[JSON error](evidence/cpu-long-context/index-translate/qa/oversized.json)
`research fixture exceeds 2 MiB` before model load (`load_ms=0`). The research
reader guard does not establish a product artifact limit.

`CGO_ENABLED=0 go test ./internal/sqliteprobe ./tools/cpuspike -count=1 -v`
passed with the measured-results opt-in enabled, including the existing busy,
operation cancellation and rollback scenarios. The six supervisor tests passed
with `python3 ci/test-long-context-supervisor.py`, including process-wall, RSS,
host-memory-floor interruption and preservation of failures/unrun stages.
`gopls check` on the five changed Go files returned zero errors (one optional
`fmt.Appendf` style suggestion). These checks and actual CLI exit statuses are
captured under `.omo/evidence/issue-14-task2/`; reproducible model outputs and
supervisor observables are the linked JSON artifacts above.
