# Yakuori（訳織り）

ゲームのID・tag・placeholder・binary構造を保ち、ローカルLLMの検証済み翻訳だけを成果物へ戻すPure Goのローカライズ基盤です。最初の目標はWitcher 3の実MOD `.w3strings` の日本語化です。

## 状態

#2のGo基盤に加え、Linux向けの安全な公開・復旧（#9/#10）と、全件生成・保護復元・最終artifact検証・一括TM commitを接続するCore（#11）、CPU inference EngineとCore adapter（#12）を実装しています。Engineは同じGGUFのtokenizer/templateでrequestを一度構築し、固定greedy policyと正確なtoken countを使い、取消し後のdrainを終えてから再利用・Closeします。text Adapterは全文1unitを扱い、最終検証とcommit後に訳bytesをstdoutへ一括出力します。通常の `localize` はEngine・TM未接続のため、出力指定の解析後も `localize pipeline is not implemented` でno-I/O終了します。SQLite TM接続・hit再検証、実ゲーム検証、翻訳modelの品質選定、releaseはまだありません。[CPU wrapperの再現方法と制限](docs/cpu-inference-spike.md#production-wrapper-12)を参照してください。

- [実装計画とissue index](https://github.com/sh4869221b/yakuori/issues/1)

このrepoはコードと再現用技術資料を扱います。非公開の設計資料の全文は含みません。実装済みの契約・再現手順・制限は、このREADMEとリンク先のrepo内ドキュメントを参照してください。

## v1の採用方針

- Linuxのみ。Go 1.27 baseline、`CGO_ENABLED=0`、CPUを基盤にoptional CUDA
- goinferはwrapper内へ閉じ、TMは`database/sql`とCGO-free SQLite。具体pin・model・形式fixture・上限は実現性gateで確定
- 未検証candidateを確定せず、1 unitでも失敗なら成果fileを公開しない。生成の自動retryなし
- TMはsource/profileの完全一致＋現在unitでの再検証
- 最終staged artifact検証 → 短いTM commit → 安全なpublish。既存outputは既定拒否、明示時のみ置換
- 同一pathでは原文backupをrenameして保持。2 rename全体は非atomicで、backup上書きや復旧先の他fileの自動置換は禁止
- 通常I/O失敗とプロセス停止からの安全な復旧を対象とし、電源断耐性はv1の正式保証外
- 通常処理はoffline、明示的なmodels pullだけnetwork取得。MVP完了には実MODの独立読み取りと代表訳の人手確認が必要

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

`doctor`はXDGのconfig/data/cache/stateディレクトリを表示するだけで、作成・config読込み・model検証は行いません。`localize` はまだsource/configを読み込まず、publisherも呼びません。実装済みprimitiveはoutput parentにmode `0700` の `.yakuori-run-<runID>` を作り、その中へmode `0600` の候補stageと `record.json` を保存します。記録に本文やpromptは含めず、path・identity・hash・size・phase等のmetadataだけを含めます。in-placeの原文backupはsourceと同じdirectoryに置き、成功後も保持します。

失敗時のrun/stage/recordは自動削除しません。`Prepare` は新しいsource snapshotの前に記録と実fileを照合し、安全に戻せる原文backupを `RENAME_NOREPLACE` で復元します。`Publish` もbackup移動後の通常error・取消しで同じ復旧を試みます。復元時はbackupを原文pathへ移動し、公開済みの場合はbackupを保持します。旧stageの自動公開は行いません。記録の欠落・破損やfileの競合は `RecoveryError` と診断pathを返し、証拠を残して手動確認で停止します。`Run.Close` はFDとlockだけを解放し、cleanup commandはありません。CLIはまだこれらのAPIを呼びません。実装範囲と検証結果は[Linux publication調査記録](docs/research/linux-publication.md)を参照してください。

CLIのexit code、stdout/stderr、設定のfallback、fake試験の入口、clean CIの再現方法・制限は[基盤契約](docs/foundation.md)を参照してください。Linux arm64はcross-buildのみで、実行対応は未検証です。詳細な非目標・依存関係・受入れ試験はissue indexを参照してください。

## SQLite feasibility

The test-only [SQLite storage gate](docs/sqlite-feasibility.md) records driver pin,
rollback/corruption protection and finite-wait evidence for #4. It does not enable TM.

## 公開準備と権利表示

Yakuoriの原著コード・文書は[MIT License](LICENSE)です。第三者のfixture・依存コード・モデルには各権利者の条件が適用され、MITへの変更を意味しません。研究用Rust oracleが依存するGPL-3.0-onlyのw3stringsは独立した別ツールで、本体CLIへリンクしません。[第三者通知](THIRD_PARTY_NOTICES.md)と[公開前チェック](docs/public-readiness.md)を参照してください。現時点では完成した翻訳アプリや検証済みreleaseを提供していません。
