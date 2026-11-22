# Context Pack v1

Context Pack は、翻訳データとは別に、作品の設定・登場人物・場面・unit との対応を保存する JSON 形式です。参照先を明示することで、複数の artifact や MOD が同一作品の設定を共有できます。`internal/contextpack.Read` は検証済みの Go データ、canonical JSON、pack 全体の SHA-256 fingerprint を返します。

## フィールド

表の並びが各オブジェクトの canonical JSON のフィールド順です。`optional` の項目は省略できます。文字列の ID・namespace・タグ・出典・説明等は大小文字を区別し、空文字・空白のみの値・NUL を拒否します。文字列自体の値は変換しません。unit の `adapter`・`format_schema`・`stable_id` は既存の `unit.UnitID` と同じ空でない opaque 文字列です。

| オブジェクト | フィールド（宣言順） |
| --- | --- |
| ルート | `schema`（`yakuori.context/v1`）、`scope`、`source_language`、`target_language`、`world` / `characters` / `scenes` / `units` / `glossary_refs`（optional 配列） |
| `scope` | `game_namespace`、`artifacts`（optional 配列） |
| `artifacts[]` | `artifact_namespace`、`original_artifact_digest` |
| `world[]` | `id`、`text`、`artifacts` / `tags`（optional 文字列配列）、`provenance` |
| `characters[]` | `id`、`name` / `description`（optional 文字列）、`artifacts` / `tags`（optional 文字列配列）、`provenance` |
| `scenes[]` | `id`、`summary`（optional 文字列）、`artifacts` / `speaker_ids` / `tags`（optional 文字列配列）、`provenance` |
| `units[]` | `id`、`artifact_namespace`、`adapter`、`format_schema`、`stable_id`、`raw_source_digest`、`scene_id` / `speaker_id` / `previous_unit_id`（optional 文字列）、`tags`（optional 文字列配列）、`provenance` |
| `glossary_refs[]` | `id`、`schema`（`glossary-v1`）、`reference`（利用側が解釈する文字列）、`provenance` |
| `provenance` | `method`、`version`、`status`、`user_edited`（boolean）、`sources`（optional 配列） |
| `sources[]` | `kind` に続いて、下表で `kind` に指定された文字列フィールド |

言語は登録済み言語に任意の script と region を足した BCP 47 の範囲を受理します（例 `en`、`zh-Hant-TW`、`es-419`）。`golang.org/x/text/language.Parse` の結果と大小文字を除いて一致することを要求します。variant、extension、private-use は v1 の範囲外です。大小文字も含め元の綴りを保存し、同一言語の指定を許可します。構造 metadata だけで人物・場面を参照する場合は、分かっている情報だけを記録し、`name`・`description`・`summary` の未取得項目を省略できます。用語集の `reference` は外部参照として保存します。検証時のアクセスや用語集ルールの解釈は発生しません。

## Scope と unit の参照

