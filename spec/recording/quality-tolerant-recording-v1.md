# 受信品質が乱れた録画の継続・確定仕様 v1

- Status: Accepted
- Proposed date: 2026-10-04
- Accepted date: 2026-10-04
- Decision: [ADR-0077](../../docs/adr/0077-recording-quality-continuation.md)
- Product base: `b36891cb4016a7d0a7242c3d6b65a380457d1ea1`
- Requirements: `QRC-001`〜`QRC-012`

Project ownerは2026-10-04に最終仕様とhandoffの正式採用を承認した。製品実装にはhandoffのgateと固定sourceからの文書同期、製品側実装計画の確認が必要である。明記した差分以外は固定base内のAccepted契約を維持する。

## QRC-001: 受信品質だけでは録画を停止しない

CCの飛び・重複、TEI、PSIの連番・CRC・構造異常、packet同期乱れ、PMTの更新失敗、read末尾の不完全packet／PSIを品質情報へ集約する。同じstreamを読み続け、保存可能な完全packetを予定終了まで保存する。TS内容の補完、CCの書換え、欠損量の推定は行わない。

DB失敗、file作成・write・sync・close失敗、容量不足、安全でない保存先、明示停止、process取消し、予定終了・絶対上限は品質警告へ丸めない。

## QRC-002: packet組立てと同期回復を有界にする

録画専用の逐次処理を使う。共通 `mpegts.ParsePacket`、`PSICollector`、probe、HLSのstrict既定は維持する。

TEIは元packetのheaderから観測する。TEI付きpacketの構造確認は、録画専用adapter内の固定188-byte scratchへコピーし、そのscratchのTEI bitだけを外して共通parserへ渡す。この観測のために元packetのTEIを変更して出力しない。正常選別時の既存PMT再生成は維持する。TEI以外の構造検査は維持し、TEI付きPAT／PMTをPSI collectorへ渡さない。

- 出力は188 bytesの倍数とし、任意read分割で内容を変えない。
- 同期中は完全なpacketを逐次処理する。構造不正ならそのpacketを捨て、固定イベントを数える。
- 同期を失ったら、188 bytes間隔の五つの同期候補と読めるheaderを確認して復帰する。探索保持は64 KiB以下とし、先へ進んだbyteを再保存しない。
- 候補なしでも入力を読み続ける。古い探索byteは破棄し、bufferを増やさない。予定終了・取消し・read idle期限は維持する。
- TEI付きでも構造を読める非PAT／PMT packetは原byteを保存できる。選別中のTEI付きPAT／PMTは出力・設定へ採用せずcollectorをリセットする。QRC-003で元stream保存へ切り替えた後も、設定としては採用しない。
- 同期回復後はPSI組立てとCC期待値をリセットする。推測したpacket、壊れたPCR値、PSIを正常な設定として生成しない。

同期探索で捨てたbyte数、構造不正で捨てたpacketイベント、端数を区別する。探索中に最後まで同期できない場合も、既に保存した正常なpacketを捨てない。

## QRC-003: 選別不能時は明示的に元stream保存へ切り替える

正常時は字幕・データ放送の既存選別を維持する。字幕・データ放送とも含む設定でも、packet境界と有界な品質観測だけは共通で行う。

選別が必要な場合、初期PAT／PMTは最大1 MiBまで保持する。有効PMTが得られれば従来の選別へ進み、初期PMT中の別PID packetも一度だけ保存する。CRC、pointer、連番などで組立てが壊れたらcollectorをリセットし、次のPUSIから再取得する。完全同一のPSI packet再送は二重にsectionへ追加しない。

次のいずれかで選別保証を失った場合、元サービスstreamの保存へ一度だけ切り替える。

1. 有効PAT／PMTを得ないまま初期保持上限へ達した。
2. 採用済みPAT／PMTの更新が壊れ、または対象PMT PIDが変わり、旧・新PIDを安全に選別できない。
3. 有効な選別情報がsection／PMT entryの上限へ収まらない。

