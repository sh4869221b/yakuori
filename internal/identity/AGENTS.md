# identity

## OVERVIEW
source/profile の永続化用 canonical identity と完全一致キーを定義する独立ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| source identity | `source.go` | artifact・source・scope・保護spanを含む |
| effective profile | `profile.go` | model・prompt・生成・分割・用語集条件 |
| 完全一致キー | `key.go` | source/profile の canonical bytes |
| 共通表現 | `canonical.go` | source version と digest 表現 |
| 永続表現のfixture | `testdata/*-v1.json` | source/profile の golden |
| 同一性の回帰 | `source_test.go`, `profile_test.go` | 入力変更と有効条件の区別 |

## CONVENTIONS
- `SourceIdentity` と `ProfileIdentity` は比較可能な不変値。
- zero value は有効な canonical identity を持たない。
- source JSON のフィールド順と encoding は v1 永続契約の一部。
- key は opaque bytes として扱い、source identity 内でバイト順に整列する。
- 空keyと重複keyは消さず、nil key は空bytesと同じ表現にする。
- protection span の順序は維持する。
- JSONが不正UTF-8を置換する前にidentity文字列を拒否する。
- 入力sliceを複製し、返すbytesも内部状態から切り離す。
- 非活性backend設定は `NewProfileIdentity` で正規化する。
- `MatchesEffective` は呼び出し側所有のModelID・Glossaryを比較対象から外す。
- `MatchesModel` はrequestごとのbudget・seed・stopを除外する。
- thinking とmodel-load prefill条件はmodel比較にも残す。

## ANTI-PATTERNS
- source digestだけで永続キーを代用しない。scopeとprofileもキーの一部。
- canonical / effective / model の比較用途を混同しない。
- CPUに無効なCUDA設定を実効identityへ残さない。
- 無効なversionや不完全な有効条件を推測で受け入れない。
- goldenの変更は `docs/source-profile-identity.md` の永続契約と照合する。
