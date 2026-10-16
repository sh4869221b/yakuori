# protect

## OVERVIEW
adapterが明示したliteral・tag・placeholderと用語集保護を復元する独立ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| unitの保護と復元 | `protect.go` | Preparedとimport binding |
| token stream検査 | `tokens.go` | 欠落・重複・未知token |
| literal構造 | `structure.go` | 出現数・順序・tag対応 |
| 用語集との統合 | `glossary.go` | raw sourceで照合してからtoken化 |
| 安全な分割境界 | `segment.go` | Cuts / Slice / CheckCandidate |
| 回帰分類 | `binding_test.go`, `structure_test.go`, `segment_test.go` | 別import・構造・境界 |

## CONVENTIONS
- schemaは `explicit-spans-v1`。
- `Prepared` は1回のimportと1unitへ結び付くprivate manifestを持つ。
- zero valueでは候補を復元・検査できない。
- 原文のUTF-8とNULを保護準備時に検査する。
- literal全出現をadapter宣言と照合し、未宣言の同一literalも拒否する。
- 原文に衝突しない `[[YAKUORI_<n>_` namespaceを選ぶ。
- Restoreはtoken stream全体を検査してから復元文字列を組み立てる。
- 復元後もliteral構造とreserved tokenの収支を検査する。
- `Piece` は親manifestとsource/preparedの半開byte rangeを保持する。
- Cutsは保護構造の外にある空白列・文末を候補にする。
- separatorの元bytesは本文と分けて保持する。
- CheckCandidateはそのPieceが所有するtokenだけを受理する。

## ANTI-PATTERNS
- tagやplaceholderを原文から推測して宣言の代わりにしない。
- rune・token・CRLF・開いたtag・用語集範囲の途中で切らない。
- 別PreparedのPieceや別importのunitを混ぜない。
- unordered placeholderの移動まで一律禁止する順序検査にしない。
- 保護以外のpayloadが必要なunitを空白や保護literalだけへ退化させない。
