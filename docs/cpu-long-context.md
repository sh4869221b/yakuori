# Bounded CPU long-context experiment (#3)

The first sections preserve the historical Coder experiment; the final section
records the owner-adopted Index operational limits and their verification.

The historical experiment supplements [the short CPU spike](cpu-inference-spike.md). It uses exactly the
same checksum-pinned official 0.5B GGUF and goinfer v0.20.0. No model choice,
translation-quality claim, production wrapper, or #14 product limit was adopted
by that historical experiment.
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

> Historical baseline: the Index compatibility, natural-translation and adopted-limit
> measurements below were captured on goinfer v0.20.0 / aikit v1.51.1 before #51.
> “No dependency update was needed” records that earlier checkout. The separate
> v0.22.0 comparison is appended below; the existing historical values are retained.

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

### Owner-approved limited output-reservation experiment

After the original matrix, the owner approved task8 on 2026-10-10: repeat only
its existing `single-1024.json` (5159 artifact bytes, one 5150-byte source unit)
at **context4096/output2048**. This authorizes a measurement, not product limits.
The optional supervisor `--max-output-tokens` override preserves the default
quarter reservation and the legacy Coder command. All seven supervisor tests
passed, including default-quarter and explicit-2048 child command assertions.

The exact invocation was:

```sh
docker run --rm --name yakuori-issue14-reserve \
  --cpus=16 --memory=16g --memory-swap=16g \
  -v "$PWD:/src" -v /tmp/yakuori-issue14-cpuspike-final:/probe:ro \
  -v /tmp/yakuori-issue14-reserve-fixtures:/reserve-input:ro \
  -v /home/sh4869/.cache/yakuori/models:/local:ro \
  --entrypoint python3 yakuori-issue14-measure \
  /src/ci/run-long-context.py /probe /local/Index-Translate-2B.Q4_K_M.gguf \
  /src/docs/evidence/cpu-long-context/index-translate/reserve2048/results \
  --translate-fixtures /reserve-input --contexts 4096 --max-output-tokens 2048 \
  --pairs 3 --process-wall-seconds 1800 --request-timeout-seconds 300 \
  --generation-timeout-seconds 1500 --rss-limit-mib 16384 \
  --go-memory-limit-mib 14336
```

The temporary fixture directory contained only a copy of the existing
`single-1024.json`. All other resource/backend conditions match the original
matrix, including GOMAXPROCS16, GOMEMLIMIT14336MiB, ambient GOINFER removal,
16384MiB supervisor RSS limit and 1024MiB host MemAvailable floor. No Go test,
build or database job ran concurrently with inference.

The [additional summary](evidence/cpu-long-context/index-translate/reserve2048/summary.json)
and [raw supervisor results](evidence/cpu-long-context/index-translate/reserve2048/results/supervisor.json)
preserve **one failed pair and two unrun pairs**. Pair1 cold failed with MaxTokens;
warm did not run. Pair2 and pair3 stopped under the approved failure rule. The
supervisor returned nonzero; it did not interrupt the child. Original failures
remain unchanged.

| Same single unit, context4096 | Output1024, original | Output2048, added |
| --- | ---: | ---: |
| Planned segments | 1 | 1 |
| Prompt tokens | 1238 | 1238 |
| Generated tokens / finish | 1024 / MaxTokens | 2048 / MaxTokens |
| Generation seconds | 104.671 | 179.302 |
| Added load seconds | — | 3.194 |
| Added process wall seconds | — | 182.668 |
| Accepted units / validated denominator | 0 / 0 | 0 / 0 |
| Warm run | not run | not run |

The added child Rusage peak was 3262660KiB (sampled peak3264344KiB); container
memory.peak was 4157771776 bytes, cumulative since this container started. These
are observations under a 16GiB experiment frame, not a guaranteed memory bound.

The saved generated text contains repeated Japanese translations of the two
source sentences, but does not form an accepted full translation. The source
has 103 copies of each sentence; the output contains 158 copies of
`村は安全です。` and 157 copies of `警備員が門を守っています。`, ending
`村は安全です。警備`. This is excessive repetition and truncation, rather than
proof that simply increasing the output reservation yields complete long prose.
It remains a synthetic repeated-sentence input and cannot establish general
prose quality. The conditional added 100ms/reuse and SQLite measurements were
**not run**, because no added successful translation or warm batch exists.
No retry, further output increase, other input measurement or product adoption
was performed.

