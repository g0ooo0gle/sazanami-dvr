# COPY-GO-CTRL-BINDING: 上限付きCtrlCmd codecのGo binding

- Artifact status: Accepted normative specification
- Copy ID: `COPY-GO-CTRL-BINDING`
- Target work unit: `HF-GO-01-04`
- Product destination: `spec/ctrlcmd/go-codec-binding.md`
- Specification acceptance: 2026-08-02にproject ownerがAccepted
- Semantic authorities: `framing-and-primitives.md`、`codec-limits.md`、command 2200 v5仕様

## 目的

Accepted済みのCtrlCmd wire bytesとhard capを変更せず、Go APIへ固定する。新しいcommandや互換性claimは
追加しない。

## PackageとAPI境界

- Pure codec: `internal/adapters/ctrlcmd/codec`
- Command 2200 mapping: `internal/adapters/ctrlcmd/status`
- Loopback lifecycle: `internal/app/ctrlcmd`
- Decodeは8-byte headerのlengthを検証した後、上限付き`[]byte` viewだけを読む。
- Encodeはcanonical fieldを`io.Writer`へ書き、success frameのsizeを事前に固定する。
- 呼出側bufferをcall終了後に保持しない。
- 使用する標準libraryは`encoding/binary`、`unicode/utf16`、`io`などに限定する。

## Error contract

Codec errorは`TRUNCATED`、`MALFORMED`、`OVER_LIMIT`、`UNSUPPORTED`、`TIMEOUT`、
`SATURATED`、`PEER_DISCONNECT`、`INTERNAL`の安定したcategoryを使用する。

- Errorに含めるのはboundedなreason、numeric offset／sizeだけとする。
- Raw input、番組text、path、address、credentialを保持しない。
- 不正入力でpanicせずerrorを返す。
- Partial decodeをsemantic successとして返さない。
- Response開始後に`io.Writer`が失敗した場合はwrite errorを返し、第2frameを追加しない。

## Go固有の不変条件

- Signed wire lengthは、非負・hard/effective cap・parent rangeを確認してから`int`へ変換する。
- `cursor + width`のoverflowに頼らず、subtraction-based remaining checkを使う。
- Untrustedなvector countを検証前にcapacityへ使わない。
- Strict UTF-16はembedded NULとunpaired surrogateを拒否する。
- Rootとnested valueのexact consumptionを必須にする。
- Codec packageは`net`、filesystem、provider、database、environment、process、global clockを使わない。

## 検証

- CTL-A01〜A11の境界caseをGoで再現する。
- Command 2200の固定request/response oracleは独立生成し、`SYNTHETIC_GOLDEN`としてのみ扱う。
- 固定seedのbounded mutation testでpanicとside effectがないことを確認する。
- Go native fuzz entry pointは追加できるが、unbounded fuzz実行は本work unitの完了条件にしない。
