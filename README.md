# Yakuori（訳織り）

ゲームのID・tag・placeholder・binary構造を保ち、ローカルLLMの検証済み翻訳だけを成果物へ戻すPure Goのローカライズ基盤です。最初の目標はWitcher 3の実MOD `.w3strings` の日本語化です。

## 状態

計画段階です。現在はリポジトリと実装issueを準備した状態で、アプリケーションコード、build/test結果、実ゲーム検証、releaseはまだありません。

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

実際のbuild/run手順、保存path、削除・復旧手順は該当実装PRで追加します。詳細な非目標・依存関係・受入れ試験はissue indexを参照してください。
