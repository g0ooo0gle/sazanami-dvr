# KonomiTV v0.14.1全操作マトリクス仕様 v1

- Status: Accepted
- Date: 2026-08-18
- Applies to: Sazanami DVR v0.5.0候補
- Decision: `docs/adr/0063-konomitv-v0-5-0-intermediate-release.md`
- Fixed KonomiTV source: tag `v0.14.1` / commit `0a32188274b81c1e7bed642474b208bd2a543a6b`
- License: MIT

## 目的

固定KonomiTV v0.14.1をEDCBバックエンドとして使うとき、Sazanamiが対応を約束する操作と完了条件を
利用者の操作単位で固定する。CtrlCmdの番号へ応答するだけでは完了とせず、画面または公開HTTP APIから
始まり、録画、file、長時間stream、後始末まで必要な経路を一続きで確認する。

Komorebiの全対応は本仕様に含めない。既存のKomorebi実装とv1.0の長期目標は維持し、v0.5.0の次の
機能版で別の固定sourceと実機証拠を使って扱う。

## 固定母集団

### `K5-001` Source inventory

対象は固定commitの`server/app`、`server/KonomiTV.py`、`client/src`から実行経路を追える通信に限る。
生成済み成果物、同梱third-party code、定義だけで呼び出し元がない関数は母集団へ含めない。

2026-08-18の再抽出では、到達するCtrlCmdは次の19種類である。

`2200`、`1060`、`1021`、`1029`、`2060`、`1025`、`2011`、`2013`、`2015`、`1014`、
`1087`、`1081`、`2131`、`2132`、`2134`、`1033`、`1073`、`301`、`1074`

この件数は固定sourceの観測結果であり、将来のKonomiTVへ流用する仕様値ではない。再抽出結果が変わる場合は、
path、symbol、呼び出し元、利用者への影響を調べ、本仕様を更新してから別の固定版へ適用する。

固定sourceに定義がある`201`、`202`、`205`、`208`、`2`、`3`、`1030`、`1016`、`1018`、
`1019`、`1066`、`1299`、`2019`、`2020`、`2024`、`1053`、`1061`、`1043`、`2141`、
`2142`、`2144`、`2012`は、登録済みAPI、画面、起動、定期、終了処理からの呼び出し元を確認できない。
KonomiTV v0.14.1対応だけを理由に実装しない。

### `K5-002` Source provenance

Source調査にはrepository、tag、完全SHA、path、symbol、探索方法を残す。外部scriptを実行せず、外部code、
fixture、通信内容を製品へコピーしない。Source確認は実通信、画面、実放送の成功を証明しない。

## 証拠区分

### `K5-003` Evidence states

公開互換表とplanning readbackは、操作ごとに次の証拠を分ける。

| 状態 | 意味 |
|---|---|
| `SOURCE-VERIFIED` | 固定sourceの呼び出し元から送信まで静的に追跡した |
| `CONTRACT-VERIFIED` | 同じ製品commitのunit、contract、SQLite統合試験が成功した |
| `BLACK-BOX-VERIFIED` | 製品processへprotocolまたはHTTP境界から要求し、応答と後始末を確認した |
| `LAB-VERIFIED` | 来歴を固定したSazanami、KonomiTV、providerを隔離環境で接続して確認した |
| `SCREEN-VERIFIED` | 固定KonomiTVの画面から開始し、表示と後続状態を確認した |
| `NOT RUN` / `UNVERIFIED` | 未実施または証拠が不足している。成功として集計しない |

`CONTRACT-VERIFIED`を`SCREEN-VERIFIED`の代用にしない。画面を持たない公開APIは、画面証拠を要求せず、
固定sourceの登録済みAPIへ同じHTTP要求を行った`LAB-VERIFIED`を最終証拠とする。

## 全操作マトリクス

### `K5-004` Operation groups