### Owner-approved nonrepeated prose experiment

Task9, authorized after the repeated-sentence failure, uses three original
English samples with distinct coherent sentences: a lantern instruction (50
UTF8 bytes), a bridge-closure and shelter notice (258 bytes), and a harbor
supply-journey narrative (980 bytes). Each is one unit. Full source text is saved
in the [prose fixtures](evidence/cpu-long-context/index-translate/prose/fixtures/02-long.json)
(`00-short.json`, `01-medium.json`, `02-long.json` in the same directory).
No external text, padding or repeated sentences were used.

The unchanged probe was built before measurement with
`CGO_ENABLED=0 go build -o /tmp/yakuori-issue14-cpuspike-task9 ./tools/cpuspike`.
The measurement image was rebuilt from `yakuori-publication-probe:latest` with
Python3 installed, as for task2/task8. The exact measurement invocation was:

```sh
docker run --rm --name yakuori-issue14-prose \
  --cpus=16 --memory=16g --memory-swap=16g \
  -v "$PWD:/src" -v /tmp/yakuori-issue14-cpuspike-task9:/probe:ro \
  -v /home/sh4869/.cache/yakuori/models:/local:ro \
  --entrypoint python3 yakuori-issue14-measure \
  /src/ci/run-long-context.py /probe /local/Index-Translate-2B.Q4_K_M.gguf \
  /src/docs/evidence/cpu-long-context/index-translate/prose/results \
  --translate-fixtures /src/docs/evidence/cpu-long-context/index-translate/prose/fixtures \
  --contexts 4096 --max-output-tokens 2048 --pairs 3 \
  --process-wall-seconds 1800 --request-timeout-seconds 300 \
  --generation-timeout-seconds 1500 --rss-limit-mib 16384 \
  --go-memory-limit-mib 14336
```

GOMAXPROCS16, CPU/int4, greedy, thinking=false, ambient GOINFER removal,
GOMEMLIMIT14336MiB, supervisor RSS16384MiB and host MemAvailable floor1024MiB
match task8. No build, test or database workload ran concurrently with inference.
All [nine pairs](evidence/cpu-long-context/index-translate/prose/results/supervisor.json)
completed: **9 successful, 0 failed, 0 unrun**. Every cold and warm run ended
Stop and accepted one unit, with mechanical validation failures0/denominator1.
The [summary](evidence/cpu-long-context/index-translate/prose/summary.json)
retains all individual timings and observables.

| Source / JSON bytes | Completed pairs | Prompt/output tokens per run | Cold including load s | Warm s | Maximum child RSS KiB |
| --- | ---: | --- | --- | --- | ---: |
| 50 / 55 | 3/3 | 114/11 | 6.550–6.858 | 3.449–3.573 | 3601612 |
| 258 / 263 | 3/3 | 157/68 | 10.109–10.275 | 6.848–6.937 | 3551872 |
| 980 / 985 | 3/3 | 301/250 | 22.064–22.370 | 18.971–19.489 | 3602200 |

All inputs planned one segment. Maximum individual request time was19.487845s;
maximum load was3.401972s. Maximum sampled RSS was3604480KiB, and container
memory.peak reached4844163072 bytes cumulatively through these nine pairs. This
is not a per-case independent container peak or a guaranteed product footprint.

Every saved successful translation was read and compared; the six outputs per
stage are identical. The short instruction preserves meaning, although lantern
becomes the broader `灯り`. The medium notice preserves bridge closure, shelter,
soup/blankets, the scout's inspection and free accommodation. The long translation
represents all eleven narrative sentences, including supplies, safety advice,
the carpenter and the sister's letter. No omitted event, repeated passage,
untranslated clause or added explanation was observed in these samples.
However, the long output contains grammatical defects (`物資を船で送し`,
`水路をクルーを案内`), changes “stayed behind” to passive `残された`, and renders
age-unspecified “sister” as `姉`. Mechanical acceptance does not establish
publication quality or correctness on other prose.

