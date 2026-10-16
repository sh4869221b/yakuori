# inference

## OVERVIEW
backend非依存のEngine・不変request・固定生成policyを定義する独立ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| Engine境界 | `engine.go` | Info / CountTokens / Generate / Close |
| 実効model情報 | `engine.go` | tokenizer・template・context・prefill |
| policy入力 | `policy.go` | schemaと2つのbudgetを明示 |
| request snapshot | `request.go` | rendered text・spans・token IDs |
| policy回帰 | `policy_test.go` | 固定生成条件 |
| snapshot回帰 | `request_test.go` | 入出力sliceの分離 |
| backend実装 | `goinfer/` | 子ガイドの実装契約を参照 |

## CONVENTIONS
- `GenerationPolicy` は非公開fieldを持ち、constructor経由で作る。
- schemaは `PolicySchemaV1`、token数とtimeoutは正値。
- `EffectivePolicy` は入力設定ではなく、実際の固定経路のsnapshot。
- greedy生成はtemperature・TopK・TopP・MinPを0に固定する。
- seedは0固定、penalty・logit処理・speculation・sessionを無効にする。
- `PrefillPolicy` はmodel-load時点の値で、request実行経路ではない。
- `PreparedRequest` はbackendがrenderとencodeを一度終えた結果。
- `GenerationRequest` はspans・token IDs・stop IDsを複製する。
- sliceを返すaccessorもコピーを返す。
- `PromptSpan.Special` でtemplate制御文とliteral本文を区別する。
- `RequestIdentity` はmodel・tokenizer・template・prompt条件を保持する。

## ANTI-PATTERNS
- zero policyを暗黙の既定値として使わない。
- `EffectivePolicy` の公開fieldからsampling設定を追加する経路を作らない。
- 本文中の制御markerをtemplate由来のspecial spanへ昇格させない。
- backend固有のloaderやdecoder型をこの親packageのEngine境界へ漏らさない。
- model情報のprefill snapshotをrequestごとの診断理由と混同しない。
