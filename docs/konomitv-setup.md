# KonomiTVと接続する

この手順では、KonomiTVの番組表からSazanami DVRへ予約を送り、完成した録画ファイルをKonomiTVで
見つけられる構成にします。KonomiTVとSazanami DVRは、同じUbuntu上で直接動かしてください。

初回は「接続の分担」から「一件確認する」までを順に進めます。「うまくいかない場合」は、問題が起きた
ときだけ参照してください。

現在のSazanami DVRはローカル接続だけを受け付けます。KonomiTVをDockerコンテナへ入れると
`127.0.0.1`の指す環境が分かれるため、初回確認ではDockerを使いません。

確認対象はKonomiTV v0.14.1です。画面の番組表から予約し、5分間録画したファイルを録画済み一覧から
再生するところまで一件確認しました。長時間運転や幅広い環境での動作は未確認です。
操作別の状況は[互換実装表](compatibility.md)で確認できます。

## KonomiTVは予約、Mirakurunはライブ視聴を担当する

KonomiTVではバックエンドに`EDCB`を選び、その接続先をSazanami DVRにします。ライブ視聴は従来どおり
Mirakurunまたはmirakcから受信します。

```text
KonomiTV ──予約・番組表──> Sazanami DVR ──録画時だけ──> Mirakurun / mirakc
    └────────────────ライブ視聴──────────────────────> Mirakurun / mirakc
```

Sazanami DVRの待受は`127.0.0.1`のままです。LANへ公開する必要はありません。

## Sazanami DVRを先に起動する

先にDBと番組表を準備し、チャンネル設定を検証します。旧版のDBを使う場合も、`db migrate`が
事前backupを作ってから最新形式へ更新します。

```console
sazanami-dvr db migrate --data-root <data-root>

sazanami-dvr catalog sync \
  --data-root <data-root> \
  --provider mirakurun \
  --base-url <mirakurun-url>

sazanami-dvr ctrlcmd validate \
  --data-root <data-root> \
  --channel-map <channel-map>
```

チャンネル設定には、実際に確認できたTSIDだけを記載します。作り方は
[CtrlCmdチャンネル待受の使い方](ctrlcmd-channel-runtime.md)を参照してください。

続いて、KonomiTV向けの待受と録画処理を起動します。

```console
sazanami-dvr recording serve \
  --data-root <data-root> \
  --recording-root <recording-root> \
  --channel-map <channel-map> \
  --provider mirakurun \
  --base-url <mirakurun-url> \
  --listen 127.0.0.1:4510
```

KonomiTVは起動時にバックエンドへ接続できるか確認するため、Sazanami DVRを先に起動してください。

`recording serve`は起動直後と既定1時間ごとに番組表を更新します。更新に失敗した場合は前回の番組表を維持し、録画処理を止めません。作成済み予約の開始・終了時刻を新しい番組表へ追従させる機能は、まだありません。

v0.0.2以降は、KonomiTVが録画設定に使う`Bitrate.ini`と`EpgTimerSrv.ini`を固定内容で返します。録画フォルダなどのホスト固有情報は含みません。設定画面に表示される項目の一部は、予約時の入力としてまだ受け付けません。

## KonomiTVをEDCBバックエンドとして設定する

KonomiTVの`config.yaml`で、次の項目を設定します。ほかの項目は既存の設定を維持してください。
`<mirakurun-url>`と`<recording-root>`は実際の値へ置き換えます。

```yaml
general:
    backend: 'EDCB'
    always_receive_tv_from_mirakurun: true
    edcb_url: 'tcp://127.0.0.1:4510/'
    mirakurun_url: '<mirakurun-url>'

video:
    recorded_folders:
        - '<recording-root>'
```

`backend`が`Mirakurun`のままだと、KonomiTVの録画予約機能は利用できません。Sazanami DVRの完成ファイルは
録画保存先の下へ`.ts`として作られます。KonomiTV v0.14.1は登録したフォルダを再帰的に監視し、60秒以上の
TSファイルを録画済み番組の解析対象にします。

## 番組表から録画済み一覧まで一件を確認する

1. Sazanami DVRを起動する
2. KonomiTVを起動する
3. KonomiTVの番組表にチャンネルと番組が表示されるまで待つ
4. 一つの番組を予約する
5. 録画予約一覧に一件だけ表示されることを確認する
6. 録画開始後、予約が「録画中」と表示されることを確認する
7. 開始前の別の予約で、優先度の変更と予約取消しを確認する
8. 番組終了後、録画保存先に`.ts`の完成ファイルが一件あることを確認する
9. KonomiTVの「ビデオをみる」に録画済み番組が表示されることを確認する

実験では、終了まで60秒以上残っている番組を選んでください。開始から5分を超えた番組や、残り60秒未満の
番組は安全のため録画しません。

Mirakurunへの接続とファイル準備にかかる時間を吸収するため、番組開始の5秒前から録画処理を始めます。
番組の予定開始・終了時刻は変更しません。

## 問題が起きた箇所から確認する

| 表示や状態 | 確認すること |
|---|---|
| KonomiTVがEDCBへ接続できない | Sazanami DVRが先に起動し、`127.0.0.1:4510`で待ち受けているか |
| 予約画面がHTTP 422になる | KonomiTVの`backend`が`EDCB`になっているか |
| 番組表が空 | `catalog sync`と`ctrlcmd validate`が成功しているか |
| 予約後も録画が始まらない | 開始時刻、残り時間、同時録画一件の制限に該当していないか |
| 予約を変更・取消しできない | すでに録画処理が始まっていないか、初版で対応しない録画設定を選んでいないか |
| 録画中と表示されない | Sazanami DVRが最新DB形式で動き、予約が実際に録画中か |
| 録画済み番組に出ない | 完成した`.ts`か、60秒以上あるか、KonomiTVから保存先を読めるか |

接続先、番組情報、録画ファイルの中身をIssueや公開ログへ貼り付けないでください。

## この手順で使う名前

| 名前 | 意味 |
|---|---|
| `data-root` | Sazanami DVRのDBと運用データを保存する場所 |
| `recording-root` | Sazanami DVRの録画ファイルを保存する場所 |
| `channel-map` | MirakurunのサービスとKonomiTV向けチャンネル情報を対応させるJSONファイル |

この手順で解決しない問題は、秘密情報を除いた再現手順とSazanami DVRのcommitをGitHub Issueへ記載して
ください。脆弱性に関する内容は公開Issueへ書かず、[セキュリティポリシー](../SECURITY.md)に従います。
