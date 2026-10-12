# Yakuori（訳織り）

ゲームのID・tag・placeholder・binary構造を保ち、ローカルLLMの検証済み翻訳だけを成果物へ戻すPure Goのローカライズ基盤です。最初の目標はWitcher 3の実MOD `.w3strings` の日本語化です。

## 状態

#2のGo基盤に加え、Linux向けの安全な公開・復旧（#9/#10）と、全件生成・保護復元・最終artifact検証・一括TM commitを接続するCore（#11）、明示的にCPU/CUDAを選べるinference EngineとCore adapter（#12/#52）を実装しています。CUDAはLinux amd64向けのoptional build tagと実行時のNVIDIA driverを必要とします。Engineは同じGGUFのtokenizer/templateでrequestを一度構築し、固定greedy policyと正確なtoken countを使い、取消し後のdrainを終えてから再利用・Closeします。text Adapterは全文1unitを扱い、最終検証とcommit後に訳bytesをstdoutへ一括出力します。Coreの `TextWithTM` / `FileWithTM` はSQLite TMの完全一致候補を現在unitで再検証し、全件と最終artifactの検証後だけ保存します。通常の `localize` はEngine・TM未接続のため、出力指定の解析後も `localize pipeline is not implemented` でno-I/O終了します。実ゲーム検証、翻訳modelの品質選定、releaseはまだありません。[CPU wrapperの再現方法と制限](docs/cpu-inference-spike.md#production-wrapper-12)と[CUDA wrapperの実測と制限](docs/cuda-inference-spike.md#current-52-product-wrapper-result-2026-10-10)を参照してください。

