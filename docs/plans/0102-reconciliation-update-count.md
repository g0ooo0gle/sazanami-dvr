# Plan 0102: 録画再照合の変更件数を正しく記録する

> 実行: `superpowers:executing-plans`。既存契約に沿う内部修正で、新しい採用判断や文書同期は行わない。

## Goal

録画の周期再照合で、DBを実際に更新した件数だけを`changed`へ記録し、修正版を公開する。

## Context and evidence

- Base: `198a8069d8e162ccfc0f4749b211c6468eada222`（v1.3.5）。
- Authority: 2026-10-03の利用者指示「開発してテストして公開まで」。
- Spec: [録画削除・周期整合仕様](../../spec/recording/konomitv-recording-delete-reconciliation-v1.md)、KDR-006〜013。
- SQLiteのavailability更新は、差分がない場合の0行を正常なno-opとして受け入れる。
- `CompletedReconciler`は更新結果を受け取れず、no-opでも`Changed`を加算する。
- 番組表GCは最新3世代・30日・固定batchの回収を実装済み。setupのGCも後続のMCB-003で採用済み。
- PCR・初期PMT・録画確定・通常CtrlCmd応答の既知修正はv1.3.5に取り込み済み。

## Scope / Out of scope

内部Store port、SQLiteの戻り値、再照合の集計、回帰テスト、v1.3.6配布情報を変更する。
SQLの更新条件、DB schema、公開API、録画内容、NASの安全条件、GC方針は変えない。
古いNAS録画の復旧、入力経路の不連続、実機操作は対象外。
表示文字列のNUL変換はADR-0075で別判断とされており、今回の修正へ混ぜない。
契約変更がないため新規handoffとAccepted文書の変更は不要。

## Invariants / Global Constraints

- 0行は成功no-op、1行は更新、RowsAffected errorと想定外件数は失敗とする。
- SUCCEEDEDとPARTIAL / USER_REQUESTED_STOPだけを更新する。
- メインとワンセグを独立して集計し、起動時Recoveryの動作は維持する。
- KDR-007の100件page・1,000件run、KDR-008の直列実行・1分間隔を維持する。
- 通常ログに番組、path、ID、接続先、生のerrorを追加しない。

## Design and data flow / Alternatives

`SetRecordingAvailability`と`SetOneSegAvailability`の内部戻り値を`(bool, error)`へ変更する。
boolはSQL更新1行だけでtrueとし、周期再照合はtrueの場合だけChangedを加算する。
起動時Recoveryはboolを使わず、従来どおりerrorだけを扱う。
0行のエラー化は冪等性を壊すため採用しない。追加SELECTによる判定も競合とI/Oを増やすため採用しない。

## Review Focus

- 読出し後に別処理が同じ状態を保存した場合、changedが増えない。
- ワンセグだけのno-opをメインの変更として数えない。
- 同じ値の再更新でtimestampを変更しない。
- active・finalizingや不正な更新は更新成功にしない。
- cancelとDB errorを成功集計へ混ぜない。

## Steps

### Task 1: 永続化結果と変更件数を一致させる

Files: `internal/adapters/sqlite/recording_recovery_repository.go`、`internal/app/recording/{reconciler,recovery}.go`と対応テスト。
Interfaces: availability更新2メソッドだけを`(bool, error)`にし、他の呼出し契約を変えない。

- [x] 合成SQLiteで読出しと検査の間に同値を保存し、メイン／ワンセグの`Changed=0`を期待するテストを追加する。
- [x] 旧実装で`Changed=1`となるREDを確認する。
- [x] 最小修正と0／1／error、冪等更新のテストを追加する。
- [ ] 対象テスト、全Go test、race、vet、module verify、portable packagingを確認する。
- [x] 一回の独立レビューを受け、重大な指摘を修正する。

検査で見つかった追加残件: `TestStopWhileOneSegSyncsFinalizesOnlyThatRecording`は単独10回でも2回、
履歴を1秒だけ待つテスト側の上限に達した。実file／SQLiteの同期完了を確認するテストであり、
1秒以内の製品性能契約はないため、待機上限を10秒にする。停止結果、他録画の非取消し、同期回数の検査は維持する。

### Task 2: 修正版を公開する

Files: 製品version、Compose例、公開手順のversion例、CHANGELOG。

- [x] v1.3.6へ配布情報を更新し、変更履歴は今回の修正だけを書く。
- [ ] 最終commitのCI、PR差分と取り込みを確認する。
- [ ] tag、Release CI、amd64／arm64 archive、OCI imageを読み戻す。

## Verification / Failure and recovery

正常更新、競合no-op、冪等更新、ワンセグ、非対象状態、error／cancel、再起動後のDB読戻しを合成データで確認する。
実機・NAS・放送経路は`NOT RUN: hardware required`。長時間試験を完了条件にしない。
失敗時の再照合・cursor・安定reasonは既存仕様のままとする。

## Security and resource bounds / Rollback

schema 14、ファイル操作なし、追加query／buffer／goroutineなし。
修正前imageへ戻せる。DBや録画の復元・削除は不要。

## Open questions / Completion report

新しい設計判断なし。実行結果と最終SHAは完了時に追記する。
