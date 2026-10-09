# Third-party notices

Baseline: `5df2c9232c869aacb1489b67693482be3a72f651`, reviewed 2026-10-07.
Original Yakuori code and documentation are covered by the root MIT LICENSE.
Third-party material remains under its own terms; the root license does not
relicense dependencies, MOD fixtures, model assets or the separate GPL oracle.
This is a scoped engineering inventory, not a legal warranty or blanket clearance
for future research binaries, vendor trees or model-bearing images.

## Scope and retained notices

All 13 selected Go modules were obtained at the unchanged go.mod/go.sum versions
from proxy.golang.org, with checksum verification enabled. Every selected module
sum matches the baseline go.sum; `go mod verify` passed. Exact sums are retained
in `licenses/go-module-sums.json`. The nine GitHub-hosted root notices were also
independently verified by blob hash; see `licenses/upstream-index.json`.
Original full notices are retained under `licenses/`.
The Go 1.27.1 toolchain's BSD-3-Clause notice is also retained there.
Upstream goinfer/aikit NOTICE and THIRD_PARTY_LICENSES files are retained as
context, unchanged. Their references to optional GPU modules and model-bearing
upstream assets do not mean those assets are included in Yakuori.

| Component | Exact pin | Notice verified | Current use |
|---|---|---|---|
| Go runtime / standard library | 1.27.1 | BSD-3-Clause, retained | Built executables |
| golang.org/x/sys | v0.48.0 | BSD-3-Clause, retained | Linux publisher in CLI |
| github.com/townsendmerino/goinfer | v0.20.0 | MIT, retained | CPU research executable only |
| github.com/townsendmerino/aikit | v1.51.1 | MIT, retained | CPU probe transitive dependency |
| golang.org/x/text | v0.40.0 | BSD-3-Clause, retained | CPU probe transitive dependency |
| modernc.org/sqlite | v1.60.1 | BSD-3-Clause wrapper plus scoped engine/third-party notices, retained | SQLite test probe only |
| modernc.org/libc | v1.77.1 | BSD-3-Clause root plus scoped third-party notices, retained | SQLite test dependency |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause root plus scoped third-party notices, retained | SQLite test dependency |
| modernc.org/memory | v1.12.1 | BSD-3-Clause root plus mmap/Go notices, retained | SQLite test dependency |
| github.com/dustin/go-humanize | v1.0.1 | MIT, retained | SQLite test dependency |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause, retained | SQLite test dependency |
| github.com/mattn/go-isatty | v0.0.24 | MIT, retained | SQLite test dependency |
| github.com/ncruces/go-strftime | v1.0.0 | MIT, retained | SQLite test dependency |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause, retained | SQLite test dependency |

Exact archives, `go list -m all`, module verification, and linux/amd64 and
linux/arm64 package/file selections were inspected. The current CLI links only
`golang.org/x/sys/unix` outside the Go standard library; verified binary build
metadata agrees. SQLite appears only in test probes; goinfer/aikit/x/text occur
in the separately built CPU probe. The goinfer and aikit GitHub trees include
separate nested modules; their optional GPU dependencies are not authorized or
assumed to be part of this release scope.

### File-level findings and distribution limits

The Go runtime has additional Sun Microsystems, Stephen L. Moshier/Cephes and
Lucent/Vita Nuova attribution blocks. These are retained alongside its BSD
LICENSE in `licenses/upstream/go-1.27.1/THIRD_PARTY_SOURCE_NOTICES.txt`.

The SQLite/libc family has nested and generated-source notices beyond BSD root
metadata. These are preserved under `licenses/research-modernc/`, including complete
selected source-notice blocks and original nested notices, without labeling the entire module
or Yakuori as one of the licenses found in generated-header comments. No SQLite
code is linked into the current CLI. Do not redistribute SQLite probe binaries
or whole module caches based on this source-publication preparation.

The separately built CPU probe selects aikit codebooks identified as copied
from llama.cpp/gguf-py, whose additional MIT attribution is missing upstream's
summary; current primary notices are retained under `licenses/research-inference/`.
Some selected aikit numeric kernels reference Cephes without an exact licensed
source lineage. Resolve that provenance and reconcile the copied-code notices
before redistributing cpuspike binaries or inference module sources. These
findings do not affect the current CLI's x/sys-only external dependency closure.
Archive-only UI/tokenizer/model fixtures also need separate review if ever shipped.
Current reference notices are not proof of the historical copied revision.

The preparation ZIP contains notices, not copies of the dependency source
archives, binary fixtures, model weights or research executables. A future
production Engine/TM integration changes this scope and must be re-audited.

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

## Models and images

The CPU research Dockerfile downloads Qwen2.5-Coder-0.5B-Instruct-GGUF at revision
`ebb2015119c907b064c512bf053e945850b5875f` (documented Apache-2.0), with a pinned
SHA-256. No model weights are included in the repository. Redistributing that
image or a model-bearing executable adds model and base-image obligations;
retain exact model LICENSE/NOTICE if applicable and audit the image contents.
Do not redistribute optional GPU backends, drivers, models or research images
under this source-only preparation's review scope.
