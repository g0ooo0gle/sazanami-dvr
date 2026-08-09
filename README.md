# Sazanami DVR

Sazanami DVRは、Mirakurun／mirakcから番組情報と放送ストリームを受け取り、KonomiTVからの予約を録画する軽量なバックエンドです。Goで実装しており、一つの実行ファイルで動きます。

現在のバージョンは **v0.0.13（ベータ版）** です。基本的な録画の流れは動きますが、対応範囲はまだ限定的です。

## 現在できること

- Mirakurun互換APIからサービスと番組を取得し、SQLiteへ保存する
- 録画プロセスを止めずに、起動直後と一定間隔で番組表を更新する
- 録画開始前の予約を、同じ番組だと確認できた開始・終了時刻の変更へ追従させる
- 録画中に同じ番組の終了時刻が延びた場合、録画時間を固定上限内で延ばす
- KonomiTV向けに、状態確認、チャンネル一覧、番組表、予約の一覧・追加・変更・取消しを返す
- KonomiTVから、保存済みの番組表をキーワード、ジャンル、映像・音声、放送局、日時、番組長、無料・有料で検索する
- KonomiTVの番組表へ、番組詳細、ジャンル、映像、音声の情報を返す
- KonomiTV／Komorebi向けに、自動予約条件の一覧・追加・変更・削除を返す
- 番組表の更新後、対応済みの自動予約条件に一致した番組を重複なく通常予約へ加える
- KonomiTVへ、録画設定に必要な`Bitrate.ini`と`EpgTimerSrv.ini`を返す
- KonomiTVの予約一覧へ、録画中の状態を返す
- KonomiTV／Komorebiのライブ視聴へ、Mirakurunの放送ストリームを同時4本まで中継する
- 予約時刻にMirakurunから放送ストリームを受け取り、TSファイルへ保存する
- 明示した1～8件の上限内で、重なった予約を別々のストリームとファイルへ録画する
- 録画中の一時的な通信切断では、同じ部分ファイルのまま最大3回再接続する
- 再起動後も予約と録画結果を引き継ぐ
- 成功・失敗を含む録画履歴を、読み取り専用のHTTP APIで確認する
- Komorebi向けに完成録画の一覧と詳細を返し、元のTSファイルをHTTPで直接再生する
- 保存済みの番組表と運用状態を、同じPCのWebUIで確認する
- DBの移行、バックアップ、復元を明示的なコマンドで行う

MirakurunとKonomiTV v0.14.1を使い、番組表の表示、予約、録画中の番組表更新、再起動後の状態確認、録画済み一覧からの再生開始を確認しています。通信を一度切った約15分・約1.20GBの録画も、再接続後に完成し、KonomiTVから再生を開始できました。4台のチューナーを持つMirakurunでは、異なる2局を約5分間同時に録画し、別々の完成ファイルとして再起動後も読み出せることを確認しました。

自動予約は、KonomiTV v0.14.1から条件を追加・一覧取得・変更・削除し、実Mirakurunの番組表から予約を作るところまで確認しています。同じ条件を再起動後に評価しても、予約が重複しないことも確認しました。

番組時刻の追従は、録画開始前と録画中の終了時刻延長を合成データで自動確認しています。実放送で実際に時刻が変わる場面は、まだ確認できていません。複数同時録画は、自動テストと実環境の両方で確認済みです。

v0.0.9で追加した録画履歴、Komorebi向け一覧・詳細、直接再生、シークは自動テスト済みです。Android TV上のKomorebiから使う実環境確認は、まだ行っていません。

v0.0.10では、KonomiTVとKomorebiが使うライブ視聴の開始・中継・終了を追加しました。KonomiTV v0.14.1のライブAPIで25秒間の受信を確認し、直接接続も2本同時と20回の繰り返しに成功しました。Android TV上のKomorebi画面は未確認です。詳しくは[互換実装表](docs/compatibility.md)を参照してください。

v0.0.11では、KonomiTVの番組検索に対応しました。検索のたびにMirakurunへ接続せず、最後に正常取得できた番組表を256件ずつ読みます。ジャンルとあいまい検索は、条件を無視して誤った結果を返さないよう、現時点では未対応として扱います。

