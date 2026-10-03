# Bounded w3strings round-trip spike (#5)

## Decision and evidence scope (2026-10-03)

**Adopt the observed version-162 en/jp subset for subsequent implementation.**
The Go spike preserves every byte for two independently encoded authored
fixtures, the distributed BetterKeybinds MOD (19 IDs/keys) and the distributed
MonsterOfTheWeek sample MOD (30 IDs/keys). The independent Rust reader decodes
both original binaries and Go outputs; their ID/hash/text rows agree with the
original CSV through independent encode/decode and saved expected CSVs.
This establishes named historical MOD/sample no-translation compatibility
through an independent tool, beyond successful re-import by the Go parser.

This remains a bounded research result, not a production Adapter or universal
format specification. Game startup, current Next-Gen/version-164 compatibility,
large MODs, translation quality and human MVP acceptance were not tested. #6 and
#20 may use the limited identity/preservation contract below; wider support and
translation behavior require separate evidence.

## Fixture provenance and rights boundary

`tools/w3spike/testdata/{en,jp}.csv` is newly authored test content for this spike:
short Japanese text with a supplementary-plane character, an empty string,
2,048 UTF-16 code units of repeated Japanese text, and a whitespace-sensitive
literal with a tag, placeholder and pipe. It contains no game dialogue, MOD
translation, borrowed localization database or upstream test prose. The two
4,256-byte `.w3strings` files are deterministic output of the independent
encoder, not files produced by the Go code under test. Both languages contain
four IDs (including `4294967295`) and three key-hash mappings; one ID has no key.
These original project fixtures may be tracked with the project; no third-party
MOD redistribution permission is being inferred from a tool's software license.

| File | SHA-256 |
| --- | --- |
| en.csv | e4687dfdb30b077a1c2aeff3fa78a0bd6e6d23e6be432a8f8b208a879e3caf60 |
| jp.csv | 78e28175da850cac080dcba72a73fd5be1f7ce3f030416a615b1c659c3390551 |
| en.w3strings | 38876ca59c49b0d313f401f87dd2271f7e51458282dcae36b775dec1f9945599 |
| jp.w3strings | 74f4fc56e86fa008612626e84792c88ef0c33b3111cae37de35284847ad5c282 |

