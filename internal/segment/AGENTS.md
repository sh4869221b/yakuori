# segment

## OVERVIEW
保護済み本文を正確な不変requestのtoken budgetで分割・再結合する独立ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| profile識別 | `segment.go:ProfileInput` | schemaとboundary fixtures |
| plan構築 | `segment.go:BuildWithLimits` | 全segmentの事前admission |
| plan snapshot | `segment.go:Plan`, `Segment` | owner・ordinal・request |
| 再結合 | `segment.go:Join` | partitionと候補を照合 |
| token境界回帰 | `segment_test.go` | 非単調countとseparator |
| 用語集回帰 | `glossary_test.go` | glossary安全境界 |
| 上限回帰 | `limits_test.go` | bytes・segments・token予算 |

## CONVENTIONS
- schemaは `safe-boundaries-v1`。
- boundary fixturesは `safe-boundaries-fixtures-v1`。
- 実効contextはmodel情報とconfig上限の小さい方。
- whole-unit token化前にunit source byte上限を検査する。
- 最初にwhole unitのadmissionを試す。
- 分割候補は長いものから試し、token countの単調性を仮定しない。
- request identityとpolicyは同じplan内で一致させる。
- CountTokensの結果は正値かつ `len(TokenIDs())` と一致する。
- prompt tokensとmax output tokensの両方がcontext内に収まる必要がある。
- 生成不要Pieceには空request・token数0を保持する。
- 先頭separatorと各segment後方separatorは元のまま保持する。
- PlanとSegmentはPrepared由来のsource/prepared範囲を保持する。

## ANTI-PATTERNS
- safe boundaryが足りない場合に任意byte位置で短縮しない。
- token countの二分探索で未検証の単調性を持ち込まない。
- plan完成前に一部segmentを生成しない。
- Joinへ別planの結果やordinal違いを渡さない。
- segmentの受理だけで親unitの最終restore・Validateを省略しない。
