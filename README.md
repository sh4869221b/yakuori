# Yakuori（訳織り）

ゲームのID・tag・placeholder・binary構造を保ち、ローカルLLMの検証済み翻訳だけを成果物へ戻すPure Goのローカライズ基盤です。最初の目標はWitcher 3の実MOD `.w3strings` の日本語化です。

## 状態

#2のGo基盤に加え、Linux向けに検証済みstageを安全に公開するprimitive（#9）と、operation recordの検証・異常終了からの復旧（#10）を実装しています。`localize` は承認済み出力指定の解析だけを行い、有効な指定も `localize pipeline is not implemented` で終了します。#11のpipeline接続、model、TM接続、実ゲーム検証、releaseはまだありません。

- [実装計画とissue index](https://github.com/sh4869221b/yakuori/issues/1)
- [正本: 設計書 Draft v0.2](https://chatgpt.com/space/page_3565e1d53fa08191a7d8cb56e84af5a5)
- [設計レビュー対応記録](https://chatgpt.com/space/page_020ed32621908191acfa75ea9b11e340)

設計Pageが正本で、閲覧には所有者のアクセス権が必要です。このrepoはコードと再現用技術資料を扱い、正本の全文コピーは置きません。旧HTML/Markdownは履歴資料です。

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
