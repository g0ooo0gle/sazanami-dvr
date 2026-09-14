# ADR-0072: 番組表DBを固定上限で自動整理する

- Status: Accepted
- Decision date: 2026-09-15
- Deciders: Project owner
- Reviewer: Codex
- Authorization: Project ownerは番組表の自動GCを次の最優先機能とし、利用者が考えずに使える既定動作を承認した
- Related plan: [Plan 0091](../plans/0091-bounded-catalog-retention.md)
- Related specification: [番組表保持仕様 v1](../../spec/persistence/catalog-retention-v1.md)
- Extends: ADR-0015、ADR-0021、ADR-0027
- Supersedes: ADR-0021の初期製品内delete／prune対象外と、ADR-0027の古い番組表世代の削除対象外。
  いずれも番組表GCに限る
- Superseded by: None

## 背景

v1.2.0は、番組表更新ごとにサービスと番組の観測を新しい世代へ追加する。利用者へ返すのは最新の完了世代だが、
古い世代、観測、番組instance、revisionを回収しない。このまま既定5分間隔で動かすと、表示する番組数が同じでも
SQLiteだけが増え続ける。放置はできない。

番組表の大部分はMirakurunまたはmirakcから再取得できる。一方、予約、録画履歴、自動予約結果が参照する番組は、
過去の番組であっても削除できない。最新世代を読む要求と更新処理も同時に動くため、切替え直後に旧世代を消すことも
避ける必要がある。

## 判断

`recording serve`と明示`catalog sync`は、番組表を取得する前に同じ自動GCを実行する。利用者向けの有効化flag、
保持期間、batch size、実行間隔は追加しない。

### 最新世代と参照中の番組を残す

- backendごとに最新3件の`COMPLETED`世代と、全`RUNNING`世代を保護する。3件には公開中の世代と
  その前の完了世代2件が含まれ、現行の最短更新間隔5分に対して要求処理の終了猶予を確保する。
- 保護されない世代のservice／program観測は回収する。`FAILED`世代は保護しない。
- 観測を削除した世代summaryは30日保持し、その後に回収する。summaryの件数は同期完了時の実績であり、
  GC後に残る観測行数とは解釈しない。
- 番組instanceとその全revisionは、最後の観測から30日間保持する。期限内のinstanceから古いrevisionだけを
  抜き取らない。
- 30日を過ぎても、保持中の観測、予約、自動予約結果から参照される番組は回収しない。
- serviceは、最後の観測から30日を過ぎ、観測と番組instanceを持たない場合だけ回収する。
- 録画履歴、予約、自動予約規則、backup、録画ファイルは変更しない。

期限を過ぎた未参照番組のlineageは保持しない。同じprovider event IDが後から再出現した場合は新しい
`ProgramInstanceId`、revision 1、`NEW_INSTANCE`として扱う。期限付きtombstoneやidentity台帳は設けない。
これは、同じEIDが数か月後に再利用された場合を別番組とするADR-0015の境界に沿う。

revisionの内容は保持中に更新しない。回収可能になった番組instanceを削除するため、schema migrationで
`program_revisions_no_delete` triggerだけを外す。更新禁止、外部参照、foreign key検査は維持する。

### 処理時間とtransactionを制限する

削除は主キーを最大1,000件ずつ選び、1 transactionにつき1,000行以下でcommitする。1回のGCは30秒で終了し、
残りは次の通常同期へ送る。専用timer、queue、常駐goroutineは追加しない。

GCの失敗や時間切れは、現在の番組表、予約、録画を利用不能にしない。時間切れは部分完了として扱い、DB errorは
安定した理由と削除件数だけを出力する。その後のprovider取得は、親contextが有効である限り続ける。

### 空きpageを通常書込みへ再利用する

自動`VACUUM`は行わない。削除後の空きpageをSQLiteの後続書込みで再利用し、通常運転時のファイル増加を抑える。
既存DBの物理ファイルを縮小する操作は、必要性が確認された場合の別機能とする。

## 影響

同期回数に比例して増えていた観測行は、最新3世代分を中心とする固定範囲へ戻る。番組instanceとrevisionも、
参照のないものは30日後に回収される。既存DBは最初の数回で段階的に整理され、GC直後に物理ファイルが縮まなくても
空きpageが再利用される。

予約と録画履歴が参照する行は保持するため、DB全体の絶対上限は保証しない。自動GCが収束させるのは、providerから
再取得でき、利用者データから参照されていない番組表データである。対象は番組表に限る。

30日を超えて消えていた未予約番組が再び同じprovider event IDで現れた場合、新しい番組instanceとして扱うことがある。
予約と録画のfrozen情報は保持するため、既存の予約・録画結果には影響しない。

## 採用しなかった案

- **最新1世代だけ残す:** 切替え前に開始した要求が旧世代を読むため採用しない。
- **観測だけを消し、番組instanceを永久保持する:** 放送番組数に比例した増加が残るため採用しない。
- **DBを定期的に作り直す:** 予約、録画、backupとの境界が広がるため採用しない。
- **同期のたびに`VACUUM`する:** DB全体の書換え、一時容量、待ち時間が増えるため採用しない。
- **保持値をすべて設定可能にする:** 初期設定と検証範囲を増やすため採用しない。

## 検証

- 0〜4世代、複数backend、`RUNNING`、`COMPLETED`、`FAILED`を組み合わせ、保護対象を確認する。
- 30日境界と参照有無を組み合わせ、予約・自動予約結果が指す番組を保持する。
- 1,000行を超える観測を複数batchで削除し、取消し後の再実行が同じ結果へ収束することを確認する。
- 100回の合成同期で、観測行数が世代保持数に応じた範囲へ戻ることを確認する。
- GC errorと時間切れ後も、以前の完成番組表と新しいcatalog取得を利用できることを確認する。
- migration前後でforeign key検査、予約、録画履歴、番組表読出しが変わらないことを確認する。
- 時間指定の長時間耐久試験を完了条件にしない。

## 製品への複製

本ADRと番組表保持仕様v1を、v1.2.0 release commitをbaseにしたHandoff 0068へ列挙する。製品側で
byte-identicalなcopy commitをread backした後にだけ実装を始める。
