# Yakuoriの翻訳モデル選定

調査日・メインPC向け資源条件の更新日: 2026年11月5日。実装の確認対象は main の [a3742255](https://github.com/sh4869221b/yakuori/commit/a3742255fb93667e4285ecff61dd086465900ad8)。対象は、ローカルで行う英語から日本語へのゲーム文章・UIの翻訳です。

## 推奨

**メインPCの32 GB RAM・RTX 4070 12 GBを前提に、比較・受入れの上限をworker RSS 24 GiB、サンプルGPUメモリ10 GiBとする。最初の同系列比較はIndex-Translate-9B Q4_K_M、日英専用の主比較はCAT-Translate-3.3b正式版とする。**

2026年11月5日の「メモリ使用量上限はメインpcに合わせて下さい」という依頼に基づき、OS・デスクトップ等の余裕を見込んで24/10 GiBを選定しました。具体値は依頼を実行するための設計値で、ユーザーが24/10を直接指定したという記録でも、当該モデルの実測合格でもありません。旧5/3 GiBを前提にCAT-1.4bを優先した判断を更新します。

Index-9Bは現行2Bと同じQwen3.5系列で、[公式比較][index-9-card]では同じ評価内の翻訳・指示追従に改善が見られます。CAT-3.3bは英日翻訳に特化した別系列の主比較候補です。CAT-1.4bは軽量・速度の比較対照として残します。現在のIndex-2Bを基準に、同じ原文・辞書・品質基準と新しい資源条件で採否を決めます。新モデルの実推論・GPU計測・56行の人手採点は未実施です。

[Issue #87](https://github.com/sh4869221b/yakuori/issues/87)の順序に従い、まず現行モデルで辞書接続・限定した保護・必要な指示や文脈を切り分けます。モデルの変更だけで、通常経路に渡っていない用語集や不足した文脈が補われるとは考えません。翻訳精度と人手意味品質の改善は[Issue #1](https://github.com/sh4869221b/yakuori/issues/1)でv1後に位置付けられており、この更新でv1の完了条件や品質基準を変更しません。

## 現行実装と維持する条件

現在の実装はGo 1.27.3、Pure Go / CGO=0、goinferとgoinfer/cuda v0.23.0です。Rustやmistral.rsを導入済みとする前提では選びません。通常処理はオフラインで、CPUとCUDAを明示選択し、生成途中に切り替えません。[依存pin][project-go-mod]、[現行loader][project-load]を参照してください。

| 項目 | 比較で維持する条件 |
| --- | --- |
| 主用途 | .w3strings version162の英語入力から日本語出力。短いUIに加え、用語・台詞・長文・保護文字列を評価 |
| 正式評価セット | fixture-candidates-v2 / criteria-candidates-v4、3つのfresh出力、全56行・6カテゴリ |
| 処理時間 | 各fresh fixtureの measurement_wall_seconds が60秒以内 |
| メインメモリ | 各workerのRSS high-waterが24 GiB（25,769,803,776 bytes）以内 |
| GPUメモリ | 選択GPU・worker PIDに帰属するサンプル最大値が10 GiB（10,737,418,240 bytes）以内 |
| 現行生成条件 | 実効context 4,096、最大出力2,048 tokens、1 request 30秒、greedy、temperature 0、seed 0、thinking無効 |
| 安全性 | 全unitと最終artifactの検証、独立reader、完全一致の翻訳メモリと再検証、部分公開・自動再生成なし |

この予算は同時に1モデル・1推論workerを動かす条件です。主機のRAMにはおおむね7〜8 GiB、GPUメモリには約2 GiBの余裕を見込みますが、実際の空き容量は他のアプリの使用量に依存します。実行前に利用可能RAM・空きGPUメモリを確認し、別のLLMやゲームを同時実行して予算を重ねないようにします。

時間は3ファイルの合計や平均で判定しません。GPUメモリもGPU全体の使用量や連続観測した真のピークではありません。欠測・部分観測を合格にせず、超過は記録します。24/10 GiBは比較・受入れの上限であり、メモリ確保やプロセスを強制的に制限する設定ではありません。詳細は[現在のcriteria][project-criteria]と[受入れ手順][project-acceptance]が基準です。

旧criteria-candidates-v3は[criteria-v3.json][project-criteria-v3]へそのまま保存します。2026年11月3日の実測や旧レビュー票はv3のまま扱い、v4へversionを書き換えません。新しいモデル比較はv4に結び付けた新しいrun・レビュー票を作成します。60秒、56行・6カテゴリ、意味品質・用語・構造検証の条件は維持します。

### 現行Indexの基準値

既存のモデル実体は IndexTeam/Index-Translate-2B-GGUF の Index-Translate-2B.Q4_K_M.gguf、1,312,164,352 bytes、SHA-256は次です。

~~~text
044b313d29342bd3b2c77cbb64023ca0d209bd9b3247763f9d162767ef2d746a
~~~

[2026年11月3日の実測][project-gpu-run]では、RTX 4070上の3つのfresh実行が5.364〜6.057秒、worker RSSが3,737,423,872〜3,941,343,232 bytes、サンプルGPUメモリが1,868,562,432〜1,879,048,192 bytesでした。runtime computeはint4、観測されたKV精度はf32です。機械検証・公開結果・SQLite照合の成功は、意味品質の合格を表しません。

この記録では上流モデルの不変revisionが未記録です。既存GGUFのSHA-256は基準として保持し、現行公式モデルカードの評価checkpointと同じ重み版だと推定しません。新しい候補では上流revision、変換・量子化元、ファイルのsize・SHA-256を揃えて記録します。

## 候補の比較

以下の「対応」は、ファイル登録から翻訳までの製品経路を含みます。アーキテクチャの系列が推論ライブラリにあることだけでは、Yakuori対応済みとはしません。

| 候補 | 役割 | 強みと根拠 | 現行Yakuoriへの適合性 |
| --- | --- | --- | --- |
| [Index-Translate-2B][index-card] | 運用基準・比較対照 | 翻訳専用。用語・文体・構造などの制約を扱う。Apache-2.0 | 固定GGUFのCPU/CUDA経路と資源実測がある。意味品質は別判定 |
| [Index-Translate-9B][index-9-card] | **最初の同系列比較** | 現行2Bと同じQwen3.5系列。用語・構造・文脈の制約を扱う。Apache-2.0 | 公式Q4_K_Mを優先。系列の基礎対応はあるが、対象template/tokenizerと24/10 GiB・60秒の実測が必要 |
| [CAT-Translate-3.3b][cat-33-card] | **日英専用の主比較** | 日英特化、Sarashina2.2系、MIT。英日方向の評価がある | 正式版GGUFの固定とSarashinaテンプレート・tokenizer対応が必要。10 GiB条件で実測する |
| [CAT-Translate-1.4b][cat-14-card] | 軽量・速度の比較対照 | 同じ日英特化系列。MIT | CAT-3.3bとの品質・速度・資源差を比較する。小型であることだけでは標準にしない |
| [Hy-MT2-1.8B][hymt-card] | 推論基盤の対応後に比較 | 日本語を含む多言語翻訳。用語・文脈・文体・構造保持の指示例と公式GGUF。Apache-2.0 | hunyuan_v1_dense / Hunyuanの登録をgoinfer v0.23.0で確認できない。アーキテクチャ対応から必要 |
| [TranslateGemma-4b-it][tg-card] | 条件付きの追加対照 | 翻訳専用、Gemma 3系、Gemma利用条件 | 専用翻訳テンプレートが必要。公式の総入力2K、User/Assistantのみという条件を考慮する |

Hy-MT2は[公式プロンプト例][hymt-repo]に用語・文脈の扱いが明記されている点で有望です。ただし、[goinferのarchitecture registry][runtime-registry]と[GGUF loader][runtime-gguf]にはHunyuanの分岐がなく、GGUFの存在だけでは使えません。今回の第一候補にはしません。

TranslateGemmaの標準入力では、翻訳元・先の言語コードと翻訳本文を専用形式で渡します。既存のsystem指示・用語集を通常のGemmaテンプレートへ流すだけでは同じ条件になりません。拡張プロンプトを試す場合も、公式入力を再現する対照と分けて扱います。

新しい予算では9B級の量子化モデルも比較対象に含めます。初回の主比較はIndex-9BとCAT-3.3bに絞り、具体的な未達原因が残る場合に追加候補を広げます。パラメーター数、公開順位、MoEのactive parameter数だけで、必要メモリやゲーム翻訳の優劣を決めません。

## 公開評価から判断できること

### CATを日英専用の主比較にする理由

CyberAgentの[公式比較表][cat-14-card]では、英語から日本語への平均BLEUが次のように報告されています。

| モデル | 英日平均BLEU |
| --- | ---: |
| CAT-Translate-1.4b | 34.19 |
| CAT-Translate-3.3b 正式版 | 34.80 |
| TranslateGemma-4b-it | 26.76 |
| TranslateGemma-12b-it | 28.68 |

これは同じ公開表における作者の評価で、業務対話・医学・金融・特許などが対象です。BLEUは参照訳との一致を測る指標であり、この数値を「正解率」とは読みません。Indexはこの表に含まれず、ゲーム台詞・固有名・保護文字列の評価でもありません。英日用途に近い候補を絞る根拠として使用します。

3.3bは1.4bよりこの平均が0.61高く、新しい10 GiB予算では旧3 GiB制限を理由に後回しにしません。3.3bを日英品質の主比較とし、1.4bで速度と品質の差を確認します。公開評価の差だけでゲーム翻訳の標準モデルを確定しません。

### Index-9Bを最初の同系列比較にする理由

[Index-9Bの公式カード][index-9-card]では、同一作者の比較でFLORES COMET-22が9B 0.8789 / 2B 0.8655、IFMTBenchの指示追従指標が9B 0.8760 / 2B 0.7584です。多言語集計であり、CATとの英日ゲーム翻訳の直接比較ではありませんが、現行モデルと近い構成で改善幅を調べる候補になります。

[公式GGUF][index-9-gguf]のQ4_K_Mは配布表で5.78 GBです。これは保存ファイルの容量で、実行時のGPUメモリやロード時RSSではありません。対象GGUFのchat_template全文、tokenizer、終了トークン、thinking無効化、実効CUDA経路を確認し、24/10 GiBと60秒に収まるか測定します。同じモデル系列という理由だけで現在のloaderに受理されるとは断定しません。

### Index-2Bを比較基準として維持する理由

[Indexの公式カード][index-card]は、同一評価内のFLORES COMET-22でIndex-Translate-2Bを0.8655、Hy-MT2-1.8Bを0.8522と報告しています。これは多言語集計であり、英日ゲーム翻訳の順位ではありませんが、Hy-MT2へ替えれば自動的に改善するという根拠にもなりません。

CATのBLEUとIndexのCOMETを並べて順位付けしません。既存Index、CAT、Hy-MT2のどれがYakuoriで最適かは、同じ原文・辞書・保護条件・評価基準で判断します。

## CATの導入前に確認すること

### 1. 正式版と配布実体を固定する

[CAT-1.4bの公式ファイル][cat-14-files]と[CAT-3.3bの公式ファイル][cat-33-files]はsafetensors配布です。Yakuoriは現在GGUFを扱うため、正式版を元にしたGGUFの調達または変換が必要です。3.3b-betaの配布を正式版3.3bの評価に混ぜません。

上流revision、tokenizerとtemplate、変換ツールのrevision・オプション、量子化方式、成果物size・SHA-256を固定します。第三者の量子化版を使う場合は配布者を区別し、モデル作者の公式GGUFと表示しません。変換用ツールを使う場合も、製品のPure Go / CGO=0やオフライン実行の契約を変更しません。

### 2. Sarashinaの入力形式を正しく実装する

公式の基本指示は、EnglishとJapaneseを指定した翻訳指示をuserメッセージに入れ、Sarashinaの会話テンプレートを適用する方式です。単一ターンの構造は概念的に次の形です。

~~~text
<|user|>{翻訳指示と原文}</s><|assistant|>
~~~

[公式tokenizer設定][cat-tokenizer]ではBOSの自動追加は無効です。Llama系列という理由でLlama 3用のテンプレートを流用しません。goinfer v0.23.0の[chat.Detect][runtime-chat]にもこのSarashina形式の分岐はありません。

さらに[現在のYakuori loader][project-load]は既知Qwen/Indexのchat_template全文との一致を要求します。未知テンプレート拒否を解除せず、必要なCAT形式だけを明示的に扱います。これは現行models import / pullでそのまま登録できる候補という意味ではありません。

公式tokenizerと生成前のtoken ID列、空白・改行、日本語、BOS/EOS・終了判定を照合します。本文中の制御トークンに似た文字列を、会話の指示や終了として誤って解釈しないことも確認します。実際に描画した辞書・保護・文脈と同じ入力をtoken計数に使い、model/template/tokenizer/prompt/policyのidentityを翻訳メモリへ反映します。

### 3. CUDAの実動作を確認する

[1.4bのconfig][cat-14-config]はhead_dim 112、[3.3bのconfig][cat-33-config]は160です。goinfer v0.23.0の[CUDA prefill][runtime-prefill]では、fused attentionは64/128向けで、その他のサイズは一般のattention経路を使います。

これは直ちにCUDA非対応という意味ではありません。同時に、Indexや通常のhead_dimでの速度をCATへ流用もできません。ロード、生成、実効CUDA・resident状態、終了・取消し、最終出力まで確認し、既存runnerで時間・RSS・GPUメモリを測ります。

### 4. CATのメモリ概算

次は、公式configから計算した**資源比較のための概算**です。バッチ1、全layerで4,096 tokens分のK/Vをf32で保持する場合を仮定しています。

KV bytes = 2 × layers × tokens × KV heads × head dimension × 4 bytes

| 候補 | configから計算したKV容量 | 公称パラメーター数を4bitだけで保持した理想値 | 2項目の単純合計 |
| --- | ---: | ---: | ---: |
| CAT-Translate-1.4b | 0.65625 GiB | 約0.65 GiB | 約1.31 GiB |
| CAT-Translate-3.3b | 1.25 GiB | 約1.54 GiB | 約2.79 GiB |

量子化scale、embeddingなどを4bitより高精度で保持する場合の差分、計算buffer、CUDA管理領域などを加える前の値です。公称モデル規模も丸め値であり、実際の割当てや常駐量を表しません。ロード時のCPU側の保持・変換によるRSSも別です。新しい10 GiB予算では、この概算を3.3bの除外理由にはしません。正式版の実体を使ったロード時RSS・実効GPUメモリ・速度の確認が必要です。

KV精度やcontextを変更した別条件を測る場合は、変更を明記し、出力予約や本文を黙って削らず、翻訳profileと資源・品質の証拠を更新します。Indexで観測したf32 KVを、CATでも既に確認した値として扱いません。

## 配布ファイルの量子化と実行精度を分ける

[goinferの量子化方針][runtime-quant]と[GGUF loader][runtime-gguf]では、配布GGUFの型と実行時の型は別です。Yakuoriが現在使うint4経路では、読み取った重みを実行用へ再量子化します。

| 比較する入力ファイル | 現在のYakuoriの実行設定 | 比較の意味 |
| --- | --- | --- |
| Q4_K_M | int4 | 現行の基準 |
| Q6_K / Q8_0 | int4 | 元ファイルの量子化を変えた比較。8bit実行ではない |
| 高精度な元重みとint8等 | 現行loaderではint4以外を拒否 | 推論設定・profile・資源条件も扱う別の実装と評価が必要 |

量子化精度を上げれば誤訳が直るとは仮定しません。まず同じmodel revisionと入力形式で比較し、配布量子化と実効compute/KV精度を別々に記録します。入力ファイルが失った情報を、実行精度を上げるだけで復元できるとは考えません。

## 比較を進める順序と採用条件

1. **現行Indexの基準を固定する。** Issue #87の不合格票と試行差分を照合し、辞書・保護・指示・文脈の問題を切り分ける。変更後のIndexを比較対照にする場合も、その出力・profileを新しく記録する。
2. **Index-9BとCAT-3.3bの最小限の対応を確認する。** Index-9Bは公式Q4_K_Mと既存loaderの一致を先に確認する。CAT-3.3bは正式版GGUFとSarashinaテンプレートの対応を確認する。両方でtokenizer、終了理由、明示CPU/CUDAの共通契約を確認し、既存の保護・辞書・翻訳メモリ機構を再実装しない。
3. **同じ意味条件で比較する。** 原文・辞書・保護・使用文脈・資源基準を揃え、各モデルの公式形式に沿った入力を作る。テンプレートとtokenizerが違うためtoken数まで同じになるとは仮定しない。モデルと必要な入力形式を一つの候補として記録し、追加の指示調整や量子化差は別試行にする。
4. **有望な候補を全56行で確認する。** 各候補は空の翻訳メモリからfresh出力を作り、別のreuse実行も確認する。既存レビュー票を使い、実際の評価者が誤訳・省略・読みやすさ・対象用語を採点する。CAT-1.4bは軽量・速度の対照として必要に応じて比較する。
5. **品質と性能の両方で採否を決める。** 各fresh fixtureの60秒・RSS 24 GiB・サンプルGPUメモリ10 GiBを満たし、共通検証・独立reader・翻訳メモリ再検証と全56行の品質基準を満たすこと。どの候補も満たさなければ置換を確定せず、未達の原因と次の比較を残す。

正式品質判定では、主体・動作・否定・数・条件の重大な変更、命題・指示・制限の重大な省略、理解できない訳文を許容しません。全56行がpassで、6カテゴリの対象行すべてが評価済みの場合だけ合格とします。8503/8504の「信号灯」「夜警」、8506の `<b>`・`</b>`・`{player}` が各1回現れることと元の入れ子、8507の空出力を確認します。未評価・hold・出力不成立をpassへ変換せず、候補間の成功行だけを混ぜません。failとhold・未評価が併存して集計がincompleteになっても、fail件数と理由区分を保持します。[criteria][project-criteria]と[人手評価規則][project-acceptance]を変更せずに使います。

比較記録にはモデル・ファイルの出所、実効backend/精度、template/tokenizer/prompt、辞書・文脈、fixture/criteria版、run/profile/outputの対応、生成時間・RSS・GPUメモリ、機械検証、カテゴリ別の人手結果を残します。旧モデルの測定や採点を新しい出力へ転記しません。

原文・訳文・詳細な評価根拠を含むレビュー票は私有資料として保持し、公開資料は識別子・件数・原因区分・未確認範囲に限定します。

56行のうち49行は実MODの短いUIで、残るカテゴリは自作例で補われています。合格しても実MODの長い会話や大規模MODまで実証したことにはなりません。追加の補助ケースを使う場合は正式セットと別集計にします。

## 参照資料

公開モデルの記述・数値は2026年11月5日の確認内容です。実験開始時にはモデル・推論実装・変換結果を不変revisionとhashで固定してください。

- [現行実装の依存pin][project-go-mod] / [loader][project-load] / [Index用プロンプト][project-index-prompt]
- [受入れcriteria][project-criteria] / [受入れ手順][project-acceptance] / [2026年11月3日GPU実測][project-gpu-run]
- [Index-Translate-2B公式カード][index-card] / [公式GGUF][index-gguf]
- [Index-Translate-9B公式カード][index-9-card] / [公式GGUF][index-9-gguf]
- [CAT-Translate-1.4b公式カード][cat-14-card] / [CAT-Translate-3.3b公式カード][cat-33-card]
- [CAT-1.4b config][cat-14-config] / [CAT-3.3b config][cat-33-config] / [CAT tokenizer][cat-tokenizer]
- [Hy-MT2公式リポジトリ][hymt-repo] / [1.8B公式カード][hymt-card] / [公式GGUF][hymt-gguf]
- [TranslateGemma公式カード][tg-card]
- goinfer v0.23.0: [architecture registry][runtime-registry] / [GGUF loader][runtime-gguf] / [chat.Detect][runtime-chat] / [CUDA prefill][runtime-prefill] / [量子化方針][runtime-quant]

[project-go-mod]: https://github.com/sh4869221b/yakuori/blob/a3742255fb93667e4285ecff61dd086465900ad8/go.mod
[project-load]: https://github.com/sh4869221b/yakuori/blob/a3742255fb93667e4285ecff61dd086465900ad8/internal/inference/goinfer/load.go
[project-index-prompt]: https://github.com/sh4869221b/yakuori/blob/a3742255fb93667e4285ecff61dd086465900ad8/internal/prompt/index.go
[project-criteria]: ../evidence/acceptance/criteria.json
[project-criteria-v3]: ../evidence/acceptance/criteria-v3.json
[project-acceptance]: ../evidence/acceptance/README.md#approved-human-quality-criteria
[project-gpu-run]: ../evidence/acceptance/gpu-db-run-2026-11-03.md
[index-card]: https://huggingface.co/IndexTeam/Index-Translate-2B
[index-gguf]: https://huggingface.co/IndexTeam/Index-Translate-2B-GGUF
[index-9-card]: https://huggingface.co/IndexTeam/Index-Translate-9B
[index-9-gguf]: https://huggingface.co/IndexTeam/Index-Translate-9B-GGUF
[cat-14-card]: https://huggingface.co/cyberagent/CAT-Translate-1.4b
[cat-33-card]: https://huggingface.co/cyberagent/CAT-Translate-3.3b
[cat-14-files]: https://huggingface.co/cyberagent/CAT-Translate-1.4b/tree/main
[cat-33-files]: https://huggingface.co/cyberagent/CAT-Translate-3.3b/tree/main
[cat-14-config]: https://huggingface.co/cyberagent/CAT-Translate-1.4b/blob/main/config.json
[cat-33-config]: https://huggingface.co/cyberagent/CAT-Translate-3.3b/blob/main/config.json
[cat-tokenizer]: https://huggingface.co/cyberagent/CAT-Translate-1.4b/blob/main/tokenizer_config.json
[hymt-repo]: https://github.com/Tencent-Hunyuan/Hy-MT2
[hymt-card]: https://huggingface.co/tencent/Hy-MT2-1.8B
[hymt-gguf]: https://huggingface.co/tencent/Hy-MT2-1.8B-GGUF
[tg-card]: https://huggingface.co/google/translategemma-4b-it
[runtime-registry]: https://github.com/townsendmerino/goinfer/blob/v0.23.0/decoder/registry.go
[runtime-gguf]: https://github.com/townsendmerino/goinfer/blob/v0.23.0/decoder/gguf.go
[runtime-chat]: https://github.com/townsendmerino/goinfer/blob/v0.23.0/chat/chat.go
[runtime-prefill]: https://github.com/townsendmerino/goinfer/blob/v0.23.0/cuda/prefill.go
[runtime-quant]: https://github.com/townsendmerino/goinfer/blob/v0.23.0/docs/quantization.md
