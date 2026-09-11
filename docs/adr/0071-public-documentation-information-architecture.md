# ADR-0071: 公開文書を小さな目的別導線へ再編する

- Status: Accepted
- Proposed date: 2026-09-11
- Decision date: 2026-09-11
- Deciders: プロジェクトオーナー
- Reviewer: Codex
- Related: [Plan 0089](https://github.com/g0ooo0gle/sazanami-planning/blob/main/docs/plans/0089-v1-2-0-public-documentation-restructure.md)、
  ADR-0053、ADR-0069、[公開文書仕様 v1](../../spec/operations/public-documentation-v1.md)、
  [Handoff 0066](https://github.com/g0ooo0gle/sazanami-planning/blob/main/handoffs/0066-v1-2-0-public-documentation.md)
- Product copy path: `docs/adr/0071-public-documentation-information-architecture.md`
- Product sync state: NOT COPIED
- Supersedes: None
- Superseded by: None

## 背景

2026-09-11、プロジェクトオーナーは、v1.1.0の実装と公開内容を基準点にして、v1.2.0で公開文書を一から再編する
ことを明示した。現行のREADMEと複数の詳細文書は、最初に必要な導入、運用情報、開発資料、過去の経緯が同じ導線に
並び、入口と正本が分かれやすい。

v1.1.0のrelease commitはまだ確定していない。製品側での再編は、そのcommitをHandoff 0066のbaseとして読み戻した後に
始める。再編は文書だけを対象とし、製品の機能範囲、互換性claim、コード、API、DB、Compose挙動は変えない。

## 判断

### 1. 短い入口と詳細総合目次を分ける

利用者向けの基本導線を次の形に固定する。

```text
README.md（短いページ内目次）
  ├─ Linux / Compose / KonomiTV → 正規文書へ直接
  └─ 詳しい文書 → docs/README.md（詳細な唯一の総合目次） → 6分類 → 正規文書
```

ルート`README.md`は概要、公開後の現行版v1.2.0、必要なもの、最初の行動、対応範囲、詳しい文書へのリンクを短く持つ。
ページ内目次は「概要」「必要なもの」「Linux」「Compose」「KonomiTV」「対応範囲」「詳しい文書」とし、Linux→
`docs/getting-started/linux.md`、Compose→`docs/getting-started/docker-compose.md`、KonomiTV→
`docs/getting-started/konomitv.md`へ直接リンクする。6分類の詳細な総合目次や詳細手順は置かず、50～80行は目安として
必要時は読みやすさを優先する。

`docs/README.md`を公開文書の詳細な唯一の総合目次とし、次の6分類をこの順で案内する。

| 分類 | 内容 |
|---|---|
| `getting-started/` | 前提条件、初回導入、最初の確認 |
| `guides/` | KonomiTV、Komorebi、録画などの利用手順 |
| `operations/` | 更新、DB・カタログ、サービス運用 |
| `reference/` | 設定、互換性、CtrlCmd、依存の参照情報 |
| `troubleshooting.md` | 症状からの切り分けと復旧 |
| `development/` | 開発参加方法と既存の開発資料への案内 |

利用者向けの正規pathは、`getting-started/linux.md`、`getting-started/docker-compose.md`、
`getting-started/konomitv.md`、`guides/channels-and-epg.md`、`guides/recording.md`、
`guides/live-viewing.md`、`guides/web-ui.md`、`operations/update-and-remove.md`、
`operations/backup-and-restore.md`、`reference/compatibility.md`、`reference/configuration.md`、
`reference/commands.md`、`reference/clients/komorebi.md`、`troubleshooting.md`とする。
開発者向けpathは`development/README.md`、`development/architecture.md`、`development/decisions.md`、`development/testing.md`、
`development/dependencies.md`に固定し、ADR、spec、plansをその配下へ移さない。

### 2. 旧文書はstubにする

既存のユーザー向け文書は削除しない。正規文書を6分類へ整理した後、旧pathには移転先への案内stubだけを残す。
Stubは移転先、対象版の注意、リンクだけを含め、旧コマンド、旧版、本文の複製を保持しない。
旧pathと移転先の固定mappingは、[公開文書仕様 v1の`DOC-006`](../../spec/operations/public-documentation-v1.md#doc-006-旧ユーザー文書を案内stubにする)に従う。

### 3. 開発資料のpathを保持する

ADR、spec、plansは物理移動、改名、複製を行わない。`docs/development/`から既存pathへリンクして発見性だけを補う。
制作メモ、調査途中の資料、handoff、過去の品質目標、過剰な履歴は利用者向けの目次やREADMEから案内しない。

### 4. v1.1.0の範囲を文章にも適用する

公開文書の機能説明、互換性表現、コマンドはv1.1.0 release commitの実体と内部で照合する。公開READMEと正規ユーザー文書は
公開後の現行版v1.2.0として読めるようにし、v1.1.0のbaseline表記を利用者向け導線へ出さない。新しい機能、クライアント、
放送範囲、API、設定、Compose操作を推測で追加しない。過去版や過去の判断は利用者向けの現行手順と混ざらない履歴資料へ
分離する。

### 5. 外部参考は原則に限る

次の2ページはREADMEを入口として設計すること、概要と使用方法を先に示すこと、古い情報を点検することの参考にする。

- [README作成ガイド（note）](https://note.com/yukikkoaimanabi/n/n587aa648e0f9)
- [GitHub READMEの構成整理（Hatena Blog）](https://shunya-infura-engineer.hatenablog.com/entry/2026/06/28/172730)

外部ページの文言、テンプレート、例、画像、構成をそのままコピーしない。外部ページは採用判断や製品仕様の正本でもない。

## 理由

READMEの短いページ内目次と、詳細な総合目次を`docs/README.md`へ分けると、導入や運用の正規文書を探しやすい。分類を6つに
限定すれば、現在の機能を増やさずに導線だけを整理できる。旧pathをstubとして残すことで、既存リンクを壊さず、説明の二重管理を減らせる。

ADR、spec、plansは判断と実装境界の履歴であり、公開ユーザー文書と同じ場所へ移す必要がない。開発者導線だけを追加すれば、
pathの安定性と発見性を両立できる。

## 影響

### Positive

- 初回利用者はREADMEの短い目次から、Linux・Compose・KonomiTVの直接リンクまたは詳細な総合目次へ進める。
- 旧リンクは案内stubで受け止め、正規文書の重複を減らせる。
- ADR、spec、plansの履歴とpathを保ったまま、開発者が参照できる。
- README、版、コマンド、リンクの不一致を定期検査しやすくなる。

### Negative

- 最初の再編時に、既存文書の分類と旧pathの対応を棚卸しする必要がある。
- stubを経由する旧リンクでは、利用者が一度多くクリックする場合がある。
- v1.1.0のrelease commitが未確定の間は、製品側の作業を開始できない。v1.1.0の基準情報は利用者向け表示へ出さない。

## 採用しなかった案

### READMEへすべての手順と履歴を集約する

採用しない。READMEが長くなり、初回導入と開発者向けの情報が混ざる。詳細は目的別文書へ置く。

### 既存文書を6分類へ物理移動する

採用しない。pathを利用する既存リンクと、ADR・spec・plansの履歴を壊す。旧ユーザー文書はstubにし、開発資料は既存pathを保つ。

### 公開文書を自動生成サイトへ移す

採用しない。v1.2.0の判断対象は情報設計であり、製品依存、公開基盤、生成処理を追加しない。

### v1.2.0で機能説明や互換性claimも広げる

採用しない。文書再編はv1.1.0の実装・内容の読みやすさを改善するだけで、検証済み範囲を増やさない。

## 見直す条件

- v1.1.0以後に製品の公開契約やCompose構成が変わり、現行の分類では利用者が到達できないと証拠で確認されたとき。
- 文書サイトや多言語公開を正式な製品境界として採用するとき。
- ADR、spec、plansの保存先を変更する新しい判断がAcceptedになったとき。

## Product synchronization

- Handoff: Handoff 0066
- Planning source commit: 未確定
- Target product base commit: v1.1.0 release commit（未確定）
- Product destination: 同じpath
- Last synchronized product commit: None
- Known divergence: 製品側へ本ADRはまだコピーしていない
