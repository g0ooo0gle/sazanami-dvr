# CtrlCmdフレーム／基本型

- Artifact status: Accepted source-derived normative specification
- Candidate copy ID: `COPY-CTRL-FRAME`
- Candidate profile: `KT-CORE` for KonomiTV v0.14.1
- Product repository/base commit: `sazanami-dvr` /
  `9ba4f96663fe9de0bd7bb7f9fa9b9bc48cff42f3`
- Authority: Accepted 2026-07-31 by project owner; product use still requires handoff copy
- Product destination: `spec/ctrlcmd/framing-and-primitives.md`
- Reviewed source revision: `989778365685767db840d132c6fadf802cd14233`
- Evidence: `SOURCE: VERIFIED`; `FIXTURE: NOT RUN`; `LIVE: NOT RUN`;
  `BLACK_BOX: NOT RUN`
- Planning date: 2026-07-31

## 1. 目的と適用範囲

この文書は、初期Sazanami CtrlCmd adapterの純粋codecが共通に使う外側frameと基本型の
Accepted仕様である。EDCB内部設計の再現、一般EpgTimerNW互換、TCP listenerの実装、
個別commandの意味論を規定しない。

Sourceから確認できたwire形状と、Sazanamiがfail closedするために追加する安全規則を
区別する。2026-07-31にstrict方針を含むこのrevisionが採用された。製品実装の正本にするには、
Ready handoffによるproduct側copyとreadbackが必要である。

## 2. 出典と証拠境界

### 2.1 KonomiTV client

- Repository: `https://github.com/tsukumijima/KonomiTV.git`
- Ref: tag `v0.14.1`
- Resolved SHA: `0a32188274b81c1e7bed642474b208bd2a543a6b`
- Path: `server/app/utils/edcb/CtrlCmdUtil.py`
- Symbols: `__sendAndReceive`, `__sendCmd`, `__sendCmd2`, `__writeString`,
  `__writeVector`, `__readString`, `__readVector`, `__readStructIntro`
- Method: detached sourceのread-only直接確認。source textとwire bytesはコピーしていない。

### 2.2 EDCB corroboration

- Repository: `https://github.com/xtne6f/EDCB.git`
- Ref: tag `work-plus-s-260703`
- Resolved SHA: `9770536e9f04835fab2bddee26af1f17c7c40a9c`
- Paths: `Common/CtrlCmdUtil.cpp`, `Common/CtrlCmdUtil.h`, `Common/CtrlCmdDef.h`
- Symbols: integer/string/vector/structure `WriteVALUE` / `ReadVALUE`,
  `WriteVALUE2WithVersion`, `CMD_VER`
- Method: detached sourceのread-only直接確認。EDCBは突合せにのみ使い、外部実装をコピーしない。

この2系統のSource確認は、fixture上の実byte、実EDCB応答、KonomiTV black-box成功を証明しない。

## 3. レイヤー

```text
one bounded transport connection
  └─ one outer request frame
       ├─ 8-byte request header
       └─ declared request body
            └─ commandによりCmd2 versionとtyped values

one bounded transport connection
  └─ one outer response frame
       ├─ 8-byte response header
       └─ declared response body
            └─ success時にcommand固有のtyped values
```

Cmd2 versionは外側headerのfieldではなく、versioned command bodyの先頭fieldである。
versionの有無とbody構造はcommand仕様が指定する。

## 4. 外側frame

### 4.1 Request header

| Offset | Width | Type | Meaning |
|---:|---:|---|---|
| 0 | 4 bytes | I32LE | command number |
| 4 | 4 bytes | I32LE | request body byte length |

Source fact: headerは合計8 bytesで、field順序はcommand、body lengthである。

Accepted Sazanami rule:

- body lengthが負、command hard cap超過、global hard cap超過、またはchecked conversion不能なら、
  body allocation前にrejectする。
- exact 8-byte headerをdeadline内に読めない接続はtyped body decodeへ進めない。
- unknown commandはbodyの型を推測しない。

### 4.2 Response header

| Offset | Width | Type | Meaning |
|---:|---:|---|---|
| 0 | 4 bytes | I32LE | result |
| 4 | 4 bytes | I32LE | response body byte length |

Source fact: headerは合計8 bytesで、field順序はresult、body lengthである。

Accepted Sazanami rule:

- encoderはbodyの最終長がsigned 32-bitと適用hard capの両方に収まることを、headerを送る前に
  証明する。
- success header送信後にencode/writeが失敗した場合、別のfailure frameを追記せず接続を閉じる。
- resultの意味とfailure mappingはcommand/profile仕様が定める。codec内部errorから任意の
  resultを自動生成しない。

## 5. 整数

HF-02共通subsetは次の固定幅型を扱う。

