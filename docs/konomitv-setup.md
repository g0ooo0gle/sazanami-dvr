# KonomiTVと接続する

この手順では、KonomiTVの番組表からSazanami DVRへ予約を送り、完成した録画ファイルをKonomiTVで
見つけられる構成にします。KonomiTVとSazanami DVRは、同じUbuntuまたは信頼できる同じLANで動かしてください。

初回は「接続の分担」から「一件確認する」までを順に進めます。「うまくいかない場合」は、問題が起きた
ときだけ参照してください。

Sazanami DVRのCtrlCmdは、既定で同じLANから接続できます。KonomiTVをDockerコンテナへ入れると`127.0.0.1`の指す環境が分かれるため、Sazanami DVRホストのLANアドレスを指定してください。同じPCだけで使う場合は、Sazanami DVRへ`--listen 127.0.0.1:4510`を明示できます。

確認対象はKonomiTV v0.14.1です。画面の番組表から予約し、5分間録画したファイルを録画済み一覧から
再生するところまで一件確認しました。自動予約はHTTP APIから条件の追加・一覧取得・変更・削除を行い、
実Mirakurunの番組表から予約を作って、再起動後に重複しないところまで確認しました。長時間運転や幅広い環境での動作は未確認です。
操作別の状況は[互換実装表](compatibility.md)で確認できます。

## Sazanami DVRが予約とライブ視聴をMirakurunへ接続する

KonomiTVではバックエンドに`EDCB`を選び、その接続先をSazanami DVRにします。予約録画とライブ視聴のどちらも、Sazanami DVRが必要なときだけMirakurunまたはmirakcの放送ストリームを開きます。

```text
KonomiTV ──番組表・予約・ライブ視聴──> Sazanami DVR ──必要なstream──> Mirakurun / mirakc
```

CtrlCmdは既定でLANへ公開されます。同じPCだけで使う場合は、起動時に`--listen 127.0.0.1:4510`を指定します。

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
  --base-url <mirakurun-url>
```

この起動方法では、CtrlCmdを`0.0.0.0:4510`で待ち受けます。認証はないためインターネットへ直接公開しないでください。必要ならホストのファイアウォールで信頼できるLANに制限します。

KonomiTVは起動時にバックエンドへ接続できるか確認するため、Sazanami DVRを先に起動してください。

`recording serve`は起動直後と既定5分ごとに番組表を更新します。更新に失敗した場合は前回の番組表を維持し、録画処理を止めません。

KonomiTV v0.14.1の`/api/recording/conditions`から自動予約条件の一覧、追加、変更、削除を行えます。Sazanami DVRは条件を保存し、番組表の更新が成功した後に、文字列、ジャンル、あいまい検索、映像・音声、放送局、日時、番組長、無料・有料を評価します。KonomiTVの画面にこの操作がない構成では、画面からは設定できません。対応する条件と安全上の上限は[録画機能の運用手順](recording-operations.md#自動予約を使う)を参照してください。

KonomiTVから番組追従ありで作った予約は、同じMirakurun内の同じ番組で、観測間隔と時刻差が固定上限内の場合だけ新しい時刻へ追従します。判断できない場合は予約時の時刻を維持します。録画開始後は、開始時刻が変わらない終了延長だけを反映します。短縮や開始時刻変更には対応していません。

同時録画は既定で一件です。複数のストリームを同時に提供できる環境では、Sazanami DVRの起動引数へ`--max-concurrent-recordings 2`のように1～8の上限を追加します。KonomiTV側の設定変更は不要です。

v0.0.2以降は、KonomiTVが録画設定に使う`Bitrate.ini`と`EpgTimerSrv.ini`を固定内容で返します。録画フォルダなどのホスト固有情報は含みません。v0.0.16では、予約の有効・無効、優先度、番組追従、既定または個別の前後余白を受け付けます。v0.0.19では録画保存ルート内の相対フォルダーと対応済みのファイル名テンプレート、v0.0.21では字幕・データ放送の選択も使えます。v0.0.22では「デフォルト」「何もしない」と録画後スクリプトに対応します。スタンバイ、休止、再起動、シャットダウンは未対応です。

## 録画後スクリプトを使う

既定の許可ディレクトリは`<data-root>/post-recording-scripts`です。`recording serve`の起動時にディレクトリがなければ、所有者だけが使える権限で作成します。別の場所を使う場合は、所有者専用の絶対pathを`--post-recording-script-root`で指定します。

スクリプトを許可ディレクトリ内へ置き、Sazanami DVRの実行ユーザーが実行できる権限を付けます。KonomiTVには、そのファイルの絶対pathを指定してください。symlink、相対path、許可ディレクトリ外、実行権限のないファイルは受け付けません。

スクリプトは、完成ファイルと録画結果を確定した後に一度だけ実行します。引数はなく、shellも介しません。実行時間は最長5分です。失敗しても完成した録画は削除せず、再実行もしません。録画番号、完成ファイル、終了状態、終了理由は`SAZANAMI_RECORDING_NUMBER`、`SAZANAMI_RECORDING_FILE`、`SAZANAMI_RECORDING_STATE`、`SAZANAMI_RECORDING_REASON`で受け取れます。

KonomiTVの録画先欄は、Sazanami DVRの`--recording-root`から見た相対フォルダーとして解釈します。たとえば`ドラマ/新作`は`<recording-root>/ドラマ/新作`を表します。KonomiTVやSazanami DVRホストの絶対パス、`..`、Windowsドライブ、共有パスは指定できません。空欄なら従来どおり`YYYY/MM`へ保存します。

ファイル名テンプレートには、番組名の`$Title$`、放送局名の`$ServiceName$`、開始日時の`$SDYYYY$`や`$STHH$`、終了日時、放送時間、放送ID、`$ReserveID$`を使えます。末尾に`.ts`がなければ自動で付けます。対応マクロの完全な一覧は[録画機能の運用手順](recording-operations.md#録画先とファイル名を指定する)を参照してください。

## KonomiTVをEDCBバックエンドとして設定する

KonomiTVの`config.yaml`で、次の項目を設定します。ほかの項目は既存の設定を維持してください。
`<mirakurun-url>`と`<recording-root>`は実際の値へ置き換えます。

```yaml
general:
    backend: 'EDCB'
    always_receive_tv_from_mirakurun: false
    edcb_url: 'tcp://<sazanami-host>:4510/'
    mirakurun_url: '<mirakurun-url>'

