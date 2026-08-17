# ADR-0064: KonomiTVの録画削除をopt-inにし、完了録画だけを周期照合する

- Status: Accepted
- Date: 2026-08-18
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related: ADR-0033、ADR-0062、ADR-0063、Plan 0079、Plan 0080、Handoff 0055、Handoff 0056
- Supersedes: None
- Superseded by: None

## 背景

Accepted ADR-0062は、固定KonomiTVが完成録画を閲覧できるように、Sazanamiと同じhost UID/GIDで起動し、
録画rootをread-onlyでmountする。この既定は、KonomiTVの障害や管理者操作がSazanamiの録画を変更する範囲を
抑えている。

固定KonomiTV v0.14.1の管理者向け録画削除は、KonomiTVのDBと補助fileに加え、録画file本体を共有folderから
直接削除する。Accepted ADR-0063は、この操作とSazanami履歴の整合をv0.5.0の対象にした。Read-onlyの既定を
全利用者へ一律に広げず、削除を選んだ構成だけに必要権限を与える判断が必要である。

Sazanamiは起動時にDBと録画fileを照合するが、起動し続けている間の外部削除は直ちに履歴へ反映しない。
起動時Recovery全体を周期実行すると、録画中や完成処理中のattemptを中断として確定し得る。完了録画の
availabilityだけを再利用可能な既存境界で更新する必要がある。

## 判断

Composeの既定read-only録画mountは維持する。KonomiTVからの録画削除を有効にする利用者だけが、別の明示的な
Compose overrideをBase Composeと組み合わせる。OverrideはKonomiTVの録画rootをread-writeへ置き換える。

SazanamiとKonomiTVは同じhost UID/GIDで動かす。削除可能構成では、KonomiTVを録画rootの信頼済み共同所有者と
扱う。録画rootと子directoryは0700、録画fileは0600を維持し、groupやACLへ権限を広げない。

録画rootの`.sazanami-dvr.lock`は、host側の同じfileをcontainer内の同じpathへ個別にread-only bindする。
これにより、録画rootの他のfileをwrite可能にしても、KonomiTVからlockを削除または置換できないようにする。
準備scriptはlockが存在しない場合だけ0600で作る。既存lockを上書き、chmod、unlinkしない。不正な既存lockは
修正せずに起動前エラーとする。

録画サービスは、完了済み録画のavailabilityを一分ごとに再照合する専用runnerを持つ。起動時Recovery全体は
再利用せず、次の既存境界だけを共有する。

- `RecoveryAttempts`によるDB基準の上限付き読出し
- `recordingfs.Inspect`によるDB保存pathだけの安全検査
- `completedAvailability`とワンセグの同等分類
- `SetRecordingAvailability`と`SetOneSegAvailability`によるavailabilityだけの更新

対象attemptは`SUCCEEDED`と、`PARTIAL / USER_REQUESTED_STOP`に限定する。録画中、開始中、claim済み、
finalizing、failed、cancelled、missedは変更しない。メイン録画とワンセグは別々に照合する。

Runnerは同時に一件だけ動き、各runの完了から一分待って次を始める。一回の上限は1,000件、DB pageは既存の
100件とする。Process内cursorを次回へ引き継ぎ、ID空間の末尾へ達したら0へ戻す。DB schemaと公開APIは
変更しない。

完成公開済みsegmentで完成fileがない場合は`MISSING / FILE_MISSING`、所有者、mode、file種別、link数、
期待byte数が不正、または部分fileが再出現した場合は`MISMATCHED / FILE_INTEGRITY_MISMATCH`にする。
正しい所有者、mode、通常file、link数、期待byte数の完成fileが戻れば`FINAL`へ戻す。完成公開されなかった
ワンセグのsettled failureは、既存の部分file／欠落／不整合分類を維持し、後から完成fileへ昇格させない。
内容checksumは持たないため、同じsizeの内容変更は検出しない。この制限を削除可能構成の公開手順と互換表へ
明記する。

