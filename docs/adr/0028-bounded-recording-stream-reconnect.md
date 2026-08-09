# ADR-0028: 録画ストリームを少ない回数だけ再接続する

- Status: Accepted
- Proposed date: 2026-08-09
- Decision date: 2026-08-09
- Owners: Project owner
- Decision reviewer: Codex（委任されたv1.0までの技術判断）
- Related plan: [`../plans/0041-bounded-recording-stream-reconnect.md`](../plans/0041-bounded-recording-stream-reconnect.md)
- Related specification: [`../../spec/recording/bounded-stream-reconnect-v1.md`](../../spec/recording/bounded-stream-reconnect-v1.md)
- Product copy path: `docs/adr/0028-bounded-recording-stream-reconnect.md`
- Supersedes: 初回録画仕様の`Recording retry／stream reconnect = 0`
- Superseded by: None

## Context

現在は、録画streamが予定終了前に切れると、その時点で部分録画として終了する。これは初版の安全な挙動だが、短いLAN切断やMirakurunの一時停止でも残りの録画時間を失う。

一方、無制限の再試行、複数streamの同時接続、再起動後のblind appendは、負荷と復旧を複雑にする。日常運用に必要な範囲だけを、小さい直列処理として追加する。

## Decision

同じ録画処理の中で、stream leaseだけを最大3回開き直す。最初の接続を含め、1件の録画が開くstreamは最大4本である。同時に開くのは常に1本とする。

追加接続の前に古いleaseを閉じ、1秒、2秒、4秒の順に待つ。親contextが取り消された場合、予定終了まで60秒未満の場合、または3回を使い切った場合は次を開かない。

再接続の対象は、一時的な接続失敗、read timeout、早期EOF、peer切断だけとする。対象サービスなし、要求拒否、応答形式不正、明示停止、DB失敗、ファイル失敗は対象外とする。

再接続後も、同じattempt、ordinal 0のsegment、部分ファイル、byte countを使う。接続ごとの別ファイルやDB rowは作らない。プロセス再起動後には追記しない。

一度でも再接続して予定終了へ達した場合は`SUCCEEDED/COMPLETED_AFTER_RECONNECT`とする。接続上限まで回復できなかった場合は`STREAM_RECONNECT_EXHAUSTED`とし、保存済みbyte数に応じて`PARTIAL`または`FAILED`にする。

DB migrationと新しい依存は追加しない。

## Consequences

短い通信障害から自動回復でき、既存の完成処理と復旧処理をそのまま使える。TSの欠損自体は補完しないため、再接続した録画に連続性や完全性を保証しない。終了理由から再接続の有無は判断できる。

最悪の場合、既存の接続・read期限に加えて7秒の待機が発生する。ただし全体は予約終了時刻と親contextに制限される。

## Rejected alternatives

- 接続ごとの別segment・別ファイル: 結合と配信が必要になるため不採用。
- 無制限または指数的に長い再試行: 障害時の負荷と終了時間を固定できないため不採用。
- 再起動後の追記: 同じ録画処理だと安全に証明できないため不採用。
- 切断した録画を無条件で成功扱い: 欠落を隠すため不採用。

## Verification

Accepted仕様の境界、失敗分類、100回以上の切断混在試験、実験環境での録画完了と再生確認を、同じ製品コミットで行う。

## Revisit when

- Mirakurun／mirakc側が再開位置やstream継続識別子を提供する。
- 複数同時録画でprovider全体の接続上限を導入する。
- 録画品質検査やTS discontinuityの表示を製品範囲へ加える。