保持済みpacketのうち保存可能なものは元byteで一度だけ出力する。上限超過で全録画を止めたり、bufferを拡張したりしない。切替はsegmentごとに行い、同じsegmentの残りと再接続後も元stream保存を維持する。片方の切替を別segmentへ伝播させない。filterの不完全な状態を接続間で持ち越さない。

`selection_unverified=true`と `DEGRADED`を残す。除外指定した字幕・データ放送が混入し得る。保存済み予約設定は変えず、選別成功とは表示しない。元stream保存では壊れたPSIを選別設定として採用しないが、入力の制御packetを保持する場合がある。再生品質は保証しない。

予定終了・通信切断・利用者停止が初期保持中に来た場合も、新しいreadはせず、保持済みの保存可能な完全packetを元byteで出力し、選別未確認を残す。file／DB失敗、process取消しで停止した場合は保存や確定を無理に続けない。

## QRC-004: 通信終了と品質の優先順位を固定する

1. 利用者停止／親取消しを既存DBの要求と照合する。品質で上書きせず、再接続しない。
2. 更新済み予定終了へ達した場合は新しいread・接続を始めず、保存可能な完全packetを処理して閉じる。
3. 予定終了前のEOF、timeout、peer切断、0 byte進行をproviderの既存通信分類で扱う。
4. `Finish()`による端数・未完PSIは品質へ加え、通信reason・retryability・予定終了到達を上書きしない。

停止要求と通常確定の競合は既存のDB比較更新で確定する。利用者停止が先なら既存 `USER_REQUESTED_STOP`、`FINALIZING`が先なら予定された確定を継続する。通信reasonだけでDB失敗や取消しを上書きしない。

最終readで親contextの取消しと予定終了が重なった場合も、確定予定を保存する前に親取消しとDB停止要求を照合する。明示停止要求がなければ `PROCESS_SHUTDOWN`として保全し、通常確定・再生公開しない。任意のcontext取消しを利用者停止へ変換しない。executor所有の予定終了・絶対上限によるstreamContext期限と、親contextの終了は別に判定する。

## QRC-005: 既存の再接続上限を維持する

初回を含め最大四接続、追加は三回、待機は1／2／4秒、開始時に残り60秒以上、同時一接続とする。元leaseをcancel・closeした後だけ待機・接続する。一時品質劣化だけで再接続回数を消費しない。

同じattempt、segment、部分fileへ累積出力する。各接続の端数・未完PSIは持ち越さない。品質件数と一度切り替えた保存方式はsegment内で累積する。再起動後には追記しない。

## QRC-006: 保存状態と品質状態を分ける

| 保存結果 | State / Reason | 品質 |
|---|---|---|
| 予定終了、安全な確定、一接続 | `SUCCEEDED/COMPLETED` | 観測結果に応じる |
| 予定終了、安全な確定、追加接続あり | `SUCCEEDED/COMPLETED_AFTER_RECONNECT` | `DEGRADED` |
| 予定終了前に許可通信reasonで終了、安全な確定 | `PARTIAL`と元の通信reason | `DEGRADED` |
| 利用者停止、安全な確定 | `PARTIAL/USER_REQUESTED_STOP` | 観測結果に応じる |
| 有用byteなし、file／DB失敗、process中断 | 既存の該当状態・reason | 分かる範囲だけを残す |

通信partialの公開許可reasonは `STREAM_ENDED_EARLY`、`STREAM_TIMEOUT`、`STREAM_RECONNECT_EXHAUSTED`の三種類とする。接続拒否・不正応答なども含む `STREAM_UNAVAILABLE`は対象外とし、保存済みpartialは保全するが公開しない。これ以外を推測で公開しない。`SUCCEEDED`は保存処理の完了を表し、放送内容の無欠損を保証しない。

providerのpeer切断や0 byte進行も、既存分類が `STREAM_UNAVAILABLE`で終わる場合は対象外となる。追加試行を使い切って `STREAM_RECONNECT_EXHAUSTED`になった場合との違いをテストする。保存の保全とクライアントへの公開を混同しない。