video:
    recorded_folders:
        - '<recording-root>'
```

`backend`が`Mirakurun`のままだと、KonomiTVの録画予約機能は利用できません。Sazanami DVRの完成ファイルは
録画保存先の下へ`.ts`として作られます。KonomiTV v0.14.1は登録したフォルダを再帰的に監視し、60秒以上の
TSファイルを録画済み番組の解析対象にします。

`<sazanami-host>`にはSazanami DVRホストのLANアドレスを指定します。KonomiTVとSazanami DVRを同じPCで動かし、Sazanami DVRをloopback限定にした場合は`127.0.0.1`を使えます。

`always_receive_tv_from_mirakurun: false`では、KonomiTVのライブ視聴もSazanami DVRを経由します。Sazanami DVRをライブ経路から外す場合だけ`true`にします。その場合も番組表と録画予約はSazanami DVRへ接続します。

## 番組表から録画済み一覧まで一件を確認する

1. Sazanami DVRを起動する
2. KonomiTVを起動する
3. KonomiTVの番組表にチャンネルと番組が表示されるまで待つ
4. 一つの番組を予約する
5. 録画予約一覧に一件だけ表示されることを確認する
6. 録画開始後、予約が「録画中」と表示されることを確認する
7. 開始前の別の予約で、無効化、再有効化、優先度、前後余白、相対録画先、ファイル名の変更を確認する
8. 録画中の一件を停止し、停止時点までの録画が一覧へ表示されることを確認する
9. 番組終了後、録画保存先に`.ts`の完成ファイルが一件あることを確認する
10. KonomiTVの「ビデオをみる」に録画済み番組が表示されることを確認する

実験では、終了まで60秒以上残っている番組を選んでください。開始から5分を超えた番組や、残り60秒未満の
番組は安全のため録画しません。

録画処理は、予約の前後余白を反映した時刻に動きます。既定では番組開始の5秒前から番組終了の2秒後までです。

## 問題が起きた箇所から確認する

| 表示や状態 | 確認すること |
|---|---|
| KonomiTVがEDCBへ接続できない | Sazanami DVRが先に起動し、`<sazanami-host>:4510`へ同じLANから到達できるか |
| 予約画面がHTTP 422になる | KonomiTVの`backend`が`EDCB`になっているか |
| 番組表が空 | `catalog sync`と`ctrlcmd validate`が成功しているか |
| ライブ視聴を開始できない | `always_receive_tv_from_mirakurun`、チャンネル設定のONID・TSID・SID、Mirakurunのstream応答を確認する |
| 予約後も録画が始まらない | 開始時刻、残り時間、明示した同時録画上限に該当していないか |
| 予約を変更できない | すでに録画処理が始まっていないか、録画先が相対フォルダーか、ファイル名が対応マクロだけか、録画後スクリプトが許可ディレクトリ内の実行可能ファイルか、未対応の電源操作を選んでいないか |
| 録画を停止できない | すでに完成処理へ入ったか、録画が終了していないか |
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
