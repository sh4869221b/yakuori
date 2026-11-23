# 中立辞書 JSON v1

辞書は UTF-8 の JSON オブジェクトで、整数の `version: 1` と `entries` 配列を持ちます。各行の `id` は再取込みでも変わらない辞書項目 ID です。ゲーム内の原文は別に `unit` で識別します。

| 項目 | 値と用途 |
| --- | --- |
| `id`, `kind` | 空でない辞書項目 ID と、訳文の `translation` または用語の `term` |
| `source_language`, `target_language` | 原文言語と訳文言語。Native の unit が持つ値に完全一致させる |
| `source`, `target` | 原文と訳文、または原語と訳語。照合では改行・空白・Unicode 表現を保持する |
| `provenance`, `license` | 出典の説明、ライセンス名 `name`、利用許諾 `permission`（`allowed` / `denied` / `unknown`） |
| `confirmation` | 提供元の確認状態（`confirmed` / `unconfirmed` / `unknown`） |
| `scope`, `unit`, `context` | 作品・artifact の名前空間 `game`, `artifact`、原文 ID の `adapter`, `format_schema`, `stable_id`、Context Pack の `scene`, `speaker` ID |

取込みには `id`、`kind`、言語対が必要です。1 文書は 2 MiB、4,096 行、各文字列は UTF-8 で 16 KiB、JSON の深さは 8 までです。未知の version・項目名、重複キー、不正な Unicode、`null`、後続の JSON 値はエラーになります。

## 参照と採否

`dictionary.Catalog.Import` で読み込み、`References` で候補とローカルの採否を取得します。採用、棄却、手動訳の指定は `Review` で行います。`Reviews` は判断時の原本と判断内容を対にして返し、呼び出し側が保存したその組を `NewCatalog` で復元できます。再取込みで同じ ID の原本が変わると、レビュー済みの原本と判断を保持し、`import-conflict` を返します。異なる ID は個別にレビューできます。

Native では `localize.ContextPackOptions.Dictionary` にカタログを渡し、既存の `Core.FileWithTMContext` を呼びます。出典・許諾名があり、`allowed`・`confirmed`・ローカル採用が揃った訳でも、作品・artifact・言語対・原文 ID・原文・選択済み場面を照合します。話者を指定した行は選択済み話者にも一致する必要があります。原文、場面、ID、訳文などが不足する行は参照候補に留まります。複数の確定候補が異なる訳を指定すると処理を停止します。

用語を採用する際は `prefer`・`protect`・`mandatory` と `literal`・`word` を明示します。用語の範囲は指定した名前空間・原文 ID・場面・話者で絞り込みます。範囲を省略した用語は、その言語対で原語に一致する unit が対象です。場面・話者などの不一致は `OnDictionaryDiagnostic` で受け取れます。原語の重なりや訳語の衝突は既存の用語集規則で検出します。

確定候補は復元後の訳文として現在の unit の検証に渡します。保護文字列の欠落や必須用語違反があれば公開前に失敗します。全件の受理と最終 artifact の照合に成功すると、既存の Native 経路が TM に commit し、出力を公開します。

## 実行例

[自作の辞書](../internal/dictionary/testdata/neutral-v1.json)を取り込む[実行可能な Go の例](../internal/dictionary/example_test.go)と、同一原文の宿と門の訳を別々に公開する [Native テスト](../internal/localize/native_dictionary_linux_test.go)を同梱しています。Native テストには Linux の非 root ユーザーと、ext4 または Btrfs 上の書込み可能な `TMPDIR` が必要です。

```sh
export CGO_ENABLED=0 GOTOOLCHAIN=local
go test -v ./internal/dictionary -run '^ExampleCatalog$'
go test -count=1 -v ./internal/localize -run '^TestNativeDictionary(Revalidation)?$'
```
