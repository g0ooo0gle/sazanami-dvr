# ADR-0073: Mirakurun URLから静的チャンネル設定を初期生成する

- Status: Accepted
- Decision date: 2026-09-15
- Deciders: Project owner
- Reviewer: Codex
- Authorization: Project ownerは、番組表GCの完了後にMirakurun URLだけでチャンネルを自動生成し、
  KonomiTVの公開設定例を`always_receive_tv_from_mirakurun: true`にする方針を承認した
- Related plan: [Plan 0092](../plans/0092-mirakurun-channel-bootstrap.md)
- Related specification: [Mirakurunチャンネル初期化仕様 v1](../../spec/ctrlcmd/channel-bootstrap-v1.md)
- Partially supersedes: ADR-0025の手動provisioning限定と自動探索を全面禁止する部分、ADR-0070の
  初回手順でchannel mapを手動配置する部分、現在の公開KonomiTV接続手順でSazanami中継を標準例にする部分
- Preserves: ADR-0025の静的snapshot、起動時一回読込、全件照合、fail-closed、明示再起動、
  ADR-0058のcanonical path、ADR-0034の上限付きライブ中継
- Superseded by: None

## 背景

現行製品はMirakurunまたはmirakcのURLからサービスと番組を同期できるが、CtrlCmd用の
`channels.json`にはTSIDと補助flagを手入力する必要がある。TSIDはMirakurunのservice APIに含まれず、
他のIDから推測できない。この一項目のために初回導入が難しくなっている。

一方、Mirakurunのservice streamにはPATがあり、製品にはCRC検証付きのPAT parserがある。通常運転へ
探索処理を加えず、初回の明示操作だけで短時間streamを開けば、静的runtimeの安全性を維持したまま
入力をURL一つへ減らせる。

KonomiTVのライブ視聴は、Sazanamiの上限付き中継とMirakurun直結の両方を選べる。軽い標準構成には
直結が合うため、公開例の選択を変更する。中継機能と検証結果は削除しない。

## 判断

### 初回専用の`setup`コマンドを追加する

`sazanami-dvr setup --mirakurun-url <url>`を公開入口とする。Data rootは既定で
`/var/lib/sazanami-dvr`、出力はその直下の`channels.json`とする。Setup専用のSQLite入口がowner lockを一度だけ
取得し、空DBなら同じlock内で初期化してStoreを開く。CURRENT DBはそのまま利用し、既存DBのmigrationが
必要な場合は停止して従来の明示操作へ戻す。Lockはsetup終了まで保持する。

Installer自体はnetworkへ接続せず、DB、map、service状態を変更しない。LinuxとComposeの導入手順が、
installerまたは`prepare.sh`の後に利用者権限で`setup`を一回実行する。

### TSIDは一件ずつPATから確認する

Catalog同期後、KonomiTV `v0.14.1`が受理するservice typeから、製品の2K範囲にある
`0x01`、`0x02`、`0xA1`、`0xA2`を選ぶ。各service streamをpriority 0、`decode=0`、同時1接続で開き、
15秒または1 MiBへ達するまでPID 0のPATを探す。

PATのCRC、section、program数を既存parserで検証し、program numberが対象SIDと一致した場合だけTSIDを採用する。
一件でも確認できない場合は全体を失敗させる。別field、同じnetwork、過去mapからTSIDを推測しない。

### APIにない補助値は空または無効値へ固定する

MirakurunのID、ONID、SID、任意のremoconを使う。Remoconが欠落またはnullなら0とする。
事業者名、network名、TS名は空文字列、部分受信はfalse、EPG取得と検索はtrueとする。これらを
service名やIDから推測しない。Service名とservice typeは完成済みcatalogを正本とする。

### 検証後にだけ新しいmapを公開する

候補はdata root内の0600の一時fileへ決定的なJSONとして書き、同期する。既存runtime validatorで
最新の完成済みcatalogと照合し、全serviceが有効な場合だけ公開する。公開先がなければ同一filesystem内で
原子的かつ上書きなしに作る。候補と同一byteの既存fileは再実行成功とする。内容が異なるfileや
通常file以外があれば変更しない。

`setup`、stream、JSONの全処理は有限とし、通常logには件数、結果、固定理由だけを出す。URL、path、service名、
番組、生のHTTP／OS errorは出さない。

### 通常運転とライブ中継は維持する

生成物は既存のchannel-map-v1であり、`recording serve`は従来どおり起動時に一度だけ読み込む。
通常起動、定期catalog更新、CtrlCmd要求からprobe、書換え、reloadを行わない。

KonomiTVの同梱例と公開手順は`always_receive_tv_from_mirakurun: true`を標準とする。これによりライブ視聴だけを
Mirakurun / mirakcへ直接渡し、番組表と予約はSazanamiへ接続する。`false`を明示した場合は、ADR-0034の
上限付きCtrlCmdライブ中継を引き続き利用できる。

## 影響

新規利用者はTSIDやchannel map形式を調べずに初回準備を終えられる。実際の放送streamをserviceごとに短時間開くため、
空きチューナーが必要になる場合がある。録画サービスを起動する前に逐次実行し、失敗時は時間を置いて再実行する。

既存の手書きmapは自動で置き換えない。既存fileと自動生成結果が異なる場合は、利用者が内容と退避先を確認してから
明示的に整理する。通常運転中のチャンネル変更を自動追従しないため、静的snapshotの再現性は変わらない。

ライブ視聴の標準例はMirakurun直結になるため、SazanamiがライブTSを中継しない構成が一般的になる。録画streamは
引き続きSazanamiがMirakurunから読み、録画先へ直接保存する。

## 採用しなかった案

- **TSIDを0やONIDで埋める:** KonomiTVのchannel identityを偽るため採用しない。
- **全serviceを一つのmultiplex情報でまとめる:** TSMFなどで誤ったTSIDを共有する可能性があるため、初版は個別probeにする。
- **複数serviceを並列probeする:** 初回時間は短くなるが、チューナー競合と資源上限を増やすため採用しない。
- **通常のcatalog更新でmapも書き換える:** 起動中snapshotと設定世代が分かれ、利用者の選択も上書きするため採用しない。
- **既存mapを自動置換する:** 手動調整と復旧材料を失うため採用しない。
- **公開例からSazanamiライブ中継を削除する:** 既に実装・検証済みの選択肢なので、明示設定として残す。

## 検証

- 合成Mirakurun APIとMPEG-TSで、空DBからURLだけのsetupを完了する。
- 任意chunk、先頭garbage、複数packet PAT、CRC、SID、program数、timeout、EOF、1 MiBを確認する。
- 対象service type、remocon有無、重複、0件、4,096件、4,097件を確認する。
- 候補の検証失敗、既存file、同一byte再実行、symlink、公開raceで既存mapが変わらないことを確認する。
- 生成mapを1060／1021と録画起動の既存snapshotで使えることを確認する。
- Linux／Composeのportable lifecycleで手書きmapなしの初回導入を確認する。
- 実Mirakurun、実放送、KonomiTV画面は短い実機確認とし、未実施なら`NOT RUN`と記録する。

## 製品への複製

本ADRとMirakurunチャンネル初期化仕様v1を、製品commit
`2b55d9703f7a2644cde731fa3eddfb230d06632c`をbaseにしたHandoff 0069へ列挙する。製品側で
byte-identicalなcopy commitを読み戻した後にだけ実装を始める。
