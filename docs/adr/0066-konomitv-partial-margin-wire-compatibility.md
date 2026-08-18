# ADR-0066: 無効な録画マージン数値はEDCBと同じく無視する

- Status: Accepted
- Proposed date: 2026-08-19
- Decision date: 2026-08-19
- Owners: Project owner
- Decision reviewers: Codex
- Related requirements: KPM-001～KPM-009
- Related planning documents: Plan 0082
- Related handoffs: Handoff 0055、Handoff 0058
- Product copy path: `docs/adr/0066-konomitv-partial-margin-wire-compatibility.md`
- Product sync state: NOT COPIED
- Supersedes: ADR-0039のflag 0におけるwire値0制約だけを置き換える
- Superseded by: None

## Context

固定KonomiTV v0.14.1は、録画開始・終了マージンが両方存在するときだけCtrlCmdの
`use_margin_flag=1`を書き込む。片方だけが指定された場合、flagは0だが、指定された側の数値は
後続fieldへ残る。Sazanamiはflag 0で後続二値が0以外なら要求全体を拒否していたため、KonomiTVの
HTTP予約追加が500になった。

flag 0は個別マージンを使用しない意味であり、後続二値の意味を保証しない。Sazanamiが二値の0まで
要求することは、固定clientとEDCBの実用境界より厳しい。

## Decision drivers

- 固定KonomiTVの正規なHTTP入力を変更せずに受理する。
- flagを個別マージンの唯一の有効条件とする。
- KonomiTVとEDCBで録画予定を一致させる。
- Domain、DB、scheduler、公開APIを変更しない。
- 未使用値を保存、表示、ログ出力しない。

## Options

### Option A: flag 0の後続二値を無視する

flag 0は既定マージンへ正規化する。後続二値はcodec上で読むが、意味解釈、範囲検証、保存を行わない。

### Option B: 欠けた側を0秒として個別マージンを使う

片側指定を反映できるが、flag 0をSazanami独自に有効扱いし、EDCBと異なる。

### Option C: KonomiTVだけを修正する

Client forkに依存し、SazanamiのCtrlCmd互換境界は改善しない。

## Decision

Project ownerの2026-08-19の承認によりOption Aを採用する。2013と2015は、
`use_margin_flag=0`なら後続の開始・終了マージン値を無視し、既定マージンとして扱う。2011は
正規化後のflag 0と0、0を返す。flag 1では従来どおり両値を-3,600～3,600秒に制限する。

## Consequences

### Positive

- KonomiTVの片側指定で予約追加と変更が失敗しない。
- EDCBと同じflag意味を維持できる。
- Adapterの分岐だけで修正でき、永続形式を変えない。

### Negative

- KonomiTV HTTPで片側だけ指定した値は、EDCBと同じく録画予定へ反映されない。
- flag 0の未使用二値に対する従来の厳格な0検査を緩める。

### Risks and mitigations

- 意図しない値の反映は、flag 0で常に`Margins=nil`へ正規化して防ぐ。
- 範囲外入力の拡大はflag 0の未使用fieldだけに限定し、flag 1と全codec上限を維持する。
- 回帰は2013／2015／2011のwire testと実際の予定時刻testで検出する。

## Verification

- flag 0と非0の後続二値を受理し、既定マージンへ正規化する。
- flag 1の境界と一件超過を従来どおり判定する。
- 2013追加、2011読戻し、2015変更、SQLite再open、schedulerの既定予定を確認する。
- 固定KonomiTVの片側指定で追加201、一覧一件、取消し204、一覧0件を実環境で確認する。

## Product synchronization

- Handoff: Handoff 0058
- Planning source commit: NOT FIXED
- Target product base commit: `f77a275893e97a02779c67031d75c90f4467cf3e`
- Product destination: `docs/adr/0066-konomitv-partial-margin-wire-compatibility.md`
- Last synchronized product commit: NOT COPIED
- Known divergence: 製品はflag 0の後続二値へ0を要求する

## Revisit when

- 固定KonomiTVが片側指定を個別マージンとしてwireへ表すよう変更する。
- CtrlCmd互換profileを別の固定EDCB clientへ広げる。
- 既定マージンの設定源または2011の折返し形式を変更する。
