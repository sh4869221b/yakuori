# Go foundation evidence (#2)

## Scope and toolchain

Verified 2026-10-02 against the official [download manifest](https://go.dev/dl/?mode=json)
and [release history](https://go.dev/doc/devel/release). Baseline remains Go 1.27;
CI pins Go **1.27.1**, with `GOTOOLCHAIN=local` (no automatic toolchain switch).

- Archive: https://go.dev/dl/go1.27.1.linux-amd64.tar.gz
- SHA-256: `63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`
- Observed: `go version go1.27.1 linux/amd64`
- `go.mod`: `go 1.27.0`; no third-party Go dependencies or `go.sum` yet.
- `actions/checkout` is commit-pinned in the workflow.
- Clean build base is `debian:bookworm-slim`. OS package updates are not frozen;
  the toolchain archive is checksum-pinned. CI logs identify the resolved image.

The local `/usr/bin/go` was a board-game executable, not a Go compiler. Verification
used the official archive, with its checksum checked before extraction and execution.
Use the Go toolchain's `bin` directory first in `PATH`.

## Reproduce

```sh
export PATH=/path/to/go/bin:$PATH
export CGO_ENABLED=0 GOTOOLCHAIN=local
sh ci/verify.sh
# Strong clean-environment gate (Docker needed):
docker build --progress=plain -f ci/Dockerfile .
```

The Docker gate contains no C/C++ compiler, CMake or SQLite development headers;
it explicitly checks their absence before running the same script. It does not
remove software from the host. No inference libraries, SQLite driver, submodules,
CUDA toolkit, models or game fixtures are used. The only extras are the
test-only `strace` (>= 6.8, which provides `--kill-on-exit`) and `python3`,
needed by the offline observer and the
model-free matrices wired into `ci/verify.sh`. The tests are deterministic
and use generated temporary directories and injected fake path resolvers/writers.

`ci/verify.sh` checks formatting, repository `import "C"` (including excluded files),
its own negative fixture, selected non-standard dependency cgo files for Linux
amd64/arm64 (including tests), CGO=0 build/test/vet, and Linux arm64 cross-build.
The dependency probe uses `CGO_ENABLED=1 go list`, **not** a native compile.
Standard-library optional cgo implementations are excluded from that probe.
Dependency code excluded from both target build graphs is not claimed audited.
The source guard parses Go imports, so comments containing `import "C"` do not fail it.

The arm64 artifact is only cross-built. No arm64 execution compatibility is claimed.
Real CPU inference, model smoke, SQLite and .w3strings tests remain separate gates.
The gate is model-free and additionally runs three offline additions: the
observer self-test `python3 ci/test-offline.py`, the acquisition-failure suite
`YAKUORI_PULL_BINARY="$PWD/yakuori" go test -count=1 -tags modelsmoke
./internal/cli -run '^TestPullCLIFailure$' -v` against the freshly built
`$PWD/yakuori` (served fixture bytes are deliberately invalid, so no model is
needed), and the same binary's model-free CLI/error matrix
(`go test -count=1 -tags offlinesmoke ./internal/cli -run
'^TestOfflineCLIMatrix$/^model-free$' -v`) observed by `ci/offline.py`. None
of the three fetches, places or loads a model. The real-model `modelsmoke` /
`offlinesmoke` suites that need a preplaced GGUF, and the CUDA / driver-free
matrices, are explicitly provisioned local QA runs away from the gate; a green
normal CI never substitutes for them.

### Offline observer and CUDA reproduction

`ci/offline.py` refuses network attempts by default. Its test-only
`--allow-nvidia-mps` option permits only a fully decoded `connect(AF_UNIX)` to
`/run/nvidia-mps/control` or `/tmp/nvidia-mps/control`. Each such record stays
in the raw `.trace` and is listed under JSON `allowed_local`; JSON `attempts`
continues to contain forbidden attempts only. The option does not permit
resolver or proxy connections, INET/INET6, addressed sends, other UNIX
destinations, malformed or incomplete traces, or hidden preinitialization.

The `offlinesmoke` CLI fixture adds the option only when
`YAKUORI_OFFLINE_BACKEND=cuda`; its own record must match the selected policy.
An unset backend remains model-free and strict (`allow_nvidia_mps=false`), and
CPU runs remain strict. Descriptor paths use `-xx -yy` so literal terminal
hyphens and equals signs cannot be confused with annotation arrows.
The observer's target timeout is ten minutes, and its tracer preflight may take
another two, so the Go test timeout is raised above the default ten minutes.
On a Linux/amd64 host with the pinned GGUF already present and an NVIDIA driver,
the CLI matrix can be reproduced with:

```sh
YAKUORI_OFFLINE_MODEL="$MODEL" \
YAKUORI_OFFLINE_BACKEND=cuda \
YAKUORI_PULL_BINARY="$CUDA_BIN" \
YAKUORI_PULL_EVIDENCE="$E/task-7-cli" \
CGO_ENABLED=0 GOTOOLCHAIN=local \
go test -count=1 -timeout 15m -tags cuda,offlinesmoke ./internal/cli \
  -run '^TestOfflineCLIMatrix$/^real-model$' -v
```

This Go fixture automatically passes `--allow-nvidia-mps` to each observed CLI
child for the CUDA backend only. `MODEL` must be the existing
`qwen2.5-coder-0.5b-instruct-q4_k_m.gguf` fixture, revision
`ebb2015119c907b064c512bf053e945850b5875f`, SHA-256
`1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32`, size
491400064 bytes. `CUDA_BIN` is built with `-tags cuda`; this test uses the
preplaced file and does not download a model.

For a directly observed CUDA Core test, pass the option explicitly:

```sh
YAKUORI_OFFLINE_MODEL="$MODEL" \
YAKUORI_OFFLINE_BACKEND=cuda \
python3 ci/offline.py --evidence "$E/task-7-cuda-core" --expect-exit 0 \
  --allow-nvidia-mps -- "$E/localize-offline-cuda.test" \
  -test.run '^TestOfflineCoreMatrix$' -test.v -test.timeout 15m
```

The driver-free refusal is a separate run inside the existing container with
the pinned model available but no NVIDIA driver. Build the cuda-tagged driver
matrix test binary and run its real-load branch with the driver declared absent.
The test rejects a loadable `libcuda.so.1`, requires the cuda-tagged backend pin,
asserts `backend=cuda` returns no engine rather than a not-built-in error, and
loads the same model with `backend=cpu` as the explicit control. Keep the
observer strict, with no MPS option:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local \
go test -c -tags cuda,offlinesmoke -o "$E/goinfer-offline-cuda.test" \
  ./internal/inference/goinfer

YAKUORI_OFFLINE_MODEL="$MODEL" \
YAKUORI_OFFLINE_BACKEND=cuda \
YAKUORI_OFFLINE_CUDA_DRIVER=absent \
python3 ci/offline.py --evidence "$E/task-7-driverfree-cuda" --expect-exit 0 \
  -- "$E/goinfer-offline-cuda.test" \
  -test.run '^TestOfflineCUDAMatrix$/^real_load$' -test.v -test.timeout 15m
```

A timeout, a model failure or any other refusal is not accepted as success.
This refusal checks that CUDA does not fall back to CPU or acquire a driver;
it is not a real-GPU success result. The exception changes only observer
classification for the two control connects. It does not itself establish that
the GPU matrix passed; report the run's actual traces and status separately.

## Current CLI / error contract

- `help`, `-h`, `--help`: exit 0, help on stdout, empty stderr; no config access.
- `doctor`: prints the four resolved directories and concrete config, registry,
  model, TM, snapshot and lock paths with presence/mode status. It stats these
  paths and reads only the small config and registry JSON; it does not hash or
  load models, open TM, write files or use the network. Missing storage is
  normal. A registered model whose file is missing is reported as `[missing]`;
  explicit verification is required to check its bytes. Invalid schema or
  insecure existing permissions or a selected ID absent from the registry return 1.
- `models import --id ID FILE`, `models use ID`, and `models verify ID` manage
  local GGUF files. Standard flags precede positional arguments, so `--id`
  comes before `FILE`. Import makes a private copy and does not select it. IDs
  are immutable; duplicate IDs or destinations are rejected. `verify` compares
  the managed file's SHA-256 and size with the registry. Loading also passes
  the expected SHA-256 to Engine, which checks the actual snapshot bytes.
- `models pull --id ID --sha256 SHA --size BYTES --digest-source SOURCE URL`
  acquires one model over HTTPS. The four flags precede the positional URL,
  which must be absolute HTTPS with a host and no userinfo or fragment.
  Redirects may only point at HTTPS URLs and are followed at most ten times;
  only HTTP 200 is accepted; one pull is bounded by 30 minutes; there is no
  retry and no resume. Usage errors and `--help` return exit 2 before storage
  is resolved, a connection is opened or the notice is written. A one-line
  notice with quoted and escaped expectation values is written to stderr
  before the connection; when that write fails, no connection is opened.
  SIGINT/SIGTERM are mapped onto the pull context only inside this dispatch and
  report as a regular operational error (`pull was cancelled`); the cancelled
  run's stage is removed and previous state is kept. A transfer or expectation
  failure removes this run's stage and reports only a classified reason: raw
  HTTP/TLS errors, response bodies, model and prompt text are never echoed.
  The received bytes' measured size and SHA-256 must match the expectation and
  the same stage must be accepted by the CPU loader and Close cleanly before
  registration; Content-Length is auxiliary and never the acceptance basis.
  Once the registry rename in the commit section happened, later cancellation,
  stdout or lock Close errors keep the registration and say so; a returned
  non-zero entry means registered. A successful pull writes `provenance.json`
  beside the managed model and never selects the acquired ID; the registry
  schema and `verify` byte identity checks are unchanged.
- `doctor --check-model [--backend cpu|cuda]`: requires a selected model, opens
  it, reports `Engine.Info`, and closes it without generation. CPU is the
  default. It uses the existing configured 4096-token context candidate and
  reports the effective Engine values. The private snapshot directory must
  already exist; this command does not create it. Explicit CUDA failure is
  reported without CPU fallback. CUDA requires a Linux/amd64 `-tags cuda` build
  and an NVIDIA runtime driver providing `libcuda.so.1`.
- `translate --source-language LANG --target-language LANG --game GAME
  --artifact ARTIFACT [--backend cpu|cuda] SOURCE` translates a regular UTF-8
  file to stdout. `localize --format text --source-language LANG
  --target-language LANG --game GAME --artifact ARTIFACT [--backend cpu|cuda]
  --output OUT SOURCE` publishes the same supported text format to a file.
  Flags precede `SOURCE`; language, game and artifact values are required and
  non-empty. The CLI does not normalize them or infer namespaces from paths.
  `translate` is text-only and has no `--format`; `localize` requires the
  explicit `--format text`. `SOURCE` is a path, not `-`; stdin is unsupported.
  There is no command-level model override. `models use ID` selects the local
  GGUF used by both commands. CPU is the default backend; CUDA is opt-in and
  requires a Linux/amd64 `-tags cuda` build and NVIDIA driver. Neither command
  downloads a model or falls back from CUDA to CPU.
- Both commands use the selected registry model's real Engine, `Info` and
  request factory, `DefaultLimits`, `PolicySchemaV1`, canonical inference
  profile, SQLite `tm.Store`, text adapter and production Core. The profile
  binds the selected model and effective Engine/request values. Text format
  uses the whole file as one `text-v1` document unit with no keys and an empty
  glossary. It does not discover or automatically protect apparent tags or
  placeholders. Current limits are 1495 artifact bytes, 76 units, 980 bytes
  per unit, 1036 total text bytes, 76 segments, a 4096-token context
  candidate, 2048 maximum output tokens, 30-second request timeout and
  300-second generation timeout. For `text-v1`, the single unit makes 980
  UTF-8 bytes the effective source text limit.
- Translation syntax and help are handled before config, model, TM or source
  I/O. Once execution starts, the selected model is opened and its profile is
  built before the TM. The CLI uses `Create` only when `<Data>/tm.sqlite` is
  absent; an existing database is opened with `Open`. It does not recover,
  replace, or recreate a database after an error. Corrupt databases, SQLite
  sidecars, lock conflicts and other open failures stop the command.
- `localize` runs the existing publication recovery/preflight before it
  snapshots the source. Core prepares and validates every unit and the final
  artifact before its one TM commit, then publishes. A failure before commit
  does not accept TM rows or publish a new artifact; a retained private run,
  stage or record remains evidence and is not automatically deleted. After
  commit, a publication or Close error does not undo committed rows or a
  completed publication. In-place mode retains the original as a backup.
  `translate` commits accepted rows before writing the translated bytes to
  stdout; a writer failure cannot retract bytes already written or committed
  TM rows. `TMCommitted`, publication state and bytes-written state therefore
  remain part of operational error reporting.
- SIGINT and SIGTERM cancel the translation context. Native generation is
  drained before the Engine is closed. A failed `Close` returns a failure
  without erasing already committed TM or publication state. TM and publisher
  `Close` release their owned resources; neither is a general cleanup or
  recovery command.
- Normal translation diagnostics redact source, target and prompt text. The
  measured CUDA driver-initialization refusal is sanitized before the pinned
  upstream loader can print its raw error, while preserving `ErrCUDAResidency`.
  This is a measured refusal-path guarantee only. Later pinned-upstream
  resident, device, allocation, self-test or generation failures may still
  write to stderr. `doctor --check-model --backend cuda` retains its existing
  detailed diagnostic consumer.
- Missing/unknown command, unknown option or extra arguments: exit 2, empty
  stdout, diagnostic on stderr. Raw user arguments are not echoed.
- Operational error: exit 1, diagnostic on stderr. A stdout write failure also
  returns 1; previously written stdout bytes cannot be retracted.
- Diagnostic write failure retains a non-zero status.
- Only `cmd/yakuori/main.go` calls `os.Exit`. `internal/cli.Run` returns a status.
  The unexported resolver, selected-engine and file-runner seams make failures
  testable without public test flags. Controlled Engine failures and fake
  writers are unit controls, not native inference evidence. Real CLI acceptance
  separately checks stdout/output bytes and complete SQLite keys. Diagnostics
  retain unit/phase, TMCommitted, publication state and RecoveryError
  reason/paths. Partial stdout failures report the sent byte count without
  rolling back committed rows.

Model import and explicit `models pull` are the only acquisition paths, and
`pull` is the only network path in the product. `models verify` and Engine
loading cannot read the provenance sidecar: it is traceable storage, never a
trust input, and `digest_source` is the caller's statement of where the digest
came from, not an independently verified distribution guarantee. The
text-v1 CLI does not implement the Witcher 3 production adapter, MOD read/write
or human translation-quality acceptance.

## Issue #19 acceptance evidence, observed 2026-10-17

This dated evidence map records the implementation and measurements available
at Task7. It is not a claim that Issue #19 or its final gate is complete.
Receipts and raw artifacts are under
`.omo/evidence/issue-19-cli-offline/execution-20261017/`.

| Acceptance condition | Observed proof | Scope and limits |
| --- | --- | --- |
| AC1, acquisition source and digest declaration | `verify-3-pull.txt` records six actual CLI failure invocations and 43 checks. `task-5-report.md` records three pull tests RUN/PASS, 43 state checks and 10 CLI invocations, including real pinned-model registration and provenance readback. | The expected SHA-256 and size identify bytes and the declared `digest_source` is stored as provenance. The declaration is not independently authenticated and is not a distribution trust guarantee. |
| AC2, interruption and corrupt/mismatched model state | `task-5-report.md` records event-triggered SIGINT/SIGTERM, unchanged prior model/selection and no stage; SIGKILL leaves an unregistered stage that a later run preserves until explicit QA cleanup. `verify-3-pull.txt` records mismatch, short/long body, status, invalid GGUF and argument failures. | Pull transfer fixtures are synthetic bytes, not substitute inference models. SIGKILL residue is intentionally not auto-recovered or deleted. |
| AC3, real CLI, Engine, TM, CPU/CUDA and driver refusal | `task-5-report.md` records 18 clean-head CPU targets and native token cancellation. `task-6-d1-report.md` and `task-6-d1-readback.json` record CUDA, the CUDA-build CPU control, native cancellation, driver-free refusal and complete SQLite/output readback. | Task6 readback has 56 verified observer targets total, not 56 CUDA targets. Its 18 MPS `connect` records name only the two existing exact paths; other allowed local IPC records are separate. CPU and driver-free observations are strict. No unrestricted-multicore claim: accepted traces use direct strace 7.2 serialized on CPU 0 for CPU and CPU 4 for CUDA. |
| AC4, deterministic pull regression and model-free gate | `verify-4-model-free.txt` records six strict-clean model-free targets and 10/10 state checks. `task-5-report.md` records three pull tests RUN/PASS, 43 state checks and 10 CLI invocations. | These task-level runs passed. Task8's final clean Docker gate and remote final-head CI were not yet run or accepted at this evidence date; their results belong in the root-owned final summary. |
| AC5, reproducibility, pin, results and limitations | `task-7-help.txt` records actual current-source `translate --help` and `localize --help` output and exit 0. Task5/6 reports record Go 1.27.1, CGO=0, model pin, environment, commands and raw evidence. This document records the format and claim limits. | The actual inference runs predate the final clean `897c01a` source metadata: CPU proof is bound to clean `4b5c8e4`; Task6 runtime binaries were built from `4b5c8e4` with the exact D1 source change and report `vcs.modified=true`. The final error-wrapping correction changed only the initialization-refusal return and was rerun for that refusal. The clean `897c01a` metadata build is not another inference run. |

Do not count dispatch-only CLI results, fake-TM Core fixtures or controlled
Engine failures as real translation. Archived invalid tracer runs, including
strace 6.8 parser failures and seccomp-mediated strace 7.2 runs, remain failed
evidence. A tmpfs publication failure is not a successful publication result.
The accepted CPU and CUDA trace conditions are the direct, serialized strace
7.2 runs listed above; no multicore tracing guarantee is made. A skipped GPU
run, an arm64 cross-build, and absent Witcher 3 MOD or human quality review are
not runtime or product-quality proof. Failed and invalid runs remain preserved
in the evidence directory rather than being rewritten as successes.

The Task6 owner decision was to limit the diagnostic guarantee to the measured
CUDA initialization refusal and keep dependency pins. The original 500-byte
unsolicited upstream diagnostic RED is preserved. The repaired refusal emits
the measured 50-byte normal translation diagnostic, exits 1, writes no
stdout, and creates no TM or output; `ErrCUDAResidency` remains classifiable.
This does not promise silence from later pinned-upstream resident or device
failure paths. Explicit doctor diagnostics remain separate.

## Configuration boundary

Following the [XDG Base Directory specification](https://specifications.freedesktop.org/basedir/latest/):

| Purpose | Environment override | Default directory |
| --- | --- | --- |
| Config | `XDG_CONFIG_HOME` | `$HOME/.config/yakuori` |
| Data | `XDG_DATA_HOME` | `$HOME/.local/share/yakuori` |
| Cache | `XDG_CACHE_HOME` | `$HOME/.cache/yakuori` |
| State | `XDG_STATE_HOME` | `$HOME/.local/state/yakuori` |

An absolute override gets `/yakuori` appended. Empty/relative overrides fall back;
a missing/relative HOME errors only when a fallback is necessary. Paths are lexical,
not symlink-resolved and are not a filesystem security boundary. No system-wide
config discovery or fallback to the current directory is performed.

Each absolute XDG override receives `/yakuori`; otherwise the fallback directories
above are used. The current storage paths are resolved without creating them:

| Purpose | Path | Schema or contents |
| --- | --- | --- |
| Selected model | `<Config>/config.json` | `{"schema":1,"selected_model":"index-translate"}` |
| Registry | `<Data>/registry.json` | `{"schema":1,"models":{"index-translate":{"sha256":"<64 lowercase hex>","size_bytes":123}}}` |
| Managed model | `<Data>/models/<ID>/model.gguf` | Private copy for one immutable ID |
| Provenance | `<Data>/models/<ID>/provenance.json` | Schema 1 sidecar recording `source_url`, `final_url`, `expected_sha256`, `expected_size_bytes`, `digest_source` for pulled IDs |
| Registry lock | `<Data>/registry.lock` | Cooperative import/use lock |
| TM | `<Data>/tm.sqlite` | SQLite translation memory |
| Load snapshots | `<Cache>/model-snapshots` | Transient snapshots created during Engine load and unlinked when load completes |
| State | `<State>` | Displayed path; no state file is created |

Model IDs match ASCII `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Managed directories
use mode `0700`; config, registry, lock and managed model files use `0600`.
Provenance sidecars use `0600` as well.
Engine snapshots use `0400` and are transient. Import copies bytes and does not
hardlink or modify the source. Storage requires one additional persistent model
copy, plus a transient snapshot during model loading. While `models pull`
runs, the `.pull-*` acquisition stage holds one more model copy, so the
transient peak during CPU validation is stage plus snapshot on top of the
registered copies. For example, a research
model at `$HOME/.cache/yakuori/models/` remains an external source file even
though it is inside the cache namespace. Do not remove the whole cache to clear
snapshots.

There is no model removal command. To remove one registered ID manually, first
stop all Yakuori processes. Remove `config.json` only if it selects that ID,
remove only `<Data>/models/<ID>/` contents (`model.gguf`, plus
`provenance.json` when the ID was pulled) and its now-empty ID directory, then
remove only that registry entry;
keep the schema 1 registry with an empty `models` object if no entries remain.
This order avoids leaving an unregistered, ambiguous managed directory. If an
interrupted import left a directory without a registry entry, inspect that
directory manually; there is no general automatic cleanup. A
`<Data>/models/.pull-*` directory is a forced-stop acquisition leftover: it
stays unregistered, is neither auto-published nor auto-removed, and a later run
leaves it in place; inspect and delete it manually after confirming no running
pull owns it. Provenance sidecars are part of the acquired ID and are removed
with it; they are never a removal precondition for registry edits. Preserve the
source model.

TM removal is separate: stop all processes, check ownership through
`tm.sqlite.lock`, and follow the documented `-journal`, `-wal`, `-shm` sidecar
and explicit recovery conditions in the
[SQLite feasibility record](sqlite-feasibility.md). Preserve the database and
sidecars whenever recovery is unresolved. Publication run records and original
backups live under the chosen source/output parent, outside XDG model/TM
storage; recover them before any cleanup and preserve other runs' files. See the
[publication recovery contract](research/linux-publication.md).
