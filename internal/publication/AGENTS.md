# publication

## OVERVIEW
Linuxのstage公開・原文backup・記録照合による復旧を扱う独立ドメイン（score 12）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| modeと入力 | `options.go`, `preflight_linux.go` | Create / Replace / InPlace |
| path・mount・能力 | `paths_linux.go`, `capability_linux.go` | FD基準の検査 |
| stage生成と検証 | `stage_linux.go` | writerを閉じてからreader検証 |
| 公開状態 | `publish_linux.go` | renameとResult.State |
| 記録形式 | `record_linux.go`, `record_read_linux.go` | 厳格JSONと実file照合 |
| 中断復旧 | `recovery_linux.go`, `recovery_scan_linux.go` | backupの安全な復元 |
| failure注入 | `stage_linux_test.go`, `record_linux_test.go`, `prepare_recovery_failure_linux_test.go`, `recovery_failure_linux_test.go` | stage・record・復旧時の失敗 |

## CONVENTIONS
- OS依存実装はlinux build tagと `_linux.go` に置く。
- run directoryはoutput parent内のprivate directory。
- stage・record・backupの識別情報と実fileを再照合する。
- record.jsonとstageは、record内pathで探索せず保持済みprivate directoryを基準に読む。
- 復旧時のsource/outputはrecord内pathから親directoryを開き、保持済みlock・親識別情報・実fileと照合する。
- Stage callbackは同期実行で、writer/readerを保持・共有しない。
- stageの書込みFDを閉じてから検証用FDを開く。
- stage rename成功後は、後続errorでもResult.StateはPublished。
- InPlaceの原文backup移動後の失敗は復旧を試みる。
- 復旧時の原文戻しは `RENAME_NOREPLACE`。
- `RecoveryError` は診断pathを残して手動確認を要求する。
- Run.CloseはFDとlockを解放し、runや証拠fileを消さない。

## ANTI-PATTERNS
- `Publish` 自身にTM commitを任せない。呼出側のcommit成功後に呼ぶ。
- 2回のrename全体をatomicだと扱わない。
- backupの上書きや復旧先の他fileの自動置換をしない。
- 古いstageを自動publishしない。
- path内の `..` 検査より先にcleanしてsymlinkの証拠を消さない。
- 非対応filesystemのskipを対応済みの検証結果と報告しない。
