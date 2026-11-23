# xTranslator XML v2 の外部変換サンプル

LootMan 3.4.0 の Fallout 4 向け英日辞書を [中立辞書 JSON v1](../../docs/neutral-dictionary-v1.md) へ変換する、Yakuori 本体とは別の Go コマンドです。XML を標準入力から読み、`dictionary.Catalog.Import`（内部で `dictionary.Read` を実行）に JSON を渡します。作品・artifact・言語対・原文で `References` を照会し、各行の別個の ID と訳文を含む参照候補を照合してから、検証済みの JSON を標準出力に、件数と診断を標準エラーに出します。

## 固定エクスポートと利用条件

- 対象は LootMan 3.4.0 のコミット `d0efa1e08ff20db7940f1ebeecdbc286097c1743` に含まれる `translation/Lootman_en_ja.xml` です。作者のパッケージ処理はこの辞書を xTranslator の XML エクスポートとして読み込み、プラグインの翻訳に使います。ファイルは UTF-8 BOM 付きで、`SSTXMLRessources/Params` に `LootMan.esp`、`en`、`ja`、形式版 `2` を記録しています。固定コミットの GitHub から XML 単体を入手できます。[固定 XML][lootman-xml]、[作者の読み込みコード][lootman-reader]、[LootMan 3.4.0 ソース][lootman-version]
- 翻訳ファイルには翻訳ディレクトリ固有の MIT ライセンスが適用されます。利用・改変・再配布が許可され、複製や実質的な部分を配布する場合は著作権表示（Copyright (c) 2021-2022 EideeHi）と許諾表示を含めます。変換後の辞書を配布する場合も同ライセンス全文を添付してください。実 XML と変換済みの実辞書はこのサンプルに収録していません。[翻訳 README][lootman-translation]、[翻訳 LICENSE][lootman-license]、[リポジトリ内のライセンス区分][lootman-licensing]
- 形式は xTranslator 1.6.0 のコミット `9aa38d60860273401f8bb0dd1557c82a295c41b3` にある XML 出力処理と照合しました。LootMan の XML を生成した実行ファイルの版は未確認です。xTranslator 作者は Nexus での無償提供を案内し、別の場所への再アップロードを控えるよう求めています。この手順には xTranslator の実行ファイルは不要です。[XML 出力処理][xtranslator-writer]、[xTranslator 公式 README][xtranslator-readme]

## 変換する項目

| XML と実行時の指定 | 中立 JSON v1 |
| --- | --- |
| `Params/Addon` とプロファイルの作品 | `scope.artifact` と `scope.game` |
| `Params/Source` と `Params/Dest` | `source_language` と `target_language` |
| `String/Source` と `String/Dest` | `source` と `target`。XML として解釈した文字列をそのまま渡す |
| `String@List`、`String/EDID`、`String/REC`、任意の `REC@id` | この順の文字列を JSON 配列にし、その配列の JSON 表現を `unit.stable_id` に記録する。省略した `REC@id` と `"0"` は要素数で区別する |
| 辞書名・作品・artifact・原文言語・訳文言語、続けて上記の行 ID の各要素 | 1つの JSON 配列の文字列表現を辞書項目の `id` とする。行順や訳文を変えても同じ ID になり、同じ ID を持つ複数行はエラーになる |
| 選択したプロファイル | `provenance` に出典、`license` に `MIT` と `allowed` を記録する |
| `String@Partial` | 値 `1`（`incompleteTrans`）と `2`（`lockedTrans`）は中立 JSON v1 で表現できないため、行番号・フィールド・状態を報告し、参照候補として変換する |

全行を `kind: translation`、`confirmation: unconfirmed` とします。XML に対応する場面・話者 ID がないため、それらは空欄です。`unit.format_schema` は `xtranslator-xml-v2`、`unit.adapter` は空欄とし、原文 ID はエクスポート内の識別子として保持します。ローカル採用、確定訳への昇格、推論や TM への保存はこの手順の対象外です。

`example` は同梱の自作入力に、`lootman` は上記コミットから取得した XML に使います。呼び出す前に手元のファイルの出典を確認してください。コマンドが照合できるのは選択したプロファイルの artifact、言語対、形式版です。これらが一致する別ファイルにも同じ出典と許諾が付くため、プロファイルを別データへ流用しないでください。

