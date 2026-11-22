# Generic JSON v1

外部ツールはゲームから抽出した翻訳対象を UTF-8 の JSON で渡します。ルートの `schema` は `yakuori.generic-json`、`version` は整数の `1` です。言語とゲーム・artifactの名前空間は、呼び出し側が `genericjson.New` に渡した値とそれぞれ完全一致する必要があります。同じJSONを再インポートする場合は、同じ名前空間を指定します。

- [JSON Schema（Draft 2020-12）](../internal/adapter/genericjson/schema-v1.json)
- [最小サンプル](../internal/adapter/genericjson/testdata/minimal-v1.json)

`units` の各 `id` は、外部ツールが元レコードに割り当てた安定した文字列です。数値由来でも文字列にし、同じartifact内で一意にします。空の `source` は有効です。ID・原文の空白、改行、Unicode表現はデコード後そのまま使います。`translation` を渡した場合は入力artifactの一部として保持されます。翻訳結果の受理はCoreによる検証が必要です。

`protection` の `start` と `end` は、デコードした原文のUTF-8バイト列上の半開区間です。spanは昇順で、文字の途中や他のspanと重なってはいけません。`placeholder` の `ordered` は順序制約の有無を指定します。`open_tag` と `close_tag` は `ordered: true` と、対応する非空の `pair` を指定します。tagは正しく入れ子にし、宣言したリテラルの全出現を列挙します。

`context.speaker` と `context.scene` はそれぞれ安定した参照IDです。Importで得られる `Session.Unit(id)` から、そのIDに対応する `Source()` と `Origin()["speaker"]`、`Origin()["scene"]` を取得できます。省略した参照のキーは `Origin()` に含まれません。元JSONは `Session.Artifact()` で取得できます。Coreに接続する際は、Importで照合したゲーム・artifactの名前空間をそのまま `tm.Scope` に渡します。SourceIdentityは、このスコープと元artifact、単位の `generic-json`、`generic-json-v1`、stable ID、原文、言語、保護範囲を既存契約で識別します。

## 翻訳と出力

`yakuori localize --format generic-json` は、入力のschema、version、言語、スコープ、`units` の順序を保持します。各IDに対応する原文、保護範囲、`context` 参照も保持します。既存の `translation` は検証済みの訳文に置き換わります。空の訳文も `"translation":""` として出力します。

出力はUTF-8のコンパクトなJSONです。インデント、オブジェクトのキー順、等価なエスケープの表記は変わることがあります。Yakuoriは、書込みを終えて閉じたstageを再度開き、各IDの訳文と保持する情報を検証します。全件成功後にTMへ一括commitし、JSONを公開します。ゲーム形式への再構築と最終検証は外部ツールが担当します。外部ツール側で失敗しても、commit済みのTM行は保持されます。

## 入出力上限

`genericjson.New` に有効な `config.Limits` を渡します。表の値は `config.DefaultLimits()` を使った場合の値です。全条件が同時に適用されます。

| 対象 | 上限 | 最小サンプルの実測値 |
| --- | ---: | ---: |
| JSON全体 | 1,495バイト | 652バイト |
| unit数 | 76 | 2 |
| 各可変文字列値のデコード後のUTF-8サイズ | 980バイト | 最大17バイト |
| 全unitの原文のデコード後のUTF-8サイズの合計 | 1,036バイト | 34バイト |
| JSONコンテナの深さ（ルートを1とする） | 5 | 5 |
| 1 unitの保護span数 | 64 | 最大1 |

出力には同じ上限を適用し、JSON全体のサイズは訳文とエスケープを含む出力UTF-8の実バイト数で判定します。入力が上限内でも、訳文の追加によって上限を超えるとTMを更新せず、JSONの公開は失敗します。

文字列値の上限は言語、名前空間、ID、原文、訳文、保護の `kind` と `pair`、context参照に適用されます。深さはv1の固定構造で制限されます。保護span数には固定上限を設け、宣言リテラルに対する原文の照合回数を抑えます。境界試験は各可変上限のちょうどの値と1超過を確認します。unit数とspan数の試験では、個別の境界に到達できるようJSON全体の上限を広げます。

JSON Schemaによる検証に加え、GoのImportは重複キー（エスケープをデコードした結果が同じキーを含む）、重複ID、無効なUTF-8や単独のサロゲート、スコープ・言語不一致、バイト上限、保護範囲の位置と構造を検査します。NULと指定フィールドの `null` は拒否します。整数は小数点や指数を含まないJSON数値として記述します。ルートオブジェクトの直後にはJSONの空白だけを置けます。

## 外部ツールでの生成例

Pythonで文字列と保護のバイト位置を組み立て、標準出力へv1のJSONを出力します。

```python
import json
import sys

source = "  Cafe\u0301 {name}\r\n"
start = source.encode("utf-8").index(b"{name}")
records = [("001", "keeper", "inn"), ("002", "guard", "gate")]
document = {
    "schema": "yakuori.generic-json",
    "version": 1,
    "source_language": "en",
    "target_language": "ja",
    "scope": {"game_namespace": "sample-game", "artifact_namespace": "dialogue"},
    "units": [
        {
            "id": stable_id,
            "source": source,
            "protection": [
                {"start": start, "end": start + len(b"{name}"), "kind": "placeholder", "ordered": False}
            ],
            "context": {"speaker": speaker, "scene": scene},
        }
        for stable_id, speaker, scene in records
    ],
}
sys.stdout.buffer.write(json.dumps(document, ensure_ascii=False).encode("utf-8") + b"\n")
```

v1の入力はContext Packなしで利用できます。Context Pack本文は別の資産に置きます。ゲーム固有のバイナリ情報、元ファイルとIDの対応表、再構築と最終ゲーム形式の検証は外部ツールが扱います。

## 外部ツールでのID照合例

抽出時に保持した `records` と公開済みの `translated.json` を使います。全IDの検査を終えてから、対応する訳文を元レコードに渡します。

```python
with open("translated.json", encoding="utf-8") as translated:
    units = json.load(translated)["units"]

expected_ids = {stable_id for stable_id, _, _ in records}
if len(expected_ids) != len(records):
    raise ValueError("duplicate source ID")

translations = {}
for entry in units:
    stable_id = entry["id"]
    if stable_id not in expected_ids or stable_id in translations:
        raise ValueError(f"unexpected or duplicate translated ID: {stable_id!r}")
    translations[stable_id] = entry["translation"]
if translations.keys() != expected_ids:
    raise ValueError("missing translated ID")

matched = [(stable_id, translations[stable_id]) for stable_id, _, _ in records]
```

アダプター検証は `CGO_ENABLED=0 GOTOOLCHAIN=local go test ./internal/adapter/genericjson` で実行できます。実ファイル・SQLiteを使う結合検証は、公開要件を満たすext4またはBtrfs上の `TMPDIR` を指定して `CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 -v -run 'TestGenericJSON' ./internal/cli` で実行できます。結合検証の推論は固定のテスト実装を使います。
