# KonomiTV録画削除・周期整合仕様 v1

- Status: Accepted
- Date: 2026-08-18
- Applies to: Sazanami DVR v0.5.0候補以降
- Decision: `docs/adr/0064-konomitv-recording-delete-reconciliation.md`
- Fixed KonomiTV source: tag `v0.14.1` / commit `0a32188274b81c1e7bed642474b208bd2a543a6b`

## 目的

KonomiTVの録画削除を明示したCompose構成だけで、固定KonomiTVの管理者が完成録画を削除できるようにする。
Sazanamiは録画履歴を削除せず、完了録画のfile availabilityを周期的に照合して欠落、不整合、復元を示す。

## Compose契約

### `KDR-001` Base Compose

Base Composeだけを使った場合、KonomiTVの録画root mountはread-onlyでなければならない。Sazanamiの録画mountは
read-writeのままとする。既存のhost root、Docker socket、privileged、不要deviceを禁止する境界を変えない。

### `KDR-002` 削除override

削除を有効にする構成は、Base Composeと別の明示overrideを同時に指定しなければ起動できない。Overrideは
KonomiTVの録画rootを同じhost pathからread-writeでmountし直す。Base Composeを編集または自動置換しない。

### `KDR-003` 共同所有者

SazanamiとKonomiTVは同じ`HOST_UID`／`HOST_GID`で動かす。録画rootと子directoryは0700、録画fileは0600を
維持する。削除可能構成ではKonomiTVを録画rootの信頼済み共同所有者とし、lock以外の録画root内fileを
削除、置換、同一sizeで変更できることを公開手順へ記載する。

### `KDR-004` 所有lock

Host側`${RECORDING_DIR}/.sazanami-dvr.lock`を、KonomiTV containerの
`/host-rootfs/recordings/.sazanami-dvr.lock`へ個別にread-only bindする。録画rootをread-writeにした場合も、
KonomiTVからlockの削除、置換、書込みを許可しない。

### `KDR-005` Lock準備

準備scriptはlockが存在しない場合だけ、umask 077のもとで排他的に0600の通常fileとして作る。既存lockを
上書き、chmod、unlinkしない。既存pathがsymlink、directory、特殊file、別所有者、0600以外、link数1以外なら
固定理由で停止する。Sazanamiの`OpenRoot`による通常file、所有者、link数、flock検査も維持する。

## 周期整合契約

### `KDR-006` 対象状態

周期処理がavailabilityを変更できるattemptは、次のどちらかだけとする。

- `SUCCEEDED`
- `PARTIAL`かつterminal reasonが`USER_REQUESTED_STOP`

`CLAIMED`、`STARTING`、`RECORDING`、`FINALIZING`、`FAILED`、`CANCELLED`、`MISSED`は変更しない。
Attempt state、terminal reason、byte count、file plan、publication evidenceも変更しない。

### `KDR-007` DB基準の走査

録画root全体を走査せず、DBの`RecoveryAttempts`からID昇順で読む。一pageは100件、一runは最大1,000件とする。
全件を一度にメモリへ載せない。Itemの状態にかかわらず、読んだ最後のIDをprocess内cursorとして次回へ渡す。

末尾の100件未満または空pageへ達したrunはcursorを0へ戻して終了する。次回runは先頭から始める。
一回の上限へ達した場合はcursorを維持し、次回runで続きから読む。Service再起動時はcursorを0へ戻す。
Random IDが前回cursorより小さくても、末尾wrap後のrunで必ず再確認する。

### `KDR-008` 実行間隔と重複防止

録画serviceの起動後、起動時Recoveryを一度完了してから周期runnerを開始する。一回の処理が完了してから一分後に
次を始め、同時に二回実行しない。個別runの失敗ではservice、scheduler、録画処理を停止せず、固定reasonを
観測して次回を残す。親contextのcancelでは新しいrunを開始せず、実行中runを終了する。

### `KDR-009` File検査

各対象itemは、DBに保存したメイン録画の`FilePlan`だけを`recordingfs.Inspect`で調べる。ワンセグsegmentがあれば、
ordinal 1の`FilePlan`を別に調べる。Path外へのescape、symlink、不正directory、所有者違い、mode違い、
特殊file、link数違いを安全な完成fileとして扱わない。

### `KDR-010` Availability分類

メイン録画と、完成公開済みのワンセグへ、それぞれ次の分類を適用する。

| 観測 | Availability | Integrity reason |
|---|---|---|
| 完成fileなし、部分fileなし | `MISSING` | `FILE_MISSING` |
| Unsafe、事実不正、部分file再出現、完成fileの期待byte数不一致 | `MISMATCHED` | `FILE_INTEGRITY_MISMATCH` |
| 安全な完成fileだけが存在し、期待byte数と一致 | `FINAL` | 空 |

