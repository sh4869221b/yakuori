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
CUDA toolkit, models or game fixtures are used. The tests are deterministic and
use generated temporary directories and injected fake path resolvers/writers.

`ci/verify.sh` checks formatting, repository `import "C"` (including excluded files),
its own negative fixture, selected non-standard dependency cgo files for Linux
amd64/arm64 (including tests), CGO=0 build/test/vet, and Linux arm64 cross-build.
The dependency probe uses `CGO_ENABLED=1 go list`, **not** a native compile.
Standard-library optional cgo implementations are excluded from that probe.
Dependency code excluded from both target build graphs is not claimed audited.
The source guard parses Go imports, so comments containing `import "C"` do not fail it.

The arm64 artifact is only cross-built. No arm64 execution compatibility is claimed.
Real CPU inference, model smoke, SQLite and .w3strings tests remain separate gates.

## Initial CLI / error contract

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
- `doctor --check-model [--backend cpu|cuda]`: requires a selected model, opens
  it, reports `Engine.Info`, and closes it without generation. CPU is the
  default. It uses the existing configured 4096-token context candidate and
  reports the effective Engine values. The private snapshot directory must
  already exist; this command does not create it. Explicit CUDA failure is
  reported without CPU fallback. CUDA requires a Linux/amd64 `-tags cuda` build
  and an NVIDIA runtime driver providing `libcuda.so.1`.
- Missing/unknown command, unknown option or extra arguments: exit 2, empty
  stdout, diagnostic on stderr. Raw user arguments are not echoed.
- Operational error: exit 1, diagnostic on stderr. A stdout write failure also
  returns 1; previously written stdout bytes cannot be retracted.
- Diagnostic write failure retains a non-zero status.
- Only `cmd/yakuori/main.go` calls `os.Exit`. `internal/cli.Run` returns a status.
  The unexported resolver/file-runner seams and fake writers make failures
  testable without a model. CLI tests call the real Core and text adapter through
  an internal test path, then use the shared result reporter; there is no public
  fake flag or text translation syntax. Diagnostics retain unit/phase,
  TMCommitted, publication state and RecoveryError reason/paths. Partial stdout
  failures report the sent byte count without rolling back committed rows.

There is no connected `translate` or `localize` pipeline and no placeholder
translation output, no retry, and no legacy Kotoba reads/migration. Model
import is local-file-only; network retrieval (`models pull`, tracked separately
under #19) is not implemented.

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
| Registry lock | `<Data>/registry.lock` | Cooperative import/use lock |
| TM | `<Data>/tm.sqlite` | SQLite translation memory |
| Load snapshots | `<Cache>/model-snapshots` | Transient snapshots created during Engine load and unlinked when load completes |
| State | `<State>` | Displayed path; no state file is created |

Model IDs match ASCII `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Managed directories
use mode `0700`; config, registry, lock and managed model files use `0600`.
Engine snapshots use `0400` and are transient. Import copies bytes and does not
hardlink or modify the source. Storage requires one additional persistent model
copy, plus a transient snapshot during model loading. For example, a research
model at `$HOME/.cache/yakuori/models/` remains an external source file even
though it is inside the cache namespace. Do not remove the whole cache to clear
snapshots.

There is no model removal command. To remove one registered ID manually, first
stop all Yakuori processes. Remove `config.json` only if it selects that ID,
remove only `<Data>/models/<ID>/model.gguf` and its now-empty ID directory, then
remove only that registry entry;
keep the schema 1 registry with an empty `models` object if no entries remain.
This order avoids leaving an unregistered, ambiguous managed directory. If an
interrupted import left a directory without a registry entry, inspect that
directory manually; there is no general automatic cleanup. Preserve the source
model.

TM removal is separate: stop all processes, check ownership through
`tm.sqlite.lock`, and follow the documented `-journal`, `-wal`, `-shm` sidecar
and explicit recovery conditions in the
[SQLite feasibility record](sqlite-feasibility.md). Preserve the database and
sidecars whenever recovery is unresolved. Publication run records and original
backups live under the chosen source/output parent, outside XDG model/TM
storage; recover them before any cleanup and preserve other runs' files. See the
[publication recovery contract](research/linux-publication.md).