品質は `UNKNOWN`（観測なし・未完了）、`NO_ISSUES_OBSERVED`（有界な観測範囲で異常を見なかった）、`DEGRADED`（警告・fallback・切断あり）の三値とする。「異常を見なかった」をTSの完全検査済みと説明しない。

## QRC-007: schema 15へ固定の品質summaryを保存する

`recording_segments`へ次の固定列を追加する。イベント履歴table、任意JSON、raw error本文、TS、PID別履歴は保存しない。

| 列 | 意味・上限 |
|---|---|
| `quality_status` | 上記三値。旧行の既定は `UNKNOWN` |
| `selection_unverified`、`observation_limited`、`quality_counters_saturated` | boolean。選別保証喪失、追跡外あり、数値上限到達を区別 |
| `cc_gap_events`、`cc_duplicate_events` | 観測対象PIDのイベント数。欠落packet数ではない |
| `tei_packets`、`malformed_packet_events` | 読めるTEI packet数、構造不正イベント数 |
| `psi_continuity_events`、`psi_crc_events`、`psi_structure_events`、`psi_limit_events` | PSIの下位失敗分類 |
| `sync_loss_events`、`sync_recovered_events`、`sync_discarded_bytes` | 同期乱れと回復・破棄の区別 |
| `trailing_incomplete_bytes`、`unfinished_psi_events` | 接続終了ごとの端数・未完PSIの累積 |
| `fallback_events`、`reconnect_count` | 保存方式切替0〜1回、初回後のOpenStream試行0〜3回。失敗した追加試行も含む |

イベント数・byte数は非負整数で2,147,483,647へ飽和させ、到達時はsaturatedを立てる。飽和で録画を止めない。reconnect/fallbackだけは小さい個別範囲を守る。

SQL列はすべてNOT NULLとする。`quality_status`はTEXT、DEFAULT `UNKNOWN`、三値のCHECKを持つ。三つのboolean列はINTEGER、DEFAULT 0、0／1だけのCHECKを持つ。件数・byte数はINTEGER、DEFAULT 0、0〜2,147,483,647のCHECKを持ち、`fallback_events`だけ0〜1、`reconnect_count`だけ0〜3へ狭める。domain側も同じ値域を検証し、不正なsummaryをDBへ渡さない。

固定quality型をfilter → streamCopyResult → progress／確定予定／終端Store → segment／history／recovery → Native RESTへ通す。SQLの表現型をdomainへ漏らさない。復旧・周期照合・HTTP・CtrlCmdの経路でsummaryや公開reasonの条件を落とさない。

CCの追跡は最大64 PIDとする。PAT、対象PMTを優先し、残りは有効PMTのPIDを番号順に選ぶ。直前の完全な188-byte packetと同一の再送だけをduplicateへ数え、同CCで内容が違う場合はgapイベントへ一件数える。discontinuity flagでは期待値をリセットし、それだけでgapにしない。payloadなしのpacketを期待値の前進へ使わない。追跡外があればlimitedを立てる。全PID数やCCの差から欠落量を推測しない。

新しい録画のclaimで観測summaryを初期化し、既存の進捗保存（5秒以下）、確定予定、終端の短いtransactionへ最新値を渡す。DB失敗を無視した別の非同期書込みは追加しない。最後の永続化より後のprocess crashでは件数が下限になり得るため、確定予定のない中断から復旧する場合は、既知警告の `DEGRADED`を維持し、それ以外を `UNKNOWN`にする。保存済み件数は消さない。terminalまたは確定予定が揃うsummaryは再起動後も維持する。

## QRC-008: 更新と旧録画の扱い

schema 14の事前backup、別領域へのrestore確認、integrity／FK／schemaの読戻し後だけ15へ明示migrationする。通常serveで自動migrationしない。確定予定reasonのCHECK・trigger・domain検証をQRC-006の許可組合せへ限定拡張し、利用者停止要求の既存制約は維持する。

14→15の入口では既知のschema 14の列・CHECK・trigger形状を確認する。version番号とmigration履歴が一致するだけで、形状が異なるDBを受け入れない。

