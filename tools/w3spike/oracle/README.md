# Independent research oracle

This thin driver is original Yakuori code covered by the root MIT license.
It links `w3strings =0.2.0`, which is GPL-3.0-only, when built. The resulting
research executable is not an MIT-only executable and is not part of Yakuori
CLI binary releases. It is invoked as a separate process for independent tests.

Do not bundle an oracle executable, its dependency source archive or the research
Docker image into an ordinary CLI release. If distributing the oracle separately,
review that exact binary/source package, retain applicable notices, and meet
GPL Corresponding Source obligations. `publish = false` prevents an accidental
Cargo registry publication; it does not itself fulfill license obligations.

See [third-party notices](../../../THIRD_PARTY_NOTICES.md), the locked versions
in Cargo.lock, and `licenses/research-oracle/` at the repository root.
