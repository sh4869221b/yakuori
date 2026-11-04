# CI待ち時間の計測と改善（2026-11-04）

## 共通gate化と再比較（最新の改善）

前回の最終head `dfdad2f` を固定して4回、新構成 `e2c4c88` を固定して3回比較した。
最長workload jobの中央値は **310.5秒→205秒（105.5秒、34.0%短縮）**。
新構成の `CI complete` と開始待ちを含む全体時間は **214 / 179 / 214秒、中央値214秒**。
新構成の最も遅い全体214秒も、変更前の最も速い最長job274秒より60秒短かった。
これはこの標本での実測差であり、固定された同一物理CPUでの因果推定や母集団の統計的有意性の保証ではない。

[全20 workflow attemptのjob/step時刻・URL](common-gate-measurements.json) と
[取得できたjob別CPU・kernel・メモリー](runner-metadata.json) を保存した。
変更前16 attempt、中間案1 attempt、最終案3 attemptはすべて成功。
失敗時の自動再試行や標本の選別は行っていない。中間案を最終案の反復標本に混ぜない。

| 構成 / attempt | 基盤 | CPU | 公開 | 独立往復 | 最長job | 全体* |
|---|---:|---:|---:|---:|---:|---:|
| 変更前 dfdad2f / 1 | 192 | 326 | 281 | 42 | 326 | 329 |
| 変更前 dfdad2f / 2 | 180 | 274 | 295 | 42 | 295 | 316 |
| 変更前 dfdad2f / 3 | 150 | 326 | 275 | 43 | 326 | 330 |
| 変更前 dfdad2f / 4 | 195 | 260 | 274 | 37 | 274 | 281 |
| 共通gate edb421d / 1 | 140 | 227 | 191 | 49 | 227 | 234 |
| 最終案 e2c4c88 / 1 | 171 | 134 | 205 | 41 | 205 | 214 |
| 最終案 e2c4c88 / 2 | 129 | 169 | 145 | 42 | 169 | 179 |
| 最終案 e2c4c88 / 3 | 122 | 200 | 205 | 36 | 205 | 214 |

*秒。最長jobは4つのworkloadのみ、全体は最初のattempt開始〜最後のjob完了。
変更前の手動再実行には0 / 19 / 4 / 4秒の起動差があるため、全体時間の単純差を主比較にしない。
新構成は単一workflowで同時に4jobを開始し、全成功を確認するgateが最後に5〜6秒追加される。
並列jobの時間は合算しない。原JSONにはqueue、各attempt、開始・終了時刻も残している。

| 支配するstepの中央値 | 変更前 n=4 | 最終案 n=3 |
|---|---:|---:|
| CPU image build | 185.5秒 | 70秒 |
| 公開 image build | 170.5秒 | 89秒 |
| 公開の実試験 | 100.5秒 | 100秒 |

公開の試験時間はほぼ同じで、短縮は主に準備工程に現れた。
CPU長文脈は最終案でも61 / 81 / 110秒と変動している。
110秒かかった3回目もCPU200秒・全体214秒で成功し、推論が速い標本だけの結果ではない。

### 変更と維持する契約

- **共通検証を1回に集約**: `ci/verify.sh` の全検証を `linux-cgo-free` に残す。
  CPU・公開はfreshな別イメージで固有の検証を実行する。4jobを直列の依存にせず、
  `CI complete` が全4jobのsuccessを要求し、failure/cancelled/skipped/欠落を拒否する。
- **arm64の重複コンパイルを除去**: CPU用3本の同一compile commandを、既にarm64依存を
  コンパイルした必須基盤jobの末尾へ移す。最終案1回目の追加3本は4.2秒。
  arm64の実推論を検証したとは扱わない。amd64実推論・wrapper再利用・W3検証は同一。
- **独立した待ち時間を重ねる**: Python監視器の55試験を初期Go検証/コンパイルと重ね、
  従来位置で必ずjoinしてからoffline CLI matrixへ進む。早期失敗でも子の終了とlogを回収し、
  非zeroを維持する。55試験のコード、2秒/5秒等のtimeout、判定条件は変更していない。
- 各イメージのnative compiler/header拒否、固定Go1.27.3とCGO=0、cgo負例・両arch依存監査、
  全unit/vet、Go driver/test2json/count=1、selector・期待名・timeout、non-root・別mount・
  ext4/Btrfs・tmpfs拒否・復旧・offline・1K/4K stop/deadline・model hashを維持。
- runnerは従来の`ubuntu-24.04`、推論は2CPU/4GiB。外部action、cross-run cache、
  test結果cache、権限昇格は追加していない。CPU検証はpath filterを外し全PR/main pushへ拡大した。
  既存4jobの名前を維持し、workflow内部で全成功を要求する。
  ブランチ保護設定のGETはintegration権限403だったため、リポジトリー設定上の必須checkは未確認。

### 比較方法と検証

