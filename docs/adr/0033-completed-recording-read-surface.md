# ADR-0033: 録画結果は既存DBから読み、完成ファイルだけを仮想URLで配信する

- Status: Accepted
- Date: 2026-08-09
- Decision owner: Project owner
- Delegated reviewer: Codex
- Authorization: Project ownerはv1.0.0までの技術判断、公開API変更、private LAN内の待受をCodexへ委任した
- Related: ADR-0026、Plan 0040、Plan 0046、Handoff 0021

## 背景

Sazanami DVR v0.0.8は、録画の予約、試行、終了理由、byte数、完成ファイルの相対pathをSQLiteへ保存している。一方、利用者が成功と失敗をまとめて確認できる製品APIはない。

Komorebi 1.1.0-beta6も、録画一覧2017、詳細2024、resolver、HTTP直接再生が揃わなければ録画済み画面を完走できない。必要なのは、既存データを安全に外へ見せる接続口である。

## 判断

録画履歴は既存の予約、録画試行、segmentを結合して読み、別の履歴tableを作らない。録画番号を公開IDとして使う。録画番号は正の32 bit整数で、永続化され、再利用されない。

Native REST APIは成功、失敗、一部保存、中止、未開始の終了結果を読み取り専用で返す。CtrlCmd 2017と2024は、成功状態、完成segment、利用可能な完成ファイルが揃った項目だけを返す。

外部へ返す録画pathはIDを含む仮想URLにする。絶対pathとDB内の相対pathは返さない。HTTP adapterはIDからDBの完成録画を解決し、録画保存先adapterが通常file、所有者、相対path、byte数を再確認してからRange対応で配信する。

録画常駐process内に小さいHTTP serverを追加する。録画processのCtrlCmdとHTTPはloopbackを既定とし、具体的なprivate IPを指定した場合だけLANで待ち受ける。単独の確認用`ctrlcmd serve`はloopback限定を維持する。

別daemonは作らない。認証を送れないクライアント向けの独自認証や外部frameworkも追加しない。

## 結果

- DB migrationなしで録画結果を確認できる。
- Komorebiは完成録画を一覧、詳細、直接再生まで進められる。
- 録画保存先の構成をクライアント、JSON、通常ログへ漏らさない。
- LAN待受には、信頼できるLANとホスト側firewallが必要になる。
- 画像解析と画質変換は別の変更として残る。

## 採用しなかった案

- **別の録画履歴table:** 既存状態と二重管理になるため不採用。
- **絶対pathの公開:** Komorebiのresolverに不要で、情報漏えいとpath依存を増やすため不採用。
- **recording root全体の静的公開:** DBで完成を確認できない部分fileや未知fileまで公開し得るため不採用。
- **同時に画質変換を追加:** 直接再生を通すために不要な外部processと資源管理を増やすため不採用。
