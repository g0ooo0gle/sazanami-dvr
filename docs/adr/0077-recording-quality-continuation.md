# ADR-0077: 受信品質と録画の保存結果を分け、乱れたTSでも保存を続ける

- Status: Accepted
- Proposed date: 2026-10-04
- Accepted date: 2026-10-04
- Decision owner: Project owner
- Review: 2026-10-04にProject ownerが、最終仕様とhandoffに記載したDB更新・受信エラー処理・途中録画の公開条件まで含めて正式採用を「いいよ」と承認した。TEI観測、取消し競合、schema制約は承認対象の最終仕様に含まれる。製品実装計画の確認は別工程とする。
- Product base: `b36891cb4016a7d0a7242c3d6b65a380457d1ea1`（v1.3.6）
- Accepted spec: [品質継続仕様v1](../../spec/recording/quality-tolerant-recording-v1.md)
- Related planning records (not copied): `docs/plans/0103-recording-quality-continuation.md`、`handoffs/0077-recording-quality-continuation.md`
- Partial replacements: ADR-0042、ADR-0033、ADR-0037、ADR-0064、ADR-0055の下記限定範囲

## 採用した方針

TSの乱れは停止理由ではなく品質警告とし、予定終了まで保存する。字幕・データ放送の選別を安全に続けられなくなった場合は、その録画の残りを元サービスストリームの保存へ切り替える。保存できた通信障害の部分録画も、fileとDBの確定条件を満たした場合だけ再生公開する。このためschema 15へ明示更新し、品質情報を残す。

選別fallbackには、除外指定した字幕・データ放送が残り得るという代償がある。予約設定を書き換えたり、選別成功と表示したりしない。今回の「厳密な受信品質判定より録画継続を優先する」という利用者方針に合わせ、この代償を明示して採用した。

## 根拠

本番担当の読取り報告では、予約開始後に `STREAM_FORMAT_INVALID` で途中停止し、有用と考えられる部分TSがクライアントから見えなかった。停止packetが残っているとは限らず、下位原因は特定できない。同時視聴、チューナー不足、受信設備を原因と断定しない。

固定製品baseのSOURCEでは、選別filterのpacket／PSIエラーが即時停止へ伝わる。またEOF、timeout、予定終了の分類より先に `Finish()` が未完tail／PSIを形式エラーへ変える経路がある。映像・音声PIDのCC不連続は現行でもそのまま保存されるため、「すべてのCCの飛びで止まる」という説明は誤りである。

部分録画の公開は利用者停止だけに限定され、schema 7の確定予定reasonも正常終了二種類と利用者停止だけを許す。品質を残すfieldは現行DB・履歴・RESTにない。これは単なる検査削除ではなく、選別・確定・読取り契約の変更である。

## 比較した案

| 案 | 保存優先 | 選別設定 | 変更量と制限 |
|---|---|---|---|
| 有効PMTまで有界に待ち、得られなければ保存しない | 低い | 厳密に維持 | 小さいが、受信が悪いと録画全体を失う |
| 有界な回復と、保証を失った時の明示fallback | 高い | 不履行の可能性を明示 | 推奨。既存選別と元stream保存を再利用できる |
| 常にすべての検査を外す | 高い | 保証できない | 不採用候補。file・DBの保護まで混ざり、原因も分からなくなる |

品質をログだけへ残す案も比較した。DB更新は減るが、再起動後の履歴と継続的な調査に不足するため推奨しない。既存reasonへJSONを埋める案、新しい録画状態機械、汎用イベント履歴は追加しない。

## 決定

### 同じ接続内の回復と、通信再接続を分ける

録画専用の有界packet組立て・品質観測で、同期回復、TEI、CC、PSI失敗を集約する。共通のstrict parser、初期設定probe、HLSの既定挙動は変えない。構造を読めないpacketと同期探索中のbyteは捨て、補完しない。構造を読める非制御packetはTEI付きでも元のbyteを保存できる。壊れたPSIを選別設定として採用しない。

選別は正常時の挙動を維持する。初期PAT／PMT待ちは最大1 MiB、sectionは1,024 bytes、PMT entryは64件とする。PSI破損時はcollectorを捨てて次のsection開始から取り直す。初期待ちの上限、または選別更新の破損で保証を失った場合は、保存できるpacketを元のサービスストリームとして保存する方式へ一度だけ切り替える。同じ録画中と再接続後は選別へ戻さず、`selection_unverified`を残す。

