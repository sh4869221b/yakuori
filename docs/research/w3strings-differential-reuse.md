# W3strings の差分分類と翻訳の再利用（#24）

## 決定

Witcher 3 v1 の完全一致キーを維持する。差分は、同じ論理 artifact の旧版と
新版として明示的に指定した2入力を比較して求める。unit の対応付けには
W3 アダプターが返す `UnitID`（アダプター、形式 schema、デコード済み
文字列 ID）を使う。同 ID の原文を既存の `SourceDigest()` で比較し、同じなら
`Unchanged`、異なれば `Modified` とする。新版だけにある ID は `Added`、
旧版だけにある ID は `Removed` とする。表内の位置や同じ原文から別 ID の
対応を推定しない。ここでの分類は原文の差分を表す。

現行の SQLite TM は、key schema と canonical source/profile 全体の一致で候補を
検索する。source identity はデコード済み原文の digest に加え、元ファイル全体の
digest、game/artifact namespace、`UnitID`、言語、キー関係、保護範囲を含む。
そのため1件だけ原文を更新しても、`Unchanged` を含む全 unit が旧版の行に
一致しない。W3 の表順やキー metadata の変更も入力バイト列を変える。
W3 ファイル上のキー順を変えると、unit の canonical キー関係が同じでも
元ファイル全体の digest は変わる。
ファイルパスは identity に含まれない。同一バイト列でも明示した artifact
namespace が変われば全件が旧版の行に一致しない。
[完全一致キーの契約](../source-profile-identity.md#update-rules)

キーが一致し、そのキーの行が SQLite にあれば、Core は候補の訳文を現在の
session・unit・profile で `Validate` する。合格時は現在の import に紐付く
`AcceptedTranslation` を作る。不正候補は通常の生成・検証経路に戻る。
ストアの読み取りエラーは処理を止める。削除された unit は新版の出力対象から外れ、
旧版の TM 行は残る。`Validate` は保護構造、本文制約、適用される必須用語を検査する。
原文が異なる場合に旧訳の意味が合っていることは保証しない。
[W3 アダプターの契約](../w3strings-adapter.md#key-relations-and-source-identity)

## Fixture と結果

独立に作った合成 W3 v162 en 入力を使う。基準は起点の `f26af73`、
fixture は [TestW3StringsDifferentialReuse](../../internal/localize/w3strings_diff_reuse_linux_test.go)
のテーブルと既存の `w3SQLiteArtifactBytes` に固定する。旧版の文字列 ID は
`1,2,3`。各ケースで空の SQLite に旧版を登録し、同じ canonical profile で
新版を処理する。W3 アダプターで出力の ID、順序、訳文、言語とキー関係を観測する。

| ケース | 分類 | 旧版 TM から再利用した ID | 新規生成した ID |
| --- | --- | --- | --- |
| 同一バイト列・同一 namespace | Unchanged 1,2,3 | 1,2,3（全件再検証） | なし |
| ID 2 の原文だけ変更 | Unchanged 1,3 / Modified 2 | なし | 1,2,3 |
| ID 4 を追加 | Unchanged 1,2,3 / Added 4 | なし | 1,2,3,4 |
| ID 3 を削除 | Unchanged 1,2 / Removed 3 | なし | 1,2 |
| 表順を 3,1,2 に変更 | Unchanged 1,2,3 | なし | 3,1,2 |
| ID 2 のキー hash だけ変更 | Unchanged 1,2,3 | なし | 1,2,3 |
| 同一バイト列で artifact namespace を変更 | Unchanged 1,2,3 | なし | 1,2,3 |
| ID 3 を削除し、同じ原文を ID 4 に追加 | Unchanged 1,2 / Added 4 / Removed 3 | なし | 1,2,4 |

最後のケースでは、出力に ID 3 が存在せず、ID 4 には新規生成した訳文が入る。
旧版の訳文は ID 4 に割り当てられない。同一バイト列での合格候補の再検証に加え、
既存の `TestTMHitRevalidation` は旧 import の binding が持ち越されないこと、
`TestTMInvalidRowGeneration` は不正な候補が棄却されることを確認する。
今回の fixture は決定的な fake Engine と実 SQLite を使う。実 MOD での版間比較、
実モデルによる生成・翻訳品質、ファイル公開は今回の調査では未実施。

Go 1.27.3 の Linux/amd64 環境で再現する。

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 -v ./internal/localize \
  -run '^(TestW3StringsDifferentialReuse|TestTMHitRevalidation|TestTMInvalidRowGeneration)$'
```

## 次 PR の仕様案

明示的に指定した W3 の旧版と新版について、差分と現行キーの一致可否を返す
読み取り専用の比較関数を追加する。入力は各版のバイト列、共通の game namespace、
それぞれの artifact namespace、および両版に共通の canonical profile とする。
呼び出し側が比較対象の組を指定し、既存 W3 アダプターで個別に import する。
形式や profile が契約を満たさない場合はエラーを返す。

出力は元ファイル全体の digest と artifact namespace それぞれの一致可否、および
デコード済み ID の数値順に並べた `ID`・分類・exact key 比較結果とする。
両版に存在する ID のキーは、それぞれの import のキー関係と scope で
組み立てて比較する。片側にしか存在しない ID は比較対象なしとする。
入力だけの比較で得られる情報はキーの一致可否まで。候補行の有無や検証の成否は、
実際の TM 処理で決まる。

受入れでは上の8ケースについて分類と exact key 比較結果を ID 単位で照合する。
追加・削除には比較対象がなく、同一バイト列・同一 namespace のケースだけが
全件一致する。入力不正の場合は部分結果を返さない。比較処理は生成、TM の
読み書き、翻訳済み artifact の作成・公開を行わない。

この調査を完了するための製品判断はない。将来、異なる artifact digest 間で
候補を検索するなら、どの証拠で同一 artifact の版と認めるか、キー関係の変更を
どこまで許すか、候補提示と自動採用のどちらを提供するかを決める必要がある。
承認後に #24 と影響する契約（exact key は #15、候補の再検証は #16）へ反映する。
