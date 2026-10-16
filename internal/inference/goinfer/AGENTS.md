# goinfer wrapper

## OVERVIEW
上流goinferを隔離し、model snapshot・request admission・生成終了を扱う独立ドメイン（score 9）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| loadとsnapshot | `load.go` | 同じsnapshotをmodelとtokenizerへ渡す |
| 公開optionと状態 | `engine.go` | Open / OpenWithOptions / Info |
| request構築 | `request.go`, `template.go`, `index_template.go` | template allowlist |
| 生成と終了分類 | `generate.go`, `finish.go` | drain後にactive状態を解除 |
| Close連携 | `close.go` | active生成の取消しと完了待ち |
| backend分岐 | `cuda.go`, `nocuda.go` | CUDAはlinux/amd64かつtag指定 |
| 実model検査 | `cpu*_smoke_test.go`, `cuda*_test.go` | 専用tagとmodel環境変数 |

## CONVENTIONS
- 対応quantは `int4`、backendは明示的な `cpu` / `cuda`。
- loaderは元の可変pathではなくprivate snapshotを使う。
- snapshotは0400にし、load処理終了時にunlinkする。
- 登録modelの期待SHA-256は実際のload snapshotと比較する。
- 埋込みtemplateは既知Qwen/Index文字列との完全一致のみ受理する。
- 実効contextはmodel上限・設定上限・CUDA resident上限から決まる。
- 同じEngineで同時生成はせず、active中は `ErrBusy`。
- 異なるrequest identityは `ErrForeignRequest`。
- token streamをdrainしてから `activeDone` をcloseする。
- 終了分類の優先順位はtimeout、cancel、decode error、clamp、budget整合、MaxTokens、Stop。
- 並行状態の試験は `testing/synctest` を使う既存テストを参照する。

## ANTI-PATTERNS
- CUDA失敗をCPUへ暗黙に切り替えない。resident不成立も拒否する。
- clampされた要求を成功として扱わない。
- tokenizerやmodelが元pathを再openする経路を作らない。
- smoke testの通常unit testへの置換を実model検証と報告しない。
- 実model試験は `cpusmoke` / `cuda,cudasmoke` と対応する `YAKUORI_*` を確認する。
