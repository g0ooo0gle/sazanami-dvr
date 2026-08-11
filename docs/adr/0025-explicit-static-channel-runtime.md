# ADR-0025: 静的チャンネル設定を使う明示CtrlCmd起動

- Status: Accepted
- Accepted date: 2026-08-04
- Decision owner: Project owner
- Related plan: [`../plans/0037-hf-06b-static-channel-runtime.md`](../plans/0037-hf-06b-static-channel-runtime.md)
- Related specification: [`../../spec/ctrlcmd/channel-map-v1.md`](../../spec/ctrlcmd/channel-map-v1.md)
- Related handoff: [`../../handoffs/0011-ctrlcmd-channel-runtime.md`](../../handoffs/0011-ctrlcmd-channel-runtime.md)
- Supersedes: None
- Superseded by: None

## Context

HF-06AはCtrlCmdの1060／1021応答を安全に生成できる。ただし、製品の起動経路には接続していない。
Mirakurunのservice APIにはTSID（トランスポートストリーム識別子）がなく、推測値ではKonomiTVへ
正しいチャンネルidentityを渡せない。通常起動時の外部探索やTS解析を増やすことも認められていない。

TSIDは推測しない。

Project ownerは2026-08-04に、HF-06B判断資料の5項目をすべて推奨どおり採用した。
同日、初版はセキュリティ検査を過度に増やさず、データ保存先の既存保護を利用するよう追加指示した。

## Decision

- TSIDと補助情報は、所有者限定のdata root直下に置くJSON設定から起動時に一度だけ読む。
- 設定は完成済みカタログと全件照合し、完全一致した1〜4,096件だけを変更不可スナップショットにする。
- 設定項目の存在を選択の意思とする。別のselection flagは作らない。
- Service名とservice typeは完成済みカタログを正本にする。Operator設定をprovider観測としてDBへ保存しない。
- `ctrlcmd validate`は待受なしで検証し、`ctrlcmd serve`は検証成功後だけloopbackで待ち受ける。
- Runtimeは既に採用済みの2200とHF-06Aの1060／1021だけを固定的に振り分ける。
- 自動探索、自動reload、自動retry、通常起動からの自動開始は行わない。更新は停止、file置換、
  明示検証、再起動の順とする。
- 初版ではdata rootの既存`0700`検査を信頼する。設定fileのmode完全一致、読込中差し替え検知、
  JSON duplicate key専用parser、JSON深さ専用上限は追加しない。
- KonomiTV black-boxが完了するまで互換性を主張せず、HF-06Bを完了扱いにしない。

## Consequences

既存DBを変更せず、外部接続を増やさずに、確認済みTSIDを使うチャンネル応答を明示起動できる。
設定とカタログが食い違う場合は利用者に誤った一覧を返さず、待受前に停止する。

Catalog同期や設定更新にはCtrlCmdをいったん停止する必要がある。初版は単一nodeの手動運用を優先する。
将来providerがTSIDを正式に供給する場合は、スナップショット作成前の小さい入力境界を差し替える。

## Rejected alternatives

- TSIDを0または他fieldから推測する: identityを偽るため不採用。
- 設定値を既存service観測行へ書く: 来歴が混ざるため不採用。
- YAML／TOMLを導入する: この小さい設定のために依存または独自parserを増やすため不採用。
- 設定を自動reloadする: 1060と1021の世代不一致を増やすため不採用。
- 空一覧で起動する: KonomiTV側のチャンネルを意図せず消す危険があるため不採用。
- 動的command登録機構を作る: 現在必要な3 commandには過剰で、未確認commandを有効化しやすいため不採用。

## Verification

設定fileのpermission／path／size／JSON境界、カタログ完全照合、起動前失敗、同一snapshot、明示起動だけの
network、runtime依存方向、local full／shuffle／race／vet／cross build／govulncheck、Hosted Ubuntu CI、
許可済みKonomiTV black-boxをexact product commitで確認する。