Only the largest successful input was then copied to
`/tmp/yakuori-issue14-prose-deadline/02-long.json` for one additional pair. The
same Docker command used name `yakuori-issue14-prose-deadline`, an extra
`-v /tmp/yakuori-issue14-prose-deadline:/deadline-input:ro`, output directory
`.../prose/deadline`, and
`--translate-fixtures /deadline-input --pairs 1 --deadline-probe` in place of the
matrix fixture directory/pair count. All other flags stayed identical.
Its [report](evidence/cpu-long-context/index-translate/prose/deadline/4096-02-long-pair1.json)
records request/drain105.824965ms for a100ms request, effective deadline
overrun5.824664ms, and zero accepted partial units. After drain returned, a fresh
generation context on the same model completed warm reuse in19.032822s with
Stop, accepted1 and validation0/1. Supervisor exit0 and no watchdog interruption
confirm cooperative cancellation and reuse for this sample.

The measured prose profile is **985 artifact bytes / 1 unit / 980 maximum unit
bytes / 980 total text bytes / 1 segment**, at context4096/output2048 and the
trial request300s/generation1500s/pair1800s. It adds a successful nonrepeated
single-prose observation to the earlier short-label profile
**1495 artifact bytes / 76 units / 19 maximum unit bytes / 1036 total text bytes /
76 segments**. These profiles can inform a conservative owner-selected subset
of measured workloads. A rectangular combination such as76 units each980 bytes
was not measured, and a cap assembled from every largest field does not acquire
coverage automatically. The earlier 5150-byte repeated prose failures and label
quality reservations remain valid. No input caps, context/output policy or time
budgets are adopted by this experiment; the owner must select their scope and
margin. Full CI was not repeated because probe and production code are unchanged.

## Adopted operational limits (#14)

The owner selected candidate A on 2026-10-10. `config.DefaultLimits()` now supplies
these defaults to the product Core, text Adapter and publication entrypoints;
existing explicit finite research constructors retain historical experiments.
This table is the adopted contract, superseding earlier sections' deferred
adoption statements for current defaults while retaining those trial results.

| Field | Adopted value | Measurement basis and practical limit |
| --- | ---: | --- |
| Artifact UTF8 bytes | 1495 | Largest successful 76-label JSON artifact; source binary952B is a different artifact |
| Units per artifact | 76 | Largest completed label batch |
| UTF8 bytes per unit | 980 | Largest successful nonrepeated prose unit, one unit per artifact |
| Total source text UTF8 bytes | 1036 | Largest completed label batch |
| Segments per artifact | 76 | Largest completed label batch; includes pieces needing no generation |
| Context / reserved output tokens | 4096 / 2048 | Index greedy natural translation; successful prose prompt301/output250 |
| Request budget | 30s | Longer than measured prose request19.488s; cooperative through drain/decode/classification |
| Whole generation budget | 300s | Core.Generate entrance through all planning, units and validation; label warm about235s |
| DB operation / cleanup budget | 1s / 1s | Existing SQLite research probe measurements on tmpfs; real TM implementation remains #16 |
| Retry | 0 | Existing greedy generation contract |

The input limits apply together. The largest per-field observations came from
different workloads: 76 short labels and one980B prose unit. Every combination
inside this rectangle has not been measured. In particular76 units each980B
would exceed total-text1036B anyway; other mixed combinations can satisfy the
caps but still lack performance or quality observations. These are admission
limits with selected margin, not a performance, translation-quality or memory
guarantee. The previous repetitive5150B failures and label/prose quality
reservations remain applicable.

Publication snapshots check source size before retention and read at most the
artifact limit plus one byte; growth during reading is rejected. Core.Text checks
caller-supplied bytes before another copy/import, but cannot prevent the caller's
allocation. The text Adapter checks its one unit before additional retention.
A generic Adapter may already allocate inside Import: Core checks its returned
session before protection/request/generation, not before that internal allocation.
Future format Adapters must enforce the same dimensions before their own added
retention. All units are protected/planned and the aggregate segment cap checked
before the first generation request; there is no accepted partial result on
failure. Source admission is not an output-observation cap.

The generation child context is used only within Generate. Request contexts use
the earlier of their parent and30s policy, including drain; cancellation can
overrun cooperatively, and a backend ignoring context has no hard-wall guarantee.
Finalization.commit creates its own1s child from the caller context, rather than
reusing an expired generation context, and includes connection acquisition in
the supplied TM operation. A cancellation/failure prevents subsequent publication
or output writing. The generic TM interface does not implement rollback; SQLite
busy handling and rollback/cleanup belong to #16. The1s cleanup field is reserved
for that implementation, rather than a fictitious rollback in Core.