`Finish()`は端数・未完PSIを品質情報へ返し、EOF／timeout／peer切断を上書きしない。予定終了時は完全なpacketを保全して確定へ進む。通信再接続は追加3回、1／2／4秒、残り60秒以上、同時一接続という既存契約を維持する。

### 保存結果と品質を別に残す

予定終了に達し、既存の安全な確定処理を通った録画は、従来の `SUCCEEDED/COMPLETED` または `COMPLETED_AFTER_RECONNECT`とする。これは保存処理の完了であり、放送内容の無欠損を意味しない。品質は別の固定summaryとして `UNKNOWN`、`NO_ISSUES_OBSERVED`、`DEGRADED`を持つ。

schema 15で `recording_segments` に固定の品質列を追加し、既存の進捗・確定transactionへ集約値を保存する。旧行は `UNKNOWN`とし、過去のTSを読み直さない。CCから欠落packet数を推測せず、観測イベント件数だけを記録する。

### 安全に確定した通信部分録画を公開する

`PARTIAL`の許可reasonへ `STREAM_ENDED_EARLY`、`STREAM_TIMEOUT`、`STREAM_RECONNECT_EXHAUSTED`の三種類だけを追加する。接続拒否や不正応答も含む `STREAM_UNAVAILABLE`は公開対象にしない。予定終了前に止まった事実は維持する。188 bytes以上かつ188の倍数、妥当な実開始・終了、file同期・close、DBに保存した確定予定とtoken、上書き禁止公開、directory同期、DBとfileのbyte一致が必要である。

旧partial、`STREAM_FORMAT_INVALID`、process中断、file・DB失敗を後から自動公開しない。既に新しい確定予定を持つ `FINALIZING`の復旧だけを新規許可reasonへ拡張する。周期照合は公開証跡の揃った新規partialに限りavailabilityを扱い、状態・reason・byteを変えない。

Native RESTに品質summaryを追加し、既存HTTP Range、resolver、CtrlCmd 2017／2024の公開条件をそろえる。CtrlCmdの未調査status値やdrop推定値を追加しない。固定KonomiTV／Komorebiは発見・再生を検証対象とし、品質警告の独自画面は作らない。

## 置き換える範囲と維持する範囲

| 既存契約 | 採用後に置き換える部分 | 維持する部分 |
|---|---|---|
| ADR-0042／選別仕様v1 | 入力品質異常の即時停止、PMT不明時の保存禁止、端数での全体失敗 | 正常時の選別、設定往復、PSI採用条件、上限 |
| ADR-0028／再接続仕様v1 | Finishによる通信理由の上書きをしない。通信partialも条件付きで確定 | 回数・待機・残り時間、接続拒否分類、期限、再起動後append禁止 |
| ADR-0033／CRR-003・005・008 | 新規の安全確定済み通信partialを公開対象へ加える | 仮想URL、page・応答・配信上限、file安全検査 |
| ADR-0037／USTOP-008・010 | 確定予定と復旧の対象を限定reasonへ拡張 | 利用者停止の優先順位・DB保存・通知・取消し |
| ADR-0064／KDR-006・010 | 新規公開証跡のある通信partialを周期照合へ加える | 非破壊、availabilityだけの変更、page・run・間隔 |
| ADR-0055／OSG-017〜020 | 主録画の通信partialを公開できる場合の補助segment確定、品質の分離 | メイン先行、補助失敗の分離、二出力上限、ordinal 0だけの既存再生 |

元のAccepted文書は履歴として残す。製品の実装正本が置き換わるのは、本書と仕様を固定sourceから文書同期した後である。

## 安全性と更新

保存先、所有者、mode、symlink、link数、サイズ、DB整合性、ディスク・write・sync・close障害、明示停止、絶対期限は緩めない。新依存、daemon、packet単位ログ、無制限buffer・PID・再試行は追加しない。

schema 14の事前backupと復元確認後だけ明示migrationする。予定reasonのCHECKとtriggerは必要部分だけ拡張し、旧行の状態、reason、公開flag、tokenは変えない。旧binaryへの自動downgradeは行わない。

## 未確定事項と検証

本書と最終仕様、handoff内の対象baseとcopy manifestは人間レビュー済み。固定sourceと文書同期の完全SHAを読み戻し、製品側実装計画を確認するまで、コード実装開始とは扱わない。

合成TS／HTTP／SQLite／loopbackによる回復、終了境界、停止、各file／DB失敗、確定中断と復旧、旧partial非公開、Range、クライアント要求往復を必須とする。詳細はhandoffへ集約する。新方針の製品テストは未作成・未実施。実放送、NAS、実クライアント画面は `NOT RUN: hardware required`。
