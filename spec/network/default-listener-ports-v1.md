# 既定listenerポート仕様 v1

- Status: Accepted
- Date: 2026-08-11
- Decision: `docs/adr/0049-default-listener-ports.md`
- Target: Sazanami DVR v0.0.29の次の小規模版

## 目的

Sazanami DVRの3つの接続口を固定の連番へ揃え、Mirakurunなどと同じホストで起動しやすくする。既存の公開範囲と明示設定は維持する。

## 要件

### PORT-001: CtrlCmd

`recording serve`と`ctrlcmd serve`のCtrlCmd既定値は`0.0.0.0:4520`とする。IPv4全interface、認証なしという公開範囲は変更しない。

loopbackへ絞る例と起動時の注意には`--listen 127.0.0.1:4520`を使う。

### PORT-002: 録画HTTP

`recording serve`の録画HTTP既定値は`127.0.0.1:4521`とする。利用者が`--http-listen`を明示した場合は、既存のloopback、private IP、全interfaceの検証規則を使う。

### PORT-003: WebUI

`ui serve`の既定値は`127.0.0.1:4522`とし、既定URLを`http://127.0.0.1:4522/`とする。WebUIはnumeric loopback限定を維持する。

### PORT-004: 明示設定との互換

既存のフラグ名、検証規則、安定した失敗理由を変更しない。4510、40772、40773を特別扱いせず、許可済みアドレスと1～65,535の組合せなら明示指定を受け付ける。

旧値から新値への設定書換え、DB migration、自動的な空きポート探索、別ポートへの自動再試行は行わない。

### PORT-005: 変更しない範囲

接続数、本文サイズ、期限、同時処理、停止処理、通常出力の秘匿を変更しない。予約、録画、ライブ視聴、番組表、保存形式にも影響させない。

## 必須テスト

- CtrlCmd、録画HTTP、WebUIの既定値が、それぞれ4520、4521、4522である。
- CtrlCmdの既定がIPv4全interface、録画HTTPとWebUIの既定がloopbackである。
- 4510、40772、40773を各listenerへ明示したとき、既存の許可範囲なら検証を通る。
- CLI使用方法と起動時の注意が新しい既定値を表示する。
- KonomiTVとKomorebiの接続手順が4520と4521を案内する。
- CtrlCmd、録画HTTP、WebUIの対象テストと`go vet ./...`が成功する。
- PR CIで全体テスト、shuffle、race、CGOを使わないLinux／Darwin buildを確認する。

## 完了条件

初期設定の3つの接続口が4520～4522で起動し、公開範囲と上限に回帰がない。旧ポートも明示指定で利用でき、現在の値が公開文書とCLI表示で一致している。
