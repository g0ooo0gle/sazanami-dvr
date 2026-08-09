# KonomiTV既定録画プリセットの番組追従仕様 第2版

- Status: Accepted
- Accepted date: 2026-08-09
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related decision: [`../../docs/adr/0029-bounded-mirakurun-preclaim-follow.md`](../../docs/adr/0029-bounded-mirakurun-preclaim-follow.md)
- Supersedes: `konomitv-file-copy2-v1.md`の`TuijyuuFlag=0`だけ

## 目的

KonomiTVへ返す既定録画プリセットを、実装済みの録画開始前追従と一致させる。

## 変更

変更点は一つだけである。`EpgTimerSrv.ini`の`[REC_DEF]`にある`TuijyuuFlag`を`1`にする。これ以外の行、順序、BOM、CRLF、許可ファイル名、応答上限は第1版から変更しない。

```ini
[REC_DEF]
SetName=デフォルト
RecMode=1
NoRecMode=1
Priority=3
TuijyuuFlag=1
```

KonomiTVがこのプリセットで作った予約は`requested_follow=true`になる。実際の予約更新は、録画開始前の番組時刻追従仕様の全条件を満たす場合だけ行う。

## 必須test

- `EpgTimerSrv.ini`のbyte一致。UTF-8 BOMとCRLFを維持する。
- KonomiTVの既定プリセット取得で番組追従が有効になる。
- 既定プリセットから追加した予約を取得すると、追従flagが有効である。
- 予約取消し、未許可ファイル名、本文上限、分割送信の既存testを維持する。

## 対象外

- 録画開始後の延長、短縮、event relay。
- ほかの録画設定、プリセット追加、録画folderの出力。
