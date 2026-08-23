# ADR-0067: KonomiTVバックエンドの実用導線を最終ゴールにする

- Status: Accepted
- Date: 2026-08-23
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related: ADR-0026、ADR-0053、ADR-0063、Plan 0083、Handoffs 0055、0059
- Supersedes: ADR-0026のKomorebi必須条件、ADR-0053のv0.9.0／v1.0.0必須工程、ADR-0063の全画面総当たりrelease gate
- Partially superseded by: ADR-0069（checksumを必須成果物にする部分と、時間指定耐久試験を将来へ残す部分）

## 背景

範囲を縮める。
余分な目標は足さない。
問題は範囲だった。
実装はすでにある。
確認も進んだ。

これまでの最終目標は、KonomiTVとKomorebiから到達する全操作を確認し、72時間試験を経て
v1.0.0を公開することだった。この目標は、KonomiTVをEDCB互換バックエンドで使いたいという
当面の用途に対して広すぎる。Komorebi、別版KonomiTV、固定clientが呼ばないEDCB command、
全検索条件の画面総当たり、厳しい長時間試験が同じreleaseを妨げていた。

製品mainには、固定KonomiTV v0.14.1が到達する19種類のCtrlCmd、予約、録画、ライブ、
録画済みfileの再生と削除に必要な機能が統合済みである。実験環境では、放送途中からの録画、
自然終了、通常取得、Range取得、再起動、再解析、サムネイル再生成、管理APIからの削除、
Sazanami履歴の欠落反映まで一続きで確認できた。

Project ownerは2026-08-23、「もっとシンプルな最終ゴールとして、コノミのバックエンド互換として
動けばいい」と明示した。この指示を採用判断とする。

## 判断

狙いは一つである。
KonomiTVで使える状態を作る。

最終ゴールを、固定KonomiTV v0.14.1 / commit
`0a32188274b81c1e7bed642474b208bd2a543a6b`のEDCB互換バックエンドとして、日常的な導線が
成立することへ限定する。最初の公開到達点はv0.5.0とする。

次の導線を同じ製品commitで確認できれば、機能実装は完了とする。

1. 起動し、放送局、番組表、番組検索、番組詳細、録画preset、容量、局ロゴを取得できる。
2. 予約の一覧、追加、変更、開始前取消し、放送途中からの開始、録画中停止、自然終了が動く。
3. ライブを開始し、MPEG-TSを受信し、切断後に接続とresourceを残さない。
4. 完成録画をKonomiTVが一覧化し、通常取得、Range取得、再起動後読戻し、再解析、
   サムネイル再生成を実行できる。
5. 管理者削除でKonomiTVのDB、thumbnail、録画fileを削除し、Sazanami履歴を
   `MISSING / FILE_MISSING`へ収束できる。
6. KonomiTVに画面導線がないキーワード自動予約は、公開HTTP APIでCRUD、予約生成、後始末が動く。
7. 専用release commitのCI、tag、GitHub Release、Linux amd64／arm64 archive、OCI image、
   binary revisionの来歴が一致する。

source、contract、black-box、実験環境の証拠は操作に応じて組み合わせる。固定clientから届く要求を
製品試験と実験環境で確認済みなら、全検索条件を一件ずつ画面操作することや、同じ経路を再度画面で
総当たりすることをrelease gateにしない。未実施と既知制限は公開互換表に残す。

## 対象外

- 固定KonomiTV v0.14.1から呼ばれないEDCB commandと、EDCB全体への互換宣言
- 別versionのKonomiTVへの互換宣言
- Komorebi、Android TV、TvCast、HLS、cast、画質変換、端末codecの完全対応
- 全検索条件、全画面、全失敗条件の実機総当たり
- 時間指定の耐久試験、厳密な長期resource測定、v0.9.0を経由すること
- BS4K、直接チューナー制御、WebUIのLAN公開・認証

これらの実装や既存証拠は削除しない。将来必要になった時点で、別の版とhandoffとして扱う。

## 既存判断との関係

- ADR-0026の固定source調査、上限、失敗応答、来歴、秘匿の原則は維持する。Komorebiを
  最終releaseの必須条件にする部分だけを置き換える。
- ADR-0053の版、tag、Release、assetを同じcommitへ固定する規則は維持する。v0.9.0と時間指定耐久試験を
  最終ゴールの必須工程にする部分だけを置き換える。
- ADR-0063と全操作マトリクス仕様v1は、source inventory、実装表、既知制限を追跡する資料として残す。
  全12操作群の未実施を残さないことと、全検索条件総当たりをv0.5.0のrelease gateにする部分は、
  本ADRと実用バックエンド完了仕様v1が置き換える。
- ADR-0064の録画削除・周期整合境界は変更しない。

## 影響

- 利用者へは「EDCB全互換」ではなく、「KonomiTV v0.14.1の日常導線に対応」と表示する。
- 未確認の細部を対応済みに引き上げず、KonomiTV側の制限と対象外を互換表に残せる。
- 機能追加を止め、製品版、文書、CI、配布物の一致へ作業を集中できる。
- v0.5.0は0.x系なので、既存release workflowに従いGitHub上ではprereleaseとする。

## 採用しなかった案

### 従来のv1.0.0条件を維持する

採用しない。KonomiTVの日常利用と無関係なKomorebi、時間指定の耐久試験、全画面総当たりが公開を妨げる。

### 19種類のCtrlCmdが応答すれば完了とする

採用しない。録画file、ライブstream、再起動、削除後整合まで通らなければ、利用者の操作は完了しない。

### 実験環境だけ確認し、配布物を作らない

採用しない。導入可能なtag、archive、OCI image、binary revisionまで同じcommitへ固定して初めて利用可能になる。

## 検証

- 実用バックエンド完了仕様v1の導線と、製品mainの実装・CI・実験結果を対応付ける。
- 公開互換表に固定client、確認済み導線、未実施、既知制限、対象外を記録する。
- release-prep差分へ新機能、schema変更、migration、依存追加を混ぜない。
- candidate CIと統合後main CIを成功させた後にだけtagを作る。
- 公開した二つのarchive、`OCI_IMAGE`、OCI digest、binary revisionを読み戻す。
