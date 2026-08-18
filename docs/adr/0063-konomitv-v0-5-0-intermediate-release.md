# ADR-0063: KonomiTV全到達操作をv0.5.0の中間リリース条件にする

- Status: Accepted
- Date: 2026-08-18
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related: ADR-0026、ADR-0053、Plan 0040、Plan 0058、Plan 0079、Handoff 0055
- Supersedes: None
- Superseded by: None

## 背景

Accepted ADR-0026は、v1.0で固定KonomiTVと固定Komorebiから到達する全操作を確認する方針を採用した。
これが最終目標である。Accepted ADR-0053は、対象クライアントの操作をv0.9.0までに終え、
72時間試験を経てv1.0.0に進む版番号方針を採用した。

その長期方針は維持する。ただし、KonomiTVの予約、録画、録画済み一覧、再生、ライブの実環境確認には、同一製品commitでまだ閉じていない行がある。KomorebiのAndroid TV、HLS、端末codec、直接HTTPを同時に進めると、KonomiTVの不足を切り分けにくい。

利用者が次に必要とする中間目標は、KonomiTV v0.14.1をEDCBバックエンドとして普通に使えることだと判断する。Komorebiは次の機能版に回すが、実装済み機能やv1.0の長期目標を取り消さない。

## 判断

v0.5.0を、KonomiTV v0.14.1の固定commit `0a32188274b81c1e7bed642474b208bd2a543a6b`に対する中間機能版とする。対象は、同commitの画面、登録済みHTTP API、起動処理、定期処理、終了処理からEDCBバックエンドへ実際に到達する全通信である。Sazanamiが作る録画fileをKonomiTVが共有folderから一覧化、解析、再生、削除する操作も、Sazanamiの保存形式と権限に依存するため対象へ含める。

対象callは、固定sourceから再実行したinventoryで決める。2026-08-07のinventoryではCtrlCmd 2200、1060、1021、1029、2060、1025、2011、2013、2015、1014、1087、1081、2131、2132、2134、1033、1073、301、1074が到達した。これは現時点の観測結果であり、19件という固定数を仕様にはしない。

v0.5.0で各操作を対応済みと表示するには、次をすべて満たす。

1. 固定sourceのtag、完全SHA、path、symbol、呼び出し元を確認する。
2. 正常、入力不正、上限超過、切断、期限切れ、後始末の契約testが同じ最終製品commitで成功する。
3. KonomiTVの画面、公開HTTP API、起動・定期処理の該当操作を実環境で確認し、Sazanami起因の未対応応答、HTTP 5xx、バックエンドエラーがない。
4. 新規録画について、完成、KonomiTVの録画済み一覧への反映、一覧からの選択、通常取得、Range再生、再起動後の読戻しを一続きで確認する。
5. 使い捨ての試験録画について、管理者による削除、KonomiTV側DBとfileの削除、Sazanami履歴の整合または明示的な欠落表示までを確認する。既存録画や利用者の録画は削除試験に使わない。
6. Sazanami、KonomiTV、Mirakurun／mirakc、配布物の版と完全SHAまたは実行時来歴、未実施項目、既知の制限を公開互換表へ記録する。

KonomiTV同梱画面に導線がないキーワード自動予約条件は、登録済みの公開HTTP APIとして別行で確認する。反対に、録画済み一覧はCtrlCmd録画済み情報の有無ではなく、KonomiTVが監視folderを走査して新規録画を表示・再生できることで確認する。固定sourceの`VideoDeleteAPI`は録画fileを直接削除するため、read-only mountのまま対応済みとは扱わない。書込み権限、削除対象、Sazanami履歴との整合は別のAccepted判断とhandoffで固定する。

Komorebi stable 1.0.0、Komorebi 1.1.0-beta6、Android TV、TvCast、view、HLS、cast、画質変換の全対応はv0.5.0のrelease条件から外す。これらは次の機能版の候補であり、v0.5.0で未対応または未検証と明示する。KonomiTVのためにEDCB全体を実装すること、KonomiTVの固定sourceで呼ばれない定義だけのコマンドを追加することも対象外とする。

v0.5.0のtag、GitHub Release、配布物は、KonomiTVの全行が同一製品commitで上記の証拠を満たし、専用release-prep handoffをreviewした後に作る。現時点でv0.5.0のtagやReleaseを作成してはならない。

## ADR-0026・ADR-0053との関係

本ADRは、Accepted ADR-0026またはADR-0053の本文、status、履歴を編集しない。

- ADR-0026が定めたv1.0の「KonomiTVとKomorebiの全到達操作」という長期目標は維持する。
- ADR-0053が定めたv0.9.0の機能完成版とv1.0.0の72時間試験後の安定版も維持する。
- v0.5.0は、その途中にKonomiTVだけの完了条件を置く新しい機能版である。Komorebiの確認を放棄する判断ではない。
- Project ownerは2026-08-18に、KonomiTV全操作をv0.5.0の中間目標とし、Komorebi全対応を次の機能版へ移す方針を明示した。この指示を本ADRの採用根拠とする。

## 影響

- KonomiTV利用者には、対応範囲をコマンド番号ではなく、画面、API、定期処理、録画後の再生までで示せる。
- 開発と実環境検証は、KonomiTVの録画一覧・再生やライブの問題を先に閉じられる。
- KonomiTVからの録画削除はv0.5.0の確認対象になるが、既存のread-only録画mountを根拠なくread-writeへ変えない。削除境界を別判断で確定するまで未対応のまま扱う。
- Komorebiの既存実装と調査証拠は残るが、v0.5.0の適合主張には使えない。
- v0.5.0を出しても、v0.9.0／v1.0.0の条件が自動的に満たされるわけではない。

## 採用しなかった案

### Komorebiを含めたままv0.5.0を出す

採用しない。Android TVとHLSの実環境要因をKonomiTVの操作完了と混ぜると、中間版の境界が不明確になる。

### CtrlCmdが19種類実装済みならv0.5.0とする

採用しない。file relay、録画完了、folder scan、Range再生、長時間streamまで成功しなければ、利用者の操作は終わらない。

### KonomiTVの全versionまたはEDCBの全機能を対象にする

採用しない。固定sourceから実際に到達する操作だけを母集団にし、未検証の広い互換性を主張しない。

## 検証

- Plan 0079のsource再抽出、操作matrix、契約test、black-box、実環境、release、文書の完了条件を満たす。
- `server/app/routers/VideosRouter.py::VideoDeleteAPI`、`client/src/services/Videos.ts::deleteVideo`、`client/src/components/Videos/RecordedProgram.vue::deleteVideo`を固定SHAで読み、管理者の録画削除が到達可能な操作であることを確認する。
- KonomiTVの画面baselineだけでなく、最終製品commitと配布物来歴を固定した実環境結果を残す。
- source、CI、black-box、実環境、長時間試験を別々に記録する。
- v0.5.0のtag、Release、assetが事前に存在しないこと、release時には最終commitだけを指すことを確認する。

## 見直す条件

- 固定KonomiTV source inventoryに、今回のmatrixへない到達callが見つかった。
- KonomiTVを先に完了しても、Komorebiの機能を同じ製品commitで確認しなければ利用者の主要操作が成立しないと分かった。
- 次のKonomiTV固定versionを採用し、source呼び出し経路が変わった。
- v0.5.0の範囲で新しい公開境界、認証、直接チューナー制御、BS4Kを求める判断が出た。
