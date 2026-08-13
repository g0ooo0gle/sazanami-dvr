# 放送中に追加した予約の録画開始仕様 第1版

- Status: Accepted
- Accepted date: 2026-08-13
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related decision: [`../../docs/adr/0061-in-progress-reservation-start.md`](../../docs/adr/0061-in-progress-reservation-start.md)

## 目的

放送中の番組へ新しく録画予約を追加した場合、古い予約の安全境界を維持しながら現在時刻から録画を始める。

## 要件

| ID | 要件 |
|---|---|
| `IPSTART-001` | 録画処理を持たない有効予約が途中録画条件を満たす場合、予定開始から5分を超えていてもclaimする。 |
| `IPSTART-002` | 途中録画条件は、作成時刻が予定開始以降、現在以前、作成から5分以内であることとする。 |
| `IPSTART-003` | 予定終了まで60秒未満なら、途中録画条件を満たしても`MISSED / LATE_START_EXPIRED`とする。 |
| `IPSTART-004` | 予定開始と予定終了は予約の元の値を維持し、実開始にはstreamを開始した時刻を保存する。 |
| `IPSTART-005` | 番組冒頭を補完、連結、推測せず、現在取得できるTSから既存の録画fileへ書く。 |
| `IPSTART-006` | 番組開始前に作成された古い予約は、従来の予定開始から5分という上限を維持する。 |
| `IPSTART-007` | 作成時刻が欠落、未来、不正な場合は途中録画として救済しない。 |
| `IPSTART-008` | DB schema、CtrlCmd形式、Native API、provider、同時録画上限、file形式を変更しない。 |
| `IPSTART-009` | 録画中停止と再生可能な部分録画は、録画中停止仕様第1版をそのまま適用する。 |
| `IPSTART-010` | 番組名、接続先、絶対path、生の要求・応答を通常ログへ追加しない。 |

## 判定

Schedulerが現在時刻`now`に予約を読んだとき、次を満たせば途中録画として開始できる。

```text
created_at >= planned_start
created_at <= now
now - created_at <= 5 minutes
planned_end - now >= 60 seconds
```

時刻はUTCで比較する。境界値は含む。予約に録画処理が一件でもあれば、schedulerの既存queryにより対象外となる。

途中録画条件を満たさない場合は、既存どおり次を使う。

```text
now - planned_start <= 5 minutes
planned_end - now >= 60 seconds
```

どちらにも当てはまらなければ`MISSED / LATE_START_EXPIRED`とする。

## 録画と履歴

Claim、stream取得、再接続、書込み、完成名公開、復旧は既存処理を使う。新しい録画状態と終了理由は追加しない。
録画開始後は、通常録画と同じ終了予定、番組時刻追従、利用者停止、容量不足、再接続の規則を適用する。

途中録画が正常に予定終了へ達した場合は通常の成功として扱う。途中から始めた事実は、予定開始と実開始の差で
確認できる。利用者がKonomiTVから削除した場合は、CtrlCmd 1014でその一件だけを止め、既存仕様に従って
部分録画を確定する。

## 固定値

| 項目 | 値 |
|---|---:|
| 新規作成後の処理猶予 | 5分、境界を含む |
| 録画を始める最小残り時間 | 60秒、境界を含む |
| 追加goroutine／queue／timer | 0 |
| 追加DB column／network要求 | 0 |

## 必須test

- 番組開始から20分後、残り30分、作成直後の予約を開始する。
- 作成後5分ちょうどを開始し、5分1ミリ秒後は開始しない。
- 番組開始前に作成した予約を、開始から5分1ミリ秒後に開始しない。
- 作成時刻が現在より1ミリ秒後、または0値なら途中録画として開始しない。
- 残り60秒ちょうどを開始し、59秒999ミリ秒では開始しない。
- 予定開始と実開始を別の値として履歴へ保存する。
- 同時録画枠、再接続、時刻追従、ワンセグ、容量不足、process停止を回帰確認する。
- CtrlCmd 1014の開始前取消しと録画中停止、188 byte境界、部分録画再生を回帰確認する。

## 実験環境

固定KonomiTVから放送中番組を一件追加し、Sazanamiがstreamを開始してbyte数を増やすことを確認する。その後、
KonomiTVから削除し、録画中0件、`PARTIAL / USER_REQUESTED_STOP`、再生可能な部分録画へ収束することを
確認する。番組名、放送局名、予約番号、接続先、保存path、TS内容は記録しない。