| 操作群 | 固定KonomiTVの入口 | CtrlCmd／共有境界 | v0.5.0の完了条件 |
|---|---|---|---|
| 起動・接続設定 | 起動、`/settings/server` | 2200 | 正常起動、設定検証、調査済みの失敗表示 |
| 放送局・番組表更新 | 起動、定期更新、手動更新 | 1060、1021、1029 | 放送局と番組表を更新し、重複timerやHTTP 5xxを残さない |
| 録画preset・容量・ロゴ | 予約設定、チャンネル表示 | 2060 | presetと容量を取得し、ロゴ欠落時は固定fallbackを返す |
| 検索・番組詳細 | 検索画面、番組詳細 | 1060、1025、1029 | 全検索条件class、上限、詳細metadataを確認する |
| 予約の閲覧・追加 | 番組表、検索、視聴画面 | 2011、1029、2013 | 重複せず追加し、一覧と詳細へ同じ値を返す |
| 予約の変更 | 予約詳細 | 2011、2015 | 有効状態、優先度、余白、保存先、名称、字幕・データ、録画後設定を読戻す |
| 予約削除・録画中停止 | 予約詳細、視聴画面 | 2011、1014、1087、1081 | 開始前削除と録画中停止を分け、部分録画を安全に確定してresourceを解放する |
| キーワード自動予約 | 登録済み公開HTTP API | 1060、2131、2132、2134、1033 | CRUD、評価、予約生成、再送、上限、試験条件の後始末を確認する |
| ライブ視聴 | 視聴画面 | 1073、301、1074 | 選局、継続再生、切断、idle、上限、終了後のresource解放を確認する |
| 録画済み閲覧・再生 | 録画一覧、検索、詳細、player | 完成録画と共有folder | 新しい90秒以上の録画を一覧化し、通常取得、単一Range、再起動後読戻しを確認する |
| 録画済み再解析・削除 | 詳細、管理者削除 | KonomiTV `/api/videos`、共有folder、Sazanami周期整合 | 新しい使い捨て録画一件だけを再解析・削除し、DB、file、履歴を矛盾なく収束させる |
| 定期・終了処理 | 定期更新、server shutdown | 1060、1021、1029、1074 | 更新失敗後の再試行、二重実行防止、close、再起動を確認する |

### `K5-005` Search and metadata coverage

検索はkeyword、除外語、正規表現、大文字・小文字、channel、genre、曜日、時刻、番組長、無料・有料、
あいまい検索を含む。番組詳細は、固定KonomiTVが読む開始時刻、長さ、番組名、説明、無料状態、genre、
映像、音声、イベント共有、イベントリレーを、providerに事実がある範囲で同じsnapshotから返す。

### `K5-006` Reservation and automatic-reservation boundary

予約の追加、変更、削除はKonomiTV画面から確認する。キーワード自動予約の2131、2132、2134、1033は
KonomiTV v0.14.1のサーバーに公開HTTP APIとして登録されているが、同梱画面からの呼び出し元はない。
公開互換表では「KonomiTV公開API」と表示し、「画面対応」と表示しない。

自動予約APIの完了には、条件の一覧、追加、更新、削除だけでなく、条件に合う予約の一度だけの生成と、
試験条件の後始末を含める。利用者の既存条件を試験へ流用しない。

### `K5-007` Live boundary

`always_receive_tv_from_mirakurun: false`のEDCB経路では1073、301、1074を一続きで確認する。301は成功header後も
MPEG-TSを同じ接続で流す長時間streamである。接続数、buffer、queue、期限、idle、closeの既存上限を維持し、
停止後にlease、connection、timerを残さない。

`always_receive_tv_from_mirakurun: true`のMirakurun直結は利用可能な別構成だが、SazanamiのCtrlCmdライブ成功の
証拠に数えない。

### `K5-008` Completed-recording boundary