既存値と同じ場合はDBを更新しない。異なる場合だけ`SetRecordingAvailability`または
`SetOneSegAvailability`を使う。メイン録画の結果でワンセグを上書きせず、ワンセグの結果でメイン録画を
後退させない。

完成公開されなかったワンセグのsettled failureは、既存の`settledOneSegAvailability`に従う。期待byte数の
部分fileだけがあれば`PARTIAL`と既存reasonを維持し、fileがなければ既存のmissing reasonまたは
`FILE_MISSING`、unsafe、完成fileの出現、期待byte数不一致は`MISMATCHED / FILE_INTEGRITY_MISMATCH`とする。
この状態から`FINAL`へ昇格させない。

### `KDR-011` 復元の意味

削除後に同じ相対pathへ、安全な所有者、0700のdirectory、0600の通常file、link数1、期待byte数を満たす
完成fileを置けば、完成公開済みsegmentは次回runで`FINAL`へ戻せる。Sazanamiはchecksumを保存しないため、元内容と同じか、
同じsizeの別内容かを判定しない。同一sizeの内容変更も`FINAL`として扱う。この制限を公開文書へ記載する。

File観測とDB更新は一つのfilesystem transactionではない。観測後に外部変更が競合した場合、そのrunの表示は
一時的に古くなり得る。次のrunで同じDB事実とfile metadataを再照合して収束させる。

### `KDR-012` 非変更範囲

周期処理はfileを削除、作成、rename、link、chmod、chownしない。未知fileとKonomiTVのDB、thumbnail、補助fileを
走査または変更しない。Sazanamiの予約、録画履歴、attempt、segmentを削除しない。DB schemaと公開APIを
変更しない。

### `KDR-013` 観測と秘匿

一runの完了、失敗、確認件数、変更件数、missing件数、mismatched件数、durationだけをboundedに観測する。
失敗reasonは64byte以下の固定lower-kebab-caseとする。番組名、局名、録画番号、reservation／attempt ID、
相対・絶対path、接続先、raw error本文を通常logやmetric labelへ出さない。

## 必須テスト

- Base Compose単独の録画mountがread-onlyである。
- Base＋削除overrideで録画rootがread-write、lockだけがread-onlyである。
- Lock不存在では0600通常fileを一度だけ作り、二回目は変更しない。
- 既存lockの正常、symlink、directory、特殊file、所有者、mode、link数を検査し、不正時に変更せず停止する。
- KonomiTVと同じUID/GIDで完成録画を削除でき、lockの削除・置換・書込みは失敗する。
- `SUCCEEDED`と`PARTIAL / USER_REQUESTED_STOP`だけを更新し、active／finalizingを変更しない。
- メイン／ワンセグそれぞれのmissing、unsafe、owner、mode、type、link、size、partial再出現、正しい復元を確認する。
- 同一sizeで内容だけを変更したfileが`FINAL`になる制限を直接確認する。
- 99、100、101、999、1,000、1,001件、cursor継続、末尾wrap、service再起動時0、cancelを確認する。
- 長いrun中に次のrunを開始しない。失敗後も一分待って次回を実行する。
- DB更新件数0／1だけを受け入れ、reader、writer、RowsAffected errorを成功扱いにしない。
- 未知file、KonomiTV DB、thumbnail、補助fileを変更しない。
- 通常、shuffle、race、vet、module verify、既知脆弱性検査、CGO無効の主要四環境build、Hosted CIを実行する。

## 実環境確認

許可済みLinux amd64実験環境で、公開候補と固定KonomiTVを同じhost UID/GIDのComposeから起動する。
新しく90秒以上録画した使い捨て一件だけを対象に、KonomiTV一覧、通常再生、Range再生、管理者削除、
KonomiTV DBとfileの削除、Sazanamiの`MISSING / FILE_MISSING`を順に確認する。既存録画を削除しない。

必要なら同じ期待byte数の合成fileを隔離rootへ復元し、`FINAL`へ戻ることを確認する。実放送内容をGitや報告へ
残さない。同一内容の復元とは表明しない。実験環境を使えない場合は`NOT RUN: hardware required`とする。

## 公開上の制限

- 削除可能構成はopt-inであり、Base Composeはread-onlyである。
- KonomiTVは録画rootの信頼済み共同所有者になり、lock以外のfileを変更できる。
- SazanamiはKonomiTV DBや補助fileの削除をtransactionで保証しない。
- Sazanami履歴は削除せず、file availabilityだけを更新する。
- 同じsizeの内容変更は検出しない。
- 外部変更と周期処理が競合した場合、表示は次回runまで一時的に古いことがある。
- KonomiTV v0.14.1以外、別UID、network filesystemは未検証である。
