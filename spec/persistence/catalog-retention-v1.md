# 番組表保持仕様 v1

- Status: Accepted
- Accepted date: 2026-09-15
- Decision owner: Project owner
- Product destination: `spec/persistence/catalog-retention-v1.md`
- Related decision: [ADR-0072](../../docs/adr/0072-bounded-catalog-retention.md)
- Requirements: `CGR-001`〜`CGR-012`

本仕様は、番組表GCに限って、番組表schema v1の「HF-05Aでは自動GCを行わない」という初期境界と、
ADR-0015に記録された未採用の保持値候補を置き換える。番組identity、immutable revision、予約snapshotの
保護は変更しない。

## 目的

定期的な番組表更新でSQLiteが同期回数に比例して増え続けないようにする。最新の番組表、実行中の要求、予約、
録画履歴、自動予約結果を壊さず、Mirakurunまたはmirakcから再取得できる古い観測を自動で整理する。
利用者の設定は増やさない。

## 要件

| ID | 要件 |
|---|---|
| `CGR-001` | `recording serve`と`catalog sync`は、新しいcatalog世代を作る前に自動GCを1回実行する。 |
| `CGR-002` | backendごとの最新3件の`COMPLETED`世代と全`RUNNING`世代を保護する。最新3件には公開中の世代とその前の完了世代2件を含める。 |
| `CGR-003` | 保護対象外のterminal世代から、program観測、service観測の順で削除する。 |
| `CGR-004` | 観測を持たないterminal世代summaryは終了から30日保持し、保護対象でなければ削除できる。summaryの件数は同期完了時の実績として維持する。 |
| `CGR-005` | program instanceとその全revisionは最終観測から30日保持し、保持中観測、予約、自動予約結果の参照があれば期限後も残す。期限内のinstanceから古いrevisionだけを削除しない。 |
| `CGR-006` | serviceは最終観測から30日保持し、service観測とprogram instanceの両方がない場合だけ削除する。 |
| `CGR-007` | 1 transactionの削除を1,000行以下、1回のGCを30秒以下に制限する。 |
| `CGR-008` | 時間切れと取消し後の再実行は、進捗tableなしで同じ保持状態へ収束する。 |
| `CGR-009` | GCの失敗だけを理由に完成済みcatalogを無効化せず、親contextが有効なら次のprovider取得を試行する。 |
| `CGR-010` | GCは録画、録画履歴、予約、自動予約規則、backup、録画ファイルを削除または変更しない。 |
| `CGR-011` | 自動`VACUUM`、`REINDEX`、filesystem走査、network I/O、追加dependencyを導入しない。 |
| `CGR-012` | 通常logは削除件数、続きの有無、所要時間、固定理由だけを出し、番組・局・URL・path・SQL詳細を出さない。 |

予約と録画履歴が参照する行は無期限に残り得る。本仕様が固定範囲へ収束させる対象は、providerから再取得でき、
利用者データから参照されていない番組表データであり、DB全体の絶対byte数ではない。対象は番組表に限る。

## 固定値

| 項目 | 値 |
|---|---:|
| 保持する完了世代 | backendごとに最新3件 |
| 世代summary保持期間 | 終了から30日 |
| program instance／revision保持期間 | 最終観測から30日 |
| service保持期間 | 最終観測から30日 |
| 1回のID取得 | 1〜1,000件 |
| 1 transactionの削除 | 1〜1,000行 |
| 1回のGC期限 | 30秒 |
| 専用worker／queue | 0 |
| 自動`VACUUM` | なし |

保持期間の比較にはUTC millisecondを使う。`finished_at_utc_ms < now - 30日`または
`last_seen_at_utc_ms < now - 30日`を期限超過とし、境界と同値の行は残す。

## 保護対象

次の行は削除候補に含めない。

1. 全`RUNNING` catalog世代と、そのservice／program観測
2. backendごとの最新3件の`COMPLETED`世代と、そのservice／program観測
3. 保持中の観測から参照されるservice、program instance、program revision
4. `reservations.program_instance_id`または`reservations.program_revision_id`から参照される番組
5. `automatic_reservation_matches.program_instance_id`から参照される番組
6. 保護対象の番組instanceを持つservice

録画処理と録画segmentは予約を経由して番組情報を保持する。予約からの参照が不確実な場合は削除しない。

期限を過ぎた未参照program instanceを削除した後、同じprovider event locatorが再出現しても古いIDを復元しない。
新しいinstance、revision 1、`NEW_INSTANCE`として保存する。期限付きtombstone、identity台帳、revision番号の
継続用counterは作らない。期限内または参照中のinstanceは全revisionを残すため、その間のcontent hash再観測は
既存のIDを再利用する。

## 実行契機

### `recording serve`

起動直後と定期更新の各回で、新しい`RUNNING`世代を作る前にGCを呼ぶ。GCが30秒の処理上限へ達した場合は
部分完了として終了し、catalog取得を続ける。親contextが取り消された場合は新しい取得を始めない。

### `catalog sync`