Independent oracle: [`w3strings` 0.2.0](https://crates.io/crates/w3strings/0.2.0),
GPL-3.0-only, registry archive SHA-256
`5a58d64fa6670ec7d07ba0d6812487a730c7d2accc3de907cfdb1bb288b86b86`,
upstream source revision
[`c785d7ee5d9f1d1b25249b0c5e4ed9aed7ae8e7b`](https://github.com/Odashikonbu/w3strings-rust/tree/c785d7ee5d9f1d1b25249b0c5e4ed9aed7ae8e7b).
The crate's own integration tests require a separately supplied game file and
Windows encoder, so we do **not** claim those tests ran or passed. No upstream
test file was redistributed. Registry dependency versions and checksums are
locked in `tools/w3spike/oracle/Cargo.lock`. The thin driver calls the published
encode/decode API without patching it. Its GPL dependency is research-only:
not copied into Go, linked into Yakuori, or distributed as a Yakuori binary.
Anyone redistributing the oracle executable must separately comply with its
licenses; this change includes only the small driver, lockfile and licensed
fixture data.

Additional format reference, read but not executed:
[WolvenKit W3StringFile](https://github.com/WolvenKit/WolvenKit-7/blob/c3c1c2028177de37c97a2706412b499a5c04cbf4/WolvenKit.W3Strings/W3StringFile.cs).
This is a community reverse-engineered reference, not a publisher specification.

### External MOD/sample fixtures

The following public MOD/sample assets are retained under their upstream MIT
licenses, with successful acquisition and independent no-translation validation.
Each imported binary is the original fixed-commit file, also checked against the
named release ZIP. Neither original binary was regenerated or replaced by an
encoder output. No MOD scripts or upstream executables were imported.

| Fixture | Fixed source and release | Original binary |
| --- | --- | --- |
| BetterKeybinds, distributed MOD for Witcher 3 1.12 | [source commit 6fdebda0cb7ba02d52ff7321660a93d74e8cd79f](https://github.com/mpstark/BetterKeybinds/tree/6fdebda0cb7ba02d52ff7321660a93d74e8cd79f), tag [1-1.12](https://github.com/mpstark/BetterKeybinds/releases/tag/1-1.12), asset `BetterKeybinds-1-1.12.zip` | 952 bytes; version 162, en; expected CSV has 19 ID/key rows |
| MonsterOfTheWeek, distributed sample MOD | [source commit b9ae964d3d76560c689d6f3b3dbbfd1d5a31af3b](https://github.com/SpontanCombust/tw3-settings-framework/tree/b9ae964d3d76560c689d6f3b3dbbfd1d5a31af3b), tag [1.0.2](https://github.com/SpontanCombust/tw3-settings-framework/releases/tag/1.0.2), asset `TW3_MSF_Samples.zip` | 1,122 bytes; version 162, en; expected CSV has 30 ID/key rows |

All local paths below are relative to `tools/w3spike/testdata/`.

| Fixed-commit source path | Local path | Role |
| --- | --- | --- |
| BetterKeybinds `modBetterKeybinds/content/en.w3strings` | `better-keybinds/en.w3strings` | original binary |
| BetterKeybinds `localization/en.csv` | `better-keybinds/en.source.csv` | original CSV, including key spelling |
| BetterKeybinds `LICENSE.txt` | `better-keybinds/LICENSE.txt` | original MIT notice, Copyright (c) 2016 Michael Starkweather |
| Existing locked oracle encode/decode of BetterKeybinds original CSV | `better-keybinds/en.expected.csv` | independently generated expected ID/hash/text CSV; key spelling is absent |
| tw3-settings-framework `samples/MonsterOfTheWeek/Mods/modSampleMonsterOfTheWeek/content/en.w3strings` | `monster-of-the-week/en.w3strings` | original binary |
| tw3-settings-framework `samples/MonsterOfTheWeek/Mods/modSampleMonsterOfTheWeek/content/en.w3strings.csv` | `monster-of-the-week/en.source.csv` | original CSV, including key spelling |
| tw3-settings-framework `LICENSE` | `monster-of-the-week/LICENSE.txt` | original MIT notice, Copyright (c) 2023 Przemysław Cedro |
| [w3stringsx commit f1da749ffe63aa3747d367e1f1edd04a6f1168ed](https://github.com/SpontanCombust/w3stringsx/tree/f1da749ffe63aa3747d367e1f1edd04a6f1168ed), `tests/decode_en/expected/en.csv` | `monster-of-the-week/en.expected.csv` | external expected ID/hash/text CSV |

The fixed w3stringsx root `LICENSE` was downloaded and compared with the fixed
tw3-settings-framework root `LICENSE`: `cmp` exited 0. The same verbatim MIT
notice therefore covers both Monster sources and is retained once. Both MIT
notices permit redistribution with their copyright and permission notices
included, and provide the assets without warranty. These notices were inspected
directly; a license of an unrelated parser is not the basis for importing them.

| Local file | Bytes | SHA-256 |
| --- | --- | --- |
| better-keybinds/LICENSE.txt | 1087 | 4d57a8e2e09674e97f31546e2534df84a09a56b79c7c734429eeee16c2b05855 |
| better-keybinds/en.expected.csv | 731 | 62b1515b89c2f6c15decbcbd6814aa0e10bd8ea7406516c787e26a18fe4c3b92 |
| better-keybinds/en.source.csv | 1031 | af883a18f2889d8bbc90252d42df97009058e1e888f8b837943308739f673fe3 |
| better-keybinds/en.w3strings | 952 | d3c9e9e51fa666381d3530293e3334de142edf4a32da4227194108933845b57c |
| monster-of-the-week/LICENSE.txt | 1074 | dc5bb23ba01c4910c6452e462cb54cb3e443711842ba1efd7c3c690fc1a33730 |
| monster-of-the-week/en.expected.csv | 937 | a06754b60dcbf5e5691d52e3a80f5b22f73df208bd0845ec13cc4c47e8839fe3 |
| monster-of-the-week/en.source.csv | 1575 | 7aaba894d0d2fe132e6968004a23fa528c83008c0d89829cdc60aac3250e91e9 |
| monster-of-the-week/en.w3strings | 1122 | 9f0227c983bcdf9d145afc6e614c88e2fb6ad4fc50027adf9efbd4c082b59cb3 |

Acquisition used `curl -fL --retry 2` on
`https://raw.githubusercontent.com/<owner>/<repo>/<commit>/<source-path>` for
each original file and on each release's `releases/download/<tag>/<asset>` URL.
Only the following ZIP entries were extracted with `unzip -p` for comparison:

- `BetterKeybinds-1-1.12/modBetterKeybinds/content/en.w3strings`
- `samples/MonsterOfTheWeek/Mods/modSampleMonsterOfTheWeek/content/en.w3strings`

`cmp` of each local binary, source CSV and notice against its downloaded pinned
source exited 0. Both binary comparisons against the extracted release entries
also exited 0. Monster's saved expected CSV matches its downloaded pinned
w3stringsx source byte for byte. SHA-256 values and sizes above were captured by
`sha256sum tools/w3spike/testdata/better-keybinds/* tools/w3spike/testdata/monster-of-the-week/*`
and `wc -c` on the same eight files.

Better's expected CSV was generated by the unchanged locked `w3strings` 0.2.0
oracle: encode the original source CSV to a temporary binary, then decode that
temporary binary to CSV. Both commands exited 0; the saved expected CSV matched
the temporary decoded CSV with `cmp`. The original imported binary was never
an encoder output destination. Acquisition ZIPs and temporary comparison files
were removed after verification.

## Observed binary contract

The fixture header is `RTSW`, little-endian version 162, a 16-bit language-key
prefix, table counts and records, encrypted UTF-16LE payload, then the 16-bit
language-key suffix. The joined language key is `prefix << 16 | suffix`.
Observed pairs are en `43975139` / ID mask `79321793` and jp `54834893` /
ID mask `59825646` (hex). The filename alone is not language evidence.

The first table contains encoded uint32 ID, offset and length. Decoded stable ID
is encoded ID XOR language mask. Offsets and lengths count UTF-16 code units,
not UTF-8 bytes or Unicode scalar values. Length excludes a two-byte zero
terminator in these fixtures. The second table contains uint32 key hash and
encoded ID. It is a separate relation: not every string has a key. Key spelling
is not present in the binary, and recovering it requires an external dictionary;
the spike never invents one or substitutes an array index for a stable ID.

The payload-count field equals the complete string buffer's UTF-16-unit size.
The string transform resets for each entry, starting with the mask's middle
16 bits. A unit is XORed with the low 16 bits of `(length + 1) * state`, and
state rotates left one bit after each unit. This is format obfuscation, not a
security or confidentiality boundary.

The first count byte carries six data bits in the verified subset; a continuation
uses a second byte. Only minimal one/two-byte encodings with values 0–4095 are
accepted here. Counts requiring larger encodings are deliberately unsupported.
The 1 MiB file cap and these count constraints are **research safeguards**, not
measured product limits or claims about Witcher 3's maximum supported sizes.

## Identity, preservation and permitted differences

For this no-translation experiment the allowed binary difference set is empty:
**every byte must match**. The spike retains original table order, offsets,
header/version/language bytes, encoded count bytes, terminators and any unclaimed
payload bytes. It reconstructs table values and encrypted text from decoded
values, then requires exact equality. Tests deliberately change decoded text
and require rejection, so byte copying alone cannot satisfy the experiment.

No sorting, Unicode normalization, trimming, newline conversion, key recovery,
ID renumbering or language change is permitted. The independent decoder's CSV
presentation pads ID fields and changes row order; comparison removes only that
ID-field padding and sorts complete rows. It does not normalize string text or
remove quote characters. Binary comparison still permits **zero** differences.

For later translation work, separately specify the permitted changes: text
payload, UTF-16 lengths/offsets and aggregate payload count, plus the explicit
target language's key/mask and consequent encoded IDs. The decoded ID/key
relation and multiplicities must stay unchanged. Do not compare the encoded
ID bytes across language changes as if they were language-independent IDs.
Unknown metadata needs explicit preservation or a fail-closed rejection, never
an arbitrary normalization allowance. These translation rules are proposals for
#20's tests, not exercised functionality in this spike.

## Reproduction

Linux amd64, `CGO_ENABLED=0`, pinned Rust 1.90.0. The host's Go is
`go1.27.1-X:nodwarf5`; the independent Docker image uses official Go 1.27.1.
The local oracle build used `rustc 1.90.0 (1159e78c4 2025-09-14)` and
`cargo 1.90.0 (840b83a10 2025-07-30)`, selected explicitly rather than the host's
default Cargo 1.99.0. Install the research toolchain, if absent, with
`rustup toolchain install 1.90.0 --profile minimal`. Existing oracle source and
Cargo.lock remain unchanged. Network is needed for fixture/toolchain acquisition,
crate setup and the clean Docker build; saved-fixture commands themselves are local.

```sh
cargo +1.90.0 build --locked --manifest-path tools/w3spike/oracle/Cargo.toml
export W3STRINGS_ORACLE="$PWD/tools/w3spike/oracle/target/debug/yakuori-w3strings-oracle"
sh ci/w3strings-spike.sh
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./tools/w3spike
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./tools/w3spike -run '^$' -fuzz FuzzParse -fuzztime=10s -parallel=2
sh ci/verify.sh
# Clean independent-tool reproduction, also run by its dedicated CI job:
docker build --progress=plain -f ci/w3strings-spike.Dockerfile .
# If the host's GOVERSION differs from the exact ci/verify.sh requirement:
docker run --rm <image-produced-by-the-build> sh ci/verify.sh
```

For authored en/jp, the oracle script regenerates each input and checks its pinned
bytes. For external MODs it always uses the saved original binary as Go input.
It round-trips to a new temporary output and requires `cmp` equality, independently
decodes original and output, and compares both to the saved expected CSV and a
fresh oracle encode/decode of the original source CSV. Only leading ID spaces
and row order are ignored. It fails on any unexpected result and removes its
temporary directory on exit. To reproduce Better's expected-CSV generation
separately without replacing the original binary:

```sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
"$W3STRINGS_ORACLE" encode tools/w3spike/testdata/better-keybinds/en.source.csv "$work/reference.w3strings"
"$W3STRINGS_ORACLE" decode "$work/reference.w3strings" "$work/expected.csv"
cmp tools/w3spike/testdata/better-keybinds/en.expected.csv "$work/expected.csv"
```

The Go CLI creates only a new output path (`O_EXCL`); it rejects existing output
and identical input/output paths. This temporary research output path is **not**
the production staged publication/rollback API. If a filesystem write fails,
its newly created output may be incomplete; it must never be used as a final
translated artifact.

## Results and known negative evidence

Validation on 2026-10-03 used the host Go runtime and pinned Rust 1.90.0 for the
local spike, and official Go 1.27.1 with Rust 1.90.0 for independent Docker checks.

| Invocation / scenario | Observable result |
| --- | --- |
| `CGO_ENABLED=0 GOTOOLCHAIN=local go test -v ./tools/w3spike -run 'TestMODFixtures\|TestRejectMalformed\|TestOutputProtection\|TestIndependentFixtures' -count=1` | exit 0; both MOD fixtures, both changed-text/changed-key rejection subtests per MOD, authored en/jp, malformed inputs and output protection passed |
| `CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./tools/w3spike` | exit 0, `ok` in 0.004s; includes four original-binary fuzz seeds |
| `W3STRINGS_ORACLE="$PWD/tools/w3spike/oracle/target/debug/yakuori-w3strings-oracle" sh ci/w3strings-spike.sh` | exit 0; authored en/jp plus Better 19 entries/19 keys and Monster 30 entries/30 keys reported `ExactBytes:true`; both external loops printed `original/output/source ID-text-key and exact-byte checks passed` |
| `CGO_ENABLED=0 GOTOOLCHAIN=local go test ./tools/w3spike -run '^$' -fuzz FuzzParse -fuzztime=10s -parallel=2` | exit 0; 4/4 initial seeds, 451,618 executions, PASS in 11.006s |
| Host `sh ci/verify.sh` | exit 1 before build/test: exact version guard rejects `go1.27.1-X:nodwarf5`; `sh -x` confirmed the failing comparison against `go1.27.1` |
| `docker build --progress=plain -f ci/w3strings-spike.Dockerfile .` | exit 0; fresh locked Rust build and official Go archive verification; all four fixture CLI reports have `ExactBytes:true`, both external ID/text/key loops pass, exact zero-key failure reproduced |
| `docker run --rm b46cb75b7f0dfdd8f20312dbdd6daa8851a2692279df3f7e54f1cbd6fbd86078 sh ci/verify.sh` (image from preceding build) | exit 0; `go version go1.27.1 linux/amd64`, expected forbidden-cgo self-test rejection, all seven package tests pass, vet and Linux arm64 CLI/SQLite test cross-builds complete |
| `sh -n ci/w3strings-spike.sh`; Go LSP diagnostics; `git diff --check` | all clean; shell LSP unavailable (previously declined installation), no new tooling installed |

`TestMODFixtures` compares every decoded ID and exact text against the original
CSV and saved expected CSV, and every decoded key hash/ID against the latter.
Counts, duplicate detection and consumed-map entries prevent missing, extra or
duplicate records being hidden. Its CSV helper splits into four columns, removes
only numeric-field space/tab padding and leaves the text column untouched.
Each external shell loop reads the saved original binary, compares the new Go
output with `cmp`, independently decodes both, and compares their full rows to
the saved expected CSV and a fresh oracle encode/decode of the original CSV.
Only leading ID spaces and row order are ignored. Original binary regeneration
is not used for external fixtures. The pinned zero-key `UnexpectedEof` check
also passed unchanged. The script's EXIT trap removed its temporary directory.
No production parser or oracle changes were needed. The host version mismatch
was resolved for validation by running the unchanged CGO-free gate in the clean
official-Go image; the host failure remains recorded rather than relabeled a pass.
The disposable verification container removed its CLI/cross-build outputs on
exit. No host `yakuori` or `bin/` outputs were created. Docker images/build cache
and the existing local oracle build were retained; no shared cache was purged.

| Check | Local result |
| --- | --- |
| Independent fixture generation reproduces pinned hashes | pass, en + jp |
| Go decoded IDs, key relation and exact text assertions | pass, all four fixtures |
| Go re-encryption equals every input byte | pass, en + jp + BetterKeybinds + MonsterOfTheWeek |
| Independent reader reads original/Go output and matches CSV-derived reference/expected | pass, all four fixtures |
| Empty, 2,048-unit Japanese, supplementary character, whitespace and pipe | pass, authored en/jp only; absent from the external MOD/sample fixtures |
| Bad header/version/language, duplicate ID/key, dangling key ID, range overflow, overlap, malformed UTF-16/NUL, terminator, trailing bytes | reject |
| Every truncation of jp fixture, oversized input | reject |
| Go-only Bit6 positive boundaries 0/1/63/64/127/128/4095, reordered tables and opaque payload gap preservation | pass |
| Existing output and identical input/output protection | pass |
| Fuzz, 10 seconds, two workers | pass; 451,618 executions with four original-binary seeds |
| CGO=0 build/test/vet, cgo guard, Linux arm64 cross-build | pass via unchanged ci/verify.sh in official-Go Docker image; host exact-version guard failed |
| Independent Docker reproduction | pass, all four fixture comparisons and known zero-key failure |
| Official game / current Next-Gen / large MOD / human translation quality | not run |

## Issue #5 acceptance and adoption

| Acceptance item | Evidence and decision |
| --- | --- |
| Fixture origin/hash/use and redistribution conditions | pass: fixed commits, original paths, release ZIP binary `cmp`, eight file hashes and retained MIT notices above; authored fixtures contain original test content |
| No-translation meaning and immutable bytes | pass for all four fixtures: TestIndependentFixtures/TestMODFixtures and independent shell require exact ID/text/key relations and zero binary differences; no unavoidable binary normalization is allowed |
| Independent tool version/commands/results | pass: locked w3strings 0.2.0, Rust 1.90.0, original/output/source-CSV comparison and independent Docker results above; game confirmation is not the chosen research oracle |
| Japanese/empty/long text, ID and metadata constraints for later fixtures | pass within the observed subset: authored en/jp cover these text cases, external en fixtures add named MOD evidence; UTF-16 units, decoded/encoded ID distinction, absent key spelling and preserved opaque bytes are specified above |
| Reproduction/fixture pins/results and CGO=0 gates; grounds/adoption/blockers | report ready for PR: commands, pins, pass/fail/not-run and adoption limits are recorded; unchanged CGO=0 build/test/vet/cross-build gate passed in official Go 1.27.1 Docker; hosted PR publication/checks are a separate delivery step |

#6/#20 can adopt stable decoded uint32 IDs, separately preserved key-hash/ID
relations, UTF-16 offset/length units, explicit en/jp language keys, and exact
no-translation preservation for the tested version-162 files. Both independent
CSV comparisons are limited to at most one key per ID. Translating text and
recalculating offsets/lengths/payload counts or target-language encoded IDs remain
proposed follow-up behavior, not a verified writer contract. There is no remaining
blocker to this bounded research adoption; the wider cases below remain outside
its evidence. This report does not automatically close the Issue or change the
canonical design Page.

**Reproduced oracle limitation:** a one-string CSV with no key mappings encodes
successfully, but the pinned independent reader fails on its own output with
`UnexpectedEof`. Its writer emits `0x80` for a zero table count; this is not
accepted by the spike. The shell test asserts the `0x80` key-count byte at offset 23, exit status 1
and the exact `UnexpectedEof` diagnostic, rather than accepting any unrelated
nonzero exit or silently skipping the case. The CSV API folds key records into an ID-key map, so it cannot independently
prove multiple key mappings for the same ID or key-only record preservation.
Our independent fixtures have at most one key per ID; wider relation/multiplicity
verification needs a raw-structure reader or another tool.
The oracle must not become Yakuori's
production parser or a universal correctness oracle. Larger Bit6 forms,
key-only records, shared/overlapping payloads, embedded NULs, invalid UTF-16,
other versions/languages and empty files need additional independent fixtures
before an implementation-support decision. The observed failures are not proof
that such files are invalid in the game.

## Remaining gates

Named historical BetterKeybinds and MonsterOfTheWeek files have passed the
independent no-translation gate. Broader MOD/format support needs additional
lawful representative files with recorded provenance, version, hash and rights,
then the same original/output independent-reader comparison. For unsupported
files record the precise reason and expand the research contract only with
evidence. Keep private text and unnecessary decoded output out of logs and CI
artifacts. Do not infer redistribution rights from an unrelated tool's license.

For game verification, use a separate test mod/profile, preserve the original,
record game/build/language and mod ordering, and verify known IDs in the game's
UI. Nothing here installs or activates a MOD. Game, current-build and human
translation-quality verification remain later MVP gates; the independent-reader
research result does not establish them.