### Final real CPU verification of adopted defaults

`cpuspike translate --operational-limits` is a research-only verification switch.
It uses DefaultLimits for bounded source reading, unit/text admission before model
Open, measurement preplanning and the real segmented Core. Context/output/request/
generation flags may be omitted; they become4096/2048/30s/300s. Explicit conflicting
values fail with JSON and nonzero exit. Historical commands without this switch
continue to use their explicit research limits. This does not add a production
configuration loader or CLI settings surface.

The Generate-only probe has no TM lookup, commit, export or publication. Its
success/rejection evidence therefore establishes generation admission and
cancellation, while product tests establish the no-commit/no-publish boundary.

For final CPU QA, the existing76-label and980B prose fixtures were copied to a
temporary input directory as `00-label.json` and `01-prose.json`. A temporary
executable wrapper contains exactly:

```sh
#!/bin/sh
exec /probe "$@" --operational-limits
```

The finite supervisor invocation is:

```sh
docker run --rm --name yakuori-issue14-operational \
  --cpus=16 --memory=16g --memory-swap=16g \
  -v "$PWD:/src" -v /tmp/yakuori-issue14-operational:/probe:ro \
  -v /tmp/yakuori-issue14-operational-wrapper:/operational:ro \
  -v /tmp/yakuori-issue14-operational-fixtures:/inputs:ro \
  -v /home/sh4869/.cache/yakuori/models:/local:ro \
  --entrypoint python3 yakuori-issue14-measure \
  /src/ci/run-long-context.py /operational /local/Index-Translate-2B.Q4_K_M.gguf \
  /src/docs/evidence/cpu-long-context/index-translate/operational/results \
  --translate-fixtures /inputs --contexts 4096 --max-output-tokens 2048 --pairs 1 \
  --process-wall-seconds 700 --request-timeout-seconds 30 \
  --generation-timeout-seconds 300 --rss-limit-mib 16384 \
  --go-memory-limit-mib 14336
```

GOMAXPROCS16, CPU/int4/greedy/thinking=false, ambient GOINFER removal,
GOMEMLIMIT14336MiB and host MemAvailable floor1024MiB remain fixed. The700s
supervisor wall covers two300s cooperative runs plus load; it is an experimental
process bound, not a product setting. No build/test/database job runs alongside
inference. `revalidation_ms` still measures the additional research check;
Core validation remains part of combined overhead. Container memory.peak remains
cumulative over the two pairs, not an independent peak for each input.

The [final adopted-policy reports](evidence/cpu-long-context/index-translate/operational/results/supervisor.json)
show both pairs completed with exit0 and no watchdog interruption:

| Input | Cold generation run s | Load s | Warm run s | Accepted / validation failures / denominator |
| --- | ---: | ---: | ---: | --- |
| 76 labels, artifact1495B/text1036B/76 segments | 234.733 | 3.305 | 235.476 | 76 / 0 / 76 in both runs |
| Prose, artifact985B/unit980B/1 segment | 19.059 | 3.130 | 19.077 | 1 / 0 / 1 in both runs |

Every request reports adopted policy30000ms. Label maximum request was3.314s;
prose prompt301/output250 ended Stop. Child/sample peak RSS for labels was
3584508KiB, with cumulative container peak4621688832 bytes; prose child peak
was3401684KiB. All final cold/warm outputs match the previously inspected label
and prose outputs exactly, including their documented quality limitations. The
selected30s request budget exceeds the prior observed prose request19.488s, and
the300s generation budget exceeds this final label warm run235.476s. This
comparison does not guarantee completion for every admitted mixed workload.

For cancellation, only the prose fixture was copied to a temporary deadline
input directory. The command above used container name
`yakuori-issue14-operational-deadline`, that directory mounted at `/inputs`, output
`.../operational/deadline`, and `--deadline-probe` added. In operational mode this
sets a100ms **caller context**, retaining the adopted30s request policy and300s
normal generation budget. The
[actual report](evidence/cpu-long-context/index-translate/operational/deadline/4096-01-prose-pair1.json)
records `caller_deadline_100ms`: whole run107.110163ms, request/drain105.906626ms,
effective caller deadline overrun7.153624ms, policy30000ms and accepted0.
The overrun is measured against the earlier caller deadline, which starts before
preplanning; it is not request-duration minus30s. After cancellation/drain returned,
a fresh context reused the same loaded model: warm19.043476s, Stop, accepted1,
validation0/1, same inspected prose output. Supervisor exit0/no kill records
cooperative cancellation and reuse, rather than forced interruption.

