# Sazanami DVR

Sazanami DVRは、Mirakurun／mirakcから番組情報と放送ストリームを受け取り、KonomiTVからの予約を録画する軽量なバックエンドです。Goの単一実行ファイルとして動作します。

現在のバージョンは **v0.5.0（プレリリース）** です。固定KonomiTV v0.14.1の日常的なバックエンド操作を対象とします。EDCB全機能、別版KonomiTV、Komorebi全体への互換性は表明しません。変更内容は[変更履歴](CHANGELOG.md)を参照してください。

## 最短セットアップ

1. **Mirakurun／mirakcとチャンネルを準備する。** Mirakurunまたはmirakcを先に用意し、利用するサービスとチャンネル設定を確認します。
2. **配布物を検証する。** Releaseから利用するアーカイブと`SHA256SUMS`をダウンロードし、`sha256sum --ignore-missing --check SHA256SUMS`で照合します。ハッシュが一致しなければ、展開せずにそこで止めます。
3. **インストールして初期設定する。** 専用利用者でアーカイブを配置し、`db migrate`、`catalog sync`、チャンネルマップの設定と検証を順に行います。既存DBを更新する前には、必ずバックアップを作成します。
4. **systemdを起動して確認する。** systemdサービスを起動し、`systemctl is-active sazanami-dvr`が`active`になることと、CtrlCmdの`4520`、録画HTTPの`4521`が待ち受けていることを確認します。CtrlCmdは認証なしで待ち受けるため、インターネットへ直接公開しません。

## 主な機能

### 番組表と予約

- Mirakurun互換APIからサービス、番組、番組詳細を取得してSQLiteへ保存し、起動直後と一定間隔で番組表を更新します。
- KonomiTV向けに、状態確認、チャンネル一覧、番組表、予約の一覧・追加・変更・取消しを提供します。
- 保存済みの番組表を検索し、自動予約条件に一致する番組を重複なく通常予約へ加えます。

### 録画と再生

- 予約時刻に放送ストリームを受け取り、TSファイルへ録画します。放送中に追加した予約、番組時刻の変更、録画中の停止にも対応します。
- 起動時に取得した台数または明示した上限の範囲で同時録画し、一時的な通信切断では部分ファイルのまま再接続します。
- 録画履歴、完成録画、Range取得、再起動後の状態を確認できます。ワンセグ別出力にも対応します。

### クライアント連携

- KonomiTVとKomorebiへ、予約に必要な設定、局ロゴ、番組表、完成録画を返します。
- KonomiTV／Komorebiのライブ視聴へ、Mirakurunの放送ストリームを同時4本まで中継します。
- Komorebiの非直接ライブには、元のTSを変換せずHLSとして配信します。

### 運用

- 保存済みの番組表と運用状態を同じPCのWebUIで確認できます。
- DBの移行、バックアップ、復元を明示的なコマンドで行えます。
- 明示したDocker Compose構成では、KonomiTVから完成録画を削除した後の履歴状態も更新します。

## 注意事項

- CtrlCmdは既定でIPv4の全インターフェースに待ち受けます。認証とTLSはないため、信頼できる宅内LANだけで使い、ルーターのポート転送も行わないでください。
- データ保存先と録画保存先は、サービスの実行ユーザーだけが読み書きできる場所にしてください。
- 更新前にはサービスを停止してバックアップを作成し、更新後にDBの状態が`CURRENT`になることを確認してください。
- `purge`は設定、DB、バックアップ、録画、専用利用者を削除する不可逆操作です。必要なデータを退避し、対象を確認してから実行してください。
- EDCB全機能、別版KonomiTV、Komorebi全体、全画面・全検索条件の実機総当たり、72時間試験は対象外です。

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

変更方法は[CONTRIBUTING.md](CONTRIBUTING.md)、脆弱性の連絡は[SECURITY.md](SECURITY.md)を参照してください。

## License

[MIT License](LICENSE)および[第三者ライセンス表示](THIRD_PARTY_NOTICES.md)を参照してください。
