# CtrlCmdチャンネル待受の使い方

この機能は、保存済みのMirakurunカタログと手動設定を照合し、KonomiTV向けのチャンネル情報を返します。CtrlCmdは既定で同じLANから接続できます。

現在実装しているのは、状態確認の2200、`ChSet5.txt`取得の1060、サービス一覧の1021です。番組表、予約、録画を使う場合は`recording serve`を起動してください。

## チャンネル設定を作成する

先に`catalog sync`を実行し、Mirakurunのサービス情報をDBへ保存します。次に、データ保存先の直下へJSON形式のチャンネル設定を作成します。

MirakurunからTSIDを取得できない場合は、確認済みの値だけを`transport_stream_id`へ記載してください。推測値は使いません。

```json
{
  "format": "sazanami-channel-map-v1",
  "backend_id": "00000000-0000-4000-8000-000000000000",
  "services": [
    {
      "provider_locator": "123456789",
      "network_id": 32736,
      "service_id": 1024,
      "transport_stream_id": 32736,
      "provider_name": "",
      "network_name": "",
      "transport_stream_name": "",
      "remote_control_key_id": 1,
      "partial_reception": false,
      "epg_capture": true,
      "search": true
    }
  ]
}
```

`backend_id`は`catalog sync`の成功時に表示されます。サービス数は1〜4,096件、設定ファイル全体は1 MiB以下にしてください。

## 設定を検証する

次のコマンドは待受を始めず、設定と保存済みカタログが一致するか確認します。

```console
sazanami-dvr ctrlcmd validate \
  --data-root <data-root> \
  --channel-map <data-root>/channels.json
```

成功時はサービス数だけを表示します。設定ファイルの場所、サービス名、識別子、ハッシュは表示しません。

## 待受を始める

```console
sazanami-dvr ctrlcmd serve \
  --data-root <data-root> \
  --channel-map <data-root>/channels.json
```

起動前に`validate`と同じ検査を行い、すべて成功した場合だけ`0.0.0.0:4520`で待受を始めます。認証とTLSはないため、インターネットへ直接公開しないでください。

待受先には、numeric loopback、numeric private IP、`0.0.0.0`、`::`と、1〜65,535のポートを指定できます。ホスト名、link-local、multicast、global IPは指定できません。同じPCだけに絞る場合は`--listen 127.0.0.1:4520`を追加します。

停止するには`SIGINT`または`SIGTERM`を送ります。受付済みの処理が期限内に終わるのを待ってから、DBのロックを解放します。

## 設定を更新する

1. 待受を停止する
2. 新しい設定ファイルを作り、使用中のファイルと置き換える
3. `ctrlcmd validate`を実行する
4. 成功したら`ctrlcmd serve`を再起動する

設定の自動再読込、自動修復、自動再試行は行いません。検証に失敗した場合は、直前に動作していた設定へ戻してください。

## エラーを調べる

エラーには`channel-map-json-invalid`や`channel-service-mismatch`などの短い理由だけを表示します。原因を調べるときも、実際の番組名、宅内アドレス、利用者名、パスを公開ログやIssueへ貼り付けないでください。

`ctrlcmd serve`はMirakurun、チューナー、放送ストリームへ接続しません。
