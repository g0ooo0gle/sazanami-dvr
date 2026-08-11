# ADR-0058: 標準チャンネル設定をdata root直下へ置く

- Status: Accepted
- Date: 2026-08-11
- Deciders: Project owner（v1.0までの技術判断をCodexへ明示委任）
- Delegated reviewer: Codex
- Related: ADR-0025、ADR-0051、Plan 0073、Handoff 0049
- Product copy path: `docs/adr/0058-channel-map-standard-path.md`
- Product sync state: NOT COPIED
- Supersedes: ADR-0051のチャンネル設定pathだけ
- Superseded by: None

## 背景

ADR-0025とチャンネル設定仕様v1は、TSIDを含むJSON設定をcanonical data rootの直下からだけ読むよう
定めている。製品v0.1.1の`loadChannelMap`も、設定fileの親directoryがdata rootと異なる場合は
`channel-map-path-invalid`で停止する。この境界により、設定、完成済みカタログ、data rootの所有権を
一つの単位として検証できる。

後から採用したADR-0051とLinux導入仕様v1では、チャンネル設定の標準pathを
`/etc/sazanami-dvr/channels.json`としていた。標準data rootは`/var/lib/sazanami-dvr`なので、公開手順の
ままではpath検査を通らない。

公開v0.1.1を使った隔離Ubuntu確認では、番組表取得がサービス139件、番組24,132件、約13秒で完了した。
続く`ctrlcmd validate`は`channel-map-path-invalid`で待受前に停止した。unitは配置せず、サービスも起動せず、
既存の別構成には触れていない。この結果はprovider障害ではなく、標準配置の不整合を示している。

## 判断

標準チャンネル設定は`/var/lib/sazanami-dvr/channels.json`へ置く。これは標準data rootの直下であり、
ADR-0025、チャンネル設定仕様v1、既存実装のpath検査を同時に満たす。

環境設定例の`SAZANAMI_CHANNEL_MAP`、初回配置例、`ctrlcmd validate`の引数、公開Linux手順は同じpathを使う。
Data rootは`sazanami-dvr:sazanami-dvr`、0700、チャンネル設定は`root:sazanami-dvr`、0640という既存の
標準権限を維持する。実行ファイルのpath検査は変更しない。

ADR-0051のうち、次は変更しない。

- 版別配布物、実行リンク、環境設定directory、data root、既定録画先の配置。
- systemd unit、既定port、チューナー数自動取得。
- 明示DB更新、backup、切り戻しの順序。
- 更新時に設定を自動上書きしない方針。
- 通常削除で設定、DB、録画、backup、専用利用者を残す方針。
- 利用者が絶対pathを確認した明示purgeだけがデータを削除する方針。

公開済みv0.1.1は変更しない。環境設定例と公開手順の修正はpatch版v0.1.2として公開する。

## 結果

- 標準手順と実行時検査が同じpathを使う。
- 製品code、API、DB形式、録画処理を変更せずに初回導入を再開できる。
- 通常削除ではdata rootと一緒にチャンネル設定が残る。
- Purgeでは確認済みdata rootと一緒にチャンネル設定を削除できる。
- v0.1.1から更新しても、環境設定は自動で上書きされない。旧pathが残る場合だけ、起動前にfileと
  設定値を直す必要がある。

## 採用しなかった案

### 実行ファイルに`/etc`を許可する

標準文書の誤りを直すために、採用済みのpath境界と製品codeを広げることになる。設定とカタログの所有単位も
分かれるため採用しない。

### Data rootから`/etc`へsymlinkを置く

チャンネル設定仕様と実装はsymlinkを拒否する。検査を回避する運用になるため採用しない。

### 二つのpathを標準として併記する

同じ設定の正本が二つになり、更新時の不一致を増やす。標準はdata root直下の一つに固定する。

## 検証

- 環境設定例、Linux導入手順、仕様v2の現在値が`/var/lib/sazanami-dvr/channels.json`で一致する。
- 設定fileの親directoryがdata rootと一致することを静的testで確認する。
- 旧`/etc` pathは、歴史的仕様とv0.1.1からの移行説明だけに残す。
- 製品の`loadChannelMap`、CtrlCmd、API、DB、録画codeに差分がないことを確認する。
- 公開v0.1.2のamd64配布物を使い、導入、更新、切り戻し、通常削除、隔離purgeを確認する。
- 実験結果は件数、所要時間、固定理由だけを記録し、接続先、番組、利用者、実path、生の応答を残さない。

## 製品同期

- Handoff: Handoff 0049
- Planning source commit: Handoff 0049で固定する
- Target product base commit: `6443e5e6b9dba5f207a5f913ecc307f5819670c8`
- Product destination: `docs/adr/0058-channel-map-standard-path.md`
- Last synchronized product commit: NOT COPIED
- Known divergence: 製品v0.1.1の環境設定例とLinux手順は`/etc`を示すが、実行時はdata root直下だけを許可する
- 製品側正本の復元: 製品baseに欠けているADR-0025とチャンネル設定仕様v1も、Handoff 0049の同じ
  文書同期commitで製品へ復元する

## 見直す条件

- チャンネル設定をdata rootから分離するAccepted設計が必要になる。
- 複数data rootまたは複数チャンネル設定を同時に扱う要件が採用される。
- systemd以外の標準配置を第一級として提供する。
