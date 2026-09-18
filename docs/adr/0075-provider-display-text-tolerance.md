# ADR-0075: 表示文字列のU+FFFDを受け入れる

- Status: Accepted
- Proposed date: 2026-09-19
- Decision date: 2026-09-19
- Decision owner / reviewer: Project owner
- Authorization: 最小修正範囲、PDT-001〜004、製品base、単一文書copy、Handoff 0072を提示し、利用者が「その範囲でいいよ」と明示承認した。
- Product base: `5913b84b316831016dc18a154e7c6d7873193389`
- Related planning records: `sazanami-planning` のPlan 0095、Handoff 0072
- Product copy state: NOT COPIED

## 背景

v1.3.0のMirakurun adapterは、JSON文字列にU+FFFD（置換文字「�」）が含まれると拒否する。
そのため、番組名や説明の一文字を復元できないだけで、番組表全体の更新が失敗する。
U+FFFD自体は有効なUnicode文字であり、それが含まれることとUTF-8の不正は同じではない。

Go 1.26.6の `encoding/json` は、文字列中の不正UTF-8や不正なUTF-16サロゲートをU+FFFDへ置換する。
decode後は、元から存在した置換文字とdecoderが生成した置換文字を区別できない。
利用者は、同種の表示文字の問題でも同期を止めない方針を希望している。
その後、次版の修正PR作成が許可され、今回は実障害の原因へ絞るよう依頼された。

## 判断

表示テキストでは標準decoderが返したU+FFFDをそのまま受け入れる。識別子や構造の検証、資源上限は維持する。
以下の例外範囲だけを採用する。製品側ではHandoff 0072の文書copy gateを通してから実装する。

| ID | 対象 | 採用する動作 |
|---|---|---|
| PDT-001 | 番組の `name`、`description`、`extended` の見出し・本文、サービスの `name` | U+FFFDを含んでも受け入れる。サービス名はcatalogとsetup用取得で同じ扱いにする。 |
| PDT-002 | 読み飛ばす未知フィールドの文字列値 | 置換文字を理由に失敗させない。未知オブジェクト内も値に限り同じ扱いとし、構造・深さ・token上限は保つ。 |
| PDT-003 | 上記表示文字列の不正UTF-8、孤立サロゲート | 標準decoderが生成したU+FFFDも保持する。元の文字は推測しない。JSONの構文エラーは回復しない。 |
| PDT-004 | その他 | 構造キー、ID、時刻、数値範囲、言語コード、必須値、型、重複、JSON構文、通信・保存の検証を維持する。NUL変換と警告の新設は含めない。 |

`extended` のキーだけは表示用の見出しなのでPDT-001・003の対象にする。
構造キーとは区別するが、decode後の見出しが重複する場合は既存の重複拒否を維持する。
上書きや結合でどちらかの内容を勝手に採用しない。

現行製品に警告集計の経路はない。新たな同期結果・定期更新通知の契約を導入するより、
今回の最小修正では表示の制限をPRと詳細文書に説明する。U+FFFDだけを理由とする失敗をなくし、
後続の通信・保存・構造エラーは従来どおり失敗として扱う。

## 維持する境界

- 正常な日本語、絵文字、補助平面文字、結合文字はそのまま保持する。フォントの対応可否を判定しない。
- Unicode正規化、trim、空白整理、ASCIIへの縮退、NULの置換・削除を追加しない。
- 文字列の型・必須性・空文字の既存条件は変えない。HTTP本文とdecode後のbyte上限を守り、超過を無制限な切詰めで隠さない。
- 言語コード、provider版文字列、チューナー識別情報、URL、パスへ表示用の回復処理を流用しない。
- 全件JSONをメモリへ読み込まず、既存の逐次decodeとページ境界を維持する。
- 文字の回復後も完全な取得・検証・保存を終えてから世代を公開する。
  それ以外の失敗は既存どおり前回の完了世代とチャンネル設定を保持する。
