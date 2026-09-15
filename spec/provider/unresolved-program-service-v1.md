# サービス未解決番組の保存仕様 v1

- Status: Accepted
- Accepted date: 2026-09-16
- Decision: [ADR-0074](../../docs/adr/0074-unresolved-program-service.md)
- Scope: catalogの番組保存。DB schema、CtrlCmd形式、録画streamは変更しない。

## 必須動作

### UPS-001: 同じ同期世代でサービスを照合する

番組のservice locatorは、対象backendかつ当該syncのservice観測から解決する。
過去の`services`行だけで解決してはいけない。同一locatorを持つ別backendのサービスも使わない。
Fake providerを含め、保存境界では同じ世代照合を適用する。

### UPS-002: 未解決観測を残す

サービスがない場合は`program_observations`に次を1行保存して、ほかの番組を続行する。

- `sync_id`、provider service/event locator、raw event IDは検証済みの入力値を使う。
- `classification`は`INVALID`。
- `validation_reason`は`service-not-in-current-catalog`。
- `program_instance_id`、`program_revision_id`、`content_hash`はNULL。

instance、revision、serviceを新規作成せず、既存のそれらも更新しない。
通常の番組表と新規録画候補には含めない。受信program countには含め、保存件数の照合を維持する。
生のprovider応答全体や未検証の番組本文を記録しない。

### UPS-003: 次回同期と既存参照

次回の同期世代でサービスを確認できれば、その番組を通常の保存・同一性判定へ渡す。
過去の未解決行を成功行へ書き換えない。既存予約・録画履歴のinstance/revision参照は保持する。
サービスがなくなった間は、過去にサービスを観測したDBでも新規DBと同じ未解決判定にする。

### UPS-004: 例外を広げない

サービス検索の「該当なし」だけを未解決として扱う。検索の取消し・DB障害を該当なしへ変換しない。
Locatorの不正、重複、必須項目欠落、JSON不正、body切断、期限・件数・byte上限超過は従来どおり失敗する。
当該世代はFAILEDにし、前回COMPLETED世代と既存channel mapを保持する。
PAT確認で失敗したサービスを除外する部分成功は引き続き禁止する。

### UPS-005: 資源とGC

schema、依存module、公開設定を追加しない。既存の1同期4,096サービス、262,144番組、
1page256件、SQLite書込み100件の上限を維持し、未解決行も件数上限へ数える。
全番組をメモリへ保持せず、既存の短いtransaction単位で保存する。
未解決行も世代ベースのGCで整理し、保持対象の予約・録画参照は引き続き保護する。

## 必須検証

1. 有効番組と未解決番組の混在で同期が完了し、有効番組だけが公開される。
2. 過去service行の有無と別backendの同一locatorで判定が変わらない。
3. 次回にserviceが現れれば通常処理でき、既存予約参照は変わらない。
4. 未解決行も重複・上限・取消し・DB失敗を隠さない。
5. 未解決行がGCで整理され、最新世代と保護参照は残る。
6. 合成providerでURL-only setupを確認し、実環境は対象製品commitを固定して短く再確認する。