| Name | Width | Byte order | Range/use |
|---|---:|---|---|
| `I32LE` | 4 bytes | little-endian | signed command/result/extent field |
| `U16LE` | 2 bytes | little-endian | Cmd2 version等 |
| `U32LE` | 4 bytes | little-endian | count、identifier等 |

各commandが別の整数型を必要とする場合、そのcommand仕様がsignedness、width、semantic
rangeを追加する。host-native endianness、暗黙narrowing、wraparoundは使用しない。

長さ計算はchecked 64-bit intermediateで行い、wire I32へ変換する直前にも範囲を検査する。
負数をunsignedへ再解釈して有効な長さ／countにしてはならない。

## 6. Cmd2 version

Source fact:

- pinned KonomiTVのversioned requestはbody先頭へ`U16LE` versionを書き、値5を使用する。
- command 2200のpinned EDCB success responseはversioned bodyを返す。
- pinned KonomiTVはcommand 2200 response version 5以上をparserへ通す。

Accepted `KT-CORE` codec rule:

- request versionはexact 5とする。
- response encoderはrequestに対してversion 5を返す。
- 5以外のrequestはcodecでは`UNSUPPORTED`とする。Wire failure mappingはcommand/profile仕様が
  決定し、codecは任意のresultを生成しない。
- version許容方針はcommand/profileごとに明示し、codecが暗黙negotiationしない。

## 7. Size-prefixed structure

Source fact:

- structureは先頭に`I32LE` serialized extentを持つ。
- extentはその4-byte prefix自身を含む。
- 従ってgeneric minimum extentは4 bytesである。

Accepted decoder rule:

1. 親に4 bytes残っていることを確認してからprefixを読む。
2. extentが4未満、型／command hard cap超過、親の残byte超過ならrejectする。
3. `childEnd = childStart + extent` をchecked arithmeticで計算する。
4. fieldを宣言順にdecodeし、各子fieldが`childEnd`を越えないようにする。
5. command仕様がoptional tailを明示しない限り、`childEnd`でexact consumptionを要求する。

encoderは一度確定したfield順序だけをcanonicalに出力し、未知field、padding、未初期化byteを
付加しない。

## 8. String

Source fact:

- stringは先頭に`I32LE` serialized extentを持つ。
- extentは4-byte prefix自身を含む。
- payloadはUTF-16LE code unitsで、末尾に1個のzero `U16LE` terminatorを持つ。
- generic minimum extentはprefix 4 + terminator 2 = 6 bytesである。

Accepted decoder rule:

1. extentは6以上、偶数、per-string cap以下、親extent以内でなければならない。
2. prefixを除いたpayload lengthも偶数でなければならない。
3. 最後のcode unitはzero terminatorでなければならない。
4. semantic textはterminatorを除いてdecodeする。
5. terminal zero以外のembedded NUL、unpaired surrogate、malformed UTF-16LEはrejectし、
   置換文字で黙認しない。
6. byte capと、必要ならcommand固有のcode-point／field capをmaterialize前に交差適用する。

encoderはちょうど1個のzero terminatorを追加し、extentをchecked arithmeticで算出する。
canonical encodingにBOM、余分なterminal zero、paddingを含めない。

malformed UTF-16LEの厳密方針は人間採用済みだが、fixture/black-box互換性は未確認である。

## 9. Vector

Source fact:

- vectorは`I32LE` serialized extent、続いて`I32LE` element countを持つ。
- extentは自身の4-byte prefixを含み、generic minimum extentは8 bytesである。

Accepted decoder rule:

1. extentは8以上、適用されるbody／command cap以下、親extent以内でなければならない。
2. countは負であってはならず、globalおよびcommand semantic cap以下でなければならない。
3. element型のminimum serialized sizeが既知なら、`count * minimum` をchecked arithmeticで
   計算し、vectorの残byteで実現可能かをallocation前に確認する。
4. untrusted countをcollection capacityへ直接渡さない。
5. 各elementをvector end内でdecodeし、最後にexact consumptionを要求する。
6. empty vectorはextent 8、count 0のcanonical形だけを出力する。

固定幅0-byte elementやminimumを証明できない汎用型は導入しない。個別command仕様がelement
型とsemantic count capを必ず指定する。

## 10. 入れ子decode不変条件

- root body、structure、string、vectorは、それぞれ`start`, `end`, `cursor`を持つbounded viewで
  扱う。
- 子viewは親viewの範囲内にしか作成できない。
- `cursor + width`、`start + extent`、`count * minimum`はすべてchecked arithmeticとする。
- depthとaggregate logical-item budgetを各frameで共有する。
- error後にcursorを再解釈して別の型としてdecodeし直さない。
- malformed／over-limit frameから部分的なdomain objectを返さない。
- decoderはdeclared bodyのexact consumptionを成功条件とする。

