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
| `provenance` | `method`、`version`、`status`、`user_edited`（boolean）、`sources`（optional 配列）、`generation`（optional オブジェクト） |
| `generation` | `model_id`、`model_sha256`、`prompt_version`、`template_family`、`template_source`、`template_renderer_version`、`policy_version` |
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

`generation` を付ける場合は `method: llm`、全識別フィールド、同じ pack 内の unit 出典を 1 件以上指定します。`model_id` はローカルモデル登録の ID、`model_sha256` は実際にロードしたモデル snapshot の digest です。確認・編集に伴う状態変更後も、生成元の識別情報を保持できます。既存の `generation` がない v1 pack の canonical bytes は従来と同じです。`generation` の追加前の strict validator は、このフィールドがある pack を未知のメンバーとして拒否します。

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

## 下書きの生成

`context build` はローカルの [Generic JSON v1](generic-json-v1.md) を取り込み、Context Pack v1 の検証を通した下書きを生成します。指定した言語と名前空間は入力と一致させます。入力は通常ファイルに限り、最終パス成分のシンボリックリンクを拒否します。既定の上限は入力 JSON 全体で 1,495 バイト、unit 数などは [Generic JSON v1 の入力上限](generic-json-v1.md#入力上限) に従います。構造抽出にはモデル、ネットワーク接続、XDG 設定は不要です。

```sh
mkdir -p context-candidates
result=$(./yakuori context build --source-language en --target-language ja \
  --game sample-game --artifact dialogue --output-dir context-candidates \
  internal/contextpack/testdata/build-dialogue-v1.json)
printf '%s\n' "$result"
candidate=$(printf '%s\n' "$result" | sed -n 's/^candidate: //p')
./yakuori context validate "$candidate"
```

成功時は `candidate: <パス>` と `fingerprint: <小文字16進>` を出力します。検証コマンドで同じ fingerprint が得られます。出力先には既存のディレクトリを指定します。候補は同じディレクトリ内の非公開の一時ファイルに書き、書き込み完了とキャンセル確認の後で `context-candidate-*.json` に上書きなしで移動します。確定済み・編集済みの pack を含む既存ファイルは保持します。移動前の失敗やキャンセルでは一時ファイルを削除します。再実行では別ファイルが作られます。構造抽出では、同じ入力・バージョン・設定なら内容と fingerprint は一致します。構造抽出の各レコードは `method: extraction`、`version: context-build-v1`、`status: candidate`、`user_edited: false` で生成されます。内容の確認と採否は利用者が行います。

### 関連付けと出典

unit は、入力の adapter・format schema・stable ID・原文 digest と、artifact の名前空間・digest を保持します。人物と場面は、同じ artifact・adapter・format schema の中で、明示された `speaker` と `scene` の値がそれぞれ一致した場合に共有します。場面の話者集合には、同じ unit が場面と話者の両方を指定した関係を記録します。どちらかが不明な unit は、欠けた参照を省略します。表示名や概要も、取得できる情報がなければ省略します。

Builder が翻訳 unit の `Origin()` から使うキーは `speaker`、`scene`、`quest`、`previous_unit`、`world` です。`quest` は unit の `quest:<値>` タグ、`previous_unit` は同じ artifact・adapter・format schema の stable ID、`world` は明記された本文として扱います。前の unit の参照が未解決、別場面への参照、循環の場合は検証エラーになります。Generic JSON v1 が供給するのは `speaker` と `scene` です。原文の一致や入力配列の順番から関連を補間することはありません。原文やメタデータを更新すると、新しい artifact snapshot の候補を生成します。原文が変わらない unit では原文 digest を維持しますが、候補の artifact digest は更新後の入力全体に対応します。

生成 ID は `種類/名前空間/adapter/format schema/識別子` で、各可変部分をパス成分としてパーセントエンコードします。識別子には unit・world では stable ID、人物・場面では参照 ID を使います。metadata 出典は対象 artifact と、`units/<adapter>/<format schema>/<stable ID>/origin/<キー>` 形式の論理位置を持ちます。元データを確認するときは、`scope.artifacts` の digest に対応する artifact を選び、パス成分を個別にデコードし、stable ID でレコードを探します。例えば fixture の `units/generic-json/generic-json-v1/inn-1/origin/speaker` は、`units[]` で `id` が `inn-1` のレコードの `context.speaker` を指します。人物・場面には寄与した全 unit の根拠を残します。

[生成用 fixture](../internal/contextpack/testdata/build-dialogue-v1.json) はリポジトリの [MIT ライセンス](../LICENSE) 下の合成データです。実データから生成した pack を共有・再配布する場合は、原 artifact と外部ツールが追加したメタデータの出典・ライセンス・利用条件を確認してください。pack には識別子や明示された設定本文が含まれる場合があります。

### ローカルモデルによる候補生成

明示的に `--infer` を指定すると、構造抽出に加え、選択済みのローカル ChatML GGUF モデルで人物・場面・世界設定・用語の候補を作成します。先に登録済みモデルを `yakuori models use MODEL_ID` で選択します。`--backend` の既定値は `cpu` です。`cuda`・`webgpu` は対応するビルドと実行環境で明示的に指定します。

```sh
mkdir -p context-candidates
result=$(./yakuori context build --infer --backend cpu \
  --source-language en --target-language ja --game sample-game --artifact dialogue \
  --output-dir context-candidates internal/contextpack/testdata/infer-dialogue-v1.json)
printf '%s\n' "$result"
candidate=$(printf '%s\n' "$result" | sed -n 's/^candidate: //p')
./yakuori context validate "$candidate"
```

同じ import と構造抽出結果から、canonical unit 順に空白だけの原文を除いて素材を選びます。モデルに渡す原文と話者・場面の metadata はデータ区間内の JSON 文字列です。人物と場面は既存の構造 ID を個別の番号で参照します。prompt は、根拠が弱い場合や矛盾する場合は候補を省略し、同名の別人物を区別するよう要求します。[推論用 fixture](../internal/contextpack/testdata/infer-dialogue-v1.json) には同名の人物、弱い根拠と矛盾する噂、原文と metadata に混入した指示を含めています。

モデル出力は単一の `{"candidates":[...]}` オブジェクトとして検証します。候補には選択済みの unit 番号を 1 件以上求めます。人物と場面の根拠は該当する構造 ID に属する unit に限り、用語の原語は根拠原文に出現する必要があります。候補の生成元・状態・ID は CLI 側で設定し、pack 全体を v1 validator に通します。各 LLM 候補は `method: llm`、`status: candidate`、`user_edited: false` です。根拠に基づく記述の正確さは利用者が確認します。

人物と場面の候補は既存 ID と構造上の出典を保持します。世界設定の ID は、根拠のうち canonical 順の最初の unit を使った `world-inferred/<unit ID の種類部分を除いたパス>` です。同じ最初の根拠 unit を共有する世界設定は 1 件にまとめるようモデルに求め、同じ ID に複数の候補がある場合は検証エラーになります。用語候補の ID は `glossary-candidate/<artifact namespace>/<原語>` です。用語の `reference` は `urn:yakuori:glossary-candidate:v1:<原語>/<訳語候補>` として保存します。各成分は個別にパーセントエンコードし、確認時は接頭辞を除き、`/` で分割してから各成分をデコードします。参照は候補のまま保存され、自動確認・必須用語化・翻訳メモリへの反映は発生しません。

同じ作品・言語の既存 pack を `--against PACK` で指定すると、検証のうえ読み取り専用で比較します。確認済み・手動編集済みレコードとの ID 衝突、同じ artifact・adapter・format schema・stable ID の unit 衝突、共有 artifact namespace の digest 不一致を、成功時に両 pack の canonical 位置で報告します。候補は毎回別ファイルに保存します。

素材 JSON とモデル応答はそれぞれ最大 16 KiB です。テンプレート適用後に token を数え、実効 context と 4096 token の小さい方から出力枠 2048 token を引いた値を入力の上限とします。要求は 30 秒、モデルの open から保存直前までは 300 秒の協調的な制限です。上限到達、キャンセル、生成停止理由の異常、応答の破損や候補 1 件でも不正な場合は新しい pack の保存に失敗します。空の候補配列は構造抽出の結果を保存します。

`prompt_version` は `context-infer-v1`、`policy_version` は `context-infer-policy-v1` です。`template_source` の `gguf:tokenizer.chat_template` は、モデル digest が固定する GGUF 内のテンプレート位置です。再現を試す場合は入力 bytes、モデル snapshot、prompt、template renderer、policy、backend・実行環境を揃えます。greedy 生成を使用し、backend や実行環境をまたいだ候補の一致は保証しません。