- [実装計画とissue index](https://github.com/sh4869221b/yakuori/issues/1)

このrepoはコードと再現用技術資料を扱います。非公開の設計資料の全文は含みません。実装済みの契約・再現手順・制限は、このREADMEとリンク先のrepo内ドキュメントを参照してください。

## v1の採用方針

- Linuxのみ。Go 1.27 baseline、`CGO_ENABLED=0`、通常のCPU buildに加え、明示選択するLinux amd64向けoptional CUDA（`-tags cuda`、NVIDIA driverが必要）
- goinferはwrapper内へ閉じ、TMは`database/sql`とCGO-free SQLite。具体pin・model・形式fixture・上限は実現性gateで確定
- 未検証candidateを確定せず、1 unitでも失敗なら成果fileを公開しない。生成の自動retryなし
- TMはsource/profileの完全一致＋現在unitでの再検証
- 最終staged artifact検証 → 短いTM commit → 安全なpublish。既存outputは既定拒否、明示時のみ置換
- 同一pathでは原文backupをrenameして保持。2 rename全体は非atomicで、backup上書きや復旧先の他fileの自動置換は禁止
- 通常I/O失敗とプロセス停止からの安全な復旧を対象とし、電源断耐性はv1の正式保証外
- 通常処理はoffline。モデル取得コマンドはなく、`models import` は指定されたローカルfileだけを読む。MVP完了には実MODの独立読み取りと代表訳の人手確認が必要

CLI・XDG namespaceは`yakuori`。旧Kotobaのコード・設定・TM・registryを暗黙移行しません。single binaryはmodel本体やGPU driverの内包を意味しません。

## Build / test / run

Go 1.27.1を使用します（公式archiveとchecksumは[基盤の再現資料](docs/foundation.md)）。

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go build ./cmd/yakuori
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./...
CGO_ENABLED=0 GOTOOLCHAIN=local go vet ./...
./yakuori --help
./yakuori doctor
sh ci/verify.sh
```

通常の `doctor` は保存先をstatし、小さなconfig/registryを読みますが、modelのhash・load、TMのopen、書込みはしません。欠落は表示し、既存の不正schemaや不安全なmodeは失敗として報告します。`doctor --check-model` は選択済みmodelを指定backendで開き、Engine情報を表示して閉じます。生成せず、snapshot directoryも作りません。`localize` はまだsource/configを読み込まず、publisherも呼びません。実装済みprimitiveはoutput parentにmode `0700` の `.yakuori-run-<runID>` を作り、その中へmode `0600` の候補stageと `record.json` を保存します。記録に本文やpromptは含めず、path・identity・hash・size・phase等のmetadataだけを含めます。in-placeの原文backupはsourceと同じdirectoryに置き、成功後も保持します。

失敗時のrun/stage/recordは自動削除しません。`Prepare` は新しいsource snapshotの前に記録と実fileを照合し、安全に戻せる原文backupを `RENAME_NOREPLACE` で復元します。`Publish` もbackup移動後の通常error・取消しで同じ復旧を試みます。復元時はbackupを原文pathへ移動し、公開済みの場合はbackupを保持します。旧stageの自動公開は行いません。記録の欠落・破損やfileの競合は `RecoveryError` と診断pathを返し、証拠を残して手動確認で停止します。`Run.Close` はFDとlockだけを解放し、cleanup commandはありません。CLIはまだこれらのAPIを呼びません。実装範囲と検証結果は[Linux publication調査記録](docs/research/linux-publication.md)を参照してください。

CLIのexit code、stdout/stderr、設定のfallback、fake試験の入口、clean CIの再現方法・制限は[基盤契約](docs/foundation.md)を参照してください。Linux arm64はcross-buildのみで、実行対応は未検証です。詳細な非目標・依存関係・受入れ試験はissue indexを参照してください。

## ローカルmodelの登録

GGUFはsourceから個人用の管理copyへ登録します。sourceは変更・削除せず、登録IDは一意で既存IDを置換できません。importだけでは選択されないため、使うIDを明示してください。`models verify` は管理copyの登録時SHA-256とsizeを照合し、Engineも実際のload snapshotを同じ登録SHA-256と照合します。CPU/CUDAは同じGGUFと埋込みtokenizerを使います。

```sh
./yakuori models import --id index-translate /path/to/model.gguf
./yakuori models use index-translate
./yakuori models verify index-translate
./yakuori doctor
./yakuori doctor --check-model --backend cpu
./yakuori doctor --check-model --backend cuda
```

`import` はローカルFILEを指定する構文で、`--id` はFILEより前に置きます。CUDA確認にはLinux/amd64向け `-tags cuda` buildと実行時のNVIDIA driver (`libcuda.so.1`) が必要です。CUDAが利用できない場合は失敗し、CPUへ暗黙に切り替えません。通常の `doctor` は保存先と登録状態を確認します。`--check-model` は選択済みmodelをCPU（既定）または明示backendで開いて `Engine.Info` を出し、生成せず閉じます。設定contextは既存の4096 token候補で、実効値はEngineから報告します。registryに登録されたmodelの管理fileが欠落している場合、通常doctorは `[missing]` を表示します。管理copyのhash照合は `models verify ID` または `--check-model` で行います。

各XDG base directoryには `/yakuori` が追加され、相対または空のoverrideは既定値へ戻ります。

| 保存物 | Path | 用途 |
| --- | --- | --- |
| Config | `<Config>/config.json` | `{"schema":1,"selected_model":"index-translate"}` |
| Registry | `<Data>/registry.json` | `{"schema":1,"models":{"index-translate":{"sha256":"<64 lowercase hex>","size_bytes":123}}}` |
| Managed models | `<Data>/models/<ID>/model.gguf` | IDごとのprivate managed copy |
| Registry lock | `<Data>/registry.lock` | import/use中の協調lock |
| TM | `<Data>/tm.sqlite` | translation memory |
| Load snapshots | `<Cache>/model-snapshots` | Engine load時に作成し、load完了時にunlinkするtransient snapshot |
| State | `<State>` | 現時点で保存物なし |

Private directoryはmode `0700`、Config/Registry/lockとmanaged `model.gguf` は `0600`、Engine snapshotは `0400` です。登録には管理copy分の保存容量が別途必要で、model読込み時はtransient snapshot分も必要です。hardlinkは使いません。たとえば既存の研究用modelが `$HOME/.cache/yakuori/models/` にあっても、それは外部source fileです。登録先の `<Data>/models/` やtransient用 `<Cache>/model-snapshots/` と混同せず、cache全体を削除しないでください。

modelを手動削除するときは、先にすべてのYakuori processを停止します。`models remove` commandはありません。削除するIDが現在の `selected_model` と一致するときだけconfig fileを取り除き、`<Data>/models/<ID>/model.gguf` だけを削除して、ID directoryが空なら取り除いてから、registry JSONのそのIDだけを削除します。他IDのentryは保持し、最後の1件を消す場合はschema 1の空 `models` objectを残します。registryだけ先に消すと未登録directoryが残るため、その状態を作らないでください。directoryに予期しないfileがある場合や、中断したimportでregistryにないdirectoryがある場合は自動削除せず、個別に内容を確認してください。source modelは残します。

TMを消すときは別途全processを止め、`tm.sqlite.lock` と `tm.sqlite-journal` / `-wal` / `-shm` を含むSQLite sidecar、Recovery条件を[TM契約](docs/sqlite-feasibility.md)に従って確認します。未解決journalやrecovery errorがあるときはDBとsidecarを保持します。publicationのsource、output、run record、原文backupは利用者が指定したsource/output parentにあり、XDG model/TM storageとは別です。未復旧backupやpublication recordを先に消さず、復旧対象と他runのfilesを維持してください。

## SQLite feasibility

[SQLiteの調査記録と現在のTM契約](docs/sqlite-feasibility.md#current-core-tm-integration-16)に、明示的な `Create` / `Open` / `Recover`、scope・manifest照合、競合時停止と公開境界を記録しています。Btrfs上で既定の件数・入力byte上限のfixtureを検証済みですが、実modelの最大出力bytesと低速な対応filesystemの性能は未検証です。通常CLIへの接続は含みません。

## 公開準備と権利表示

Yakuoriの原著コード・文書は[MIT License](LICENSE)です。第三者のfixture・依存コード・モデルには各権利者の条件が適用され、MITへの変更を意味しません。研究用Rust oracleが依存するGPL-3.0-onlyのw3stringsは独立した別ツールで、本体CLIへリンクしません。[第三者通知](THIRD_PARTY_NOTICES.md)と[公開前チェック](docs/public-readiness.md)を参照してください。現時点では完成した翻訳アプリや検証済みreleaseを提供していません。
