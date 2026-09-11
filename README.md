# Sazanami DVR

Sazanami DVRは、Mirakurun／mirakcから番組情報と放送ストリームを受け取る、KonomiTV向けの軽量な録画バックエンドです。予約に従って録画し、単一の実行ファイルで動作します。

現在のバージョンは **v1.1.0（安定版）** です。KonomiTV v0.14.1の日常的なバックエンド操作を対象とします。EDCBの全機能、KonomiTVの他のバージョン、Komorebi全体との互換性は表明していません。変更内容は[変更履歴](CHANGELOG.md)を参照してください。

## 標準の導入方法は2つ

どちらの方法でも、先にMirakurunまたはmirakcを利用できる状態にし、`channels.json`を用意します。

### Linuxへ直接入れる

systemdが動くLinuxで使う方法です。GitHub ReleaseからCPUに合うアーカイブを展開し、同梱のインストーラを実行します。

```console
sudo ./packaging/install.sh install
```

`install`は専用利用者、標準ディレクトリ、実行ファイル、systemdのサービス定義、環境設定ファイルを配置します。続けて接続先とチャンネルを設定し、DBを準備してからサービスを起動してください。コピーして使える手順は[Linuxへ導入する](docs/linux-installation.md)にまとめています。

### Docker ComposeでSazanami DVRとKonomiTVを起動する

Docker EngineとDocker Composeが動くLinuxで使う方法です。配布アーカイブの`packaging/compose`を専用ディレクトリへコピーし、コピー先へ移動して準備スクリプトを実行します。

```console
cp -R packaging/compose <install-dir>
cd <install-dir>
./prepare.sh
```

作成された2つの設定ファイルを編集し、用意した`channels.json`を配置したら、Sazanami DVRとKonomiTVを起動できます。詳しくは[Docker Composeで導入する](docs/docker-compose.md)を参照してください。

## 主な機能

### 番組表と予約

- Mirakurun互換APIからサービス、番組、番組詳細を取得してSQLiteへ保存し、起動直後と一定間隔で番組表を更新します。
- KonomiTV向けに、状態確認、チャンネル一覧、番組表、予約の一覧・追加・変更・取消しを提供します。保存済み番組表の検索にも対応します。
- 自動予約条件に一致した番組を重複なく通常予約へ加え、確認できた番組時刻の変更に予約を追従させます。

### 録画・ライブ視聴・再生

- 予約に従って放送ストリームをTSファイルへ録画します。放送中に追加した予約、録画中の停止、再起動後の予約と録画結果の引き継ぎに対応します。
- 同時録画の上限は、起動時に取得したチューナー数または明示した正の値で管理します。
- 録画ストリームが一時的に切断された場合は、同じ部分ファイルへ最大3回再接続します。
- 録画履歴、完成録画、Range取得、ワンセグ別出力を提供します。
- KonomiTVとKomorebiのライブ視聴向けにMirakurunの放送ストリームを同時4本まで中継します。Komorebiの非直接ライブ視聴では、元のTSを変換せずHLSで配信します。

### クライアント連携と運用

- KonomiTVへ予約に必要な設定、局ロゴ、番組表を返します。完成録画は共有フォルダーからKonomiTVが読み取り、Komorebiには録画一覧と取得APIから提供します。
- 保存済み番組表と運用状態を同じPCのWebUIで確認し、DBの移行、バックアップ、復元を明示的なコマンドで行えます。
- 明示したDocker Compose構成では、KonomiTVから完成録画を削除した後の履歴状態も更新します。

詳しい条件と確認範囲は、目的別ガイドの互換実装表を参照してください。

## 注意事項

- CtrlCmdは既定でIPv4の全インターフェースで待ち受けます。認証機能とTLSは提供していないため、信頼できる宅内LANだけで使い、ルーターのポート転送も行わないでください。
- データ保存先と録画保存先は、サービスの実行ユーザーだけが読み書きできる場所にしてください。
- 更新前にはサービスを停止してバックアップを作成し、更新後にDBの状態が`CURRENT`になることを確認してください。
- `purge`は標準の設定、DB、バックアップ、録画先、専用利用者を削除する不可逆操作です。別に指定した録画先は自動で削除しません。必要なデータを退避し、対象を確認してから実行してください。
- EDCB全機能、KonomiTVの他の版、Komorebi全体、実機での全画面・全検索条件の総当たり試験は対象外です。

## 目的別ガイド

- 初回導入、更新、切り戻し、通常削除、purgeは[Linuxへの導入・更新・削除](docs/linux-installation.md)を参照してください。
- KonomiTVの接続と予約操作は[KonomiTVと接続する](docs/konomitv-setup.md)を参照してください。
- KonomiTVとSazanami DVRをコンテナで動かす場合は[Docker Composeで導入する](docs/docker-compose.md)を参照してください。
- 対応済み操作と既知の制限は[互換実装表](docs/compatibility.md)を参照してください。
- 予約、録画、再生の運用は[録画機能の運用手順](docs/recording-operations.md)を参照してください。
- DBの作成、移行、バックアップ、復元は[カタログDBの運用・復旧手順](docs/catalog-database-operations.md)を参照してください。

## 開発

Go 1.26.6、CGOなしでビルドします。

```console
go mod verify
go test ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/sazanami-dvr
```

変更に参加する方法は[CONTRIBUTING.md](CONTRIBUTING.md)、脆弱性の報告方法は[SECURITY.md](SECURITY.md)を参照してください。

## License

[MIT License](LICENSE)および[第三者ライセンス表示](THIRD_PARTY_NOTICES.md)を参照してください。
