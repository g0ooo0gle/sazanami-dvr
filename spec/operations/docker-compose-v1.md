# Docker Compose導入仕様 v1

- Status: Accepted
- Date: 2026-08-14
- Applies to: Sazanami DVR v0.3.0以降
- Decision: `docs/adr/0062-docker-compose-deployment.md`
- Fixed KonomiTV source: tag `v0.14.1` / commit `0a32188274b81c1e7bed642474b208bd2a543a6b`

## 目的

Linux単一host上で、Sazanami DVRとKonomiTVをDocker Composeから再現できるようにする。SazanamiのDB、
チャンネル設定、録画と、KonomiTVの設定、DB、log、captureはcontainerの外へ残す。Mirakurun／mirakc、
チューナー、driver、カードは既存hostのものを使い、Composeへ含めない。

本仕様はDockerを追加の導入方式として定める。既存の配布アーカイブとsystemd導入を置き換えない。

## 対象環境

- Docker EngineとDocker Compose pluginを使えるLinux amd64を、Compose全体の初回検証対象とする。
- Sazanami imageはLinux amd64／arm64を提供する。
- KonomiTV imageは固定sourceの公式Dockerfileからlocal buildし、Sazanami projectのregistryへ再配布しない。
- KonomiTVを含むCompose全体のarm64、GPU encoder、Docker Desktop、rootless Dockerは`NOT RUN`とする。

## Service構成

### Sazanami DVR

- GHCRの完全な版tag、または同じsourceからbuildしたlocal imageを使う。
- ImageはCGO無効のsingle binary、CA証明書、timezone data、license、third-party noticeだけを含む。
- Imageの既定利用者は専用non-root UID/GIDとする。Composeではhost側directoryの所有者に合わせて
  `HOST_UID`／`HOST_GID`を指定する。
- `network_mode: host`を使い、CtrlCmdは`127.0.0.1:4520`、録画HTTPは`127.0.0.1:4521`を既定にする。
- `cap_drop: [ALL]`、`no-new-privileges:true`、`init:true`、停止猶予2分を設定する。
- DB migration、catalog sync、チャンネル設定検証を通常起動前後に自動実行しない。

### KonomiTV

- 固定tagとcommitの公式Dockerfileから、利用者がlocal imageをbuildする。
- BackendはEDCB、ライブ受信はSazanami経由、encoderはFFmpeg、Web portは7200を既定にする。
- `network_mode: host`を使い、CtrlCmd接続先は`tcp://127.0.0.1:4520/`とする。
- Sazanamiと同じ`HOST_UID`／`HOST_GID`で起動し、container rootへowner-only録画の読み取りを許可しない。
- KonomiTVのdata、log、captureはhost bindへ保存する。
- 録画directoryは`/host-rootfs/recordings`へread-onlyでbindする。Host root全体は渡さない。
- `cap_drop: [ALL]`、`no-new-privileges:true`、`init:true`を設定する。
- Sazanamiのhealthcheck成功後に起動する。

### Mirakurun／mirakc

- Composeへservice、device、driver、card設定を追加しない。
- SazanamiとKonomiTVはhost networkから、利用者が明示したMirakurun URLへ接続する。
- 設定例に実環境のIP、hostname、token、card情報を含めない。

## Host側の永続path

設定例はCompose directory直下に、次の相対pathを使う。

| Path | 用途 | Container access | 初期mode |
|---|---|---|---|
| `data/sazanami/` | DB、backup、チャンネル設定 | Sazanami read-write | 0700 |
| `recordings/` | 録画partial／完成file | Sazanami read-write、KonomiTV read-only | 0700 |
| `config/konomitv.yaml` | KonomiTV設定 | KonomiTV read-only | 0600 |
| `data/konomitv/` | KonomiTV DBとdata | KonomiTV read-write | 0700 |
| `logs/konomitv/` | KonomiTV log | KonomiTV read-write | 0700 |
| `captures/` | KonomiTV capture | KonomiTV read-write | 0700 |
| `.env` | image版、UID/GID、接続設定 | Composeだけが読む | 0600 |

Repositoryはdirectoryだけを保持するための`.gitkeep`を必須にしない。実data、DB、TS、log、秘密を
`.gitignore`と`.dockerignore`で除外し、設定例だけを追跡する。

## 明示準備

初回起動では、次の順を変えない。

1. 配布されたCompose例と設定例を専用directoryへコピーする。
2. `.env.example`を`.env`へ、KonomiTV設定例を`config/konomitv.yaml`へコピーする。
3. `HOST_UID`／`HOST_GID`、Mirakurun URL、KonomiTV Web portを設定する。
4. 永続directoryを作り、同じUID/GID、directory mode 0700へそろえる。
5. チャンネル設定を`data/sazanami/channels.json`へ置き、通常file、0640以下にする。
6. `docker compose build konomitv`で固定sourceのKonomiTV imageをbuildする。
7. Sazanamiの`db status`、`db migrate`、`db status`を明示実行する。
8. `catalog sync`と`ctrlcmd validate`を明示実行する。
9. `docker compose up -d`で起動し、Sazanami healthcheckとKonomiTV Web画面を確認する。

