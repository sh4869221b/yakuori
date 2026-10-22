# Witcher 3 production Adapter (#20)

`internal/adapter/w3strings` implements the existing `localize.Adapter`
interface. It imports the adopted version-162 en/jp subset and exports Japanese
files from complete, validated translations. `localize --format w3strings`
routes literal argv through the same selected Engine, canonical profile,
SQLite TM, Core and publication path as text. `translate` and
`localize --format text` remain whole-file text only.

## Supported input and identity

The parser accepts `RTSW`, little-endian version 162, language key
en `0x43975139` or jp `0x54834893`, and minimal one/two-byte Bit6 counts in
0..4095. Language comes from the header prefix and final suffix, not the path.
The ID XOR masks are en `0x79321793` and jp `0x59825646`. Larger/nonminimal
counts, unknown versions and other languages are unsupported, not necessarily
invalid in the game.

Each decoded uint32 ID becomes one unit with:

- Adapter `witcher3-w3strings`, schema `w3strings-v162-en-jp-v1`.
- Stable ID in unsigned decimal, including `0` and `4294967295`; never a table
  index or language-dependent encoded ID.
- Source language `en` or `ja` (binary `jp` maps to `ja`), target `ja`.
- Exact decoded UTF-8 source, without trimming, normalization or newline changes.
  No game tag/placeholder grammar or key spelling is inferred.

`New() *Adapter` uses `config.DefaultLimits()` and Japanese output.
`NewWithLimits(config.Limits) (*Adapter, error)` checks explicit finite limits.
`Import([]byte, validate.Profile) (unit.Session, error)` validates sizes,
counts, record ranges, unique IDs/hashes, key references, disjoint extents,
terminators, UTF-16 and actual UTF-8 budgets before retaining decoded text and
the original snapshot. Invalid UTF-16, embedded NUL and trailing bytes fail.
Bounded inspection indexes are allocated only after count/table bounds checks.

An Adapter retains one successful import's private state. Failed imports and
Observe do not replace it. A new successful import invalidates bindings to the
previous import. Identical bytes imported separately are still different
sessions for Export and KeyRelations; accessors return copies.

## Key relations and source identity

`KeyRelations(session unit.Session) ([]artifact.KeyRelation, error)` returns a
copy of all original key-table relations in table order, bound by `SameImport`.
`KeyRelation.Key` is an opaque string containing the four little-endian hash
bytes, not hex text. Multiple different hashes may point to one ID. Hash zero
is a real four-zero-byte key, distinct from having no relation.

The low-level sequence below runs inside a caller that already has `raw`,
`profile`, explicit namespaces and an imported unit ID. Errors return to that
caller; `source` is its canonical identity for this unit:

```go
a := w3strings.New()
session, err := a.Import(raw, profile)
if err != nil {
    return err
}
relations, err := a.KeyRelations(session)
if err != nil {
    return err
}
var keys [][]byte
for _, relation := range relations {
    if relation.UnitID == id {
        keys = append(keys, []byte(relation.Key))
    }
}
source, err := unit.NewSourceIdentity(session, id, gameNamespace, artifactNamespace, keys)
if err != nil {
    return err
}
```

Use an ID from this `session.Units()`. For research fixtures, construct the
Adapter with the explicit limits below instead of `New`. This manual sequence
requires the caller to collect keys per unit and supply its own scope; the
accessor does not populate `scope.Keys` automatically, and the example is a
manual identity construction, not an automatic FileWithTM integration.

`localize.Core.FileWithTM` binds key relations automatically instead. It
recognizes the Adapter's `KeyRelations` capability and, immediately after the
single Import and before planning and `TM.BeginRun`, freezes the exact
current import's complete relations into the TM scope. A nil `scope.Keys`
accepts the imported relations as-is. A nonnil `scope.Keys`, including an
empty one, is an explicit multiset assertion over full UnitID/raw-key pairs
with multiplicity: a mismatch returns `tm.ErrInvalidScope` at phase `tm_begin`
before lookup or generation, and an equal set keeps a frozen copy of the
imported relations. The W3 CLI and real-file tests drive `FileWithTM` with a
controlled deterministic Engine and real SQLite, proving candidate lookup and
revalidation, final observation before TM commit and publication, and exact
repeat generation-zero reuse. They do not establish native GGUF inference for
this format.

## Japanese export and final observation

