# Third-party notices

Initial inventory baseline: `5df2c9232c869aacb1489b67693482be3a72f651`, reviewed 2026-10-07.
Original Yakuori code and documentation are covered by the root MIT LICENSE.
Third-party material remains under its own terms; the root license does not
relicense dependencies, MOD fixtures, model assets or the separate GPL oracle.
This is a scoped engineering inventory, not a legal warranty or blanket clearance
for future research binaries, vendor trees or model-bearing images.

## Scope and retained notices

The current Go module versions and their module sums are recorded in
`licenses/go-module-sums.json`; `go mod verify` passed after the 2026-11-06
PLaMo integration update. Replacement module paths and immutable revisions are
included. Original root notices for the current pins are retained under
`licenses/upstream/` and `licenses/research-modernc/verbatim-files/`.
The original 2026-10-16 inventory, independently verified GitHub-hosted root
notices, and file-level source notice collections describe the versions examined
at that time; see `licenses/upstream-index.json`, `licenses/research-inference/`,
and `licenses/research-modernc/`. They have not been re-audited for the new pins.
Upstream notices also describe optional backends and model-bearing release assets;
those assets are not included. The current package selections use the modules
listed below.

| Component | Exact pin | Root notice | Current use |
|---|---|---|---|
| Go runtime / standard library | 1.27.3 | BSD-3-Clause, retained | Built executables |
| golang.org/x/sys | v0.49.0 | BSD-3-Clause, retained | Linux publisher in CLI |
| github.com/townsendmerino/goinfer → sh4869221b/goinfer | 994cbf5ee38dd8dcc612aad6e6803ac79f772f3b | MIT, [retained](licenses/upstream/goinfer-plamo3-994cbf5/LICENSE) | CPU/CUDA inference engines and research tools; source-only PLaMo reference fixtures have separate terms |
| github.com/townsendmerino/goinfer/backend/cuda → sh4869221b/goinfer/backend/cuda | 994cbf5ee38dd8dcc612aad6e6803ac79f772f3b | MIT, [retained](licenses/upstream/goinfer-cuda-plamo3-994cbf5/LICENSE) | CUDA inference backend (`cuda && linux && amd64`) |
| github.com/townsendmerino/aikit | v1.76.0 | MIT, [retained](licenses/upstream/aikit-v1.76.0/LICENSE) | Inference transitive dependency |
| [github.com/eitamring/gocudrv](https://github.com/eitamring/gocudrv/blob/v0.3.2/LICENSE) | v0.3.2 | MIT, [retained](licenses/upstream/gocudrv-v0.3.2/LICENSE) | CUDA driver API binding |
| [github.com/ebitengine/purego](https://github.com/ebitengine/purego/blob/v0.11.2/LICENSE) | v0.11.2 | Apache-2.0, [retained](licenses/upstream/purego-v0.11.2/LICENSE) | CUDA driver loading |
| golang.org/x/text | v0.43.0 | BSD-3-Clause, retained | Inference transitive dependency |
| modernc.org/sqlite | v1.61.1 | BSD-3-Clause wrapper plus scoped engine/third-party notices, retained | Translation-memory persistence and SQLite probes |
| modernc.org/libc | v1.80.0 | BSD-3-Clause root plus scoped third-party notices, retained | SQLite dependency |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause root plus scoped third-party notices, retained | SQLite dependency |
| modernc.org/memory | v1.12.1 | BSD-3-Clause root plus mmap/Go notices, retained | SQLite dependency |
| github.com/dustin/go-humanize | v1.1.0 | MIT, retained | SQLite dependency |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause, retained | SQLite dependency |
| github.com/mattn/go-isatty | v0.0.24 | MIT, retained | SQLite test dependency |
| github.com/ncruces/go-strftime | v1.1.0 | MIT, retained | SQLite test dependency |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause, retained | SQLite dependency |

At the 2026-10-16 update, `go list -m all` and linux/amd64 and linux/arm64 CLI package
selections were inspected again, including linux/amd64 with `-tags cuda`.
The CLI selected goinfer/aikit/x/text and SQLite in addition to x/sys;
the CUDA build also selected the CUDA rows above. The module graph included
`aikit/gpu/visioncuda v0.1.0`, but no package from that module is imported by these
builds, and it was not among the 17 go.mod-selected modules inventoried there.
The goinfer and aikit GitHub trees include separate nested modules. Running CUDA
inference or evaluation requires a working
NVIDIA driver; gocudrv dynamically loads the host's `libcuda.so.1` at runtime
([upstream requirement](https://github.com/eitamring/gocudrv/blob/v0.3.2/README.md)).
The driver is supplied by the runtime host and is not distributed by this
repository.

### File-level findings and distribution limits

The Go 1.27.1 runtime had additional Sun Microsystems, Stephen L. Moshier/Cephes
and Lucent/Vita Nuova attribution blocks. These are retained in
`licenses/upstream/go-1.27.1/THIRD_PARTY_SOURCE_NOTICES.txt`; the current Go
1.27.3 source has not been re-audited for file-level notices.

The SQLite/libc family has nested and generated-source notices beyond BSD root
metadata. The prior-pin collections under `licenses/research-modernc/` include selected
source-notice blocks and original nested notices, without labeling the entire module
or Yakuori as one of the licenses found in generated-header comments. SQLite is
now selected by the CLI's translation-memory persistence code. Do not redistribute SQLite probe binaries
or whole module caches based on this source-publication preparation.

The inference engines and separately built CPU probe select aikit codebooks identified as copied
from llama.cpp/gguf-py, whose additional MIT attribution is missing upstream's
summary; notices from the prior pins are retained under `licenses/research-inference/`.
Some selected aikit numeric kernels reference Cephes without an exact licensed
source lineage. Resolve that provenance and reconcile the copied-code notices
before redistributing inference-bearing binaries or inference module sources.
Archive-only UI/tokenizer/model fixtures also need separate review if ever shipped.
Current reference notices are not proof of the historical copied revision.

The initial preparation ZIP contained notices, not copies of the dependency source
archives, binary fixtures, model weights or research executables. The current
Engine/TM package selections above supersede that baseline's x/sys-only CLI
closure; this dependency-pin update does not resolve the recorded copied-source
provenance findings or constitute a new distribution audit.

## Third-party fixtures already in the source tree

- `tools/w3spike/testdata/better-keybinds/`: MIT; Copyright (c) 2016
  Michael Starkweather. Its `LICENSE.txt` must remain with the binary and CSV
  fixture copies, including expected/derived CSV data.
- `tools/w3spike/testdata/monster-of-the-week/`: MIT; Copyright (c) 2023
  Przemysław Cedro. The retained `LICENSE.txt` covers the pinned MOD source and
  the matching w3stringsx expected CSV notice. Keep it with all copies.
- `tools/w3spike/testdata/{en,jp}.{csv,w3strings}`: documented as project-authored
  synthetic content; not game dialogue. These project-authored fixtures are covered by the root MIT license.

Exact source commits, origin paths, transformations and SHA-256 hashes are in
[fixture provenance](docs/research/w3strings-roundtrip.md#fixture-provenance-and-rights-boundary).
No fonts, artwork, model weights or borrowed localization databases are tracked
in the inspected main tree. This observation is not a third-party rights guarantee.

## Rust research oracle is separately licensed

`tools/w3spike/oracle` uses `w3strings =0.2.0`, **GPL-3.0-only**, plus the exact
transitive versions in Cargo.lock. It is a separate research program, not linked
into the Go CLI. The upstream crate archive hash is documented in the provenance
report. A GPL executable/source redistribution must be handled separately,
including applicable notices and Corresponding Source requirements. The root MIT
license for original Yakuori code does not replace those conditions. Do not put the oracle executable or its Docker image into a Yakuori
CLI release archive. All 16 locked third-party versions now have exact-version source-tag license
evidence and full texts retained under `licenses/research-oracle/`, with runtime
source-header scanning. Notable additional terms are Unicode-3.0 for
unicode-ident and original Ulf Adams attribution for ryu. Only w3strings has
registry-archive checksum equivalence verified; the other 15 registry archives
and a future oracle binary remain separate gates.

## Copied Index chat template

`internal/inference/goinfer/index_template.go` contains the 7,755-character
`chat_template` copied verbatim from `tokenizer_config.json` in the official
[Index-Translate-2B base model](https://huggingface.co/IndexTeam/Index-Translate-2B).
The official [GGUF conversion](https://huggingface.co/IndexTeam/Index-Translate-2B-GGUF)
identifies that base model. Both model cards declare Apache-2.0, as does the
upstream [Index-Translate repository](https://github.com/bilibili/Index-Translate).
The examined template has no separate attribution header or accompanying
NOTICE file; the source repository root also has no NOTICE. A full copy of its
Apache-2.0 LICENSE is retained at
[`licenses/research-inference/notices/index-translate-2B-APACHE-2.0.txt`](licenses/research-inference/notices/index-translate-2B-APACHE-2.0.txt).

## PLaMo materials

Built with PLaMo.

`internal/inference/goinfer/testdata/plamo3-chat-template.jinja` is copied verbatim
from Preferred Networks, Inc.'s `pfnet/plamo-3-translate-0.8b` at revision
`cc8e88169807d5a272e8ff06db12eb0129b67f81`. Its SHA-256 is
`cd49e7e0dfa8ec847c163db88dfca89b280dddbb73ddc60ba38c62a420eaced2`.
It and the explicitly identified model-generated expected translations in
`internal/cli/plamo3_cpu_smoke_test.go` and `docs/plamo3.md` are PLaMo model
materials under the [PLaMo Community License Agreement](licenses/plamo-community/LICENSE).
The full English agreement is retained verbatim, with its pinned origin and hash
in `licenses/upstream-index.json`. These materials are not relicensed as MIT.
The test template fixture is not embedded in the CLI binary. No released weights
or PFN Python model implementation are tracked here.

The fork's root LICENSE, NOTICE and THIRD_PARTY_LICENSES.md are retained in
`licenses/upstream/goinfer-plamo3-994cbf5/`. Its source-only PLaMo fixtures and
reference outputs carry separate terms described in that NOTICE. The current
nested CUDA module incorporates code formerly in aikit/gpu, although that module
is no longer selected separately. The historical copied-code/provenance findings
above remain open; this pin update is not a complete distribution audit.

## Models and images

The CPU research Dockerfile downloads Qwen2.5-Coder-0.5B-Instruct-GGUF at revision
`ebb2015119c907b064c512bf053e945850b5875f` (documented Apache-2.0), with a pinned
SHA-256. No model weights are included in the repository. Redistributing that
image or a model-bearing executable adds model and base-image obligations;
retain exact model LICENSE/NOTICE if applicable and audit the image contents.
Do not redistribute optional GPU backends, drivers, models or research images
under this source-only preparation's review scope.
