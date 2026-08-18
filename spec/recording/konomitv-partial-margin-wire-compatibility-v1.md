# KonomiTV片側録画マージンwire互換仕様 v1

- Status: Accepted
- Version: 1
- Accepted date: 2026-08-19
- Decision: ADR-0066
- Product copy path: `spec/recording/konomitv-partial-margin-wire-compatibility-v1.md`

## Scope

固定KonomiTV v0.14.1がCtrlCmd 2013／2015へ送る録画マージンのうち、
`use_margin_flag=0`なのに後続数値が0でない入力を扱う。この仕様は
`basic-recording-settings-v1.md`のflag 0におけるwire値0制約だけを置き換える。

## Requirements

| ID | Requirement |
|---|---|
| `KPM-001` | 2013と2015は`use_margin_flag=0`の後続開始・終了マージンを読み、値にかかわらず要求を継続する。 |
| `KPM-002` | `KPM-001`の二値を意味解釈、範囲検証、保存、ログ出力しない。 |
| `KPM-003` | flag 0は`Margins=nil`へ正規化し、Sazanamiの既定開始・終了マージンを使う。 |
| `KPM-004` | 2011はflag 0の予約を0、0の後続二値で返す。受信した未使用値を折り返さない。 |
| `KPM-005` | flag 1は両値を-3,600～3,600秒に制限し、範囲外を固定失敗として拒否する。 |
| `KPM-006` | flag 2以上、truncated、余分なbyte、他の未対応設定を引き続き拒否する。 |
| `KPM-007` | 予約追加、変更、DB再open、実際の録画予定で同じ正規化結果を使う。 |
| `KPM-008` | Domain、DB schema、scheduler、HTTP API、製品版、依存を変更しない。 |
| `KPM-009` | 固定KonomiTVの片側指定を無効予約で追加・一覧・取消しし、録画fileを作らず確認できる。 |

## Wire examples

| flag | start field | end field | Result |
|---:|---:|---:|---|
| 0 | 0 | 0 | 受理し、既定マージン |
| 0 | 0 | 30 | 受理し、既定マージン。30は無視 |
| 0 | -15 | 0 | 受理し、既定マージン。-15は無視 |
| 0 | 32-bit境界 | 32-bit境界 | 受理し、既定マージン。両値は保持しない |
| 1 | -3,600 | 3,600 | 受理し、個別マージン |
| 1 | -3,601 | 0 | 拒否 |
| 1 | 0 | 3,601 | 拒否 |

## Failure behavior

入力全体の構造が不正な場合は、既存のCtrlCmd失敗応答を返す。flag 0の未使用値だけを理由に失敗させない。
保存失敗、番組不一致、重複は従来の失敗経路を維持し、未使用値の受理で成功へ見せ替えない。

## Required tests

- 2013と2015でflag 0、片側正数、片側負数、32-bit最小・最大を受理する。
- 受理後のapplication要求が`Margins=nil`である。
- 2011がflag 0、0、0を返す。
- flag 1の-3,600／3,600、一件超過、flag 2、truncated、余分なbyteを確認する。
- SQLite再openとschedulerの既定予定時刻を確認する。
- 固定KonomiTVのHTTP片側指定で追加201、一覧一件、取消し204、一覧0件を確認する。

## Evidence boundary

固定source確認と合成wire testは製品動作の証拠にできる。実験環境のHTTP往復は固定KonomiTVとの
black-box証拠にできる。番組名、ID、宅内IP、credential、raw要求は保存しない。