周期処理はSazanamiのDBと、DBに記録したmain／one-segのfile metadataだけを扱う。KonomiTV DB、thumbnail、
補助file、未知fileは変更しない。Fileを自動削除、rename、復元しない。

## ADR-0062との関係

本ADRはADR-0062をSupersededにしない。ADR-0062のBase Compose、read-only既定、限定bind、同一UID/GID、
host rootとDocker socketを渡さない境界は維持する。本ADRは、録画削除を明示したopt-in構成と、その削除後に
完了録画のavailabilityを更新する処理だけを追加する。

## 影響

- 閲覧だけの利用者は、従来のread-only既定をそのまま使える。
- 削除を明示した利用者は、固定KonomiTVの管理者画面から録画を削除できる。
- 削除可能構成では、KonomiTVがlock以外の録画root内fileを削除、置換、同一sizeで変更できる。
- `.sazanami-dvr.lock`は個別mountで保護するが、録画root全体を信頼境界の外へ隔離するものではない。
- Sazanami履歴は外部削除後も残り、遅くとも後続の周期処理で欠落を示す。録画fileを自動削除しない。
- 正しいmetadataと期待sizeでfileを復元すれば再び利用可能になるが、元内容との同一性は証明しない。
- File観測とDB更新の間に外部変更が起きた場合、表示は一時的に古くなり、次の周期で収束する。
- DB schema、公開API、CtrlCmd、recording planは変わらない。

## 採用しなかった案

### Base Composeをread-writeへ変更する

採用しない。削除を使わない利用者にも書込み権限を与え、ADR-0062の安全な既定を失う。

### Group共有と0640／02750へ変更する

採用しない。固定Composeは同じUID/GIDを使っており、削除にはdirectory書込みも必要である。既存のowner-only
file検証を広げる変更量に対して利点が小さい。

### Sazanamiの削除APIを追加する

採用しない。固定KonomiTVの削除経路はそのAPIを呼ばない。公開API、認証、client改変を追加せず、固定sourceの
到達操作へ合わせる。

### 起動時Recoveryを周期実行する

採用しない。未完了attemptを復旧・確定する責務があり、稼働中の周期処理として安全ではない。

### File eventだけを監視する

採用しない。Event欠落と再起動時の再構築が別途必要になる。DBから上限付きで再照合する方が小さく、冪等である。

### Checksumで内容同一性を検証する

今回は採用しない。Schema、既存録画の移行、録画終了時I/Oが増える。v0.5.0では欠落、metadata不一致、復元を
扱い、同一size改変を検出しない制限を公開する。

## 検証

- Base Composeと削除overrideを個別・組合せで展開し、録画rootとlockのread-only状態を確認する。
- Lock準備の不存在、正常、不正mode、不正所有者、symlink、directory、link数違いを確認する。
- Terminal二状態だけを周期更新し、active／finalizingを変更しない。
- メイン／ワンセグの欠落、不正metadata、partial再出現、正しい復元、同一size改変を確認する。
- 100件page、1,000件上限、cursor継続、末尾wrap、cancel、重複実行なしを確認する。
- 周期処理の失敗がscheduler、録画、次回runを止めないことを確認する。
- 許可済み実験環境では、新しい90秒以上の使い捨て録画一件だけをKonomiTVから削除する。既存録画を使わない。
- KonomiTV DBと補助fileの削除はKonomiTV自身の結果として読み戻し、Sazanamiが変更したとは扱わない。

## 見直す条件

- KonomiTVが録画fileを直接削除せず、backendの認証済み削除APIを使うようになった。
- 別UID、group、ACL、network filesystemを正式対応する。
- 同一sizeの内容変更を検出する必要が生じ、checksumまたはcontent identityを採用する。
- File件数が一分・1,000件の上限で実用的に収束しない。
- 個別read-only bindでlockを保護できない対象runtimeを正式対応する。