KonomiTV v0.14.1はCtrlCmdの録画済み情報ではなく、設定した`recorded_folders`を走査する。新規録画は60秒未満を
除外する固定client条件を避け、90秒以上の使い捨て録画を一件だけ作る。既存録画の一覧表示や再生成功で代用しない。

録画fileの通常取得と単一Range取得は、同じ完成fileを返す。部分file、未確定、失敗、復旧途中、path外、symlink、
所有者・mode・link数が不正なfileを完成録画として公開しない。

### `K5-009` Deletion and reconciliation boundary

削除はAccepted ADR-0064とKonomiTV録画削除・周期整合仕様v1へ従う。Base Composeのread-only既定を維持し、
削除overrideだけでKonomiTVを信頼済み共同所有者にする。所有lockはread-onlyで保護する。

試験は新しく作った使い捨て録画一件だけを対象にする。KonomiTV DB、thumbnail、補助file、録画fileの削除と、
Sazanami履歴が`MISSING`へ収束することを個別に読み戻す。既存録画を削除試験へ使わない。

## 製品境界

### `K5-010` Bounds and failure behavior

CtrlCmd frame、HTTP body、検索結果、file copy、Range、録画stream、ライブstream、database query、timer、cacheは、
既存の明示上限を保つ。入力不正、上限超過、期限切れ、切断、provider失敗はpanic、無制限retry、不意の切断にせず、
固定した失敗応答または既存のHTTP error classにする。

### `K5-011` Privacy and non-goals

通常log、metric label、公開文書、CI artifactへ、接続先、認証情報、利用者名、番組名、録画名、予約・録画ID、
相対・絶対path、raw要求・応答、TSを出さない。実験記録は操作群、版、完全SHA、HTTP class、固定reason、結果だけを残す。

Komorebi、Android TV、TvCast、HLS、cast、画質変換、EDCB全機能、固定sourceから呼ばれないコマンド、別版KonomiTV、
BS4K、直接チューナー制御、WebUIのLAN公開・認証は本仕様の対象外とする。

### `K5-012` Release gate

全12操作群を同じFinal product implementation commitへ結び付ける。各行はsource、contract、black-box、LAB、画面の
うち必要な証拠を持ち、未実施は`NOT RUN`のまま残す。Sazanami起因の未対応応答、HTTP 5xx、バックエンドエラー、
resource残留が一行でも残る間はv0.5.0のrelease-prepへ進まない。

版番号、変更履歴、tag、GitHub Release、asset、checksum、OCI imageは、専用release-prep handoffで同じrelease commitへ
固定する。v0.5.0を公開しても、Komorebi全対応、72時間試験、v1.0.0の条件は満たしたことにならない。

## 必須テスト

- 固定sourceのtag、完全SHA、全到達call、除外call、path、symbol、探索方法を読み戻す。
- 各CtrlCmdの正常、入力不正、上限、切断、期限、provider失敗、後始末を同じ製品commitで試す。
- 検索の全条件class、番組詳細、予約CRUD、録画中停止、自動予約CRUDと評価を試す。
- ライブの選局、継続、client切断、idle、上限、close、shutdown後のresource数を試す。
- 新しい90秒以上の録画について、完成、scan、一覧、通常取得、単一Range、再起動後読戻しを試す。
- 同じ使い捨て録画について、再解析、サムネイル再生成、管理者削除、KonomiTV DB、file、Sazanami履歴を試す。
- 起動、定期更新、手動更新、更新失敗後の再試行、終了、再起動を試す。
- Full、shuffle、race、vet、module verification、既知脆弱性検査、主要四環境build、Hosted CIを同じfinal commitで成功させる。

## 完了記録

各操作群は、固定KonomiTV source、製品commit、実験環境のruntime provenance、実施した入口、期待値、結果、
未実施、既知制限を公開互換表とplanning handoffへ戻す。利用者の操作を完了していない途中の応答成功は、
対応済みへ格上げしない。
