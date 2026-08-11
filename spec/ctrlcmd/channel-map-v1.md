# CtrlCmdチャンネル設定 v1

- Artifact status: Accepted
- Intended copy ID: `COPY-CHANNEL-MAP-V1`
- Authority: Project ownerが2026-08-04にHF-06B判断1〜3を採用
- Related ADR: [`../../docs/adr/0025-explicit-static-channel-runtime.md`](../../docs/adr/0025-explicit-static-channel-runtime.md)
- Related plan: [`../../docs/plans/0037-hf-06b-static-channel-runtime.md`](../../docs/plans/0037-hf-06b-static-channel-runtime.md)
- Product use: Ready handoffと製品側copyが必要

## Purpose

Mirakurunのservice APIにないTSID（トランスポートストリーム識別子）を、完成済みカタログへ安全に
対応付ける。CtrlCmd応答に必要な補助情報も扱う。設定はprovider観測を変更せず、起動時のread-only入力とする。

不足値を黙って補わない。

## File boundary

| Item | Requirement |
|---|---|
| Path | `--channel-map`で絶対pathを明示する。Canonical data rootの直下だけを許可する |
| Type | 通常fileだけ。Symbolic link、directory、device、socketを拒否する |
| Permission | Data rootの既存`0700`検査を使う。設定file自体のmode完全一致は要求しない |
| Size | 1 MiB以下。1 byte超過も拒否する |
| Encoding | UTF-8 JSON。BOM、invalid UTF-8、末尾の別値を拒否する |
| Structure | Service 1〜4,096件。標準decoderでunknown fieldを拒否する |
| Read timing | `ctrlcmd validate`または`ctrlcmd serve`の開始時に1回だけ読む |
| Reload | 行わない。変更は明示再起動で反映する |

初版はpathを1回検査してfileを読み切る。読込中の差し替えを検出する追加処理や、duplicate key専用parserは
実装しない。1 MiB上限、型付きfield、unknown field拒否、末尾値拒否を基本防御とする。

## JSON shape

```json
{
  "format": "sazanami-channel-map-v1",
  "backend_id": "00000000-0000-4000-8000-000000000000",
  "services": [
    {
      "provider_locator": "123456789",
      "network_id": 32736,
      "service_id": 1024,
      "transport_stream_id": 32736,
      "provider_name": "",
      "network_name": "",
      "transport_stream_name": "",
      "remote_control_key_id": 1,
      "partial_reception": false,
      "epg_capture": true,
      "search": true
    }
  ]
}
```

この例の識別子と数値は構造説明用であり、実環境の値や推奨値ではない。

## Required fields and limits

Top-levelの3 fieldはすべて必須とする。各serviceの11 fieldも同様である。`null`や暗黙defaultを許可しない。

| Field | Type / limit | Ownership and validation |
|---|---|---|
| `format` | exact string | `sazanami-channel-map-v1`だけを許可する |
| `backend_id` | canonical UUID v4 text | 対象の完成済みカタログを持つbackendと完全一致させる |
| `services` | array 1〜4,096 | 項目の存在自体を選択済みとみなす |
| `provider_locator` | UTF-8、1〜256 bytes | カタログのlocatorと完全一致させる。重複不可 |
| `network_id` | unsigned 16 bit | カタログ値との照合専用。設定値で上書きしない |
| `service_id` | unsigned 16 bit | カタログ値との照合専用。設定値で上書きしない |
| `transport_stream_id` | unsigned 16 bit | Operatorが確認したTSID。0を含め範囲内でもidentity重複は拒否する |
| `provider_name` | UTF-8、0〜4,096 bytes | Operator所有。NUL、tab、CR、LFを拒否する |
| `network_name` | UTF-8、0〜4,096 bytes | Operator所有。NUL、tab、CR、LFを拒否する |
| `transport_stream_name` | UTF-8、0〜4,096 bytes | Operator所有。NUL、tab、CR、LFを拒否する |
| `remote_control_key_id` | unsigned 8 bit | Operator所有。推測しない |
| `partial_reception` | boolean | Operator所有。必須で明示する |
| `epg_capture` | boolean | Operator所有。必須で明示する |
| `search` | boolean | Operator所有。必須で明示する |

Service名は完成済みカタログの表示名を使い、空または上限超過なら全体を拒否する。Service typeはカタログの
`broadcast_kind`に保存された10進整数を使い、0〜255以外、符号付き、空、数字以外を拒否する。

## Whole-file validation

次のいずれかがあれば、1件だけ除外して続行せず設定全体を拒否する。

- Backendが存在しない、`MIRAKURUN`ではない、完成済み世代がない。
- 設定したprovider locatorが存在しない、または複数に一致する。
- Network IDまたはservice IDがカタログと一致しない。
- Provider locatorまたはNetwork ID／TSID／service IDの組が重複する。
- Service type、service名、補助名、flag、remocon IDがこの仕様を満たさない。
- 設定項目が0件または4,096件を超える。
- Catalog読込が4,096件を超える、page契約に違反する、途中で失敗する。

カタログに存在して設定にないserviceは正常に無視する。これは部分的な選択を許可するためであり、
欠落の自動補完は行わない。

## Provenance and snapshot

- 設定fileのSHA-256を計算するが、通常出力やlogへ表示しない。
- 選択したカタログ項目をNetwork ID、TSID、service ID、provider locator順に正規化し、SHA-256を計算する。
- 二つのhashから128 bytes以内の内部snapshot keyを作る。
- `serve`はdata rootの単独所有を保持し、同一processにcatalog syncやreloadを組み込まない。
- 1060と1021はこの同一snapshotだけを参照する。

## Stable failure reasons

製品実装はprivate valueを含めず、次の分類を安定して返す。詳細なOS error、path、backend ID、service情報を
そのまま連結しない。

| Category | Example stable reason |
|---|---|
| Path | `channel-map-path-invalid`, `channel-map-not-regular` |
| Size / JSON | `channel-map-over-limit`, `channel-map-json-invalid` |
| Schema | `channel-map-field-missing`, `channel-map-field-invalid`, `channel-map-count` |
| Catalog | `channel-backend-unavailable`, `channel-catalog-unavailable`, `channel-catalog-over-limit` |
| Match | `channel-service-orphan`, `channel-service-mismatch`, `channel-service-duplicate` |
| Lifecycle | `channel-snapshot-failed`, `channel-listen-failed`, `channel-context-ended` |

## Update procedure

1. CtrlCmd待受を停止する。
2. 同じdirectoryに新しいfileを作り、対象名へ置き換える。
3. Data rootが既存の所有者限定条件を満たすことを確認する。
4. `ctrlcmd validate`を実行する。
5. 成功後に`ctrlcmd serve`を明示起動する。`serve`自身も同じ検証を再実行する。

製品は設定fileを書き換えず、自動backup、自動repair、自動retryを行わない。失敗時は旧fileを同じ手順で
戻し、再度validateする。
