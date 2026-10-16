# validate

## OVERVIEW
復元済み親unitの検証と、そのimportへ束縛されたAcceptedTranslationを担う独立ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| 受理とbinding | `accepted.go` | AcceptedTranslation / CheckBinding |
| canonical profile | `profile.go` | identity・用語集・ExactKey |
| 親unit検証 | `validate.go` | 保護構造とmandatory用語 |
| 本文検査 | `content.go` | restored contentの拒否条件 |
| 人手確認所見 | `review.go` | ReviewCodeとfinding |
| 外部受理契約 | `accepted_external_test.go` | 未検証値と別importを拒否 |
| 保護・用語集回帰 | `protected_validation_test.go`, `glossary_test.go` | 復元後検査 |

## CONVENTIONS
- content schemaは `restored-content-v1`。
- Validateへの入力はtoken化中ではなく復元済みの親unit全文。
- source側の用語一致と保護準備を現在unitで再構成する。
- CheckRestoredと本文検査を通した後にAcceptedTranslationを構築する。
- mandatory用語は対応targetの出現を候補内で検査する。
- accepted値はsession・UnitID・source digest・Profileへ束縛する。
- CheckBindingは全条件とvalidatedフラグを照合する。
- Profileは比較可能な値で、zero digestは拒否する。
- 分割profileはschemaとboundary fixturesも保持する。
- canonical profileは用語集の定義と完全一致キーの生成元を保持する。
- ReviewFindingは受理結果に付随する確認所見。
- source同一・固有名詞らしさ・対象言語不確実性はreview分類で扱う。

## ANTI-PATTERNS
- AcceptedTranslationのzero valueを受理済みとして使わない。
- 内容が同じでも別importに束縛された受理値を再利用しない。
- TM rowをこのValidateを通さず受理済みとしない。
- segment単位の検査だけで親unitの復元後検査を代用しない。
- review所見と構造・本文の検証エラーを混同しない。