A1496-byte file (1496 spaces) was passed to the actual built CLI:

```sh
/tmp/yakuori-issue14-operational translate --operational-limits \
  --model missing-model --fixture /tmp/yakuori-issue14-operational-qa/1496.json
```

It returned exit1 and [JSON](evidence/cpu-long-context/index-translate/operational/qa/oversized.json)
`artifact bytes: 1496 exceeds 1495`, load_ms0 and no runs. Adding
`--request-timeout 300s` returned a
[conflict error](evidence/cpu-long-context/index-translate/operational/qa/conflict.json)
before load. A focused test also covers per-unit981B rejection before model Open;
product boundary tests cover the other adopted dimensions and no-commit/no-publish
contracts. The [summary](evidence/cpu-long-context/index-translate/operational/summary.json)
retains input dimensions, policy and individual observations.

Focused `CGO_ENABLED=0 go test ./tools/cpuspike ./internal/config ./internal/localize ./internal/segment`
passed after the research switch edit, and `gopls check` on the three changed Go
files reported zero errors. The final full `ci/verify.sh` passed in the existing
official Go1.27.1/linuxamd64 disposable base image, network disabled, read-only
module/source mounts, no C compiler or SQLite headers, and a fresh source copy.
The qualified run used user1000:1000 and a Btrfs bind mount as TMPDIR, so
publication success, permission-denial and recovery scenarios actually ran.
CGO0 build/test/vet and arm64 CLI/SQLite test cross-build completed.

The initial qualified run as root failed the tests that explicitly require a
non-root user; that failed log is retained. Re-running the same final source with
the required non-root identity passed. The extra verbose qualified publication
run skipped only `TestUnsupportedFilesystem` because the filesystem qualified;
that exact refusal test was then run under ordinary container `/tmp` and passed.
Ordinary unqualified-TMPDIR CI alone would skip qualified success scenarios and
is not used as their proof. The exact disposable-container command and all
results are captured in `.omo/evidence/issue-14-task6/ci-qualified-command.txt`,
`verify-qualified.log` and `ci-qualified-result.txt` (`exit=0`); initial failure is
`verify.log`. The final source was unchanged between these CI checks and commit.

## goinfer v0.22.0 CPU regression (#51, 2026-10-10)

This is a controlled comparison of goinfer v0.20.0 / aikit v1.51.1 against
goinfer v0.22.0 / aikit v1.57.0 using the same local
Index-Translate-2B.Q4_K_M.gguf, Ryzen 7 7700X host, and unchanged code-level
operational limits. The model metadata declares 262,144 context tokens. This
comparison sets a total context of 4096 tokens and reserves 2048 for output,
leaving a 2048-token prompt budget.

The four copied inputs were the [19 English labels](evidence/cpu-long-context/index-translate/fixtures/01-fixture.json)
and the [short](evidence/cpu-long-context/index-translate/prose/fixtures/00-short.json),
[medium](evidence/cpu-long-context/index-translate/prose/fixtures/01-medium.json),
and [long prose](evidence/cpu-long-context/index-translate/prose/fixtures/02-long.json)
fixtures. Each version ran three sequential fresh-process pairs per fixture. A pair
contains one cold pass (model load plus first generation) and one warm pass; there
was no page-cache flush. Both images used Linux amd64, Go 1.27.1, CGO=0, 16 CPUs,
Docker memory and combined memory-plus-swap limits of 16 GiB each (no extra
swap), no network, GOMAXPROCS=16, GOMEMLIMIT=14336 MiB,
and removed ambient GOINFER_* tuning. The generation policy was greedy, seed 0,
thinking disabled; request timeout 300s, generation timeout 1500s, process wall
1800s, RSS limit 16384 MiB, and host-available floor 1024 MiB. Inputs and model
were read-only mounts.

The measurement invocation, with TASK_INPUTS, MODEL_CACHE and RESULTS pointing to
the same retained inputs, local model directory and empty output directory for
each run, was:

```sh
docker run --rm --network none --cpus=16 --memory=16g --memory-swap=16g \
  -v "$TASK_INPUTS:/inputs:ro" -v "$RESULTS:/evidence" \
  -v "$MODEL_CACHE:/local:ro" --entrypoint python3 "$IMAGE" \
  /src/ci/run-long-context.py /cpuspike \
  /local/Index-Translate-2B.Q4_K_M.gguf /evidence \
  --translate-fixtures /inputs --contexts 4096 --max-output-tokens 2048 \
  --pairs 3 --process-wall-seconds 1800 --request-timeout-seconds 300 \
  --generation-timeout-seconds 1500 --rss-limit-mib 16384 \
  --host-available-min-mib 1024 --go-memory-limit-mib 14336
```

The recorded old/new image names were yakuori-issue51-old and
yakuori-issue51-new, with output directories E/old and E/new respectively.
The original invocations and mount paths are in
`.omo/evidence/issue-51-goinfer-release-update/task-2-runtime.txt` and
`.omo/evidence/issue-51-goinfer-release-update/task-6-runtime.txt`. The permanent
[v0.20.0 raw reports and empty stderr files](evidence/cpu-long-context/goinfer-v0.22.0/old/)
and [v0.20.0 supervisor](evidence/cpu-long-context/goinfer-v0.22.0/old/supervisor.json)
are paired with the [v0.22.0 raw reports and empty stderr files](evidence/cpu-long-context/goinfer-v0.22.0/new/)
and [v0.22.0 supervisor](evidence/cpu-long-context/goinfer-v0.22.0/new/supervisor.json).

Each version completed 12/12 pairs, 24/24 cold/warm passes, and 132/132 requests.
All requests ended Stop; each 19-label pass was accepted at 19/19 with zero
validation failures, and each one-unit prose pass was accepted at 1/1 with zero
validation failures. In fixture, pair, cold/warm, and unit order, the 132
request texts and 24 ordered output arrays matched exactly. Rendered prompts,
source spans, token IDs, effective policy and StopIDs, finish, token counts, model
identity, template source/family/schema, and output text were equal. The only
request-identity differences were the expected goinfer backend pin and template
renderer version (132 occurrences each). All 12 supervisor cases per version
returned zero with no watchdog kill, and all 12 stderr files per version were
empty.

| Fixture | Cold load + run, ms (v0.20.0 → v0.22.0) | Warm run, ms (v0.20.0 → v0.22.0) | Per-process report peak RSS, KiB (v0.20.0 → v0.22.0) | Supervisor sampled peak RSS, KiB (v0.20.0 → v0.22.0) |
| --- | ---: | ---: | ---: | ---: |
| 19 labels | 61636.255–61727.898 → 62364.058–62481.620 | 58369.334–58535.023 → 58618.330–58997.552 | 3376736–3619720 → 3606164–3781160 | 3377732–3619720 → 3606164–3781160 |
| Short prose | 6553.427–6688.357 → 6628.030–6846.421 | 3491.309–3508.504 → 3469.237–3553.960 | 3538732–3686896 → 3346288–3763856 | 3539844–3688472 → 3346876–3764452 |
| Medium prose | 9941.889–10162.293 → 10022.921–10183.526 | 6843.327–6874.848 → 6846.770–6891.641 | 3297848–3682076 → 3172756–3610992 | 3297848–3682076 → 3172756–3611696 |
| Long prose | 22041.801–22515.142 → 22284.528–22430.689 | 18944.635–19093.199 → 19034.904–19195.187 | 3292344–3574544 → 3466144–3583468 | 3292344–3574544 → 3466144–3583468 |

Cold is load_ms plus the first run's total_ms; warm is the second run's total_ms.
The report RSS is per child process; supervisor sampled RSS is a separate process
observation, not cumulative container memory. These are three-run ranges under
one host and fixed conditions, not a speed threshold or causal performance claim.
The exact-equality result is limited to these fixtures and model. The 19-label
quality caveats recorded above—including untranslated words and awkward
transliterations such as Cast Aard, Cast Axii, and Sheathe Auto—remain; output
equality is not translation-quality approval.

There were no failed, killed, or unrun measurement pairs among the 12 per
version. This comparison does not establish arm64 runtime, hard cancellation,
natural 32K translation, GPU behavior, or performance/quality invariance for other
inputs or models. The release's wrapper-smoke outcomes and their captured logs are
in the [CPU release update](cpu-inference-spike.md#current-cpu-release-update-51-2026-10-10).