[`codec-limits.md`](codec-limits.md) の `CTL-A02`–`CTL-A11` がHF-02 hard capを
与える。command固有capが小さい場合は、常に小さい方を適用する。

## 11. Connection lifecycle

Pinned KonomiTV sourceはcallごとに新しいconnectionを使う。Accepted `KT-CORE` server境界は、
1 connectionにつきrequest 1個、response 1個としてcloseする。keep-alive、pipelining、同一
connection上の第2requestは対象外である。

この規則は純粋codecをsocketへ結合する指示ではない。HF-02はbounded byte source/sinkを使う
codecだけで検証可能とし、listener security boundaryは最初にlistenerを導入するwork unitで
Accepted authorityを要求する。

## 12. 内部error分類

製品側codecは、少なくとも次の内部categoryをpayload非依存のtyped errorとして区別する。

| Category | Examples | Wire mapping |
|---|---|---|
| `TRUNCATED` | header/body/fieldの不足 | Open; response前なら通常close候補 |
| `MALFORMED` | 不正extent、terminator、UTF-16、trailing bytes | Open |
| `OVER_LIMIT` | frame/string/vector/depth/work hard cap超過 | Open |
| `UNSUPPORTED` | command、version、out-of-profile shape | command/profileで決定 |
| `TIMEOUT` | header/body/work/write deadline | stageによりclose |
| `SATURATED` | connection/handler/queue/spool枯渇 | listener/work-unitで決定 |
| `PEER_DISCONNECT` | partial read/write中の切断 | responseを追加しない |
| `INTERNAL` | invariant違反、encoder size不一致 | successを送らずfail closed |

診断にはcommand number、phase、bounded size、reason code、elapsed bucket等の数値だけを使い、
raw payload、番組／予約文字列、copied-file内容、host pathを含めない。

## 13. Limits仕様との関係

この文書は形状と検査順序を定め、純粋codecの数値hard capはAccepted
[`codec-limits.md`](codec-limits.md)が定める。最低限、次を同時に満たす。

- outer header: `CTL-A01`
- request/response body: `CTL-A02`, `CTL-A03`とcommand-specific cap
- structure/string/vector/depth/work/arithmetic/exact consumption: `CTL-A04`–`CTL-A10`
- Cmd2 version: `CTL-A11`

Connection、one-request lifecycle、deadline、spool等はcross-command
[`limits.md`](limits.md)に残し、listener/command work unitで適用する。片方だけがAcceptedに
なってもHF-02実装authorityは完成しない。形状とcodec hard capを同じhandoffで正確な
Accepted revisionとしてcopyする必要がある。

## 14. 将来の検証契約

| Level | Required cases | Current evidence |
|---|---|---|
| Unit | 各整数の境界、partial width、canonical encode/decode | `NOT RUN` |
| Boundary/property | extent/count/depth/workのlimit-1/limit/limit+1、checked overflow | `NOT RUN` |
| String | empty、terminator、odd length、embedded NUL、surrogate、cap | `NOT RUN` |
| Vector | empty、negative count、impossible count、nested exact consumption | `NOT RUN` |
| Frame | short header、negative/over-limit body、trailing body、第2request | `NOT RUN` |
| Fuzz | 任意partial inputでbounded time/memory、domain副作用なし | `NOT RUN` |
| Fixture | 独立review済み最小artifactでextent包含規則を確認 | `NOT RUN` |
| Live/black-box | exact product commitとpinned KonomiTVで対象journeyを確認 | `NOT RUN` |

synthetic test oracleはこの独立仕様から作り、上流serializerをtest helperとしてコピーしない。
fixture/live/captureは別の明示承認があるまで実行しない。

## 15. Acceptance record and remaining completion gates

- 外側frame、extent、strict UTF-16LE、embedded NUL、trailing bytesを2026-07-31に採用した。
- `CTL-A01`–`CTL-A11`は`codec-limits.md`を単一normative定義として採用した。
- HF-02はunit/boundary/property-style deterministic/bounded mutationを必須とし、
  coverage-guided fuzz toolはHF-03以降の別decisionへ残した。
- HF-02 pure codecの完了にfixture/liveを必須としない。互換性claimは引き続き禁止する。
- Wire resultはcommand/profile仕様で決定し、HF-02 codecでは生成しない。
- Baseline memory/time measurementとproduct testはhandoff Completion gateであり`NOT RUN`である。

## References

- [`commands/2200-get-status-notify2.md`](commands/2200-get-status-notify2.md)
- [`codec-limits.md`](codec-limits.md)
- [`limits.md`](limits.md)
- [`Plan 0003`](../../docs/plans/0003-get-status-notify2-evidence-contract.md)
- [`Plan 0025`](../../docs/plans/0025-ctrlcmd-frame-and-primitives-candidate.md)
- [`Pinned KT-CORE inventory`](../../docs/research/konomitv-v0.14.1-kt-core-inventory.md)