- `game_namespace` と `artifact_namespace` は呼出し側が決めます。1 pack で 1 作品を扱い、各 artifact namespace は原 artifact の 1 snapshot を指します。digest は既存の [SourceIdentity](source-profile-identity.md#source-record-in-wire-order) と同じ、非ゼロ SHA-256 の小文字 16 進 64 桁です。`original_artifact_digest` は原 artifact 全体、`raw_source_digest` は変換前の unit 原文を識別します。
- `id` は artifact namespace と独立した pack 内の ID です。`world`、`characters`、`scenes`、`units`、`glossary_refs` を通じて一意です。unit は、登録された artifact snapshot と `adapter`・`format_schema`・`stable_id`・`raw_source_digest` の組合せで識別します。1 artifact 内で同一の `adapter`・`format_schema`・`stable_id` を繰り返すと、digest の一致・不一致にかかわらず失敗します。別の stable ID や別の artifact は同じ原文でも区別されます。
- `world`、`characters`、`scenes` の `artifacts` は適用先の明示リストです。空または省略なら pack 内の情報として保持し、特定の artifact に結び付きません。unit の `scene_id`・`speaker_id` は種類が合い、かつ当該 unit の artifact を明示したレコードを指します。
- 場面の `speaker_ids` に列挙した各人物は、その場面の全 artifact を列挙します。場面と unit の両方が話者を指定する場合、unit の話者は場面の話者集合に含まれます。`speaker_ids` が空または省略の場面にも、unit が個別の話者を設定できます。
- `previous_unit_id` は同一 artifact の別 unit を指します。両側が場面を指定した場合は同じ場面を指し、前の unit をたどる経路に循環を許しません。出典の参照はこの関係と独立します。原文の一致、同じ tag、配列での隣接位置から関係を推定しません。

## 出典と確認状態

各レコードに `provenance` を付けます。`method` は `manual`、`extraction`、`llm` のいずれかで、`version` は作成手段のバージョンです。`status` は `confirmed` または `candidate` です。`confirmed` には出典が 1 件以上必要です。`candidate` は出典なしで保持できます。`user_edited` はユーザーが編集したかを示し、`false` も明記します。

| `kind` | 続くフィールド | 意味 |
| --- | --- | --- |
| `artifact` | `artifact_namespace` | `scope.artifacts` の原 artifact snapshot |
| `unit` | `unit_id` | 同じ pack の `units[].id` が指す完全な unit 参照 |
| `metadata` | `artifact_namespace`、`metadata_key` | 登録した原 artifact 内の metadata キー |
| `manual` | `reference` | 手作業で記録した根拠の参照文字列 |

各出典は該当種別に列挙したフィールドだけを持ちます。`metadata_key` や手動参照先の実在は編集者が確認します。pack の検証で確認するのは構造と pack 内の参照整合性です。`confirmed` と `user_edited` は pack の申告として保持します。読込みや正規化による状態の昇格はありません。将来の自動更新では `confirmed` または `user_edited: true` のレコードを保持し、新しい提案は別の `candidate` として扱います。人が確認・編集した内容の変更には明示的な編集が必要です。

## 正規化と上限

入力は UTF-8 の単一 JSON オブジェクトです。メンバー名は大小文字を区別し、未知のメンバー・schema、重複キー、`null`、余分な値、不正な UTF-8 や単独 surrogate を拒否します。各配列は重複を許さない集合です。

- input と canonical JSON はそれぞれ最大 2 MiB。配列要素は全階層を合計して最大 4096 件。キーと値の文字列はデコード後それぞれ最大 16 KiB。オブジェクトと配列のネストはルートを 1 として最大 8。
- `scope.artifacts` は `artifact_namespace`、各レコード配列は `id`、文字列配列は文字列自身の UTF-8 バイト順に整列します。出典は `(kind, artifact_namespace, unit_id, metadata_key, reference)` を同じバイト順で比較し、省略フィールドは空として比較します。
- 空の optional 配列は出力から省きます。canonical JSON は上表の順で、区切りの空白や末尾改行を加えません。UTF-8 の文字列は標準の Go `encoding/json` のエスケープを使い、`"`・`\`、U+0000–U+001F、`<`・`>`・`&`、U+2028・U+2029 をエスケープします。改行コード、Unicode の合成・分解、大小文字、空白など、デコードした文字列の内容は保持します。
- fingerprint は canonical JSON の SHA-256 です。将来の Resolver は使用した context の digest を別に計算します。

## 検証コマンド

`context validate` はローカルの通常ファイルを検証します。最終パス成分のシンボリックリンクは拒否します。成功時は exit 0 で stdout に `fingerprint: <小文字16進>` を 1 行出力します。読み込み・検証の失敗は exit 1、使用法のエラーは exit 2 です。stderr の診断には理由と位置を使い、パス、入力キー・値、本文は出力しません。モデルや XDG 設定は不要です。

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go build ./cmd/yakuori
./yakuori context validate internal/contextpack/testdata/minimal-v1.json
./yakuori context validate internal/contextpack/testdata/dialogue-v1.json
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./internal/contextpack ./internal/cli
```

[最小 fixture](../internal/contextpack/testdata/minimal-v1.json) は scope と言語だけの pack です。[会話 fixture](../internal/contextpack/testdata/dialogue-v1.json) は複数の場面・話者・artifact、同一原文の独立した unit、手動確認と未確認候補、外部用語集参照を含みます。