v0.0.12では、Mirakurunから番組詳細、ジャンル、映像、音声を取得して番組表へ保存します。KonomiTVの番組表示と検索へ同じ情報を返し、ジャンル検索、ジャンル除外、映像・音声の絞り込み、表記の違いや小さな入力違いを許すあいまい検索を追加しました。v0.0.13では、実環境で見つかったKonomiTVのジャンル条件のバイト順を修正しています。

## 主な制限

- 同時録画の既定値は一件。2～8件へ増やす場合は、Mirakurunと保存先の能力を確認して明示指定が必要
- 予約変更は、録画開始前の優先度など初版対応項目に限定
- 予約取消しは録画開始前だけに限定し、録画ファイルは削除しない
- 自動予約で実行できる検索条件と録画設定は初版の安全な組み合わせに限定。未対応の条件は保存するが予約を作らない
- 番組検索はKonomiTV v0.14.1が送る条件に対応。自動予約では、高度な検索条件を保存できても予約作成にはまだ使わない
- 録画中に追従するのは、開始時刻が変わらない同じ番組の延長だけ。短縮や開始時刻変更には未対応
- 録画ストリームの再接続は最大3回。通信が切れていた間の欠損は補完しない
- KonomiTVの録画済み一覧は従来どおりKonomiTV自身が管理。Sazanamiの録画履歴APIとは別の経路
- Komorebi向けの録画一覧と直接再生は自動テスト済みだが、Android TV実機では未確認。詳しくは[互換実装表](docs/compatibility.md)に記載
- ライブ視聴は同時4本、チャンネル選択から受信開始まで30秒、一接続12時間まで。映像変換とHLSは行わない
- WebUIから予約、番組情報の更新、録画開始はできない
- 録画プロセスは明示設定でLAN待受が可能。ユーザー認証とTLS終端は未対応
- チューナーやDVBデバイスを直接制御しない

## 必要なもの

- Go 1.26.5
- Mirakurunまたはmirakc
- 所有者だけが読み書きできるデータ保存先
- 所有者だけが読み書きできる録画保存先

## ビルド

```console
go build -o ./bin/sazanami-dvr ./cmd/sazanami-dvr
./bin/sazanami-dvr --version
```

## 最短の起動手順

まずDBを作成し、番組情報を一度取得します。次の`catalog sync`は一回だけMirakurunへ接続します。

```console
./bin/sazanami-dvr db migrate --data-root <data-root>

./bin/sazanami-dvr catalog sync \
  --data-root <data-root> \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

接続先の指定方法と失敗時の動きは[Mirakurunから番組情報を取得する](docs/mirakurun-catalog-sync.md)を参照してください。

次にチャンネル設定を用意し、保存済みカタログと一致するか確認します。設定形式は[CtrlCmdチャンネル待受の使い方](docs/ctrlcmd-channel-runtime.md)を参照してください。

```console
./bin/sazanami-dvr ctrlcmd validate \
  --data-root <data-root> \
  --channel-map <channel-map>
```

録画保存先を作り、KonomiTV向けの待受と録画処理を開始します。

```console
mkdir -m 700 <recording-root>

./bin/sazanami-dvr recording serve \
  --data-root <data-root> \
  --recording-root <recording-root> \
  --channel-map <channel-map> \
  --provider mirakurun \
  --base-url <mirakurun-url> \
  --listen 127.0.0.1:4510 \
  --http-listen 127.0.0.1:40773
