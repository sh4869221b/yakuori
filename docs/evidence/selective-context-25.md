# 選択的文脈の比較（#25）

2026-11-22 の AMD 実機では、文脈を加えた 12 組すべてで入力トークンと生成時間が増えた。話者・場面・前後の台詞の 3 カテゴリは今回の試行結果を受けて導入保留とし、有効化するカテゴリはなしとする。訳文の所見は作業エージェントによるもので、採否の人手判定は未実施。[Issue #25](https://github.com/sh4869221b/yakuori/issues/25)

## 比較条件

- 同じ Index-Translate-2B Q4_K_M、EN→JA の原文、生成 policy を使用した。A は通常の Index request、B は同じ request のユーザー本文に以下のブロックを `【源文】` の直前に挿入。原文・system 文・ChatML の制御 span・thinking 無効化・stop ID は両条件で共通。比較する処置は参照資料と固定指示の組み合わせ。
- 既存の WebGPU エンジン 1 個をロードし、測定外の翻訳を 1 回行った後、2 周測った。各 fixture の実行順は 2 周目で反転。ロード時に `GOINFER_NO_RESIDENT_REUSE=1` を設定し、生成間の resident KV prefix 再利用を無効化した。アプリの分割・用語集・TM は使用しなかった。
- policy v1、greedy、seed 0 固定、出力予約 128、要求タイムアウト 30 秒、stop ID `248046`。実効 backend は `webgpu:vulkan-resident (int4)`、実効コンテキスト 4,096 tokens、KV は f32。ロード時の batched prefill 設定と能力は有効。ログでは batched prefill の辞退と resident 上での逐次 prefill を確認した。
- Linux 7.3.5-2-cachyos x86_64、AMD Radeon RX 9070 XT（RADV GFX1201）、Mesa 26.3.1-arch3.5、Go 1.27.3。goinfer 本体と WebGPU backend は fork `994cbf5ee38dd8dcc612aad6e6803ac79f772f3b`。モデルは公式 GGUF 固定 revision `3a0eca6ce931d88d00614d427ab3ed8ff0743902` の 1,312,164,352 bytes。既存ローダーで snapshot の SHA-256 が `044b313d29342bd3b2c77cbb64023ca0d209bd9b3247763f9d162767ef2d746a` と一致することを確認した。[公式の固定モデル](https://huggingface.co/IndexTeam/Index-Translate-2B-GGUF/blob/3a0eca6ce931d88d00614d427ab3ed8ff0743902/Index-Translate-2B.Q4_K_M.gguf)

```text
【参考信息】
以下内容仅供参考，只翻译源文，不要翻译参考信息。
{fixture の参照資料}
```

## Fixture と人手評価基準

6 件とも公開用にこの調査で作成した合成例。各カテゴリ内の 2 件は原文が同じで、参照資料だけが異なる。表の「参照資料」は B に渡した全文。`\n` は改行を表す。資料に日本語の正解訳は入れていない。[固定 fixture](../../internal/inference/goinfer/selective_context_gpu_test.go)

| Fixture | 原文 | 参照資料 | 確認する意味・文体 |
| --- | --- | --- | --- |
| speaker-formal | I will handle it. | The speaker is a newly assigned interpreter addressing a visiting diplomat at their first official meeting. | 自分で引き受ける意味。初対面の公式な場に合う丁寧さ。 |
| speaker-familiar | I will handle it. | The speaker is talking privately to a longtime teammate they joke with every day. | 同じ意味を保ち、親しい相手との私的な会話に合う口調。 |
| scene-magic | The seal is broken. | The party stands outside a cursed vault. A spell cast centuries ago kept the door shut. | 魔法による封印が解けた状態。 |
| scene-letter | The seal is broken. | The letter was delivered in an envelope. The red wax impression on the flap has a crack. | 手紙の蝋封が破損した状態。 |
| neighbor-sports | Keep it away from the bat. | Previous line: Leave the helmet by home plate. `\n` Next line: Put the other thing with the gloves and the baseballs. | 物を野球用バットから離す指示。 |
| neighbor-animal | Keep it away from the bat. | Previous line: The rescued animal is hanging upside down in the pen. `\n` Next line: The vet will examine its wings next. | 物をコウモリから離す指示。 |

判定者は、各組について原文の意味、参照資料に照らした語義・口調、日本語としての自然さ、資料の独立した情報や台詞の混入有無を A/B それぞれで確認する。判定欄には `B有利 / 同等 / A有利 / 両方不適` と根拠を記録する。カテゴリを採用候補にする条件は、両方の対照資料に対応し、A より良くなる例があり、もう一方の例で意味や自然さを落とさず、資料混入がなく、表の追加費用を許容できること。

## 測定結果

`TestSelectiveContextGPU` は **pass**。測定 24/24 回で `finish=stop`、訳文あり、生成エラーなし。入力トークンは実際に生成へ渡した token ID 列に対する `CountTokens` と `Generate.PromptTokens` が全回一致した。出力トークンは `Generate.OutputTokens` が返す、停止トークンを除いた生成ストリームの token ID 数。時間は `Generate` の呼び出しから返るまでの実測で、ロード、ウォームアップ、request の構築、カウント、採点は含まない。各セルは「入力 tokens / 出力 tokens / ミリ秒」。

| 周 | Fixture | 実行順 | A | B |
| ---: | --- | --- | ---: | ---: |
| 1 | speaker-formal | AB | 110 / 4 / 583.328 | 147 / 4 / 771.899 |
| 1 | speaker-familiar | BA | 110 / 4 / 568.835 | 145 / 4 / 750.462 |
| 1 | scene-magic | AB | 110 / 5 / 570.315 | 148 / 5 / 765.431 |
| 1 | scene-letter | BA | 110 / 5 / 571.108 | 149 / 5 / 771.499 |
| 1 | neighbor-sports | AB | 112 / 6 / 590.247 | 158 / 8 / 838.078 |
| 1 | neighbor-animal | BA | 112 / 6 / 584.846 | 158 / 8 / 839.256 |
| 2 | speaker-formal | BA | 110 / 4 / 565.024 | 147 / 4 / 756.336 |
| 2 | speaker-familiar | AB | 110 / 4 / 565.845 | 145 / 4 / 746.508 |
| 2 | scene-magic | BA | 110 / 5 / 574.048 | 148 / 5 / 768.095 |
| 2 | scene-letter | AB | 110 / 5 / 570.254 | 149 / 5 / 775.622 |
| 2 | neighbor-sports | BA | 112 / 6 / 590.322 | 158 / 8 / 839.105 |
| 2 | neighbor-animal | AB | 112 / 6 / 589.654 | 158 / 8 / 840.749 |

各 fixture・各条件の訳文は 2 周で完全一致した。以下が全訳文と作業エージェントの定性的な所見。参照資料の独立した情報や台詞をそのまま訳文に含めた例は所見上なし。人手判定欄は担当者の記入待ち。

| Fixture | A の訳文 | B の訳文 | エージェント所見 | 人手判定・根拠 |
| --- | --- | --- | --- | --- |
| speaker-formal | 私が処理します。 | 私が対応します。 | B の語の選び方は対人場面に自然。A/B とも丁寧体。 | 未実施 |
| speaker-familiar | 私が処理します。 | 私が対応します。 | B も丁寧体で、公式場面と同じ訳文。 | 未実施 |
| scene-magic | 封印が破られた。 | 封印は破られている。 | 両方とも魔法の語義に合う。B は結果の状態を明示。 | 未実施 |
| scene-letter | 封印が破られた。 | 封印は破れています。 | B でも蝋封と明示する語義の区別は見られない。一般的な「封印」で場面に通じるかは判定待ち。 | 未実施 |
| neighbor-sports | バットから離して。 | バットから離して置いてください。 | 両方とも野球用具として読める。B は丁寧になり、置く動作を明示。 | 未実施 |
| neighbor-animal | バットから離して。 | ハエから離して置いてください。 | B では名詞が変わったが、コウモリという語義を外した。 | 未実施 |

### カテゴリの採否

費用は同一 fixture・同一周の B−A で算出した。時間はカテゴリごとの 4 組の平均と範囲。

| カテゴリ | 入力 tokens 差 | 出力 tokens 差 | 生成時間差 | 暫定判断と次の判断材料 |
| --- | ---: | ---: | ---: | --- |
| 話者 | +35〜37 | 0 | +185.5 ms（+180.7〜191.3） | 保留。両場面で文脈付きの訳文が同一。語彙の改善候補と費用を踏まえ、親しさと丁寧さの違いを人手で確認する。 |
| 場面 | +38〜39 | 0 | +198.7 ms（+194.0〜205.4） | 保留。状態の表し方は変わったが、魔法と蝋封を分ける利益は明確でない。語義の許容範囲を人手で確認する。 |
| 前後の台詞 | +46 | +2 | +250.5 ms（+247.8〜254.4） | 保留。野球の語義は A で解決済み。動物の語義は B でも外れたため、追加費用を正当化する改善は観測できなかった。 |

1 モデル・1 デバイス・1 種類の挿入方法で、カテゴリごとに 2 例を 2 周した観測。時間は同一プロセス内の測定で、追加トークンと出力の長さの影響を含む。実ゲームの分布、複数カテゴリの併用、別の描画方法や本番のトークン予算への外挿は未検証。この限定調査とは別に、人手品質評価は #87 が扱う。[Issue #87](https://github.com/sh4869221b/yakuori/issues/87)

## 後続実装に渡す同一性と再検証のルール

- pack 全体の fingerprint は pack の同一性に使う。unit に適用した文脈の fingerprint は、順序付きのカテゴリ、実使用資料の正確な bytes、選択と描画の version から構成する。効果と費用を人が確認したカテゴリだけを有効化する。[#41](https://github.com/sh4869221b/yakuori/issues/41) [#44](https://github.com/sh4869221b/yakuori/issues/44)
- 既存の `SourceIdentity` は原文 bytes・所属・unit の識別を担い、文脈の条件は canonical profile 側に記録する。無適用は `context.mode=none` と空 bytes、適用時は `context.mode=provided` と実際に渡した追加文脈の正確な bytes を使う。実使用カテゴリ・文脈・順序または選択・描画仕様の変更を exact profile の差にし、未使用の pack 項目だけの変更は当該 unit のキーを維持する。描画条件は prompt/profile が識別し、同じ原文も effective context が異なれば TM miss とする。[既存の source/profile 契約](../source-profile-identity.md) [#45](https://github.com/sh4869221b/yakuori/issues/45) [#46](https://github.com/sh4869221b/yakuori/issues/46)
- exact TM hit も候補として扱い、現在の unit と profile で保護・mandatory 用語・`Validate` を再適用する。旧 `none` 行を `provided` に流用しない。語義と口調は人手判定の対象。TM・公開との接続は各アダプターの後続 Issue が担当する。[Native #90](https://github.com/sh4869221b/yakuori/issues/90) [Generic JSON #91](https://github.com/sh4869221b/yakuori/issues/91)
- 比較テストだけが B の実験用 request を組み立てる。比較に共用した内部 `RequestIdentity` はエンジンへの admission 用。現在の製品 profile は空でない `AdditionalContext` を拒否する。

## 再現と確認

Go 1.27.3、通常の Go module と cgo の依存物、対応する AMD Vulkan adapter 1 台を用意する。モデルをローカルに用意して `model` にパスを設定する。スナップショット等に使う `scratch` は書き込み可能な空き容量のあるディレクトリ。標準テストは既存の公開試験に対応した Btrfs 上の作業領域を使用した。

```sh
model=/path/to/Index-Translate-2B.Q4_K_M.gguf
scratch=/path/to/btrfs/temp-dir

env -u DISPLAY -u WAYLAND_DISPLAY -u WAYLAND_SOCKET -u XDG_RUNTIME_DIR \
  -u GOINFER_ATTN_KEYS EGL_PLATFORM=surfaceless MESA_SHADER_CACHE_DISABLE=true \
  VK_DRIVER_FILES=/usr/share/vulkan/icd.d/radeon_icd.json TMPDIR="$scratch" \
  CGO_ENABLED=1 GOTOOLCHAIN=local YAKUORI_CONTEXT_GPU_MODEL="$model" \
  go test -count=1 -tags gpu -run '^TestSelectiveContextGPU$' -v ./internal/inference/goinfer

TMPDIR="$scratch" CGO_ENABLED=0 GOTOOLCHAIN=local go build -o "$scratch/yakuori" ./cmd/yakuori
TMPDIR="$scratch" CGO_ENABLED=0 GOTOOLCHAIN=local go test -p 1 -count=1 ./...
TMPDIR="$scratch" CGO_ENABLED=0 GOTOOLCHAIN=local go vet ./...
git diff --check
```

- **Pass** — AMD 実機での測定。全 24 回の比較を完走。
- **Pass** — CGO 無効の build・全パッケージの test（`-p 1`）・vet。
- **Fail（初回）** — デフォルト並列度の標準テストで、既存の `TestTMCommitDeadlineNoPublication` の reader/writer が rollback 後の実行で `localize commit failed`。上記のコマンドで再実行し成功。
- **未実施** — 人手による意味・文体・費用の判定と最終採否。製品の Context Pack、用語集取り込み、Context 適用時の TM/E2E は各後続 Issue の範囲。
