# Exact source and translation profile identity (#15)

`internal/identity` defines immutable, comparable source/profile values and an
exact key. `internal/unit/identity.go` builds source identity from a session;
`internal/localize/profile.go` projects effective inference conditions into
`validate.NewCanonicalProfile`. Core's `TextWithTM` / `FileWithTM` now connect
these identities to SQLite candidate lookup and revalidation (#16).
`internal/glossary` now supplies executable glossary matching, protection, prompt
terms and restored-parent validation (#17). Game adapters and CLI glossary/scope
configuration remain separate work.

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
| `prompt` | `schema`, `variant`, actual `system_template` and `user_template` bytes (including the static optional terms template), `language_aliases` rule version and `disabled_thinking_prefix` bytes. Plain and Index differ even with the same prompt schema. |
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

## Executable glossary contract (#17)

`internal/identity` keeps glossary schema and entries as caller-owned opaque
bytes. It does not parse, sort or match them. At the execution boundary,
`validate.Profile.Glossary()` requires supported, nonempty, byte-for-byte
canonical entries for `mode=provided`; unsupported schemas and noncanonical
bytes are profile errors. `validate.GlossaryInput` maps an empty glossary to
`mode=none` with empty schema/entries. Independently constructed equal glossary
profiles remain comparable by content and retain the existing acceptance/TM
binding. No glossary span is added to the original unit or source identity.

### Schema and matching

The separately supplied schema is `glossary-v1`. Entries are a JSON array:

```json
[{"source":"cat","target":"猫","mode":"mandatory","match":"word"},{"source":"KEEP","mode":"protect","match":"literal"}]
```

This illustrates the input shape; callers use `glossary.New` or `glossary.Parse`
to obtain canonical bytes. Canonical entries sort byte-lexicographically by
`source`, `target`, `mode`, then `match`, remove completely identical entries,
and serialize fields in `source`, optional `target`, `mode`, `match` order with
no trailing newline. The fixed
[canonical-v1.json](../internal/glossary/testdata/canonical-v1.json) pins those
bytes; it tests serialization, not a conflict-free executable dictionary.

| Field | Contract |
| --- | --- |
| `source` | Nonempty, valid UTF-8 without NUL. |
| `target` | Nonempty valid UTF-8 without NUL for `prefer` and `mandatory`. `protect` has no target; JSON must omit it, even an empty or null value is rejected. |
| `mode` | `prefer` recommends a target; `protect` restores the matched original bytes; `mandatory` requires its target in the completed translation. |
| `match` | Per-entry `literal` or `word`; no default. |

Unknown schemas, modes, match kinds and JSON fields are rejected. Matching is
case-sensitive exact UTF-8 bytes, without Unicode normalization, case folding
or transliteration. `literal` finds every substring occurrence. `word` further
requires that neither adjacent rune is Unicode Letter, Mark, Number or `_`;
string ends are boundaries. Thus `cat` in `cat catch scatter` matches once with
`word`, three times with `literal`. A Japanese letter or combining mark is a
word rune too; this is not morphological word segmentation.

All occurrences, including self-overlap, are collected as half-open raw-source
byte spans before conflicts are checked. There is no longest-match, entry-order
or mode-priority winner. A protect entry's output requirement is its source;
other entries' output requirement is their target.

| Relationship between applicable spans | Result |
| --- | --- |
| Disjoint or adjacent | Both apply. |
| Exactly equal, equal output requirements | Both apply; equal-target prefer and mandatory keep both modes. Protect can coexist when the other target equals the original source. |
| Exactly equal, different output requirements | Conflict, including protect with a different target. |
| Partial overlap, containment or one entry's self-overlap | Conflict even if outputs are equal. |
| Exactly equal to an adapter protection span | Allowed only when output equals original span bytes; glossary protect reuses adapter metadata. |
| Partial overlap with an adapter protection span | Conflict; existing adapter semantics are retained. |

### Generation, restoration and TM

Core matches the raw parent source before protection. All units' conflicts,
protection and request plans are checked before the first Generate call or TM
begin. Matching prefer/mandatory entries form a deduplicated canonical per-parent
terms value; protect uses the existing protection-token instructions. Plain and
Index prompts render terms as JSON data with separate instructions. The static
optional terms template is included in shared prompt identity; actual applicable
terms are immutable request data in `PreparedRequest` / `GenerationRequest`,
separate from model identity. Empty terms preserve existing rendered messages.

The terms-aware builder must return exactly the terms supplied by Core, compared
by their complete canonical content. Dropped, global instead of applicable, or
changed terms fail preflight. CountTokens includes the rendered terms and
instructions. Segmented requests retain all applicable parent terms. Cuts and
separators cannot split or consume any matched source phrase, including prefer
and mandatory phrases that do not create protection tokens. An indivisible
request exceeding the available token budget returns `segment.ErrContextLimit`.

Glossary protect uses the same matching boundaries in preparation, restoration
and candidate validation. For word protect `cat`, `cat scatter` protects only
the first occurrence; the substring in `scatter` is neither tokenized nor counted
as another protected occurrence. Missing, changed or multiplied tokens and
restored protected occurrences are rejected. Adapter literals retain their
existing all-occurrence and tag/order checks.

After segments are joined and protection is restored, `validate.Validate`
requires each applicable mandatory target as an exact substring at least once
in the complete parent candidate. Two source occurrences can be satisfied by
one target occurrence; source/target occurrence counts need not correspond.
Case or Unicode-normalization differences do not satisfy the target. Mandatory
entries absent from the raw source do not apply, and prefer noncompliance does
not reject a candidate. These are mechanical constraints, not a guarantee of
semantic translation quality.

Exact-key TM candidates pass the same restored-parent validation. Mandatory or
protect violations are recorded as invalid candidates and treated as misses;
invalid regeneration produces no accepted output or TM commit. Glossary content
changes use the existing full profile/key contract and miss earlier entries.
No rescue replacement or automatic generation retry is added.

### Internal API connection

Inside this module, with an already loaded `*goinfer.Engine` named `engine`, a
valid generation `policy`, `ctx` and matching limits, the terms-aware path is:

```go
g, err := glossary.New(glossary.Schema, []glossary.Entry{
    {Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word},
})
if err != nil { return err }
limits := config.DefaultLimits()
build := localize.TermsRequestBuilder(func(ctx context.Context, _ unit.UnitID, input localize.RequestInput) (inference.GenerationRequest, error) {
    return engine.NewRequest(ctx, prompt.Input{
        SourceLanguage: "en", TargetLanguage: "ja", Text: input.Text, Terms: input.Terms,
    }, policy)
})
base, err := build(ctx, unit.UnitID{}, localize.RequestInput{Text: "profile"})
if err != nil { return err }
info, err := engine.Info(ctx)
if err != nil { return err }
profile, err := localize.NewCanonicalInferenceProfile(info, base, localize.InferenceProfileInput{
    ModelID: "my-model", Glossary: validate.GlossaryInput(g), Limits: limits,
})
if err != nil { return err }
core := localize.NewCore(localize.NewGlossaryInferenceEngine(engine, build))
accepted, err := core.Generate(ctx, session, profile)
```

The caller supplies the imported `session` and handles `accepted`/`err` as usual.
For segmentation, use `NewGlossarySegmentedCore` (or its WithLimits variant) and
set `InferenceProfileInput.Segmentation=true`. Existing `NewInferenceEngine` and
`NewSegmentedCore` callbacks remain supported when requests have no applicable
prefer/mandatory terms. Protection-only or empty units need no generation but
still undergo glossary/schema/conflict and final validation checks. This is an
internal API connection, with no CLI loader or game-specific wiring.

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
the current session/unit/profile, including restored protection/content and
applicable glossary checks.
`tm.Scope` supplies explicit game/artifact namespaces and all key relations;
the final manifest must match those relations, including empty keys and duplicates,
before commit. All Core preflight checks still run on hits. A valid hit creates a
fresh Accepted binding without generation or another protection Restore; only
`ErrInvalidCandidate` returns to generation. Lookup I/O and source/profile errors
stop the run. Only the complete accepted parent set is staged in run-local memory.
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

Glossary-specific executable scenarios are recorded in the existing tests:

| Contract | Scenario and binary observable | Test artifact |
| --- | --- | --- |
| Canonical schema and exact boundaries/conflicts | Canonical golden equality; expected spans or explicit conflict error | `internal/glossary/glossary_test.go`; `match_test.go`: `TestMatchBoundaries`, `TestMatchConflicts`; `testdata/canonical-v1.json` |
| Profile and restored-parent validation | Equal profiles bind; changed semantic fields change exact keys; missing mandatory target or invalid protected occurrence rejects | `internal/validate/glossary_profile_test.go`; `glossary_test.go`: `TestValidateMandatoryUsesRawParentAndExactTarget`, `TestValidateGlossaryProtectUsesMatchingBoundaries` |
| Protection and phrase-safe cuts | Exact round trip and adapter metadata retained; token corruption, overlap and separator escape reject | `internal/protect/glossary_test.go`: `TestGlossaryProtectionRoundTrip`, `TestGlossaryProtectionRejectsAdapterConflicts`, `TestGlossaryTermsAreIndivisibleWithoutTokens`, `TestGlossaryTermCannotEscapeIntoSeparator` |
| Actual prompt and request terms | Both prompt variants contain quoted terms; wrong request terms reject with zero Generate calls | `internal/prompt/terms_test.go`: `TestGlossaryDataInBothPrompts`; `internal/localize/glossary_request_test.go`; `glossary_pipeline_test.go`: `TestGlossaryCoreRejectsWrongRequestTermsBeforeGeneration` |
| Parent scope, preflight and TM | Normal/segmented parent accepts one mandatory target; later conflict or excess terms cost prevents TM begin; invalid exact hit regenerates; changed glossary misses | `internal/localize/glossary_pipeline_test.go`: `TestGlossaryRegularAndSegmentedParent`; `glossary_preflight_test.go`; `glossary_tm_test.go` |

Focused reproduction, when relevant code has changed (unchanged focused results
need not be rerun before the Docker gate):

```sh
CGO_ENABLED=0 go test ./internal/glossary ./internal/identity ./internal/unit ./internal/protect ./internal/validate ./internal/localize ./internal/artifact ./internal/prompt ./internal/inference ./internal/inference/goinfer ./internal/segment ./internal/tm -count=1
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
SQLite hit revalidation, repair, parent-only staging, commit/publication failures
and explicit reruns are covered by `internal/localize/tm_test.go` and
`tm_linux_test.go`. The [current storage record](sqlite-feasibility.md#current-core-tm-integration-16)
describes their persistent-filesystem measurements and limits.
