# ADR-0029: 録画開始前だけMirakurunの番組時刻変更へ追従する

- Status: Accepted
- Proposed date: 2026-08-09
- Decision date: 2026-08-09
- Owners: Project owner
- Decision reviewer: Codex（委任されたv1.0までの技術判断）
- Related plan: [`../plans/0042-completed-catalog-preclaim-follow.md`](../plans/0042-completed-catalog-preclaim-follow.md)
- Related specification: [`../../spec/recording/bounded-preclaim-follow-v1.md`](../../spec/recording/bounded-preclaim-follow-v1.md)
- Product copy path: `docs/adr/0029-bounded-mirakurun-preclaim-follow.md`
- Supersedes: ADR-0018のcompleted-catalog preclaim部分
- Superseded by: None

## 背景

予約後に放送開始や終了が変わると、固定した予約時刻のままでは番組の先頭または末尾を失う。v0.0.4は録画中にも番組表を更新できるが、予約を新しいrevisionへ移さない。

Mirakurunの番組情報にはTSIDとevent generationがない。service IDやprogram IDだけでは、一般的な番組identityを確定できない。一方、同じMirakurun設定内で短時間に観測した同じserviceとeventの変更まで全て拒否すると、実用上の時刻追従ができない。

## 判断

同じMirakurun backend内の短期間の変更だけに使う、上限付き継続判定を追加する。次の条件を全て満たした場合だけ、新しいrevisionを`VERIFIED_SUCCESSOR`とする。

1. backend、service locator、event locator、raw event IDが一致する。
2. 以前の観測から36時間以内である。
3. 旧・新の開始時刻と放送時間が妥当である。
4. 開始時刻の差が前後6時間以内である。
5. 旧・新の放送時間が60秒以上12時間以内で、差が6時間以内である。

この判定は同じbackend内の予約追従にだけ使う。Mirakurun serviceのidentityは`PROVISIONAL`のままとし、別backendとの統合、別providerへの移行、一般的な互換性の証明には使わない。runtime versionの文字列だけでは判定しない。

条件を満たさない変更は`AMBIGUOUS`とする。その世代では以前のrevisionを表示用に維持し、予約を変更しない。

完成済み番組表への切替え後、利用者が追従を要求した有効予約を評価する。対象が`VERIFIED_SUCCESSOR`で、まだ録画処理が存在しない場合だけ、予約version、選択revision、開始時刻、放送時間、更新時刻を一つのtransactionで更新する。同じrevisionの再評価は更新しない。

録画処理が作られた後は、予定時刻を変更しない。録画中の延長、短縮、停止、retuneは別の判断とする。

KonomiTVへは追従要求を有効として返す。初版DBの`effective_follow=0`列は変更せず、`requested_follow`と実装済み能力を応答の根拠にする。DB migrationと新しい依存は追加しない。

## 結果

短時間の通常の編成変更へ追従できる。EID再利用や大きな移動は予約時の内容へ倒れるため、取り逃しが残る場合はあるが、別番組へ自動で移る危険を抑えられる。

予約更新が失敗しても、完成済み番組表と以前の予約は残る。旧revisionはimmutableなままDBへ残るが、追従専用の履歴tableは作らない。

## 採用しなかった案

- program ID一致だけで更新する: EID再利用を防げないため不採用。
- 番組名や説明の類似を条件にする: 表記変更と内容変更を安定して区別できないため不採用。
- 利用者確認を毎回求める: 無人録画の時刻変更に間に合わないため不採用。
- 録画中の延長まで同時実装する: deadlineと復旧の責任が異なるため分離する。

## 再検討する条件

- Mirakurun／mirakcがTSID、event generation、または明示的な後継識別子を提供する。
- 実験環境で36時間または6時間の境界が通常の編成変更を妨げる証拠が得られる。
- 録画中の延長を実装する。
