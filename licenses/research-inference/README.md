# Inference notice evidence

Exact upstream module notices and current primary reference notices are kept
separately here. The current-reference documents do not establish the exact
historical revision used by upstream for copied code. In particular, reconcile
copied gguf-py codebooks and the Cephes kernel provenance before shipping an
inference-bearing binary or the upstream module sources. The reference SciPy Cephes
permission statement must not be interpreted as proof of aikit's source lineage.

The initial 2026-10-07 reviewed baseline's CLI did not link these packages.
The current CLI selects the CPU inference engine and, with `-tags cuda` on
linux/amd64, the CUDA backend. Exact-version goinfer v0.23.0 and aikit v1.70.1
notices are retained here alongside the previous pins. The root notices for
current aikit v1.76.0 are retained under `../upstream/aikit-v1.76.0/`; source
notice findings for this version have not been re-audited.
`go-module-sums-v0.22.0.json` preserves the previous selected-module inventory;
`../go-module-sums.json` records the current pins.
No upstream model/tokenizer fixtures, executable or module source tree is included.
