# 公開文書仕様 v1

- Status: Accepted
- Date: 2026-09-11
- Applies to: Sazanami DVR v1.2.0の公開文書
- Content baseline: v1.1.0の実装・公開内容（release commitは未確定）
- Decision: [`docs/adr/0071-public-documentation-information-architecture.md`](../../docs/adr/0071-public-documentation-information-architecture.md)
- Related: [Plan 0089](https://github.com/g0ooo0gle/sazanami-planning/blob/main/docs/plans/0089-v1-2-0-public-documentation-restructure.md)、
  ADR-0053、ADR-0069、
  [Handoff 0066](https://github.com/g0ooo0gle/sazanami-planning/blob/main/handoffs/0066-v1-2-0-public-documentation.md)
- Requirements: `DOC-001`～`DOC-014`
- Precedence: 本仕様は公開文書の導線だけを定める。v1.1.0の製品挙動、既存の互換性claim、
  ADR、spec、plansの保存場所を変更しない

## 目的

実装と内容の正本はv1.1.0の内部基準点とし、v1.2.0の公開文書を利用目的で探せる小さな構成へ再編する。
ルートREADMEは短いページ内目次と3つの正規ページへの直接リンクを持ち、「詳しい文書」から詳細な総合目次へ案内する。旧pathは
正規文書へのstubとして残し、開発者向け資料は既存pathのまま案内する。

## 用語

- **基準点**: v1.1.0 release commitから読み戻した実装、公開版、コマンド、文書の組。commitが確定するまで未確認とする。
- **正規文書**: v1.2.0で内容を管理する唯一のユーザー向け文書。
- **案内stub**: 旧pathを残す短い移転案内。手順、旧版、旧コマンドを複製しない。
- **開発者導線**: ADR、spec、plansなどの既存pathを、開発者向けにリンクする経路。

## 共通境界

- ルートREADME、`docs/README.md`、6分類の正規文書、旧pathのstubだけを公開導線として扱う。
- 現在の機能範囲と互換性claimはv1.1.0の基準点から変更しない。未確認の範囲は未確認のまま残す。
- 公開READMEと正規ユーザー文書は、公開後はv1.2.0だけを現行版として表示する。v1.1.0は計画・Handoffのprovenanceと検査基準に限定し、利用者向け導線へ出さない。
- 製品コード、Native REST API、CtrlCmd、DB schema、migration、録画、予約、ライブ、WebUI、Composeの挙動を変更しない。
- 実環境のURL、token、認証情報、宅内path、番組、録画、TS、raw応答を記載しない。
- 外部URLは原則の出典としてのみ記録し、外部の文言、テンプレート、例、画像をコピーしない。

## 公開導線

### `DOC-001`: v1.1.0を基準点に固定する

製品側の作業は、v1.1.0 release commitの完全SHA、tagのpeeled SHA、公開文書のtreeを読み戻した後に始める。
release commitが未確定、またはtagとtreeが一致しない場合は作業を開始しない。実装・内容は基準点を根拠にし、
文書再編を理由に機能や互換性claimを追加しない。

現行版の表示は、v1.2.0のrelease判断で確定した値を一箇所から反映する。release未確定の段階で版を推測して記載しない。
v1.1.0は内部の基準点として検査に使うが、公開READMEと正規ユーザー文書の版表示や現在地には出さない。

### `DOC-002`: ルートREADMEを短い入口にする

ルート`README.md`は公開後の現行版v1.2.0を示す短い入口とし、50～80行を目安に次を示す。

1. 製品名と一行の概要
2. ページ内目次（概要、必要なもの、Linux、Compose、KonomiTV、対応範囲、詳しい文書）
3. 前提条件と最初に読むべき行動
4. Linux→`docs/getting-started/linux.md`、Compose→`docs/getting-started/docker-compose.md`、KonomiTV→`docs/getting-started/konomitv.md`の直接リンク
5. 「詳しい文書」から`docs/README.md`へのリンク
6. Licenseと問い合わせ先

分類別の詳細な総合目次、制作メモ、過去の品質目標、過剰な履歴、旧コマンドをREADMEに置かない。50～80行は目安とし、
必要時は行数を無理に合わせず読みやすさを優先する。

### `DOC-003`: `docs/README.md`を詳細総合目次にする

`docs/README.md`を公開文書の詳細な唯一の総合目次とする。ルートREADMEには短いページ内目次とLinux・Compose・KonomiTVの
直接リンクを置くが、6分類の総合目次や詳細手順は複製しない。`docs/README.md`では次の順で6分類を一度ずつ案内する。

1. `getting-started/`
2. `guides/`
3. `operations/`
4. `reference/`
5. `troubleshooting.md`
6. `development/`

各分類の入口は存在する正規pathへリンクし、リンク先を推測で作らない。`docs/README.md`には利用者が最初に必要とする
説明を置き、planningの経緯や内部の作業記録を列挙しない。

### `DOC-004`: 正規pathを固定する

利用者向けの新規pathは、次の合意案にそろえる。各pathは必要な内容を一つだけ持ち、同じ手順を別pathへ複製しない。

| 分類 | 正規path | 役割 |
|---|---|---|
| getting-started | `docs/getting-started/linux.md` | Linuxの前提と初回導入 |
| getting-started | `docs/getting-started/docker-compose.md` | Docker Composeの初回導入 |
| getting-started | `docs/getting-started/konomitv.md` | KonomiTV接続の初回確認 |
| guides | `docs/guides/channels-and-epg.md` | チャンネルと番組表の利用 |
| guides | `docs/guides/recording.md` | 予約と録画の利用 |
| guides | `docs/guides/live-viewing.md` | 現行のライブ視聴経路 |
| guides | `docs/guides/web-ui.md` | 現行WebUIの利用範囲 |
| operations | `docs/operations/update-and-remove.md` | 更新、通常削除、purge |
| operations | `docs/operations/backup-and-restore.md` | backupとrestore |
| reference | `docs/reference/compatibility.md` | 検証済み互換性と対象外 |
| reference | `docs/reference/configuration.md` | 設定値と配置 |
| reference | `docs/reference/commands.md` | 利用者向けコマンドの参照 |
| reference | `docs/reference/clients/komorebi.md` | Komorebi向けの現行案内 |
| troubleshooting | `docs/troubleshooting.md` | 症状からの切り分け |
| development | `docs/development/README.md` | 開発参加と資料導線 |
| development | `docs/development/architecture.md` | アーキテクチャ資料への導線 |
| development | `docs/development/decisions.md` | 判断資料への導線 |
| development | `docs/development/testing.md` | 検証資料への導線 |
| development | `docs/development/dependencies.md` | 依存資料への導線 |

開発者向けの新規pathは上記5つに限定する。ADR、spec、plansをこの配下へ移動しない。

### `DOC-005`: 正規文書を一つの情報源にする

導入、利用、運用、設定、互換性、復旧の各説明は、対応する正規pathだけを内容の正本とする。別の正規pathからは短いリンクで
案内し、コマンドブロック、版、制限を複製しない。実装・公開内容を説明する場合は、基準点であるv1.1.0の実装または既存の公開文書で確認できる
内容だけを残す。

### `DOC-006`: 旧ユーザー文書を案内stubにする

旧pathは削除せず、タイトル、移転の一文、固定した正規pathへのリンクだけを含む案内stubへ置き換える。旧pathにある本文、
旧版、旧コマンド、古い互換性claimを残さない。移転先は次のとおり固定する。

| 旧path | 案内する正規path |
|---|---|
| `docs/linux-installation.md` | `docs/getting-started/linux.md`、`docs/operations/update-and-remove.md` |
| `docs/docker-compose.md` | `docs/getting-started/docker-compose.md` |
| `docs/konomitv-setup.md` | `docs/getting-started/konomitv.md` |
| `docs/komorebi-setup.md` | `docs/reference/clients/komorebi.md` |
| `docs/mirakurun-catalog-sync.md` | `docs/guides/channels-and-epg.md` |
| `docs/catalog-database-operations.md` | `docs/operations/backup-and-restore.md` |
| `docs/recording-operations.md` | `docs/guides/recording.md` |
| `docs/web-ui-operations.md` | `docs/guides/web-ui.md` |
| `docs/compatibility.md` | `docs/reference/compatibility.md` |
| `docs/ctrlcmd-channel-runtime.md` | `docs/guides/channels-and-epg.md`、`docs/reference/commands.md` |
| `docs/dependencies.md` | `docs/development/dependencies.md` |

表にない旧ユーザー文書も、v1.1.0 treeのinventoryで同じ規則を適用する。履歴資料や開発資料をstub化して公開導線へ追加しない。

### `DOC-007`: 開発者向け資料は既存pathを案内する

ADR、spec、plansは物理移動、改名、複製を行わない。`docs/development/`から、次の既存pathへ開発者向けリンクだけを置く。

- `docs/adr/`と必要な個別ADR
- `docs/plans/`と必要な個別Plan
- `spec/`配下のAccepted仕様または関連仕様

`docs/input/`、調査途中のmatrix、handoff、制作メモ、過去の品質目標、過剰な履歴は利用者向け目次から案内しない。
開発者導線からも、実装判断に不要な内部資料を一括列挙しない。

## 整合性検査

### `DOC-008`: 古い内部リンクを検査する

変更対象とstubのMarkdownリンクを列挙し、相対path、file、anchorが現行treeで解決することを確認する。外部URLと、意図的に
参照するrelease URLは別扱いにし、無効な内部リンクを残さない。リンク先を移動で解決せず、既存pathの保持を優先する。

### `DOC-009`: 版の表記を検査する

公開導線の現行版はv1.2.0のrelease判断と一致させる。v1.1.0は計画・Handoffのprovenanceと内部検査基準に限定し、公開READMEと
正規ユーザー文書には版表示や現在地として出さない。v1.0.0以前の表記や旧版の版番号は、現在版に見える位置へ残さない。
過去版を説明する場合は、公開導線から分離された履歴としてラベルを付ける。release commitが未確定の間は、版、tag、commitを
作らず、未確定と報告する。

### `DOC-010`: コマンド例を検査する

コードブロックとinline commandを抽出し、v1.1.0基準点の配布物、systemd、Docker Compose、DB、catalog、CtrlCmdの提供済み
操作と内部で照合する。この内部検査はv1.1.0を公開文書へ表示することを意味しない。存在しない操作、旧pathを前提とする操作、順序が変わった操作を推測で修正しない。確認できないコマンドは
削除または未確認表示とし、文書を成功扱いにしない。

### `DOC-011`: 公開導線の除外を検査する

README、`docs/README.md`、正規文書、stubを通読し、制作メモ、過去の品質目標、過剰な履歴、調査途中の証拠、handoffが利用者
向けリンクとして露出していないことを確認する。過去の長時間試験やchecksumなど、現行の品質条件ではない情報を、現在の手順や
必要条件として再掲しない。

### `DOC-012`: 機能範囲と互換性claimを検査する

公開文書に、基準点で確認されていないクライアント、CtrlCmd、放送範囲、API、WebUI、録画・予約機能が追加されて
いないことを確認する。版番号や文書構成の変更を、互換性の証拠として扱わない。

### `DOC-013`: 外部参考を原則として記録する

次の2 URLを、情報設計の原則を確認した外部参考として記録する。

- [README作成ガイド（note）](https://note.com/yukikkoaimanabi/n/n587aa648e0f9)
- [GitHub READMEの構成整理（Hatena Blog）](https://shunya-infura-engineer.hatenablog.com/entry/2026/06/28/172730)

外部ページの文言、テンプレート、例、画像、著者情報を公開文書へコピーしない。外部参考は製品仕様、互換性証拠、licenseの
代替にならない。

### `DOC-014`: docs-only diffを確認する

変更は公開文書、正規path、旧stub、開発者導線に限定する。製品コード、API、DB、Compose、依存、release資産に差分があれば、
文書PRを止めて差分をplanningへ戻す。

## 必須テスト

- v1.1.0 release commit、tag、treeのreadbackと、対象製品baseの完全SHAを確認する。
- ルートREADMEのページ内目次7項目、Linux・Compose・KonomiTVの直接リンク、「詳しい文書」から`docs/README.md`へのリンクを確認する。
- `docs/README.md`の詳細な6分類総合目次と、`DOC-004`の正規pathへのリンクが存在することを確認する。6分類の総合目次や詳細手順がREADMEへ重複していないことも確認する。
- READMEの行数を数え、50～80行を目安として読みやすさを優先していることを確認する。
- 旧ユーザー文書を削除せず、`DOC-006`のstubと固定linkだけになっていることを確認する。
- ADR、spec、plansのpath、内容、履歴に物理移動・改名・複製がなく、開発者導線だけが追加されていることを確認する。
- 内部リンク、anchor、版表示、コマンド例、互換性claim、公開導線からの除外項目を検査する。
- `git diff --name-status`と差分レビューで、コード、API、DB、Compose、依存、release資産に差分がないことを確認する。
- Markdownの構文と自然な日本語を確認する。製品コードの単体・統合・実機テストは本仕様の新規対象ではない。

## 完了条件

- `DOC-001`のv1.1.0基準点を完全SHAでHandoff 0066へ記録した。
- ルートREADMEの短いページ内目次と3つの直接リンク、`docs/README.md`の詳細な6分類総合目次、`DOC-004`の正規pathが解決する。
- READMEが50～80行を目安に読みやすく整理され、詳細手順を複製していない。
- 旧ユーザー文書が案内stubになり、本文、旧版、旧コマンドの二重管理がない。
- ADR、spec、plansを移動せず、開発者導線から既存pathへ到達できる。
- 古いリンク、版、コマンド、互換性claim、公開導線からの除外項目を検査済みである。
- 製品のコード、API、DB、Compose挙動、既存の互換性claimに差分がない。
- v1.2.0のrelease commit、製品PR、既存CIの結果はHandoff 0066へread backする。未確定の事項を成功扱いにしない。
