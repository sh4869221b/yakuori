# unit

## OVERVIEW
adapter由来の翻訳unitとimport sessionの不変snapshotを提供する独立ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| unit識別・原文 | `unit.go` | UnitIDとTranslationUnit |
| 保護span | `unit.go` | Placeholder / OpenTag / CloseTag |
| import境界 | `session.go` | NewSession / SameImport / Unit |
| source identity連携 | `identity.go` | scopeとkey関係を組み込む |
| session回帰 | `session_test.go` | duplicate・unknown・snapshot |
| 保護境界回帰 | `session_protection_span_test.go` | spanとsessionのコピー |
| identity回帰 | `identity_test.go` | artifactとsourceの区別 |

## CONVENTIONS
- UnitIDはadapter・format schema・stable IDで識別する。
- TranslationUnitは原文bytesとsource digestを保持する。
- 原artifact全体のbytesとdigestはSessionに属する。
- NewSessionはunit IDの重複を拒否する。
- Sessionのコピーは同じimportを共有する。
- NewSessionの呼出しごとに、内容が同じでも別importになる。
- SameImportはcanonical内容の比較ではなくimport割当の同一性。
- unit、artifact、protection spansはsession構築時に切り離す。
- Source / Origin / Artifact / Unitsのaccessorはコピーを返す。
- ArtifactBytesとUnitCountは大きなsnapshotをコピーせず件数を返す。
- zero SessionはCheckで拒否する。
- 存在しないUnitIDは `ErrUnknownUnit`。

## ANTI-PATTERNS
- 同じ原文bytesの別importをSameImportとして扱わない。
- artifact digestとunit source digestを入れ替えない。
- sessionの内部sliceをaccessorで公開しない。
- adapterのkey関係やscopeをsource本文だけから推測しない。
- 保護spanの意味を捨てて文字列だけのunitへ縮退させない。
