# Exact source and translation profile identity (#15)

`internal/identity` defines immutable, comparable source/profile values and an
exact key. `internal/unit/identity.go` builds source identity from a session;
`internal/localize/profile.go` projects effective inference conditions into
`validate.NewCanonicalProfile`. These APIs do not connect SQLite, TM lookup,
Glossary matching, game adapters or CLI scope configuration. Those integrations
remain separate work (#16, #17 and #20).

## Canonical bytes and exact equality

The current versions are `SourceVersion=1`, `ProfileVersion=1` and `KeySchema=1`.
The wire format is `encoding/json.Marshal` of fixed-order structs with explicit
JSON tags, no maps or omitted fields, and no trailing newline. Numbers are
integers; digests are 64 lowercase hexadecimal characters. Opaque byte sequences
are base64 JSON strings, including the empty string for empty bytes. Empty key,
span and stop-ID collections are `[]`, never `null`. Disabled optional features
use explicit modes. Text identity fields must be valid UTF-8; opaque content is
never converted to text for normalization.

Constructors snapshot slice content. Accessors return copies. The source/profile
digest is SHA-256 of its canonical bytes. Raw source and original artifact
digests are the existing SHA-256 values from the unit/session. The exact key is
the tuple `(key_schema, source canonical BLOB, profile canonical BLOB)`; equality
includes both complete BLOBs. It has no combined hash-only lookup contract.

The checked-in fixtures [source-v1.json](../internal/identity/testdata/source-v1.json)
and [profile-v1.json](../internal/identity/testdata/profile-v1.json) pin complete
bytes. Tests read these files and compare them to constructor output; they do
not regenerate or automatically approve fixtures.

## Source record, in wire order

| Field | Meaning and scope |
| --- | --- |
| `version` | Source serialization version, currently 1. |
| `game_namespace`, `artifact_namespace` | Required explicit caller-owned namespaces. Neither a path nor a session address supplies them. |
| `original_artifact_digest` | Digest of all original artifact bytes, including content outside this unit. |
| `adapter`, `format_schema`, `stable_id` | The unit's adapter, format contract and stable identifier. |
| `keys` | All caller-supplied raw key bytes for this unit, sorted byte-lexicographically. Empty keys and duplicates are preserved; nil and empty bytes represent the same empty key. No keys is `[]`. |
| `raw_source_digest` | Digest of the original unit source bytes before trimming, newline conversion, Unicode normalization, protection or prompt rendering. |
| `source_language`, `target_language` | Exact unit language identifiers. |
| `protection_spans` | Source-order records of `start`, `end`, `kind`, `pair`, `ordered`, preserving the unit's byte offsets and protection semantics. |

`unit.NewSourceIdentity(session, unitID, gameNamespace, artifactNamespace, keys)`
reads the unit and original artifact digests from that session. The caller must
collect the actual manifest key relationships; the identity leaf package does
not import artifact code. Text-adapter callers supply an explicit namespace and
no keys. Origin metadata is not copied wholesale into identity.

Equal original bytes and conditions across separate imports produce equal
source identities. Acceptance is still specific to each import. A one-byte
artifact update invalidates every unit identity, even unchanged source units.
Identical bytes in a different game/artifact namespace also miss. There is no
cross-artifact/game reuse or partial-artifact reuse in this contract.

## Profile record, in wire order

All values describe effective import-wide conditions. Caller labels alone do
not establish model or generation equivalence.

| Field | Included content |
| --- | --- |
| `version` | Profile serialization version, currently 1. |
| `model_id`, `model_digest` | Explicit caller ID and digest of the actual loaded model bytes. Same ID with different bytes misses. |
| `backend`, `backend_pin`, `cuda_backend_pin` | Effective CPU/CUDA backend and pinned implementation releases. CPU canonicalizes the unused CUDA pin to empty. |
| `compute_quant`, `kv_precision` | Actual compute quantization and effective KV precision. Runtime CPU projection uses `not-applicable` for unavailable CPU KV precision. |
| `tokenizer` | `model_digest`, `backend_pin` identifying tokenizer contents/implementation. |
| `template` | `family`, `source`, `renderer_version` for model template rendering. |
| `prompt` | `schema`, `variant`, actual `system_template` and `user_template` bytes, `language_aliases` rule version and `disabled_thinking_prefix` bytes. Plain and Index differ even with the same prompt schema. |
| `generation` | `schema`, effective `max_output_tokens`, integer `request_timeout_ns`, `seed`, `seed_fixed`, ordered `stop_ids`, `thinking`, and model-load `prefill` policy below. |
| `segmentation` | `mode`; when enabled, `schema`, `boundary_fixtures`, actual `unit_text_bytes` and `segments` limits used to split. Disabled mode has empty schema/fixtures and zero limits. |
| `glossary` | `mode=none` with empty schema/entries, or `mode=provided` with caller-supplied schema and canonical effective `entries` bytes. Identity does not perform matching, ordering or conflict resolution. |
| `protection`, `validation` | Each includes `schema` and `fixture_set`. Current runtime fixture sets are `explicit-spans-fixtures-v1` and `restored-content-fixtures-v1`. |
| `context` | Effective `tokens=min(Core limit, Engine context)`, and additional-context `mode` plus exact `additional` bytes. `none` requires empty bytes; `provided` requires nonempty bytes. |

The low-level canonical constructor can represent explicitly provided additional
context. The current inference constructor rejects nonempty `AdditionalContext`
because the current prompt does not use it. It must not claim unused context
was applied. Unit/segment source, rendered prompt, token IDs and token counts are
request-specific and do not belong in this shared profile. Index's existing
TrimSpace rendering remains unchanged; source identity still uses raw bytes.

The generation schema defines currently fixed greedy/disabled sampling,
logprobs and speculation behavior. Enabling such behavior requires a policy
schema/serializer update rather than adding unrelated unused option values.

### Prefill policy

`prefill` contains `exact`, `path`, `cpu_fast_attention`, `batched`,
`batched_capability`, `fast`, `floor`, `chunk`. It is a model-load snapshot from
public `ExactPrefill()`, `PrefillPath()` and `Knob()` values, not a later reread
of process environment. `path` is a static CPU/CUDA/none policy identifier;
`batched_capability` is static capability. Neither asserts the execution path
of an individual request. Diagnostic reason strings are excluded.

- CPU fast attention is enabled when ExactPrefill is false and
  `GOINFER_CPU_FAST_ATTENTION != "0"`. CUDA-only fields are inactive on CPU.
- CUDA batched policy uses `GOINFER_BATCHED_PREFILL != "0"`; CPU fast attention
  is inactive. Fast selector trim/lowercase maps `0`, `false`, `off` to `off`,
  `attn`/`gemm` to themselves, and other values to `both`.
- Fast floor is a nonnegative integer or defaults to 512. Chunk is a positive
  integer or defaults to 512, matching the pinned upstream default.
- ExactPrefill disables fast attention/kernels and their floor. `fast=off`
  ignores floor. **Chunk remains included whenever CUDA batched policy and
  capability use it, including ExactPrefill and `fast=off`.** Without batched
  policy or capability, fast, floor and chunk are inactive.

Requested backend, decline reasons, timings, driver names and configured limits
that are not effective are excluded. Changes to these diagnostics alone retain
identity; effective CPU/CUDA, release, precision and policy changes do not.

## Runtime matching and acceptance

Full canonical identity includes ModelID, Glossary and every generation field.
`MatchesEffective` excludes only caller-owned ModelID and Glossary from runtime
comparison. `MatchesModel`, for imports without generation, additionally excludes
request-specific output/timeout/seed/stops, retaining policy schema, thinking and
prefill. These projections never change exact key equality.

Core checks protection/validation schemas and full segmentation conditions at
entry, including fake engines and empty/protection-only imports. With an
inference engine it checks effective model conditions and every planned request
before generating any unit or segment. Any mismatch returns an error with zero
Generate calls, accepted translations and TM commits/output publication.

An exact-key candidate is untrusted translated text. It must pass `Validate` for
the current session/unit/profile, including restored protection/content checks.
Accepted values from another import or unit cannot be rebound. Accepted binding,
ExpectedTranslations, Manifest and final artifact comparison retain the complete
comparable Profile. Legacy `NewProfile`/`NewProfileWithSegmentation` remain
supported, but have no canonical content and cannot produce persistent exact
keys. A legacy profile with the same digest is still a different full Profile.

## Update rules

| Change | Required result |
| --- | --- |
| Original artifact bytes, namespace, stable ID, raw source, language, key content/multiplicity or ordered protection semantics | New source BLOB and exact-key miss. Artifact changes invalidate all units. |
| Only key ordering or nil versus empty representation of the same key | Same source BLOB/key. |
| Effective model, prompt, glossary, context, policy, segmentation or schema/fixture conditions | New profile BLOB and exact-key miss. |
| Only inactive backend knobs or diagnostic fields | Same canonical profile/key. |
| Serialization semantics or newly active generation behavior | Explicit source/profile/policy schema update as applicable; update fixed fixtures deliberately. |
| Unknown source/profile wire version or incomplete required input | Constructor error, not compatibility inference. |
| Future persisted version/schema | Exact versioned byte comparison; old and new keys miss. No automatic migration, fallback match or model compatibility inference is provided here. |

## Acceptance evidence

The following are executable test scenarios, not claims of real-model inference
or production TM cache hits. Each named test fails if its stated observable is
absent. Existing task1–4 focused results can be reused when code is unchanged.

| Acceptance | Scenario and binary observable | Test artifact |
| --- | --- | --- |
| IS-1 deterministic bytes/key across imports | Equal independently constructed values and key BLOBs; byte-for-byte fixed golden equality and canonical digest check | `internal/identity/source_test.go`: `TestSourceIdentityCanonicalGolden`; `profile_test.go`: `TestProfileIdentityCanonicalGolden`; `key_test.go`: `TestExactKeyCanonicalBlobs`; `internal/unit/identity_test.go`: `TestSourceIdentitySessionScope`; both `testdata/*-v1.json` fixtures |
| IS-2 source scope and every included source field | Single-field changes differ; key reorder is equal; empty/duplicate multiplicity differs; all units differ after metadata-only artifact change | `internal/identity/source_test.go`: `TestSourceIdentityFieldChanges`, `TestSourceIdentityKeysAndSnapshot`, `TestSourceIdentityProtectionOrder`; `internal/unit/identity_test.go`: `TestSourceIdentityWholeArtifactInvalidatesAllUnits` |
| IS-2 raw bytes and immutability | CRLF/LF, spaces, composed/decomposed Unicode and invalid raw UTF-8 differ; input/accessor mutation leaves identities unchanged | `internal/unit/identity_test.go`: `TestSourceIdentityRawBytes`; `internal/identity/source_test.go`: `TestSourceIdentityKeysAndSnapshot`; `profile_test.go`: `TestProfileIdentitySnapshot` |
| IS-2 full profile/model/prompt/glossary/schema/context | Every included field changes canonical bytes/digest/exact key; none/provided differ; unknown versions and malformed input error | `internal/identity/profile_test.go`: `TestProfileIdentityFieldChanges`, `TestProfileIdentityRejectsInvalidInput`; `source_test.go`: `TestSourceIdentityInvalidInput`, `TestSourceIdentityRejectsLossyJSONString` |
| IS-3 effective backend/release/precision/policy/prompt | Effective changes differ; unused settings compare equal; request/info mismatch and unsupported context error; batched chunk differs even with fast off/ExactPrefill | `internal/localize/profile_test.go`: `TestCanonicalInferenceProfileEffectiveConditions`, `TestCanonicalInferenceProfileUnusedConditions`, `TestCanonicalInferenceProfileCUDA`, `TestCanonicalInferenceProfileRejectsMismatch`; `internal/identity/profile_test.go`: `TestProfileIdentityInactiveConditions`, `TestProfileIdentityBatchedChunkWithoutFastKernels`; `internal/inference/goinfer/info_test.go`; `internal/prompt/identity_test.go` |
| IS-3 runtime projections preserve full key contract | Caller-owned runtime differences match; policy/context changes reject as appropriate; no-generation comparison retains policy schema | `internal/identity/profile_test.go`: `TestProfileIdentityEffectiveProjection`, `TestProfileIdentityModelProjectionRetainsPolicySchema` |
| IS-4 preflight all requests and entry conditions | Later unit/segment or model mismatch errors with zero generation/commit/output; valid canonical export succeeds; empty/protection-only/fake entry retains checks | `internal/localize/canonical_pipeline_test.go`: `TestCanonicalInferenceProfilePreflightsAllRequests`, `TestCanonicalInferenceProfileSegmentedLaterUnitAndFullLimits`, `TestCanonicalInferenceProfileEntrySchemaAndLimits`, `TestCanonicalInferenceProfileNoGenerationEntry`, `TestCanonicalInferenceProfileFakeEntry`, `TestCanonicalInferenceProfileGenerationAndExport` |
| IS-4 exact candidate requires current validation/full binding | Same key across imports still rejects old acceptance; malformed protected candidate errors; current valid candidate passes; legacy same-digest/session/unit mismatch rejects | `internal/validate/profile_test.go`: `TestCanonicalProfileExactCandidateStillRequiresValidation`, `TestCanonicalProfileBindingRequiresCompleteProfileAndCurrentImport`, `TestCanonicalProfileExactKeyEligibility`; `internal/artifact/compare_test.go`: `TestCompareFinalSegmentationProfileIdentity` |
| IS-5 scope/version rules and legacy eligibility | Fixed BLOB fixtures match schema1 key; zero identity and legacy profiles cannot produce keys | This record; `internal/identity/key_test.go`: `TestExactKeyCanonicalBlobs`, `TestExactKeyRejectsZeroIdentity`; `internal/validate/profile_test.go`: `TestCanonicalProfileExactKeyEligibility` |

Focused verification, when relevant code has changed:

```sh
CGO_ENABLED=0 go test ./internal/identity ./internal/unit ./internal/validate ./internal/localize ./internal/artifact ./internal/prompt -count=1
CGO_ENABLED=0 go test ./internal/identity -run 'TestExactKey|TestSourceIdentity|TestProfileIdentity' -count=1 -v
```

Final CI-equivalent verification uses the exact toolchain and CGO-free build,
test, vet, cgo guard and arm64 cross-build in the existing Docker gate:

```sh
docker build --progress=plain -f ci/Dockerfile .
```

CUDA-tag stub tests verify connected types/policy, not real GPU quality or
performance. Model-dependent smoke tests and filesystem qualification require
their existing fixtures/environment; skips do not prove those scenarios. Real
TM hit/store/revalidation integration remains #16 acceptance.
