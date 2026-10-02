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
- `doctor`: resolves and prints four absolute storage directories; no reads of
  config files, directory creation, model initialization or network activity.
  This is a path diagnostic only, not a claim that inference or TM is healthy.
- Missing/unknown command, unknown option or extra arguments: exit 2, empty
  stdout, diagnostic on stderr. Raw user arguments are not echoed.
- Operational error: exit 1, diagnostic on stderr. A stdout write failure also
  returns 1; previously written stdout bytes cannot be retracted.
- Diagnostic write failure retains a non-zero status.
- Only `cmd/yakuori/main.go` calls `os.Exit`. `internal/cli.Run` returns a status.
  The unexported resolver seam and fake writers make failures testable without
  a model. This does not introduce an Engine abstraction ahead of #3/#11/#12.

There are deliberately no working `translate`, `localize`, or `models` commands,
no placeholder translation output, no retry, and no legacy Kotoba reads/migration.

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

No config file/schema is loaded in #2. Model, registry and TM filenames, input/time
limits and their schemas await the corresponding design gates; no unmeasured values
are introduced. Future writes must use private directories/files per design §9.
Because this foundation writes none of these files, there is no stored translation,
model, staging or recovery data to delete. Future storage/publication work must add
concrete cleanup instructions and must preserve unresolved backups/recovery records.
