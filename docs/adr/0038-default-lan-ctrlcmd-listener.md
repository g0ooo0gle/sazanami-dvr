# ADR-0038: CtrlCmdはIPv4全interface待受を既定にする

- Status: Accepted
- Date: 2026-08-09
- Deciders: Project owner, Codex（v1.0までの委任範囲）
- Supersedes: ADR-0026、ADR-0033、ADR-0034のCtrlCmd既定待受に関する判断だけ
- Superseded by: None

## 背景

Sazanami DVR v0.0.11はCtrlCmdを`127.0.0.1:4510`で待ち受ける。別端末のKomorebi、別PCやコンテナのKonomiTVから使うには、`recording serve`へLAN向け待受を明示する必要がある。単独の`ctrlcmd serve`はループバック以外を受け付けない。

初期版では厳しい公開制限より、LAN内で設定しやすいことを優先するというプロジェクト責任者の判断を受けた。CtrlCmdには対象クライアントと互換な認証がないため、公開範囲と使いやすさの釣り合いを既定値で決める必要がある。

## 決定

`recording serve`と`ctrlcmd serve`のCtrlCmd待受は、`0.0.0.0:4510`を既定にする。利用者が追加指定しなくても、ホストのIPv4全interfaceで接続を受け付ける。

ループバック、numeric private IP、`0.0.0.0`、`::`は明示指定できる。IPv6全interface待受は自動で有効にせず、必要な場合だけ`[::]:4510`を指定する。hostname、link-local、multicast、port 0、不正なport、明示したglobal IPは引き続き拒否する。

CtrlCmdへ独自の認証やTLSを追加しない。起動時と導入文書には、認証なしで全interface待受中であること、インターネットへ直接公開しないこと、必要に応じてホストのファイアウォールでLAN内へ制限することを短く表示する。

録画再生HTTPと運用WebUIの既定待受は変更しない。接続数、本文、deadline、同時処理、停止処理の上限も変更しない。

## 理由

別端末やコンテナから使うたびに待受アドレスを調べて指定するより、LANからそのまま接続できる方が初期版の利用体験に合う。IPv4全interfaceなら、Wi-Fi、有線、コンテナbridgeが併存してもSazanami側の設定を増やさずに済む。

利用者は`--listen 127.0.0.1:4510`を指定するだけで、従来と同じ端末内限定へ戻せる。DBや設定形式の移行も不要である。

## 影響

- 既定起動のCtrlCmdは、同じLANの別端末から到達できる。
- ホストがインターネットへ直接接続されている場合、OSやネットワーク側の遮断がなければ外部からも到達し得る。
- KonomiTVとKomorebiの接続設定はSazanamiホストのLANアドレスだけで済む。
- HTTP、WebUI、Mirakurun接続先の既定は変わらない。
- ADR-0026、ADR-0033、ADR-0034のその他の判断と上限は維持する。

## 採用しなかった案

### ループバック既定を維持する

別端末とコンテナで毎回追加設定が必要になるため、今回の利用者判断に合わない。

### private interfaceを自動検出する

複数interfaceから正しい一つを安全に選べないため採用しない。

### WebUIとHTTPも同時に全interfaceへ変える

変更対象はCtrlCmdだけであり、公開する操作を増やさないため採用しない。
