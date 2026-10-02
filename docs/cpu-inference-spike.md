# CPU GGUF feasibility evidence (#3)

## Result and scope

On 2026-10-02 the **pinned plain CPU path** generated real text, counted the exact
rendered request, and exposed terminal/error behavior that can be mapped
conservatively by a future wrapper. This is a probe, **not** the production Engine,
model registry, translation pipeline, adopted translation model, or quality gate.
No CUDA/GPU package, server, cloud LLM, implicit download or generation retry is used.

The [canonical design](https://chatgpt.com/space/page_3565e1d53fa08191a7d8cb56e84af5a5)
remains authoritative. No product defaults or design changes are introduced.
#12 must implement the production boundary and #14 must choose practical finite
limits from representative measurements. #3 does not prove full-window quality,
real MOD translation, hard real-time cancellation, or arm64 runtime support.

## Reproducible pins and rights

| Component | Pin / source | License |
| --- | --- | --- |
| Go | 1.27.1, archive digest in [foundation](foundation.md) | BSD-style Go license |
| goinfer | `github.com/townsendmerino/goinfer v0.20.0`, tag commit `890ca565f982aa268c7c2f6f461dbf7fa7ad0e6c` | MIT |
| aikit | `github.com/townsendmerino/aikit v1.51.1` | MIT |
| x/text | `golang.org/x/text v0.40.0` | BSD-3-Clause |
| x/sys | `golang.org/x/sys v0.48.0` (integrated SQLite dependency minimum; original isolated probe used v0.47.0) | BSD-3-Clause |
| test model | Qwen's `Qwen2.5-Coder-0.5B-Instruct-GGUF`, revision `ebb2015119c907b064c512bf053e945850b5875f` | Apache-2.0 |

`go.mod` / `go.sum` lock the library and transitive module checksums. The similarly
named `lynxai-team/goinfer` is a proxy using external inference engines, not this
pure-Go library. Upstream remains pre-1.0; re-run this gate for every dependency
change. The terminal mapping below is specific to this pin and non-speculative CPU
Generate, not an assertion about other API paths.

Model file: `qwen2.5-coder-0.5b-instruct-q4_k_m.gguf` (491,400,064 bytes).
SHA-256: `1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32`.
On-disk quantization is **Q4_K_M**; compute option is explicitly **int4** and the
loaded model reports int4. These are distinct settings. It is a small, official,
redistribution-licensed feasibility fixture that fits the test machine, not a
selection of a Japanese translation model. No weights are committed or redistributed.

Primary sources:
- [upstream module and version](https://github.com/townsendmerino/goinfer/blob/v0.20.0/go.mod)
- [upstream MIT license](https://github.com/townsendmerino/goinfer/blob/v0.20.0/LICENSE)
- [upstream model pin](https://github.com/townsendmerino/goinfer/blob/v0.20.0/pull/curated.json)
- [official model card](https://huggingface.co/Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/blob/ebb2015119c907b064c512bf053e945850b5875f/README.md)
- [official model license](https://huggingface.co/Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/blob/ebb2015119c907b064c512bf053e945850b5875f/LICENSE)
- [pinned model download](https://huggingface.co/Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/resolve/ebb2015119c907b064c512bf053e945850b5875f/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf)

Future distribution must retain applicable copyright/license notices, including
Apache NOTICE obligations if applicable. This repository only carries probe code,
synthetic prompt text, token IDs, and measured output.

## Reproduce

Use the verified Go installation first in PATH, never an unrelated `go` executable.
The model must already exist locally for ordinary probe execution:

```sh
export CGO_ENABLED=0 GOTOOLCHAIN=local
# Set writable GOPATH/GOCACHE if the execution environment requires them.
go mod download
go mod verify
sh ci/verify.sh
go build -o bin/cpuspike ./tools/cpuspike
GOOS=linux GOARCH=arm64 go build -o bin/cpuspike-arm64 ./tools/cpuspike
./bin/cpuspike /absolute/path/to/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf
```

Missing/wrong-size/wrong-digest model is a failure, not a skip. The executable has
no acquisition implementation. The model source and checksum above enable an
explicit manual download. The stronger clean-environment gate performs that
**explicit fixture acquisition during image build**, then denies runtime network:

```sh
docker build --progress=plain -f ci/cpu-spike.Dockerfile -t yakuori-cpu-spike .
docker run --rm --network none --memory 4g --cpus 2 yakuori-cpu-spike
```

The container checks absence of cc/gcc/g++/c++/clang/clang++/cmake before compilation;
all builds and tests have CGO=0. The Docker CI runs the exact required model; it
cannot report success by skipping model-dependent assertions. Ordinary `go test`
only runs deterministic classification/context/fixture-gate unit tests and must
not be reported as evidence of real inference. The dedicated workflow supplies
that evidence separately. It uses a 20-minute job limit and each normal probe
request a 2-minute deadline; these are experimental safeguards, not MVP defaults.
The model fixture can be removed from local disk after the probe. Removing the
Docker image removes its copy; no user model registry or TM state was created.

## Observations and acceptance boundaries

The committed [local report](evidence/cpu-spike-linux-amd64.json) is one execution
on Linux amd64, Go 1.27.1, CPU, no swap, roughly 9.7 GiB host RAM. Python's child
resource measurement reported peak RSS **970,564 KiB** and total wall time **2.696 s**;
load was 1,367 ms. These are single-run feasibility observations, not a benchmark,
performance target, or an arm64 result. Clean-container results are independently
visible in the corresponding PR's `CPU inference spike` exact-head CI logs.

| Probe | Observed local result | Interpretation |
| --- | --- | --- |
| Natural greedy generation | `Hello.`, 2 emitted tokens, nil error, 190 ms | genuine CPU generation; Stop |
| Exact request tokens | 26 IDs, frozen golden in harness/report | Count and Generate use identical IDs; no second template/BOS |
| Controlled stop | 1 token, nil error, 169 ms | extra StopID terminates without emitting stop token |
| Max output 1 | 1 token, nil error, 164 ms | MaxTokens, never accepted as Stop |
| Cancel after first token | 1 token + `context canceled`, 170 ms | partial candidate rejected; drain to closure |
| 1 ms deadline during prefill | zero tokens + deadline error, 8 ms | cooperative timeout, not a hard 1 ms wall limit |
| Healthy request after interrupted streams | Stop, 1 token, 166 ms | drained interruption did not poison next request |

Controlled cases force logits **after real CPU forward passes**, using a synthetic
processor that emits a known non-stop token or the template stop token. These
validate backend control flow, not translation quality. The natural case uses
unmodified greedy logits. Plain CPU, int4, unquantized default CPU KV, no LoRA,
no speculation, no recurrent session, no logit bias/penalties are in scope.

### Exact counting and context

The model tokenizer loads from the same checksum-verified GGUF. Declined/unknown
pretokenizers or templates fail. A fixed explicit system prompt avoids upstream
ChatML's documented no-system/default-system difference. Render once; encode with
`addBOS=false`; freeze the token IDs; count their length and pass that same slice to
Generate. The model-required probe compares all 26 IDs to a frozen golden, not a
character estimate. This fixture contains no untrusted control-marker text. #12
must use the template's segmented encoding API (or an equivalent validated policy)
for literal marker strings in arbitrary input; do not generalize this simple
fixture's raw Encode call to user content without those tests.

Loaded `Config().MaxPositions` is **32,768** for this GGUF. The CPU model's RoPE
architecture is not a separate smaller resident GPU cap. The probe verifies this
loaded value and rejects `prompt + output > limit` before calling the backend,
with overflow-safe subtraction; boundary cases 32,767+1 and 32,768+1 are tested.
**This verifies metadata and admission arithmetic, not a successful 32K prefill.**
No full-window performance, memory ceiling, or long-context correctness claim is
made. The [plain CPU implementation](https://github.com/townsendmerino/goinfer/blob/v0.20.0/decoder/model.go)
and [prefill](https://github.com/townsendmerino/goinfer/blob/v0.20.0/decoder/forwardn.go)
do not provide an application admission gate enforcing that sum; the future wrapper
must enforce it itself and must not infer a supported window from success on a
short prompt. #14 remains the gate for a measured smaller effective operational
cap and finite input/unit/segment/time bounds. Full-window execution remains **not run**.

### Terminal mapping and cancellation

Upstream `Generation.Err()` conflates EOS, caller StopID, and max output when nil;
there is no explicit universal Finish enum. Stop tokens are suppressed. For this
pinned plain CPU loop the safe mapping is:

1. Drain stream, then inspect backend error **and** the request context.
2. Deadline/cancellation always rejects, including nil backend error races.
3. Any other backend or decode error rejects, even with partial output.
4. Any budget clamp rejects as ContextLimit. Unexpected budget/count is InvalidOutput.
5. Emitted count equal to requested maximum is MaxTokens. Only fewer tokens with
   nil errors and unchanged budget can mean Stop on this audited path.
6. Stop is only a generation gate; an empty/invalid/structurally wrong translation
   still needs rejection by content validation. No partial text is published.

Do not use this mapping for speculative/VL/session/GPU paths without a separate
audit. Unit tests include unknown budget, zero clamp, excess output, partial error,
and cancel/deadline races. Cancellation is cooperative at upstream check points;
a forward pass may finish after the deadline. Callers must cancel **and drain**
before model reuse/Close. `decoder.Load` has no context argument: this spike does
not claim cancellable loading. A strict wall-clock kill requirement would need
process isolation or an upstream API change; it is not silently assumed here.

## Remaining gates / decision points

- CPU short-prompt feasibility: passed locally; clean CI status is reported per PR.
- Model context metadata and fail-closed budget gate: passed. Full-window runtime:
  **not run**; no unmeasured cap adopted. If #3 is interpreted as requiring successful
  inference at the entire advertised 32K window, keep that checklist item open until
  a separately budgeted long-context experiment succeeds.
- Linux arm64: compile only. No runtime/ISA or speed claim.
- Non-cancellation partial backend fault: classification unit fixture only; real
  partial cancellation+error is exercised. No claim of injected native decoder fault.
- No production translation acceptance, Japanese quality selection, prompt schema,
  sampling policy/default max output, or startup deadline decided in this spike.
- If actual long-context quality or cooperative cancellation is insufficient, options
  are a measured smaller supported cap, a context-aware upstream/fork improvement,
  or explicit process isolation. All preserve Go/CGO=0 in principle; switching to a
  C/C++ backend would change the approved pure-Go requirement and needs a user decision.