旧行のqualityはUNKNOWN、件数は未観測の既定値とする。UNKNOWNの0は「異常0件」ではなく「未測定」として読む。旧attemptのstate、reason、byte、token、公開flag、pathは変更しない。旧partialを公開候補へ変換しない。未来schema、drift、途中失敗、再適用、backup失敗は既存の安全な失敗を維持する。

旧binaryへ戻すには更新前backupの復元が必要で、downgradeを自動実行しない。更新後に増えた予約・履歴は古いbackupに存在しないため、保全と切戻し判断は別の運用作業とする。

## QRC-009: 通信partialも既存の安全な確定経路を使う

公開前に、新しい通信partialのメインfileが188 bytes以上かつ188の倍数で、UTCの実開始・終了と1秒以上の実時間が既存の再生条件を満たすことを確認する。`Sync`、`Close`、最終進捗と品質、DBの `PARTIAL/通信reason`という確定予定・tokenを保存してから、既存の上書き禁止renameによる公開とdirectory同期、DB終端へ進む。既に公開済みの旧録画へ新しい品質検査を遡及適用しない。

file同期、公開、directory同期の三証跡、`FINALIZED/FINAL`、DBと実fileのbyte一致が揃うまで再生公開しない。owner、mode、symlink、追加link、安全な相対path、完成名衝突の検査を維持する。いずれか失敗すれば正常／再生可能へ丸めない。

再起動はDBの確定予定とtokenがあり、既存file検査を通る `FINALIZING`だけを同じplanned state/reasonへ収束させる。旧terminal partialから新しい計画を作らない。周期reconcileも新規公開証跡の揃う通信partialのavailabilityだけを扱う。

ワンセグ品質はordinal 1へ独立して残す。主録画が通常・利用者停止・許可された通信partialとして安全に確定できる場合だけ、既存のbyte・同期・整合条件を満たす補助fileを確定できる。補助失敗で主録画を取消さず、補助だけの新しい再生URL・二件目の録画履歴は追加しない。後処理・電源動作の既存許可条件は広げない。

## QRC-010: 履歴・再生公開をそろえる

Native REST、2017／2024、resolver、原画質再生、HTTP GET／HEAD／Rangeは同じ安全な確定判定を使う。Native RESTへQRC-007の固定summaryを追加する。既存fieldの意味を変えない。

CtrlCmdのwire形式、調査済みstatus値、drop／scramble fieldは変更せず、推定drop数を送らない。固定クライアントが品質summaryを表示するとは宣言しない。KonomiTVは完成拡張子の走査、Komorebiは対応する録画一覧・resolverからの発見・再生を確認する。188 bytes以上という閾値はfile公開の条件であり、デコード成功や画面表示の保証ではない。

## QRC-011: 観測は固定値と集約だけにする

通常ログは下位品質の固定分類、継続／切替／再接続／終了の区別、集約件数だけとし、生のerror、TS、番組名、局名、録画番号、内部ID、PID別label、接続先、相対・絶対pathを追加しない。

一segmentあたり開始summary一回、品質変化時の集約は30秒に一回以下、接続終了時は最大四回、最終summary一回までとする。分類文字列は64 bytes以下の固定値、ログ一行は2 KiB以下。packetごとのwarnやキューは追加しない。品質が悪い場合も通常のread／期限／停止確認を続ける。

## QRC-012: 回帰と対象外

任意read分割、CC欠損／同一再送／同CC別内容、TEI、CRC、PMT更新、同期復帰、EOF／timeout／期限、飽和・上限、正常／劣化／partial、取消し、file／DB失敗、確定中断・再起動、旧partial非公開、REST／CtrlCmd／Rangeを合成入力で確認する。共有parserの既存probe／HLS回帰も維持する。

実機は独立した証拠とする。受信設備の改善、TS修復、クライアントUI改変、過去録画の自動救済、保持・削除、新依存・daemon・無制限再接続は対象外。時間指定耐久試験や利用者向けchecksum照合を追加の必須工程にしない。