- 既存revision、予約が保持するsnapshot、canonical encodingの形式、DB schemaは変更しない。
  回復したテキストが従来と異なる場合だけ、既存ルールで新しいrevisionを作る。

## 既存判断との差分

次の表示文字に関する部分だけを置き換える。その他の要件はそのまま残す。

- [ADR-0036](0036-versioned-program-metadata.md)と[PMETA-001](../../spec/ctrlcmd/konomitv-program-metadata-v1.md): 表示文字の回復を許し、回復後に既存上限・型・canonical規則を適用する。
- [ADR-0073](0073-mirakurun-channel-bootstrap.md)と[MCB-004](../../spec/ctrlcmd/channel-bootstrap-v1.md): サービス名にも同じ受容規則を適用する。MCB-011の出力形式は変えない。
- [ADR-0027](0027-continuous-catalog-refresh.md) / [CCR-004・006](../../spec/recording/continuous-catalog-refresh-v1.md)、
  [ADR-0074](0074-unresolved-program-service.md) / [UPS-004](../../spec/provider/unresolved-program-service-v1.md): 表示文字の回復のみを失敗条件から外す。世代切替・真の失敗の扱い・秘匿は維持する。

planningにある旧HF-05B仕様とcatalog-schema-v1は、対象製品baseには同じpathのコピーがない。
本件に便乗して旧文書を復元・自動同期せず、製品baseの既存コピーと本ADRの明示差分をレビューする。

## 選択肢と理由

1. **表示用フィールドのU+FFFDを許す（採用）:** 報告された入力と同種のdecoder置換結果に対応し、同期結果の契約を増やさない。
2. **NUL変換と警告集計も同時に追加する:** 初稿で検討したが、別の後段互換問題と新しい診断経路を伴うため後続へ分離する。
3. **全ての文字列・不正番組を無条件に読み飛ばす:** 誤ったIDや壊れたJSONまで取り込むため採用しない。

元の文字は復元できず、表示・文字列検索が一部変わる。U+FFFDの受容は、
完全に復元できたことや、すべての不正入力を取り込めることを意味しない。

## 検証と出典

合成入力で、各表示フィールドの明示U+FFFD、不正UTF-8、孤立サロゲート、正常な非BMP文字を確認する。
NULの扱いが変更されていないこと、ページをまたぐ混在入力、同じ入力の再同期、CtrlCmd出力、
構造破損時の前回世代保持も確認する。
詳細な製品テスト条件はHandoff 0072に置く。実環境データをfixtureにしない。

- SOURCE: `g0ooo0gle/sazanami-dvr`、`v1.3.0` / 上記完全SHA、
  `internal/adapters/provider/mirakurun/decode.go`、`invalidJSONString` / `jsonReader`。
- SOURCE: 同じ製品SHAの `internal/adapters/ctrlcmd/channel/projection.go` / `validateService` と
  `internal/adapters/ctrlcmd/codec/writer.go` / `StringSize` はNULを拒否する。
  現在はescaped NULがproviderを通過し、サービス照合や番組出力で初めて失敗し得る。
- [Go 1.26.6 encoding/json](https://pkg.go.dev/encoding/json@go1.26.6#Unmarshal): 不正文字列の置換規則。確認日2026-09-19。
  導入済み同版の `src/encoding/json/decode.go` と `stream.go` でも確認した。
- [RFC 8259 §7–8](https://www.rfc-editor.org/rfc/rfc8259.html#section-7): 文字列・UTF-8・サロゲートの区別。確認日2026-09-19。

外部コード・文章の転載は行わず、調査は読み取りのみ。製品FIXTURE / LIVE / BLACK_BOX結果はまだない。

## 再検討する条件

NULの後段拒否は既知の未対応事項として残す。今回のU+FFFD修正に必須ではないため、
再現ケースを根拠に、表示用NUL変換と診断の要否を後続で判断する。
表示見出しの重複や資源上限超過が実運用を妨げる事例が得られた場合は、該当する任意項目だけを
省略できるかを別途判断する。今回の文字回復に混ぜて、必須情報や安全上限を緩めない。
