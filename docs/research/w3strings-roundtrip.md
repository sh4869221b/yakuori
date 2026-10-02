# Bounded w3strings round-trip spike (#5)

## Decision and evidence scope (2026-10-02)

**Pass for the two committed, independently encoded real-format fixtures.** The
Go spike decodes their IDs, key hashes and text, rebuilds the table records and
encrypted text, and produces byte-identical files. The separately published Rust
reader reads both input and output and agrees with the original authored CSV.
This is not merely a successful re-import by the Go parser.

**Not a production Adapter, universal format specification, real-MOD validation,
or game acceptance.** No game installation or rights-cleared representative MOD
was available. Keep the real-MOD/game and human-quality gates open. #6 and #20
must not treat the limited cases below as complete format coverage. This report
provides a narrow implementation contract and records remaining blockers; it
does not change the canonical design's acceptance requirements.

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
licenses; this PR distributes only the small driver, lockfile and original data.

Additional format reference, read but not executed:
[WolvenKit W3StringFile](https://github.com/WolvenKit/WolvenKit-7/blob/c3c1c2028177de37c97a2706412b499a5c04cbf4/WolvenKit.W3Strings/W3StringFile.cs).
This is a community reverse-engineered reference, not a publisher specification.

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

Linux amd64, Go 1.27.1, `CGO_ENABLED=0`, Rust 1.90.0. Use an official Rust
installation and crates.io. Network is needed only for explicit research
setup/dependency acquisition; the fixture commands themselves are local.

```sh
cargo build --locked --manifest-path tools/w3spike/oracle/Cargo.toml
export W3STRINGS_ORACLE="$PWD/tools/w3spike/oracle/target/debug/yakuori-w3strings-oracle"
sh ci/w3strings-spike.sh
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./tools/w3spike
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./tools/w3spike -run '^$' -fuzz FuzzParse -fuzztime=10s -parallel=2
sh ci/verify.sh
# Clean independent-tool reproduction, also run by its dedicated CI job:
docker build --progress=plain -f ci/w3strings-spike.Dockerfile .
```

The oracle script regenerates each input, compares its pinned bytes, runs Go
round-trip, independently decodes input and output, and compares both decoded
results to the original text/ID/key fixture. It fails on any unexpected result.
The Go CLI creates only a new output path (`O_EXCL`); it rejects existing output
and identical input/output paths. This temporary research output path is **not**
the production staged publication/rollback API. If a filesystem write fails,
its newly created output may be incomplete; it must never be used as a final
translated artifact.

## Results and known negative evidence

| Check | Local result |
| --- | --- |
| Independent fixture generation reproduces pinned hashes | pass, en + jp |
| Go decoded IDs, key relation and exact text assertions | pass |
| Go re-encryption equals every input byte | pass, en + jp |
| Independent reader reads Go outputs and matches authored CSV | pass, en + jp |
| Empty, 2,048-unit Japanese, supplementary character, whitespace and pipe | pass |
| Bad header/version/language, duplicate ID/key, dangling key ID, range overflow, overlap, malformed UTF-16/NUL, terminator, trailing bytes | reject |
| Every truncation of jp fixture, oversized input | reject |
| Existing output and identical input/output protection | pass |
| Fuzz, 10 seconds, two workers | pass; 41,340 executions in recorded local run |
| CGO=0 build/test/vet, cgo guard, Linux arm64 cross-build | pass via ci/verify.sh |
| Official game / representative third-party MOD / human translation quality | not run |

**Reproduced oracle limitation:** a one-string CSV with no key mappings encodes
successfully, but the pinned independent reader fails on its own output with
`UnexpectedEof`. Its writer emits `0x80` for a zero table count; this is not
accepted by the spike. The shell test reproduces the failure and requires it
rather than silently skipping the case. The oracle must not become Yakuori's
production parser or a universal correctness oracle. Larger Bit6 forms,
key-only records, shared/overlapping payloads, embedded NULs, invalid UTF-16,
other versions/languages and empty files need additional independent fixtures
before an implementation-support decision. The observed failures are not proof
that such files are invalid in the game.

## Remaining gate and next lawful fixture

To claim representative real-MOD compatibility, obtain a user's locally owned
file or a mod author's explicitly permitted test artifact. Record provenance,
version, file hash and permission scope separately; do not commit game/MOD text
on the strength of a code repository's license. Run the spike only if the file
fits its documented subset, then independently read the unchanged output using
an appropriate supported tool. For unsupported files record the precise reason
and expand the research contract only with evidence. Keep private text and
unnecessary decoded output out of logs and CI artifacts.

For game verification, use a separate test mod/profile, preserve the original,
record game/build/language and mod ordering, and verify known IDs in the game's
UI. Nothing here installs or activates a MOD. A representative lawful artifact
and game/tool verification are still required by the later MVP gate; passing
these original minimal fixtures does not substitute for them.