```

このプロセスは、起動直後と既定5分ごとにMirakurunの版、サービス、番組を確認します。更新が成功すると、保存済みの自動予約条件も評価します。録画用の放送ストリームは予約の開始5秒前に開き、ライブ用はクライアントが301を送った場合だけ開きます。更新間隔は`--catalog-refresh-interval`で5分から24時間の範囲に変更できます。停止には`SIGINT`または`SIGTERM`を使います。詳しくは[録画機能の運用手順](docs/recording-operations.md)を参照してください。

同時録画は既定で一件です。Mirakurunが複数のストリームを同時に提供できる環境では、`--max-concurrent-recordings 2`のように1～8の範囲で明示できます。上限を超えて開始時刻に達した予約は、録画枠なしとしてDBへ記録します。

KonomiTV側の設定と、画面から一件確認する手順は
[KonomiTVと接続する](docs/konomitv-setup.md)を参照してください。

## その他のコマンド

保存済みの情報をWebUIで確認します。

```console
sazanami-dvr ui serve --data-root <data-root>
```

既定のURLは`http://127.0.0.1:40772/`です。WebUIは表示と手動バックアップだけを行います。詳しくは[運用WebUIの利用手順](docs/web-ui-operations.md)を参照してください。

DBの状態確認、移行、バックアップ、復元には次のコマンドを使います。

```console
sazanami-dvr db status  --data-root <data-root>
sazanami-dvr db migrate --data-root <data-root>
sazanami-dvr db backup  --data-root <data-root>
sazanami-dvr db restore --data-root <data-root> --backup-id <uuid>
sazanami-dvr db recover --data-root <data-root> --operation-id <uuid>
```

詳しくは[カタログDBの運用・復旧手順](docs/catalog-database-operations.md)を参照してください。

## 録画ファイル

- 番組名や放送局名はファイル名に使わない
- 録画中は`.ts.partial`、正常に完了した場合だけ`.ts`にする
- 既存の完成ファイルを上書きしない
- 188バイト未満のデータを正常な録画として扱わない
- 途中ファイルや不明なファイルを自動削除しない
- 再起動後に途中ファイルへ追記しない

## ネットワーク

引数なしで起動した場合とDBコマンドは、待受も外部接続も始めません。

| コマンド | ネットワーク動作 |
|---|---|
| `catalog sync` | Mirakurunのサービス・番組APIへ一度接続する |
| `recording serve` | CtrlCmdと録画履歴HTTPを待ち受け、起動直後と一定間隔で番組表を更新し、予約時刻だけ放送ストリームへ接続する |
| `ctrlcmd serve` | チャンネル確認用に待ち受ける |
| `ui serve` | 運用WebUIを表示する |

待受先の既定値は`127.0.0.1`です。`recording serve`だけは、`--listen`と`--http-listen`へ宅内IPまたは`0.0.0.0`を明示するとLANから利用できます。認証とTLSはないため、信頼できる宅内LANだけで使い、ルーターのポート転送やインターネットへの直接公開は行わないでください。単独の`ctrlcmd serve`とWebUIは端末内限定です。

## 開発

SQLiteドライバーは`github.com/ncruces/go-sqlite3 v0.35.2`を使用し、CGOは使いません。Webフレームワーク、ORM、Node.js、コード生成も使っていません。

```console
go mod verify
go test ./...
go test -shuffle=on ./...
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
CGO_ENABLED=0 go build ./cmd/sazanami-dvr
```

Pull Requestと`main`へのpushでは、GitHub Actionsがこれらの検査と、Linux／macOSのIntel／Arm向けビルドを実行します。同じPull Requestへ続けてpushした場合は、古い実行を中止して新しいcommitだけを検査します。

主なパッケージ構成は次のとおりです。

```text
cmd/sazanami-dvr/              コマンド引数を読み、必要な機能を組み立てる
internal/core/                 予約や録画の型とルール
internal/app/                  番組同期、予約、録画、復旧の手順
internal/adapters/ctrlcmd/     KonomiTV向けCtrlCmd形式との変換
internal/adapters/provider/    Mirakurun接続とテスト用の接続処理
internal/adapters/recordingfs/ 録画ファイルの作成と完成処理
internal/adapters/recordinghttp/ 録画履歴と完成録画のHTTP読出し
internal/adapters/sqlite/      SQLiteへの保存、バックアップ、復元
internal/adapters/webui/       ローカル専用の運用画面
```

変更方法は[CONTRIBUTING.md](CONTRIBUTING.md)、脆弱性の連絡方法は[SECURITY.md](SECURITY.md)、外部ライブラリの情報は[依存関係の記録](docs/dependencies.md)を参照してください。

## ライセンス

[MIT License](LICENSE)
