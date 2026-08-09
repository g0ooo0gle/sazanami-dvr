# 録画開始前の番組時刻追従仕様 第1版

- Status: Accepted
- Accepted date: 2026-08-09
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related decision: [`../../docs/adr/0029-bounded-mirakurun-preclaim-follow.md`](../../docs/adr/0029-bounded-mirakurun-preclaim-follow.md)

## 目的

完成済み番組表で同じ番組の開始時刻または放送時間が変わった場合に、録画開始前の予約だけを安全に更新する。

## 要件

| ID | 要件 |
|---|---|
| `BPF-001` | 同じMirakurun backend内で、固定したlocator、raw event ID、時刻上限を全て満たす変更だけを`VERIFIED_SUCCESSOR`とする。 |
| `BPF-002` | 追従を要求した有効予約で、録画処理が存在しない場合だけ自動更新する。 |
| `BPF-003` | 完成済み番組表のrevision、開始時刻、放送時間、予約version、更新時刻を一つのtransactionで更新する。 |
| `BPF-004` | 同じrevisionの再評価、競合、曖昧な変更、時刻不明、範囲外変更では予約を変えない。 |
| `BPF-005` | 番組表更新または追従処理が失敗しても、以前の完成番組表と予約を維持する。 |
| `BPF-006` | 評価は256件ずつ、合計16,384件以下とし、追加goroutineとqueueを作らない。 |
| `BPF-007` | KonomiTVへ追従要求を有効として返し、更新後の時刻を通常の予約取得で返す。 |
| `BPF-008` | 通常出力は件数と固定理由だけにし、接続先、予約ID、番組情報、path、生の応答を含めない。 |

## Mirakurun継続判定

次を全て満たす場合だけ、以前のrevisionと新しい観測を同じ番組の変更とする。

| 条件 | 上限 |
|---|---:|
| backend | 同一 |
| service locator | 同一 |
| event locator | 同一 |
| raw event ID | 両方に存在し同一 |
| 前回観測からの時間 | 36時間以内 |
| 開始時刻の差 | 前後6時間以内 |
| 旧・新の放送時間 | 60秒以上12時間以内 |
| 放送時間の差 | 6時間以内 |

時刻の加減算はoverflowを検査する。文字列version、番組名、説明、provider IDのいずれか一つだけを根拠にしない。service identityは`PROVISIONAL`のまま維持する。

同じcontent hashは`SAME_CONTENT`とする。条件を満たす内容変更は新しいimmutable revisionを作り、`VERIFIED_SUCCESSOR`とする。条件を満たさない内容変更は`AMBIGUOUS`とし、新しいrevisionを作らず、以前のrevisionをその世代の表示に残す。

## 予約追従

対象は次を全て満たす予約である。

- `ACTIVE`である。
- `requested_follow=true`である。
- 対象番組が最新の完成済み世代で`VERIFIED_SUCCESSOR`になっている。
- 予約が選んでいるrevisionより新しい。
- `recording_attempts`に対象予約のrowがない。
- 評価時の予約versionと選択revisionが更新時にも一致する。

更新するのは`program_revision_id`、`start_at_utc_ms`、`duration_seconds`、`version`、`updated_at_utc_ms`だけとする。番組instance、service、event、title、station、priority、requested followは変更しない。

一件ずつ短いtransactionを使う。予約変更、取消し、録画確保が先にcommitされた場合は自動追従を行わない。全件を一つのtransactionに入れない。

## 実行と失敗

`recording serve`の番組表更新は、provider取得、候補保存、候補検証、世代完成、予約追従の順に実行する。世代完成前に追従しない。

追従処理が一件失敗した場合は、その予約を変更せず、残りの予約も処理せずに集計を失敗とする。完成済み番組表は戻さない。次回の正常な定期更新で同じ対象を再評価できる。

通常出力は次の集計だけを許可する。

- 評価件数
- 更新件数
- 変更なし件数
- 競合または対象外件数
- 成功または固定した失敗段階

## KonomiTV

予約追加または変更で`tuijyuu_flag=true`を受けた場合は`requested_follow=true`として保存する。予約取得では実装済みのMirakurun追従能力とrequested値からtrueを返す。追従後は更新した開始時刻と放送時間を返す。

`tuijyuu_flag=false`の予約は更新しない。未対応のactive recording延長を成功したようには返さない。

## 必須test

### Unit

- 開始時刻の前後移動、放送時間の延長・短縮、同じ内容。
- 36時間、6時間、60秒、12時間の境界と一単位超過。
- raw event IDなし、不一致、service／event locator不一致、時刻不明、overflow。
- 番組名だけの変更でも他条件を満たせば追従し、番組名だけの一致では追従しない。
- 追従off、同じrevision、古いrevision、競合、録画処理あり。

### SQLite／process

- 予約のversion、revision、開始、放送時間を同じtransactionで更新する。
- 更新後もinstance、service、event、title、station、priorityを維持する。
- 256件のpage、16,384件、16,385件、取消し／変更／claimとの競合。
- 同じ世代の再評価と再起動後の再評価でversionが重複増加しない。
- 番組表世代の失敗、予約更新失敗、DB取消し後に以前の予約を読み戻せる。
- KonomiTVの予約追加、一覧、変更、取消しが更新後の時刻と追従flagで動く。
- 通常起動以外のcommandが暗黙にnetworkへ接続しない。

### 全体

- full、shuffle、race、vet、govulncheck。
- CGOを無効にしたLinux／Darwinのamd64／arm64 build。
- Hosted Ubuntu CI。
- 実験環境の通常更新、予約、録画、再生回帰。実際の放送時刻変更が観測できなければ`NOT RUN`とする。

## 対象外

- 録画開始後の延長、短縮、停止、retune。
- EIT present/following、event relay、TS解析。
- 自動予約、複数同時録画。
