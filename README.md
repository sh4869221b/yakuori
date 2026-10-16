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
- 通常処理はoffline。取得は明示的な `models pull`（絶対HTTPS URL、期待size・SHA-256・digest出所指定）と、指定されたローカルfileだけを読む `models import` の2経路のみ。自動取得・更新や既存IDの上書きはなく、通常処理は一切通信しない。取得元・期待値・digest出所の意味と取得履歴・失敗・取消し・残骸の扱いは「ローカルmodelの登録」を参照。MVP完了には実MODの独立読み取りと代表訳の人手確認が必要

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

通常CIのclean gateはmodel不要です。`ci/verify.sh` は観測器自己試験、実binaryを使う取得失敗matrix、CLI/error matrixをmodelなしで走らせ、外部のmodel配布元には接続しません。実modelと実GPUのmatrix（`modelsmoke` / `offlinesmoke`、CUDA・driver不成立）は、事前配置したGGUFを明示したlocal実行として別に行われ、通常CIの成功はその代わりになりません。

## ローカルmodelの登録

GGUFはsourceから個人用の管理copyへ登録します。sourceは変更・削除せず、登録IDは一意で既存IDを置換できません。importだけでは選択されないため、使うIDを明示してください。`models verify` は管理copyの登録時SHA-256とsizeを照合し、Engineも実際のload snapshotを同じ登録SHA-256と照合します。CPU/CUDAは同じGGUFと埋込みtokenizerを使います。

`models pull` は絶対HTTPS URLからGGUFを一度だけ取得します。`--id`・`--sha256`（64桁小文字hex）・`--size`（正のbyte数）・`--digest-source`（空白を不可とする出所の申告）をURLより前に置き、位置引数はURL一個です。URLはhost必須でuserinfo・fragmentは不可、redirectはHTTPSのみ・最大10要求、HTTPは200のみ、1回の取得は30分で打ち切ります。retryと中断resumeはなく、半端なstageは再接続せず消します。受信bytesの実測が期待size・期待SHA-256（Content-Lengthは補助情報であり受入れ根拠ではない）に一致し、そのstageがCPU loader・tokenizerに受理されてCloseできるときだけ登録します。`--digest-source` は出所を示す利用者の申告で、独立検証済みの配布保証としては扱えません。取得は自動Useせず、選択済みmodelも既存IDも変えません。取得前にstderrへ引用・エスケープした期待値を一度だけ出力し、この出力に失敗したら通信しません。通常の失敗とSIGINT/SIGTERM取消しは当該実行のstageだけを削除し、旧model・旧選択を保持します。commitがregistry renameを通した後は取消し・出力失敗でも巻き戻さず、登録済みであることを区別して報告します。SIGKILL等の残骸は `<Data>/models/.pull-*` に保持され、自動公開・自動削除はしません。

取得履歴はmodelと同じ `<Data>/models/<ID>/provenance.json` に `schema:1` で書かれます（`source_url`, `final_url`, `expected_sha256`, `expected_size_bytes`, `digest_source`、mode 0600）。初期URLと実際に200を返した `final_url` を区別するので、redirect後の実際の配布元まで追跡できます。ローカル `import` にはこのsidecarはなく、registry schemaと `models verify` のbyte identity照合は変わりません。

```sh
./yakuori models pull --id fetched --sha256 <64桁小文字hex> --size <byte数> --digest-source <出所の申告> https://host.example/path/model.gguf
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
| Provenance | `<Data>/models/<ID>/provenance.json` | `models pull` で取得したIDの取得履歴（schema 1 sidecar） |
| Registry lock | `<Data>/registry.lock` | import/use中の協調lock |
| TM | `<Data>/tm.sqlite` | translation memory |
| Load snapshots | `<Cache>/model-snapshots` | Engine load時に作成し、load完了時にunlinkするtransient snapshot |
| State | `<State>` | 現時点で保存物なし |

Private directoryはmode `0700`、Config/Registry/lockとmanaged `model.gguf` は `0600`、Engine snapshotは `0400` です。登録には管理copy分の保存容量が別途必要で、model読込み時はtransient snapshot分も必要です。hardlinkは使いません。たとえば既存の研究用modelが `$HOME/.cache/yakuori/models/` にあっても、それは外部source fileです。登録先の `<Data>/models/` やtransient用 `<Cache>/model-snapshots/` と混同せず、cache全体を削除しないでください。
provenance.json も `0600` です。取得中は `<Data>/models/.pull-*` に取得stage（model本体＋取得履歴）が加わり、CPU loaderでの検証時の容量peakは既存分＋取得stage＋load snapshotです。

modelを手動削除するときは、先にすべてのYakuori processを停止します。`models remove` commandはありません。削除するIDが現在の `selected_model` と一致するときだけconfig fileを取り除き、`<Data>/models/<ID>/` の中身（`model.gguf`、`models pull` で取得した場合は `provenance.json` も含む）を削除して、ID directoryが空なら取り除いてから、registry JSONのそのIDだけを削除します。他IDのentryは保持し、最後の1件を消す場合はschema 1の空 `models` objectを残します。registryだけ先に消すと未登録directoryが残るため、その状態を作らないでください。directoryに予期しないfileがある場合や、中断したimportでregistryにないdirectoryがある場合は自動削除せず、個別に内容を確認してください。source modelは残します。 `.pull-*` directoryは強制停止等で中断した取得の残骸として保持され、registryには登録されず、次の取得の実行でも残ります。不要になったら内容を確認の上で手動削除してください。

TMを消すときは別途全processを止め、`tm.sqlite.lock` と `tm.sqlite-journal` / `-wal` / `-shm` を含むSQLite sidecar、Recovery条件を[TM契約](docs/sqlite-feasibility.md)に従って確認します。未解決journalやrecovery errorがあるときはDBとsidecarを保持します。publicationのsource、output、run record、原文backupは利用者が指定したsource/output parentにあり、XDG model/TM storageとは別です。未復旧backupやpublication recordを先に消さず、復旧対象と他runのfilesを維持してください。

## SQLite feasibility

[SQLiteの調査記録と現在のTM契約](docs/sqlite-feasibility.md#current-core-tm-integration-16)に、明示的な `Create` / `Open` / `Recover`、scope・manifest照合、競合時停止と公開境界を記録しています。Btrfs上で既定の件数・入力byte上限のfixtureを検証済みですが、実modelの最大出力bytesと低速な対応filesystemの性能は未検証です。通常CLIへの接続は含みません。

## 公開準備と権利表示

Yakuoriの原著コード・文書は[MIT License](LICENSE)です。第三者のfixture・依存コード・モデルには各権利者の条件が適用され、MITへの変更を意味しません。研究用Rust oracleが依存するGPL-3.0-onlyのw3stringsは独立した別ツールで、本体CLIへリンクしません。[第三者通知](THIRD_PARTY_NOTICES.md)と[公開前チェック](docs/public-readiness.md)を参照してください。現時点では完成した翻訳アプリや検証済みreleaseを提供していません。
