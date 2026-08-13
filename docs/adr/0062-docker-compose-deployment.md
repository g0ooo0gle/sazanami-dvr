# ADR-0062: Sazanami DVRとKonomiTVを分離したDocker Composeを提供する

- Status: Accepted
- Date: 2026-08-14
- Deciders: Project owner
- Delegated reviewer: Codex
- Related: Plan 0078、ADR-0012、ADR-0053、ADR-0058、Handoff 0054
- Product copy path: `docs/adr/0062-docker-compose-deployment.md`
- Product sync state: NOT COPIED
- Product sync commit: `UNCREATED`

## 背景

実験環境では、Sazanami DVRとKonomiTVを同じLinux利用者が手動で起動している。動作確認には使えるが、
導入先を作り直すと、binary、Python環境、設定、data、録画、起動順を個別に再現する必要がある。

Project ownerは、放送中に追加した予約の録画開始修正と合わせて、この環境をDocker Composeで構築しやすくし、
次の版へ入れることを決めた。Docker化は新しい利用者向け導入方式なので、ADR-0053に従って次版をv0.3.0とする。

Sazanami DVRは小さいGo binaryである。一方、KonomiTV v0.14.1の公式DockerfileはPython、Web client、
FFmpegと複数GPU向け依存を含む。両者を一つのimageへ入れると、更新、license、障害、容量の境界が崩れる。
両者は分離する。

## 判断

Sazanami DVRとKonomiTVを別image、別serviceにし、Linux向けDocker Composeでまとめる。

Sazanami DVRは、Go 1.26.5でCGOを使わずにbuildしたLinux amd64／arm64 imageを提供する。builderとruntimeの
base imageはmulti-platform digestで固定する。最終imageはnon-rootで、Sazanami binary、CA証明書、timezone、
license、third-party noticeだけを含む。通常起動、systemd起動、container起動で製品機能を分岐しない。

ComposeはSazanamiとKonomiTVを、明示した同じhost UID/GIDで起動する。これにより、owner-onlyの録画を
KonomiTVへread-onlyで渡し、container rootへ読み取り権限を広げない。

KonomiTVはSazanami imageへ同梱しない。固定tag `v0.14.1`、commit
`0a32188274b81c1e7bed642474b208bd2a543a6b`の公式Dockerfileを、別のlocal imageとしてbuildする。
Sazanami projectはKonomiTV imageを再配布しない。上流sourceの固定は、上流package取得の完全な再現性を意味しない。

構築を簡単にすることを理由に、host root、Docker socket、privileged container、不要なdeviceを渡してはならない。
必要なdataだけを狭くmountする。

ComposeはLinuxのhost networkを使う。SazanamiとKonomiTVはloopbackで接続し、外部Mirakurun／mirakcもhostの
loopbackまたは明示URLから利用する。Mirakurun、チューナーdevice、driver、カードはComposeへ含めない。

Sazanamiのdata、録画、KonomiTVのdata、log、captureはhost bindに保存する。host root全体とDocker socketは
mountしない。固定KonomiTVがDocker内で付ける`/host-rootfs`には録画directoryだけをread-onlyで渡す。
KonomiTVからの録画file削除はv1の対象外とする。

DB migration、最初のcatalog sync、チャンネル設定検証は、利用者が明示コマンドで実行する。container起動時に
DBを自動更新しない。`docker compose down`、image更新、container再作成はdataと録画を削除しない。

Compose既定ではSazanamiのCtrlCmdと録画HTTPをloopbackへ限定する。KonomiTVのWeb portだけをLANから利用する。
CtrlCmdや録画HTTPをLANへ広げる場合は、既存の認証なし境界とfirewall注意を明示した設定変更を必要とする。

v0.3.0のrelease workflowは、Sazanami imageをGHCRの完全な版tagへ一度だけ公開する。移動する`latest` tagを
必須にせず、Git tag、製品版、OCI version／revision label、multi-platform manifest digestを読み戻す。

## 影響

- Sazanami DVRとKonomiTVの実験環境を、設定と永続directoryを残したまま再作成しやすくなる。
- 既存のsystemd導入方式は残り、Dockerを必須にしない。
- Sazanami imageは小さく保てるが、KonomiTV local imageのbuildは大きく、時間と容量を必要とする。
- host networkはLinux単一hostの構築を単純にする一方、bridge networkの隔離とport mappingを使わない。
- KonomiTVの録画mountをread-onlyにするため、画面からの録画file削除は使えない。
- Compose全体の初版は固定KonomiTV Dockerfileの制約によりLinux amd64を検証対象とする。Sazanami imageはarm64も提供する。
- 新しい導入方式を含むため、途中録画開始修正と合わせた次版はv0.3.0になる。

## 採用しなかった案

### 一つのimageへ両製品を入れる

KonomiTVの依存と更新がSazanamiへ流れ込み、分離した障害とlicense境界を失うため採用しない。

### Mirakurunとチューナーも自動構築する

device、driver、カード、既存録画基盤の移行を伴うため、今回の許可範囲を超える。

### 公式Compose例どおりhost root全体を渡す

録画閲覧に不要なhost fileまでKonomiTVから見えるため採用しない。

### 起動時に自動migrationする

backup、失敗、切り戻しを利用者から隠し、restartごとに再試行し得るため採用しない。

### bridge networkを既定にする

固定KonomiTVのライブ経路とhost Mirakurunへの接続設定が増える。Linux単一hostの初版ではhost networkを採用する。

## 見直す条件

- KonomiTVの公式imageが不変digestで配布され、local buildが不要になった。
- KonomiTVの対応版が変わり、networkまたは録画pathの扱いが変わった。
- Compose全体をarm64またはGPU encoderで正式検証する。
- KonomiTVから録画fileを削除するため、read-write mountとSazanami履歴の整合を採用する。
- 認証付きLAN APIまたはbridge networkを製品の既定へする。