`db migrate`、`catalog sync`、`ctrlcmd validate`のいずれかが失敗した場合は、通常serviceを起動しない。

## 更新と切り戻し

更新前に録画中でないことを確認し、Sazanamiの`db backup`を明示実行する。完全な新version tagへ`.env`を
変更し、imageを取得して版を確認する。新しいbinaryで`db status`を確認し、`BEHIND`の場合だけmigrationを
実行する。`CURRENT`を確認してからcontainerを再作成する。

切り戻しでは、containerを停止し、以前の完全なversion tagへ戻す。DB schemaが変わった場合は、新版の
`db restore`で更新前backupを復元し、旧版の`db status`が`CURRENT`になってから起動する。録画fileと
KonomiTV dataを削除、移動、上書きしない。

## 停止と削除

- `docker compose stop`と`docker compose down`はcontainerとnetwork状態だけを停止・削除する。
- 通常手順に`down -v`、host directory削除、glob削除を含めない。
- Imageを削除してもhost側のdata、録画、設定、log、captureは残す。
- PurgeはCompose停止後、利用者が各絶対pathを一件ずつ確認した別手順とする。

## Image buildと公開

### `COMPOSE-001` Sazanami image

Go 1.26.5 builderとAlpine runtimeをmulti-platform digestで固定し、BuildKitの`TARGETOS`／`TARGETARCH`で
Linux amd64／arm64をbuildする。`CGO_ENABLED=0`、`-trimpath`を使い、製品版とVCS revisionをbinaryへ入れる。

### `COMPOSE-002` OCI metadata

少なくとも`org.opencontainers.image.source`、`version`、`revision`、`licenses`を設定する。Labelとimage layerへ
実環境の値を入れない。

### `COMPOSE-003` Registry tag

Release workflowはannotated tagと製品版が一致した同じcommitから、
`ghcr.io/g0ooo0gle/sazanami-dvr:v<version>`だけを公開する。移動する`latest`は必須にしない。同じ版tagを
上書きしない。

### `COMPOSE-004` KonomiTV image

Composeは固定sourceからlocal buildする。Sazanami workflowはKonomiTV imageをpushせず、上流source、binary、
packageをSazanami imageへコピーしない。固定commitは上流APT packageの再現性まで保証しない。

## 機能契約

### `COMPOSE-005` 永続化

Container再作成の前後で、Sazanami DB、backup、チャンネル設定、録画file、KonomiTV DBが同じhost bindに残る。

### `COMPOSE-006` 途中録画

KonomiTVから放送中番組へ録画を追加した場合、途中録画開始仕様v1の条件を満たせば現在時刻から録画を始める。
Container化によって作成時刻、予定時刻、実開始時刻の意味を変えない。

### `COMPOSE-007` 録画中停止

KonomiTVから一件の録画中停止を要求できる。安全に確定できる188 byte以上の録画は
`PARTIAL / USER_REQUESTED_STOP`として残す。兄弟録画を停止しない。

### `COMPOSE-008` 録画閲覧

KonomiTVはread-only mountから完成録画を走査できる。File削除とrenameはv1で提供しない。Read-write権限は
KonomiTV全操作を扱う後続判断で、Sazanami履歴との整合を含めて決める。

## 必須検証

- `docker buildx build`でLinux amd64／arm64のSazanami imageを作る。
- Image内の利用者がrootでないこと、版、VCS revision、CGO無効、OS、architecture、OCI labelを確認する。
- `docker compose config`を成功させ、host root、Docker socket、`privileged`、不要なdeviceがないことを静的に確認する。
- `.dockerignore`から`.git`、DB、backup、録画、log、capture、秘密設定がbuild contextへ入らないことを確認する。
- Temporary directoryで明示migration、catalog sync、チャンネル検証、通常停止、container再作成後の永続化を確認する。
- 許可済みLinux amd64実験環境で、KonomiTV番組表、放送中予約開始、byte増加、録画中停止、部分録画を確認する。
- 固定KonomiTV sourceのcommitとruntime版を別に記録する。Source buildだけで画面操作を成功扱いにしない。
- Sazanamiの通常、shuffle、race、vet、module検証、既知脆弱性検査、主要四環境buildを回帰する。
- Release後にGHCR manifest digest、platform digest、version／revision labelをregistryから読み戻す。

## 失敗時の扱い

- Image build失敗では、稼働中runtime、DB、録画を変更しない。
- Migration失敗では通常serviceを起動せず、backup IDを使う既存復旧手順へ戻る。
- SazanamiがunhealthyならKonomiTVを自動起動せず、固定された短い理由とcontainer logを確認する。
- KonomiTV build／起動失敗を理由にSazanamiのDBや録画を削除しない。
- 実験環境の切替に失敗した場合は両containerを停止し、退避した同版binaryから手動runtimeへ戻す。