実行順は旧head再測定→中間案→旧head再測定→最終案→旧head再測定/最終案再測定→最終案再測定。
一部は時間的に重なるが別hosted runnerで動作する。各回fresh builder、同じtoolchain/推論制限、
外部cache無しで実行。物理CPU・クロック・ホスト負荷は固定していない。最終案のCPU jobだけでも
AMD EPYC 9V45 / 9V74 / 7763が混在し、初期案はIntel Xeon Platinum 8573Cだった。
取得できなかった公開jobのCPU情報はunknownとし、同じCPUだったとは推定しない。

既存4jobの実行step・資源条件をYAML構造として比較し、計測stepと公開Dockerfile指定以外は同一と確認。
aggregateは全成功と各failure/cancelled/skipped/欠落/余分な結果を実コードで検証。
監視器の制御フロー9ケース（両側失敗・launch直後のTERM・繰り返し取消し等）は
[再実行用harness](check-observer-supervision.py)に保存した。

ローカルの監視器55試験は54成功、既存のdaemonのPID1祖先判定1件が失敗し、
変更しないforeground実行でも同じ失敗を再現した。実行環境のPID名前空間の制約として記録し、
判定を弱めずGitHubの全3回の基盤jobで55件成功を確認した。
YAML parse、shell構文、Docker build --check、diff --checkも成功。

```sh
python3 docs/evidence/ci-latency/check-observer-supervision.py
python3 docs/evidence/ci-latency/collect.py \
  --group baseline=41803096269,41803096123,41803096114,41803096197 \
  --group candidate1=41807403556 --group candidate2=41808576213 \
  --output /tmp/common-gate-measurements.json
```

以下は、この共通gate化より前に行った局所改善の記録。旧方針・旧標本も履歴として残す。