明示同期も、世代作成前に同じGCを呼ぶ。別の保持policyや特別な全件削除を持たせない。

`ctrlcmd serve`、`ui serve`、番組検索、予約操作はGCを開始しない。

## 削除順

GCは、次の段階を順に繰り返す。各段階は削除する主キーを最大1,000件だけ選び、同じtransactionでそのbatchだけを
削除する。batch完了後にdeadlineと親contextを確認する。

1. 保護対象外の世代に属する`program_observations`
2. 保護対象外の世代に属する`service_observations`
3. 観測がなく、30日を過ぎた保護対象外の`catalog_syncs`
4. 30日を過ぎたprogram instanceのうち、観測、予約、自動予約結果から参照されないものに属する全`program_revisions`
5. revisionがなく、観測、予約、自動予約結果から参照されない期限超過`program_instances`
6. 観測とprogram instanceを持たない期限超過`services`

削除候補は時刻、主キーの昇順で選び、同じDB状態から同じ順序になるようにする。段階4で一つのinstanceに1,000件を
超えるrevisionがある場合は、先頭batchだけを削除し、次回または次のbatchで続ける。instanceはrevisionが0件になるまで
削除しない。

一つの段階で候補が残っていても30秒に達したら終了する。次回は同じ照会から再開できるため、専用cursorや進捗行を
永続化しない。

## Schema migration

次のschema migrationを一つ追加する。

- `program_revisions_no_delete` triggerを削除する。
- `program_revisions_no_update` triggerは維持する。
- `catalog_syncs(state, finished_at_utc_ms, id)`、`program_instances(last_seen_at_utc_ms, id)`、
  `services(last_seen_at_utc_ms, id)`に加え、program／service観測と予約の参照確認に必要なGC用indexを追加する。
- 既存indexで先頭列から同じ照会を支えられる場合は重複indexを追加しない。
- 既存行、schema migration履歴、予約、録画、backupを変更しない。
- migration後に`PRAGMA foreign_key_check`が空であることを確認する。

既存のmigration contractに従い、空DBは全migrationを適用し、旧schemaからの更新は事前backupを必要とする。

## 結果とlog

GC結果は少なくとも次を持つ。

- 削除したprogram観測数
- 削除したservice観測数
- 削除した世代summary数
- 削除したrevision、program instance、service数
- 処理上限により続きが残るか
- 所要時間

正常な0件削除は成功とする。30秒到達はerrorにせず、続きありとして扱う。DB I/O、整合性違反、入力不正は
`catalog-gc-failed`へまとめ、内部errorを通常出力へ含めない。

## 失敗と再開

- transaction途中のerrorまたは取消しでは、そのbatchだけをrollbackする。
- 以前にcommitしたbatchは戻さない。次回の同じ選択条件で安全に続行できる。
- GC失敗後も最新の完了世代を変更しない。
- GC失敗だけでは`recording serve`を終了せず、catalog更新結果と別に記録する。
- schemaまたはforeign keyが想定外の場合は削除を止め、推測で修復しない。

## 必須テスト

### SQLite統合

- 完了世代0、1、2、3、4件で、最新3件だけの観測が残る。
- 複数backendが互いの最新3件へ影響しない。
- `RUNNING`世代は時刻に関係なく残る。
- `FAILED`世代の観測を削除し、30日以内のsummaryを残す。
- 30日境界の直前、同値、直後を固定clockで確認する。
- 保持中観測、予約、自動予約結果の各参照がprogram instanceとrevisionを保護する。
- 参照のない期限超過program instance、revision、serviceだけを削除する。
- 1,001件以上を複数batchで処理し、各transactionが1,000行を超えない。
- 各段階の失敗と取消し後に再実行し、foreign key違反と二重削除を起こさない。

### Migration

- 旧schemaの全table、index、trigger、代表データを準備し、事前backup付きで次schemaへ進める。
- migration後も番組表、予約、録画履歴、自動予約結果を読み戻せる。
- revisionの更新は禁止されたままで、参照中revisionの削除はforeign keyにより拒否される。
- 期限超過後に同じlocatorを再投入すると、新しいinstance、revision 1、`NEW_INSTANCE`になる。
- 空DBへの全migrationと、旧binary用backupへの復元を確認する。

### Application and command

- `recording serve`と`catalog sync`だけが同期前にGCを呼ぶ。
- GCの0件、部分完了、DB失敗の各結果後にcatalog取得を試行する。
- 親context取消しではGC後に新しいprovider取得を開始しない。
- GC logに番組、局、URL、path、生のerrorが含まれない。
- 100回の合成同期後、観測行数が保持3世代分へ収束する。

長時間の時間指定耐久試験、実放送、実チューナー、KonomiTV画面は本仕様の完了条件に含めない。

## 対象外

- 録画ファイルや録画履歴の容量管理
- 利用者向けの手動GC／全消去command
- 自動`VACUUM`、DB file shrink、定期`REINDEX`
- チャンネル設定の生成または更新
- providerから取得する番組期間の変更
- catalog schema全体の置換
