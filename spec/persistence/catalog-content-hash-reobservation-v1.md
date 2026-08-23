# Catalog content hash再観測仕様 v1

- Status: Accepted
- Date: 2026-08-23
- Applies to: Sazanami DVR v0.5.0
- Governing decisions: ADR-0015、ADR-0027
- Related handoff: Handoff 0060

## 目的

番組内容がAからBへ変わった後にAへ戻っても、同じimmutable revisionを再利用して番組表更新を
継続する。ADR-0015とcatalog schema v1の冪等性を、製品repositoryの保存処理へ適用する。

同じ内容を増やさない。基準は完成世代である。失敗した候補を次の判断へ混ぜない。

## 要件

### `CHR-001` 比較基準

既存instanceの新しい観測は、同じbackendで最後に`COMPLETED`になった世代の
`program_observation`が参照するrevisionと比較する。比較時刻には、その完成世代の開始時刻を使う。
FAILED／RUNNING世代だけが更新した最大revisionやinstanceの最終観測時刻を比較基準にしない。

完成世代に同じinstanceの観測がない場合は、FAILED／RUNNING世代のmaterialや時刻と比較しない。
現在の観測を初回公開として扱う。同じhashのrevisionがあれば再利用し、なければ既存の最大番号に一を
加えて保存する。どちらも現在の観測を`NEW_INSTANCE`として公開する。最大番号は採番だけに使う。

### `CHR-002` 既存revisionの再利用

比較基準と同じcanonical hashなら、そのrevision IDと番号を再利用して`SAME_CONTENT`とする。
hashが異なる場合は、比較基準との既存continuity判定を先に行う。判定に成功し、同じinstance内に
同じhashの過去revisionがあれば、そのIDと番号を再利用して`SAME_CONTENT`とする。
新しいrevisionを挿入しない。

### `CHR-003` 観測の保存

再利用したrevisionを現在の`program_observation`へ結び付け、分類を`SAME_CONTENT`とする。
instanceの最終観測時刻は既存規則どおり更新する。

### `CHR-004` 未観測内容の維持

continuity判定に失敗した場合は、比較基準のrevisionを`AMBIGUOUS`観測へ結び付ける。判定に成功し、
同じinstanceにhashが存在しない場合だけ、既存の最大revision番号に一を加えて新しいrevisionを作る。
失敗世代が先に番号を使っていた場合の欠番を許容する。

### `CHR-005` 永続化境界

`program_revisions`のinsert-only triggerと、instance内のrevision番号／hashのunique制約を維持する。
schema、migration、backup形式、既存行、依存を変更しない。

### `CHR-006` 世代の公開

再観測を含むsyncは通常の検証後に`COMPLETED`へ進める。FAILED／RUNNING generationをcurrent catalogへ
公開する既存条件は変更しない。

### `CHR-007` 秘匿

番組名、局名、event ID、接続先、DB path、raw provider応答を新しいlogやGit証拠へ残さない。

## 必須検証

- SQLite統合testで同じinstanceをA→B→A→Aと観測する。
- Bはrevision 2、最初へ戻ったAと続くAは同じrevision 1のIDを使う。
- 四回のsyncがすべて完了し、戻ったAは`SAME_CONTENT`でcurrent catalogに現れる。
- 完成Aの後に候補Bを保存して世代検証を失敗させ、次のCをAとのcontinuityで判定する。Cは
  FAILED世代のBを表示せず、新しい最大revision番号で`VERIFIED_SUCCESSOR`になる。
- 最初の候補Aを世代検証で失敗させ、完成世代に観測がないままBを取得する。BはAとのcontinuityを
  判定せず、最大番号を採番だけに使って`NEW_INSTANCE`として完成する。
- 既存の同一内容、変更、曖昧、failed generation、restart testを維持する。
- Ubuntu LABで同じcandidateの番組表更新を連続二回完了し、固定KonomiTV v0.14.1の番組APIを確認する。

72時間試験、DB履歴削除、VACUUM、別KonomiTV版、Komorebiは本仕様の完了条件に含めない。