対象は [PR #112](https://github.com/sh4869221b/yakuori/pull/112)。変更前は
`9f1e1bc`。比較用 `9b3c903` と同じ tree
`f74a50a4261033092e3aa5cc3b0f1d643bbf655a` であることを照合した。
生のジョブ・step時間、実行URL、試行番号、局所計測の全標本は
[measurements.json](measurements.json) に保存した。試行2〜4は全4件のCIが成功。以後の記録のみの更新に対するCI結果はPRを参照する。

## 条件と計測の限界

- GitHub: `ubuntu-24.04`、Go 1.27.3、CGO=0。全試行で外部キャッシュなしの
  新規Docker builder。実CPU推論は従来どおり `--network none --cpus 2 --memory 4g`。
- ジョブ時間はActions APIの `started_at`〜`completed_at`。stepを別々に集計。
  ジョブ作成から開始までは2〜3秒で、今回の支配項ではなかった。
- runnerのCPU型・クロック・ホスト負荷は固定できていない。長文脈計測だけでも
  111→90→62秒と変動した。全体時間の差を変更だけの効果と断定しない。
- ローカルのGo比較は同じGo 1.27.3、CGO=0、GOMAXPROCS=2、既存build cacheで
  旧/新を交互に実行。試験結果のcacheは使わず毎回実行した。
  OSページキャッシュは固定していない。最初の個別コンパイル15.04秒はcold linkingを
  含むため、warm比較から除外した。

## 初期の反復結果

秒数。公開「試験」はイメージビルド後の non-root host filesystem / separate-mount step。
基盤・CPU・公開列は各ジョブ全体であり、並列実行なので合算しない。

| 試行 | 基盤 | CPU | 公開 | 公開の試験step |
|---|---:|---:|---:|---:|
| 変更前・初回履歴 | 149 | 350 | 354 | 149 |
| 変更前・再計測 | 199 | 336 | 340 | 152 |
| 1: 一度の読み取りで照合 (`b37490a`) | 227 | 406 | 322 | 127 |
| 2: 新しく作った試験バイナリーを再利用 (`b4a5ce8`) | 187 | 319 | 291 | 97 |
| 3: 固定モデル取得をビルドと重ねる (`7328bfc`) | 180 | 233 | 297 | 100 |
| 4: ready/cancel競合の回帰修正 (`c876643`) | 183 | 236 | 238 | 87 |

同じ変更前PRのattempt 1には基盤199秒・CPU350秒・公開277秒（試験123秒）もある。
[公開初回](https://github.com/sh4869221b/yakuori/actions/runs/41791488279/job/126445661557)も成功標本であり、
上表の公開354秒はmain側の別実行。変更前公開jobにも277〜354秒の変動があった。

初回履歴は同一treeのPR CPU / main基盤・公開の組合せで、単一workflow群の
完了時間ではない。変更前の再計測は同一PR headで3workflowを再実行した。
試行1の公開は初回がsignal lifecycle不一致で停止し、同一commitのattempt 2が成功した。
失敗した初回を短縮の証拠には使っていない。

### 1. 試験証拠の照合

期待名ごとに何度もgrep・sort・子プロセスを起動する二重の実装を、
`ci/check-test-events.py` に集約した。固定期待名とrootのRUN/PASS各1回、
余分なterminal拒否、FAIL/SKIP拒否、Go driverのframe以外を証拠にしない条件を維持。
名前は正規表現でなく完全一致として扱う。

過去の実JSONログに対する3回の交互比較の中央値:

| 実ログ | 期待名数 | 旧 | 新 |
|---|---:|---:|---:|
| CommitPublicationState | 930 | 22.6434s | 0.0865s |
| PublicationFailures | 523 | 11.0778s | 0.0673s |
| CLIPublicationExit | 68 | 1.3524s | 0.0590s |
| Filesystem | 234 | 4.7968s | 0.0642s |

局所標本は同一入力に対する比較であり、CI全体の短縮率ではない。
欠落・重複・余分な名・FAIL/SKIP・child transcriptによる偽装の拒否を8試験で確認した。

### 2. 毎回のGoコマンド起動・再リンクを除去

normal / pipelinefaultsの各4パッケージをtag集合ごとにまとめてコンパイルし、
freshなバイナリーを各laneで再実行する。パッケージcwd、count=1、timeout、
`-test.paniconexit0`、`-test.v=test2json` を維持。
Go同梱の `test2json` を使い、childとdriverの境界を維持する。
tagged vetと通常の全テストは従来どおり実行する。

同じstdout試験の5回比較は `go test` 中央値0.5404秒→バイナリー0.1303秒。
4パッケージのwarmコンパイルは個別1.208/1.210秒、まとめて0.385/0.510/0.462秒。
30系統の公開試験についてselectorとtimeoutが旧実装と一致することも照合した。
意図した失敗と1ns timeoutは両方ともnonzeroかつJSON failになった。

### 3. モデル取得と検証の独立性を利用

CPU Dockerfileをtoolchain / model / checks段階に分けた。固定URL・SHA-256・
取得timeoutは同一。モデル取得とコード検証をBuildKitが同時に進め、両方の成功後に
のみ最終イメージを作る。ランタイムのnetwork禁止は維持した。

試行3はモデル取得6.8秒がchecks 130.3秒と重なり、追加COPYは2.5秒。
同一ビルド内で隠せた待機は約4.3秒。CPUジョブ全体の319→233秒には、
長文脈計測90→62秒等のrunner差も含まれる。

### 4. 再実行を招いたready/cancel競合の回帰修正

試行1の保存証拠では `generate-return` が欠落していた。試験用エンジンが
最初のトークンを渡す前にgenerate-readyを通知し、直後の取消しで先頭トークンを
落としてcanary検証で早期returnする競合だった。

通知時に同期的に取り消す試験を追加し、変更前はattempt 0で同じcanaryエラーを再現。
先頭トークンを受け渡してから通知する修正後は100回×20実行で成功。
既存のイベント列・canary・取消し・drain・Close順序の判定は変更していない。
この回帰試験もclean gateで毎回実行し、成功名を照合する。試行4で全4件のCIが成功した。

## 維持した範囲と見送った案

製品コード、既存試験の選択条件、期待名、timeout、CPU実推論、1K/4K stop/deadline、
arm64 cross-build、cgo負例・依存監査、native compiler/header拒否、
ext4/Btrfs・tmpfs拒否、別mount・non-root・復旧・offlineの境界は維持。
Actions権限はcontents:readのままで、外部action・runner・cache serviceは追加していない。

- 初期には共通clean gateを単純に省く案を見送った。その後、上記の必須集約gateと各イメージの監査を実装し、検証全体を維持して重複を除いた。
- CPU実験の同時実行・runner増強: CPU/メモリー条件、測定の意味または費用を変えるので採用しない。
- timeout、件数、推論サイズ、復旧matrixの削減: 失敗検知を弱めるので採用しない。
- 完了済み試験のDocker layer / Go test result cache再利用: 再実行時の検知を省くので採用しない。
- cross-runの依存ビルドcache: 現在のclean Docker buildへ持ち込むにはcache境界、
  test stageの強制再実行、export/restore時間、容量、追加actionの管理が必要。
  今回の局所改善と異なり、追加のリスク・費用なしの短縮を実証できないため採用しない。
  [Docker公式資料](https://docs.docker.com/build/ci/github-actions/cache/)も参照した。
- 古い実行の自動cancel: 反復比較に必要な結果やmainの証拠を欠くので追加しない。
- Python起動のさらなる一括化: 残る費用は小さく、laneごとの即時失敗を遅らせるので採用しない。

低リスクの実証できる候補を反復した時点の結果であり、全てのrunner・cache状態での
絶対的な最短時間を保証するものではない。残る支配項はクリーンコンパイルと、
条件を維持した実推論・timeout・復旧試験。コードの最終変更である試行4の全4件は成功済み。記録更新後もPRで該当CIを確認する。

## ローカル確認

```sh
python3 ci/test-test-events.py
sh -n ci/verify.sh
sh -n ci/run-test-binary.sh
CGO_ENABLED=0 GOTOOLCHAIN=local go test -tags pipelinefaults ./internal/cli -run '^TestSignalReadyCancellation$' -count=20
CGO_ENABLED=0 GOTOOLCHAIN=local go vet -tags pipelinefaults ./internal/cli
docker build --check -f ci/cpu-spike.Dockerfile .
git diff --check
```

ローカルはoverlay/tmpfsで、適格な公開positiveの実行証拠には使わない。
これらのpositiveと全体clean gate・CPU実推論はGitHub CIで確認した。
