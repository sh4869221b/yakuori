# translation memory

## OVERVIEW
canonical source/profile完全一致と短い競合検査付きcommitを担うLinux専用ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| Create / Open | `store_linux.go` | ownership・lock・1接続 |
| 明示復旧 | `recover_linux.go` | journalとstoreの検査 |
| 永続schema | `schema.go` | application ID・厳格table定義 |
| import単位の状態 | `run.go` | BeginRun / Lookup / Stage |
| commitと競合 | `commit.go` | BEGIN IMMEDIATEと観測値比較 |
| 障害・復旧回帰 | `store*_test.go` | sidecar・非通常file・中断 |
| 状態遷移回帰 | `run_test.go`, `commit_test.go` | binding・manifest・conflict |

## CONVENTIONS
- `application_id=1497451343`、`user_version=1`。
- tableは `STRICT, WITHOUT ROWID`、journal modeは `delete`。
- keyはschema・source identity・translation profileの組。
- BeginRunはcanonical profileと明示scopeを要求する。
- Runは1importに属し、呼出側が逐次利用する。
- Lookupで観測した候補はRun内に保持する。
- RecordValidationは現在候補の検証結果を記録するだけ。
- Stageは全unitの観測・候補検査とAcceptedTranslationを要求する。
- manifestのkey relationは重複数も含めscopeと照合する。
- commitは短いtransactionで現行rowと観測済みrowを比較する。
- invalid rowの修復UPDATEは観測時と同じbytesのrowだけを対象にする。
- rollback用cleanup timeoutは通常DB操作のtimeoutと分ける。

## ANTI-PATTERNS
- storageから候補をAcceptedへ昇格させない。
- 生成中にwriter transactionを保持しない。
- conflictを無条件upsertやREPLACEで上書きしない。
- WAL・SHMや未知schemaを受け入れず、Openで暗黙復旧しない。
- Create失敗をOpenへfallbackせず、復旧はRecoverで明示する。