## 再現手順

Yakuori リポジトリのルートで実行します。自作 XML には、原文が同じで `REC@id` の有無と訳文が異なる行、XML エスケープ、プレースホルダー、空訳が含まれます。

```sh
export CGO_ENABLED=0 GOTOOLCHAIN=local
sample_dir=$(mktemp -d)
go run ./tools/xtranslator-example example \
  < tools/xtranslator-example/testdata/example.xml > "$sample_dir/example.json"

curl --fail --location --output "$sample_dir/Lootman_en_ja.xml" \
  https://raw.githubusercontent.com/eideehi/fo4-lootman/d0efa1e08ff20db7940f1ebeecdbc286097c1743/translation/Lootman_en_ja.xml
go run ./tools/xtranslator-example lootman \
  < "$sample_dir/Lootman_en_ja.xml" > "$sample_dir/Lootman_en_ja.json"
```

| 入力 | 確認結果 |
| --- | --- |
| 自作 XML | 成功。2種類の原文、3件の別個の参照 ID。状態の診断2件 |
| LootMan 3.4.0 の固定 XML | 成功。60種類の原文、71件の別個の参照 ID。うち50件が `REC@id` 付き。ロック状態の診断6件 |

## 既存 CLI の評価と対象範囲

`xmllint --nonet` の XPath で、固定 XML の行数（71）と `REC@id` 付きの件数（50）を確認できます。`jq` は抽出済みの項目を JSON へ組み立てる用途に使えます。

```sh
xmllint --nonet --xpath 'count(/SSTXMLRessources/Content/String)' "$sample_dir/Lootman_en_ja.xml"
xmllint --nonet --xpath 'count(/SSTXMLRessources/Content/String/REC[@id])' "$sample_dir/Lootman_en_ja.xml"
jq -n -c '["0","EX_Notice","MESG:DESC"],["0","EX_Notice","MESG:DESC","0"]'
```

既存 CLI で抽出と整形は可能です。この手順では複数行の文字列の受け渡し、欠落属性と `0` の区別、未対応フィールドの位置付き診断、Go の参照取込みまでを一度に実行するため、追加のコマンドや中間形式を必要としない Go のサンプルを採用しました。

入力は固定サンプルにある要素順の UTF-8 XML v2 に限ります。上限は XML 2 MiB、4,096行で、中立 JSON の上限は本体の validator が適用します。未知のフィールドや属性、DTD、想定外の入れ子は位置と想定フィールドを示してエラーにします。`sID`、`REC@idMax`、音声情報を含む XML の派生形式、`.sst` バイナリ、他のゲーム・版のエクスポートは未検証です。

[lootman-xml]: https://github.com/eideehi/fo4-lootman/blob/d0efa1e08ff20db7940f1ebeecdbc286097c1743/translation/Lootman_en_ja.xml
[lootman-reader]: https://github.com/eideehi/fo4-lootman/blob/d0efa1e08ff20db7940f1ebeecdbc286097c1743/packaging/scripts/translation-xml.ts#L1-L3
[lootman-version]: https://github.com/eideehi/fo4-lootman/tree/3.4.0
[lootman-translation]: https://github.com/eideehi/fo4-lootman/blob/d0efa1e08ff20db7940f1ebeecdbc286097c1743/translation/README.md
[lootman-license]: https://github.com/eideehi/fo4-lootman/blob/d0efa1e08ff20db7940f1ebeecdbc286097c1743/translation/LICENSE
[lootman-licensing]: https://github.com/eideehi/fo4-lootman/blob/d0efa1e08ff20db7940f1ebeecdbc286097c1743/README.md#licensing
[xtranslator-writer]: https://github.com/MGuffin/xTranslator/blob/9aa38d60860273401f8bb0dd1557c82a295c41b3/TESVT_XMLFunc.pas#L95-L151
[xtranslator-readme]: https://github.com/MGuffin/xTranslator/blob/9aa38d60860273401f8bb0dd1557c82a295c41b3/README.md
