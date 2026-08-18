# Docker Composeで導入する

Sazanami DVR v0.3.0以降は、Linux上でSazanami DVRとKonomiTV v0.14.1をDocker Composeから起動できます。
既存の配布アーカイブとsystemd導入も引き続き利用できます。

この構成にMirakurun／mirakc、チューナー、driver、カードは含まれません。あらかじめhost上で利用できる
Mirakurun互換APIを用意してください。初版のCompose全体はLinux amd64、CPU版FFmpegが検証対象です。

## 構成

- SazanamiはGHCRの完全な版tagを使います。
- KonomiTVはtag `v0.14.1`、commit `0a32188274b81c1e7bed642474b208bd2a543a6b`の公式Dockerfileから、
  local imageをbuildします。Sazanami projectはKonomiTV imageを再配布しません。
- Sazanami imageのbuilderは[ADR-0065](adr/0065-go-1-26-6-security-patch.md)に従い、Go 1.26.6と
  multi-platform image digestを固定します。
- 両serviceは同じhost UID/GIDとhost networkを使います。
- SazanamiのCtrlCmdと録画HTTPは`127.0.0.1:4520`と`127.0.0.1:4521`へ限定します。
- KonomiTVのWeb画面は設定例のport 7200です。
- KonomiTVにはhost rootを渡さず、録画directoryだけをread-onlyで渡します。録画削除を明示した場合だけ、
  別のoverrideで録画directoryへの書込みを許可します。

## 初回準備

Release archiveの`packaging/compose`を、永続dataを置く専用directoryへコピーして移動します。

```sh
./prepare.sh
```

`prepare.sh`は、現在のUID/GIDを使う`.env`、KonomiTV設定、mode 0700の相対directoryを、存在しない場合だけ
作ります。録画directoryの所有lockも、存在しない場合だけ0600で作ります。既存lockがsymlink、directory、
特殊file、別所有者、0600以外、link数1以外の場合は、変更せずに停止します。既存fileは上書きしません。

`.env`の`MIRAKURUN_URL`と`config/konomitv.yaml`の`mirakurun_url`を同じ接続先へ設定します。実際の接続先、
token、番組情報をIssueやlogへ貼らないでください。

Catalog sync済みのbackend IDに合うチャンネル設定を、次へ置きます。

```text
data/sazanami/channels.json
```

KonomiTV imageは固定sourceから一度buildします。上流Dockerfileは大きく、APT packageや配布物の取得も行うため、
十分な空き容量と時間を確保してください。固定commitは上流packageの完全な再現性まで保証しません。

```sh
docker compose build konomitv
```

## DBと番組表を明示準備する

DB更新をcontainer起動へ隠しません。通常serviceを起動する前に、次を順に実行します。

```sh
docker compose run --rm sazanami db status \
  --data-root /var/lib/sazanami-dvr

docker compose run --rm sazanami db migrate \
  --data-root /var/lib/sazanami-dvr

docker compose run --rm sazanami db status \
  --data-root /var/lib/sazanami-dvr

docker compose run --rm sazanami catalog sync \
  --data-root /var/lib/sazanami-dvr \
  --provider mirakurun \
  --base-url "$(sed -n 's/^MIRAKURUN_URL=//p' .env)"

docker compose run --rm sazanami ctrlcmd validate \
  --data-root /var/lib/sazanami-dvr \
  --channel-map /var/lib/sazanami-dvr/channels.json
```

`db status`が`CURRENT`になり、catalog syncとチャンネル設定検証が成功した場合だけ起動します。

```sh
docker compose up -d
docker compose ps
```

KonomiTVはSazanamiのhealthcheck成功後に起動します。Web画面の接続先は、設定したhostのport 7200です。

## KonomiTVから録画を削除する

既定では、KonomiTVへ録画directoryをread-onlyで渡します。KonomiTVの管理者画面から録画を削除する場合だけ、
Base Composeと削除overrideを明示して起動します。

```sh
docker compose \
  -f compose.yaml \
  -f compose.konomitv-delete.yaml \
  up -d
```

削除overrideでは、SazanamiとKonomiTVを同じhost UID/GIDで動かし、KonomiTVを録画directoryの信頼済み共同所有者と
して扱います。KonomiTVは録画fileを削除、置換、同じsizeで変更できます。Sazanamiの所有lockだけは、同じpathへ
個別にread-onlyでmountします。Host root、Sazanamiのdata、SQLite、Docker socketは渡しません。

Sazanamiは完了録画を一分ごとに最大1,000件ずつ照合します。KonomiTVが完成fileを削除すると、録画履歴は残したまま
`MISSING / FILE_MISSING`へ変わります。所有者、mode、file種別、link数、期待sizeが正しい完成fileを同じpathへ戻すと、
次の照合で`FINAL`へ戻ります。内容のchecksumは保存しないため、同じsizeの別内容も`FINAL`になります。

KonomiTVのDB、thumbnail、補助file、録画directory内の未知fileはSazanamiから変更しません。File観測の直後に外部変更が
重なった場合は、一時的に古い状態を表示し、次の照合で収束します。

削除を無効へ戻す場合はcontainerを停止し、削除overrideを外してBase Composeだけで作り直します。録画fileと両DBは
削除しません。

## 更新する

録画中でないことと、直近の予約に停止時間が重ならないことを確認します。まずSazanamiのbackup IDを記録します。

```sh
docker compose run --rm sazanami db backup \
  --data-root /var/lib/sazanami-dvr
docker compose down
```

`.env`の`SAZANAMI_IMAGE`を新しい完全な版tagへ変更し、imageを取得します。

```sh
docker compose pull sazanami
docker compose run --rm sazanami --version
docker compose run --rm sazanami db status \
  --data-root /var/lib/sazanami-dvr
```

`BEHIND`の場合だけ、新版で`db migrate`を実行します。`CURRENT`を確認してから`docker compose up -d`を
実行してください。`.env`、KonomiTV設定、チャンネル設定を自動上書きしません。

## 切り戻す

DB schemaが変わっていなければ、停止後に`SAZANAMI_IMAGE`を旧版の完全tagへ戻し、旧版の`db status`が
`CURRENT`であることを確認して起動します。

Schemaが変わった場合は、新版imageのまま更新前backupを復元します。

```sh
docker compose down
docker compose run --rm sazanami db restore \
  --data-root /var/lib/sazanami-dvr \
  --backup-id <更新前のbackup-id>
```

表示されたoperationが`COMMITTED`であることを確認し、旧版tagへ戻します。旧版の`db status`が`CURRENT`に
なってから起動してください。録画fileは削除、移動、上書きしません。

## 停止と削除

```sh
docker compose stop
docker compose down
```

これらはcontainerを止めますが、bind mountしたdata、設定、録画、log、captureを削除しません。通常手順では
`down -v`やhost directoryの削除を実行しません。Purgeは対象の絶対pathを一件ずつ確認する別操作です。

## 制限

- KonomiTVの録画mountは既定でread-onlyです。削除overrideを明示した構成だけ、KonomiTV画面から削除できます。
- 削除overrideではKonomiTVがlock以外の録画directory内fileを変更できます。同じsizeの内容変更は検出しません。
- Compose全体のarm64、GPU encoder、Docker Desktop、rootless Dockerは未検証です。
- KonomiTV／Komorebiの全操作対応を示す構成ではありません。
- Source buildだけでは、番組表、途中録画、停止、再生の実通信成功を証明しません。