`Export(io.Writer, unit.Session, validate.Profile,
[]validate.AcceptedTranslation) (artifact.Manifest, error)` requires exactly
one Accepted translation per imported ID, from that session and profile.
Accepted slice order is irrelevant. Missing, duplicate, extra or foreign
bindings fail before writing. Japanese characters are not mandatory:
empty, numeric, proper-name and source-identical translations remain valid
under common validation. Invalid UTF-8/NUL and excessive output payload fail
before any write; writer errors and short writes propagate.

The string table and key table keep their original record order. Physical
string extent order is separate. The writer emits each original prefix,
middle and suffix opaque gap around translated extents in physical order,
preserving gap bytes and adjacency. It recomputes subsequent offsets, lengths,
payload count and Japanese ciphertext/encoded IDs; it never sorts tables or
renumbers decoded IDs. Offsets, lengths and payload count use UTF-16 code units.
Lengths exclude terminators; payload count includes every terminator and gap.
Supplementary characters occupy two units.

- jp -> jp with every source text unchanged is byte-for-byte identical.
- en -> jp with unchanged text changes only the language prefix/suffix,
  encoded IDs in both tables and encrypted text. Layout, lengths and count
  encodings remain unchanged.
- Changed text may additionally change string lengths, subsequent offsets,
  aggregate payload/count width and gap positions derived from that layout.
  Header/version, decoded IDs and key relations/multiplicity, table order,
  gap contents and zero terminators remain preserved.

`Observe(io.Reader) (artifact.FinalObservation, error)` re-parses actual bytes
using the retained import's finite output bound. It does not substitute
expected values for observed fields. Core closes and reopens the exact stage,
then compares the observation with the expected text and Manifest before
commit/publication. Ordered metadata makes table-order-only or gap-only
changes fail even when ID/text/key multisets are unchanged.

| Manifest metadata name | Bytes and comparison role |
| --- | --- |
| `fixed-header` | First eight bytes; preserved |
| `string-count` | Original encoded string count; preserved |
| `key-count` | Original encoded key count; preserved |
| `decoded-id-order` | LE32 decoded IDs in string-table order; preserved |
| `decoded-key-order` | LE32 hash/decoded-ID pairs in key-table order; preserved |
| `language` | LE16 prefix then LE16 suffix; expected Japanese |
| `string-records` | Actual encoded ID/offset/length records; expected |
| `key-records` | Actual hash/encoded-ID records; expected |
| `payload-count` | Encoded payload count; expected |
| `terminators` | Actual two-byte terminators in string-table order; expected |
| `opaque-layout` | Per physical gap: LE32 start/count then raw gap bytes; expected |

These fields and ID-specific expected texts cover the complete binary.
Expected metadata comes from the original state and Accepted values, not
from parsing the writer's output.

## Input budgets and finite output

Default input limits remain artifact 1495 bytes, 76 units, 980 UTF-8 bytes per
unit, 1036 total UTF-8 text bytes and 76 segments. Artifact bytes refer to the
binary; decoded-text budgets count actual UTF-8 bytes. They apply to Import,
not Japanese expansion. The authored 4256-byte/2048-UTF-16-unit fixtures
require explicit research overrides, shared by the QA driver and Core test:

```go
limits := config.DefaultLimits()
limits.ArtifactBytes, limits.Units = 65536, 4095
limits.UnitTextBytes, limits.TotalTextBytes, limits.Segments = 16384, 32768, 4095
```

These are fixture budgets, not increased product defaults or measured maximum
MOD support. Other Core generation/token/deadline limits still apply.
Output is structurally finite: payload `P <= 4095`, including gaps and
terminators. If imported string/key counts are `Ns`/`Nk`, with original
encoded count widths `cs`/`ck`, Observe's maximum file size is:

```text
Bmax = 10 + cs + 12*Ns + ck + 8*Nk + 2 + 2*4095 + 2 bytes
```

The two-byte allowance before the payload is its maximum count width; the
final two bytes are the language suffix. Observe reads at most `Bmax+1`,
using the extra byte only to detect excess. Structural and Unicode checks
remain active without input-only text budgets. Legal output larger than
1495 bytes is not rejected merely for exceeding the input budget.

## Reproduction and evidence limits

Run from the repository root. Normal Go build/test requires neither Rust nor
the oracle. The dedicated Docker image builds the unchanged locked
`w3strings` 0.2.0 reader with Rust 1.90.0 and uses official Go 1.27.1:

```sh
docker build --progress=plain -f ci/w3strings-spike.Dockerfile .
docker build --progress=plain -f ci/Dockerfile .
```

The first gate runs both `ci/w3strings-spike.sh` (four original fixtures,
exact no-op and the pinned zero-key negative) and `ci/w3strings-adapter.sh`
(real production exports, close/reopen/CompareFinal, independent decode and
saved-CSV comparison). The second runs the existing strict CGO=0 clean gate,
including offline model-free checks and Linux arm64 cross-build, not arm64
execution. A host `go1.27.1-X:nodwarf5` does not satisfy the exact official
`go1.27.1` gate.

Ordinary Docker overlayfs is not a qualified publication filesystem. The
three W3 Core tests explicitly assert prepare-phase unsupported-filesystem
rejection there; that success does not prove the intended publication,
malformed-import or stage-tamper paths. Run those paths on an owned Btrfs
directory, with both `TMPDIR` and `GOTMPDIR` pointing to it. An official-Go
non-root container can use read-only source/module-cache mounts and a writable
Btrfs runtime bind mount. Keep its network disabled and capabilities dropped;
no GPU or model is required.

For local reproduction with an existing Rust 1.90.0 toolchain, use an owned
Btrfs runtime root for filesystem/Core tests:

```sh
RUN_TMP=$(mktemp -d -p /home/sh4869 yakuori-w3strings.XXXXXXXX)
export RUN_TMP TMPDIR="$RUN_TMP"
export NODE_COMPILE_CACHE="$RUN_TMP/node-compile-cache"
export BUN_RUNTIME_TRANSPILER_CACHE_PATH="$RUN_TMP/bun-transpiler"
export GOCACHE="$RUN_TMP/go-build" GOTMPDIR="$RUN_TMP"
export CARGO_TARGET_DIR="$RUN_TMP/cargo-target"
export CGO_ENABLED=0 GOTOOLCHAIN=local
trap 'rm -rf "$RUN_TMP"' EXIT
cargo +1.90.0 build --locked --manifest-path tools/w3spike/oracle/Cargo.toml
export W3STRINGS_ORACLE="$CARGO_TARGET_DIR/debug/yakuori-w3strings-oracle"
sh ci/w3strings-spike.sh
sh ci/w3strings-adapter.sh
go test -count=1 -v ./internal/adapter/w3strings ./tools/w3adapterqa ./internal/localize
go run ./tools/w3adapterqa --input tools/w3spike/testdata/en.w3strings \
  --translations internal/adapter/w3strings/testdata/translated-jp.csv \
  --output "$RUN_TMP/translated.w3strings"
"$W3STRINGS_ORACLE" decode "$RUN_TMP/translated.w3strings" "$RUN_TMP/translated.csv"
# Must exit nonzero before any QA temporary directory is allocated:
W3STRINGS_ORACLE="$RUN_TMP/absent-oracle" sh ci/w3strings-adapter.sh
```

The Adapter script compares four cases: authored en -> a new saved Japanese
translation (empty text, supplementary character, 2050-unit text and exact
spaces/pipe), authored jp -> exact jp no-op, and BetterKeybinds 19 / Monster
30 en IDs -> jp with source-identical texts. Only explicit en -> jp CSV
language metadata, leading ID-field padding and row order are adjusted;
text is not normalized. An absent executable fails, never skips.

The independent CSV conflates keyless IDs and real hash0 as `00000000` and
collapses multiple keys per ID. It does not prove all raw key relations.
Raw Go tests separately cover multiple keys, hash0/keyless, empty tables,
table order and opaque gaps. Minimal zero counts are accepted by production;
the pinned oracle's own zero-key encoding uses `0x80` and decoding fails
with the exact known `UnexpectedEof`. The old script retains that negative.
Independent empty-table/multiple-key compatibility is not established.

Fixture origins, unchanged source/version pins and MIT notices, the oracle's
GPL research-only boundary, historical results and current validation results
are in the [round-trip report](research/w3strings-roundtrip.md).
No game launch, current Next-Gen/version164, other language, largest MOD,
model/GPU translation or human quality acceptance is claimed. Broader failure
matrices (#21) and game/quality acceptance (#22) remain separate. The W3 CLI
and TM evidence cited above is controlled-Engine QA on owned qualified
filesystems; a real native GGUF model running this same path is a separate,
unrun claim.
