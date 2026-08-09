# CtrlCmdの既定LAN待受仕様 v1

- Status: Accepted
- Date: 2026-08-10
- Deciders: Project owner、Codex（委任されたv1.0までの技術判断）
- Related ADR: ADR-0038
- Target release: v0.0.17

## 目的

同じLANのKonomiTVとKomorebiが、Sazanami側の待受設定を追加せずCtrlCmdへ接続できるようにする。録画再生HTTPとWebUIの公開範囲は広げない。

## 要件

### LAN-001: 既定待受

`recording serve`と`ctrlcmd serve`のCtrlCmd既定待受は、`0.0.0.0:4510`とする。IPv4の全interfaceで待ち受けるが、LAN内だけへ自動制限する機能ではない。

### LAN-002: 明示指定

利用者は、numeric loopback、numeric private IP、`0.0.0.0`、`[::]:4510`を明示できる。IPv6全interface待受は既定にせず、明示した場合だけ使う。

hostname、link-local、multicast、明示したglobal IP、port 0、1～65,535の範囲外、数字でないportはsocket作成前に拒否する。失敗理由は既存の安定したCLI理由へまとめ、入力値を通常出力へ含めない。

### LAN-003: 公開範囲の表示

全interfaceまたはprivate IPで待ち受ける場合は、起動時に次の内容を一度だけ短く表示する。

- CtrlCmdは認証なしでLANへ公開されている。
- インターネットへ直接公開しない。
- 狭く使う場合は`--listen 127.0.0.1:4510`を指定する。

接続元、実際の待受IP、要求内容は表示しない。loopbackを明示した場合はLAN公開の注意を表示しない。

### LAN-004: 変更しない範囲

録画再生HTTP、WebUI、Mirakurun接続先の既定値は変更しない。CtrlCmdの本文、接続数、同時処理、deadline、長時間stream、停止処理の上限も変更しない。独自認証、TLS、設定ファイル、DB migration、新しい常駐処理は追加しない。

### LAN-005: 元に戻す方法

利用者は`--listen 127.0.0.1:4510`を明示すれば、従来と同じ端末内限定へ戻せる。版を戻す場合もDBと録画ファイルの変換は不要である。

## 必須テスト

- `DefaultAddress`と両serve commandの既定が`0.0.0.0:4510`である。
- loopback、private IPv4／IPv6、IPv4／IPv6全interfaceを受理する。
- hostname、link-local、multicast、global IP、port 0、範囲外、数字でないportを拒否する。
- LAN公開時だけ注意を一度表示し、接続元や実IPを含めない。
- 録画再生HTTPとWebUIの既定値がloopbackのままである。
- 既存の接続数、本文、deadline、停止、長時間streamのテストが成功する。
- full、shuffle、race、vet、`go mod verify`、`govulncheck`、CGOを使わないLinux／Darwin amd64／arm64 build、Hosted Ubuntu CIを同じ最終commitで行う。
- 公開Linux amd64配布物を実験環境へ導入し、`--listen`を省略した状態で別のLAN端末から状態取得、サービス一覧、予約一覧を読み取れる。HTTPとWebUIが既定ではLANから到達しないことも確認する。

## 完了条件

既定起動だけでLAN内の固定KonomiTVとKomorebi相当クライアントからCtrlCmdの主要な読み取りが成功し、既存の録画・予約・ライブ経路に回帰がない。公開文書に認証なしの範囲とloopbackへ戻す指定が記載され、同じ最終commitのCIと配布物へ結び付いている。
