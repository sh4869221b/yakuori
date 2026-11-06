# PLaMo 3 Translate integration

Built with PLaMo.

2026-11-06: Yakuori can register the original BF16
[pfnet/plamo-3-translate-0.8b checkpoint](https://huggingface.co/pfnet/plamo-3-translate-0.8b/tree/cc8e88169807d5a272e8ff06db12eb0129b67f81)
and translate through its existing Core and SQLite translation memory.
The model is quantized to int4 by the GoInfer loader at load time. CPU inference
has been exercised on linux/amd64. GPU inference and real game translation quality
have not been validated by this integration.

## Get and select the model

Read the [full PLaMo Community License Agreement](../licenses/plamo-community/LICENSE)
before acquiring or using the model. It covers model materials and outputs.
Article 3 requires prior PFN registration for commercial use and limits that
permission to users/affiliated companies with annual revenue or sales not exceeding
one billion yen; otherwise a separate PFN commercial license is needed.
Articles 4–5 specify attribution, agreement delivery, model naming and output
identification when distributing materials or outputs. This is a summary of the
pinned agreement, not a replacement for it. The runtime's MIT license does not
relicense the model or its outputs.

Download explicitly with Hugging Face's CLI, outside normal Yakuori execution:

```sh
hf download pfnet/plamo-3-translate-0.8b \
  --revision cc8e88169807d5a272e8ff06db12eb0129b67f81 \
  --local-dir ./models/plamo-3-translate-0.8b
CGO_ENABLED=0 GOTOOLCHAIN=local go build -o yakuori ./cmd/yakuori
./yakuori models import --id plamo3-08b ./models/plamo-3-translate-0.8b
./yakuori models use plamo3-08b
./yakuori models verify plamo3-08b
./yakuori doctor --check-model --backend cpu
```

The build uses Go 1.27.3. Import needs these regular files in the directory:
`config.json`, `generation_config.json`, `tokenizer_config.json`, `tokenizer.jsonl`,
`chat_template.jinja`, `model.safetensors`, and `LICENSE/en`. Symlinks are rejected;
use a local-directory download, not symlinks into a shared cache. No Python model
code is executed. Yakuori does not download these files automatically, and
`models pull` remains the existing single-file acquisition command.
The downloaded checkpoint was public and usable here without authentication;
future distribution/access requirements are controlled by PFN and Hugging Face.

The supported packaging is one original, unquantized `model.safetensors` below
8 GiB. PLaMo 2, PLaMo GGUF, shards and prequantized FP8/W4/NVFP4 formats have not
been enabled. Only the named 0.8B revision has real-model validation.
Import validates through the CPU engine before registration and never selects or
overwrites an ID automatically.

## Translate

Create UTF-8 source files, then run:

```sh
./yakuori translate --source-language en --target-language ja \
  --game sample --artifact meeting-en --backend cpu ./meeting-en.txt
./yakuori translate --source-language ja --target-language en \
  --game sample --artifact meeting-ja --backend cpu ./meeting-ja.txt
```

`English` and `Japanese` are also accepted and rendered as `en` and `ja`.
Other lowercase language codes accepted by the renderer do not constitute a
translation-quality guarantee. Source/glossary text containing `<|plamo:` is
rejected, following the official template's reserved-marker rule.
The wrapper renders native PFML translation/glossary fields, preserves source
whitespace, and encodes once with one BOS. There is no general chat system message.
Existing Core protection, output validation and exact source/profile TM matching
remain in effect. No generation retry or backend fallback was added.

`localize --format text` and `localize --format w3strings` use the same selected
engine. Their existing adapter and publication restrictions still apply, including
`w3strings` source `en|ja` and target `ja`. Real PLaMo file publication was not
validated here: this cloud workspace is overlayfs, which the existing safe
publisher rejects before generation. Use a supported ext4/Btrfs output filesystem
and perform that acceptance test separately. The tested `translate` path writes
to stdout and commits SQLite TM successfully on this environment.

## Model identity and storage

The GoInfer root and `backend/cuda` modules are replaced by
`sh4869221b/goinfer` at immutable commit
`994cbf5ee38dd8dcc612aad6e6803ac79f772f3b`
(`v0.0.0-20261106011512-994cbf5ee38d`). CUDA moved from the old nested `/cuda`
module to `/backend/cuda` at this pin.

A directory import makes a deterministic `YAKPLM3\n` + uncompressed USTAR artifact
containing only the seven required files, in fixed order. The registry SHA-256
covers weights, tokenizer, config, template and license together. Schema 1 and
existing GGUF raw-file digests are preserved. For schema/path compatibility the
managed artifact is still named `model.gguf`; its content magic selects the
bundle loader. It is not a GGUF conversion. Do not rename or edit managed files.

Loading copies and hashes a private artifact snapshot, checks its registered hash,
and unpacks into one private directory for both model and tokenizer. Files are
read-only and unlinked after loading; DirectLoad disables external GIW caching.
The loader requires model type `plamo3`, the pinned license digest and the exact
reviewed official template digest. Unsupported templates fail closed.
Source, managed bundle, temporary archive snapshot and extracted checkpoint each
need roughly 1.8 GB of disk space when simultaneously present, plus model memory.
Temporary files are removed after load or ordinary error.

The PFML template and prompt identity participate in the canonical profile, as do
model/tokenizer digest and backend pins. Existing GGUF models retain their loading
and prompt paths. Updating the global decoder pin changes canonical profile keys,
so TM entries made with the old decoder are not reused as if provenance were
identical. No TM migration occurs.

| Pinned material | SHA-256 |
|---|---|
| Original `model.safetensors`, 1,780,709,128 bytes | `7c9f0066d166c075bdbe83ada3d444e08bd7a6ee3f345f47a1f3c442aa6898c5` |
| Official `chat_template.jinja` | `cd49e7e0dfa8ec847c163db88dfca89b280dddbb73ddc60ba38c62a420eaced2` |
| English Community License | `d9979039b8f1a96fa81e1be61c4550d06f8542b25119e3f765c7c011a68597fb` |
| Registered bundle, 1,784,258,568 bytes | `4424589269e3d738abb3984dd37183280013b783fcd96f01ce62eebe96bdef13` |

## Validation record

The following outputs are generated by PLaMo 3 Translate 0.8B, CPU int4,
fixed greedy policy; they are synthetic smoke examples, not a quality benchmark:

| Direction | Source | Model output |
|---|---|---|
| en → ja | The meeting has been moved to next Monday. | 会議は来週の月曜日に変更になりました。 |
| ja → en | 会議は来週の月曜日に変更されました。 | The meeting has been moved to next Monday. |

The tagged test imports and selects the real model, parses `translate` arguments,
opens the production engine, constructs its canonical profile and executes Core
with the real SQLite TM. Each direction runs twice: the first has one Generate
call and accepted kind `generated`; the second has zero Generate calls and
accepted kind `tm`. Both commit TM and produce the same expected bytes.

```sh
YAKUORI_PLAMO_MODEL_DIR=/absolute/path/to/plamo-3-translate-0.8b \
CGO_ENABLED=0 GOTOOLCHAIN=local \
go test -count=1 -tags cpusmoke ./internal/cli -run '^TestPlamoCPUCLI$' -v
```

Model-free tests cover bundle ownership/determinism, unsafe source/entry rejection,
snapshot mutation isolation, hash and template rejection, cleanup, PFML rendering,
marker injection, glossary and canonical profiles. Existing real Qwen2.5-Coder
0.5B Q4_K_M CPU smoke tests passed with the new dependency pin. Other existing
models, including Index, have model-free regression coverage only in this run.
`go vet ./...`, `go mod verify`, cgo source/normal-CPU dependency checks, CPU and tagged CUDA
builds, and linux/arm64 cross-build passed. ARM inference was not run.

`go test ./...` has one failure in
`tools/acceptance.TestSupervisorKeepsSampleWhenWorkerEndsDuringQuery`:
`QueryCleanup=unknown`, resource status `error` rather than the expected `partial`.
The same test fails on an isolated, unchanged main checkout at `4a2605a` in this
environment. All other packages passed; the full suite cannot be reported green.

CUDA requires `-tags cuda`, linux/amd64 and the host NVIDIA driver. This environment
has no NVIDIA device or `libcuda.so.1`. The tagged CLI builds, and an explicit
CUDA doctor check fails with CUDA residency/driver failure, without CPU fallback.
Real CUDA inference, RTX 4070 12 GB VRAM fit, throughput and GPU translation parity
remain unverified. The prior GoInfer FP32 reference checks do not establish CUDA
compatibility.

This change adds bundle copy/unpack and integrity-check I/O. TM hits still prepare
the selected model before lookup, so zero Generate calls do not mean zero load
cost. No CPU/GPU speed comparison, peak-memory benchmark, long-document quality
study or retuning of existing context/output limits was performed.

Selected raw test output is retained in [the evidence directory](evidence/plamo3/README.md).
