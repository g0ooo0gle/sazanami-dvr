# CtrlCmd純粋codec hard cap

- Artifact status: Accepted normative specification
- Candidate copy ID: `COPY-CTRL-CODEC-LIMITS`
- Target future work unit: `HF-02`
- Candidate profile: `KT-CORE` for KonomiTV v0.14.1
- Product repository/base commit: `sazanami-dvr` /
  `9ba4f96663fe9de0bd7bb7f9fa9b9bc48cff42f3`
- Authority: Accepted 2026-07-31 by project owner; product use still requires handoff copy
- Product destination: `spec/ctrlcmd/codec-limits.md`
- Reviewed source revision: `989778365685767db840d132c6fadf802cd14233`
- Evidence: `SOURCE: VERIFIED`; synthetic/product tests and measurement `NOT RUN`;
  `FIXTURE: NOT RUN`; `LIVE: NOT RUN`; `BLACK_BOX: NOT RUN`
- Planning date: 2026-07-31

## 1. 目的

この文書は、socket、command dispatch、domain、providerを持たないHF-02純粋codecに必要な
hard capだけを定めるAccepted仕様である。既存のcross-command
[`limits.md`](limits.md) Group Aからstable IDと値を保って切り出した。

Group Bのcommand固有値、Group Cのlistener/concurrency/spool、Group Dのdeadline/observabilityは
含めない。これらは後続work unitの別authorityである。

`CTL-A01`–`CTL-A11`の単一normative定義はこの文書である。Umbrella `limits.md` Group Aは
reference-onlyとして扱い、値または意味が食い違った場合はこの文書を優先せず実装を停止して
planningへ戻す。

## 2. 適用モデル

codec APIは次の2つを同時に受ける。

1. この文書の変更不能なprofile hard cap。
2. 呼出側がcommand/profileごとに渡す、hard cap以下のeffective cap。

実際の上限は常に小さい方である。wire input、設定、operator操作からhard capを広げることは
できない。HF-02はcommandを知らなくても、呼出側がより小さいbody/string/count budgetを
指定できる形にする。

## 3. Stable hard caps

| ID | Accepted hard cap | Allocation前の検査と失敗 |
|---|---|---|
| `CTL-A01` | Outer headerはexact 8 bytes | 不足headerはtyped body decodeへ進めない。transport timingは後続authority |
| `CTL-A02` | Request bodyは最大1 MiB | signed length、effective cap、hard capを検査してからbounded body viewを作る |
| `CTL-A03` | Response bodyは最大256 MiB | encode開始前に最終長を証明する。1個のcontiguous heap bufferを許可する値ではない |
| `CTL-A04` | 1個のnested structure extentは4 bytes以上16 MiB以下 | prefix自身を含むextent、親範囲、effective capを検査する |
| `CTL-A05` | 1個のserialized string extentは6 bytes以上256 KiB以下 | 偶数長、親範囲、zero terminator、UTF-16LE妥当性をmaterialize前に検査する |
| `CTL-A06` | Vector extentは8 bytes以上、elementは最大65,536個 | signed count、minimum encoded bytes、親範囲、aggregate budgetをallocation前に検査する |
| `CTL-A07` | Structure/vectorの最大nesting depthは12 | 13階層目へ入る前にrejectする。recursive stackへ無制限依存しない |
| `CTL-A08` | 1 frameのaggregate logical decoded itemは最大1,048,576 | structure、string、vector memberを空payloadでも数える |
| `CTL-A09` | 中間計算はchecked 64-bit、最終wire extentはsigned 32-bitとhard/effective capの両方に収める | overflow、underflow、wrap、narrowing失敗をrejectする |
| `CTL-A10` | Root bodyと全nested extentをexact consumption | unexpected trailing/unconsumed byteをrejectする |
| `CTL-A11` | 初期`KT-CORE` Cmd2 versionはexact 5 | 5未満／超過は別途指定するまでout of profile。wire failure mappingは後続command/listener仕様 |

`KiB`は1,024 bytes、`MiB`は1,048,576 bytesを意味する。

## 4. Serialized extentの定義

- Structure extent: 自身の4-byte I32LE prefixを含む。generic minimum 4 bytes。
- String extent: 自身の4-byte I32LE prefix、UTF-16LE payload、最後のzero U16LE terminatorを
  含む。generic minimum 6 bytes。
- Vector extent: 自身の4-byte I32LE prefixと4-byte signed element countを含む。
  generic minimum 8 bytes。
- Body length: 8-byte outer headerを含まず、header直後のbody bytesだけを数える。

個別型／commandがもっと大きいminimumを持つ場合は、そのAccepted仕様のminimumを使う。
generic minimumだけを満たしてもtyped valueとして有効とは限らない。

## 5. Budget交差と検査順序

1. 利用可能bytesがfieldの固定prefix width以上かを確認する。
2. signed length/countを読み、負数をrejectする。
3. checked 64-bitでend/count feasibilityを計算する。
4. hard cap、effective cap、親remaining bytesの最小値に収まるか確認する。
5. nesting depthとaggregate item budgetを消費する。
6. bounded child viewを作り、子fieldを順にdecodeする。
7. childとrootのexact consumptionを確認して初めてsuccessを返す。

どのfailureでもpartial domain valueを返さない。decode error後に同じbyte列を別型として
fallback decodeしない。

## 6. Allocation規則

- Declared body length、extent、countを配列/list capacityへ直接渡さない。
- Vector countはelement minimumとremaining bytesで実現可能性を証明してから反復する。
- Stringはbyte capとsemantic capの両方を確認してからtextへmaterializeする。
- 256 MiB response capは最大wire envelopeであり、同サイズのheap objectを許可しない。
- Pure codecはresponse sinkへincremental encodeできる設計とし、spool選択は後続work unitへ
  残す。
- encoderはcanonical field order、exact extent、ちょうど1個のstring terminatorを出力する。

## 7. Error境界

HF-02は少なくとも`TRUNCATED`, `MALFORMED`, `OVER_LIMIT`, `UNSUPPORTED`, `INTERNAL`を
payload非依存のtyped internal categoryとして区別する。

- error objectへraw bytes、decoded text、private pathを保持しない。
- positionを持つ場合はhard cap以下のnumeric offsetだけにする。
- wire result、socket close、log level、metric labelはHF-02で決めない。
- internal exceptionを未処理のままprocess terminationへ伝播させない。

## 8. HF-02 verification gate

| Area | Required synthetic cases | Status |
|---|---|---|
| Header/body | exact header、short header、negative/zero/limit±1、signed extremes | `NOT RUN` |
| Arithmetic | add/multiply/narrowing overflow、parent end境界 | `NOT RUN` |
| Structure | minimum、nested exact、outside parent、depth 12/13 | `NOT RUN` |
| String | empty、limit±1、odd、missing/extra terminator、embedded NUL、surrogate | `NOT RUN` |
| Vector | empty、negative/impossible count、65,535/65,536/65,537、aggregate budget | `NOT RUN` |
| Consumption | root/nested exact、1-byte trailing、declared/body mismatch | `NOT RUN` |
| Encoder | canonical round-trip、length proof failure、sink failure | `NOT RUN` |
| Side effect | malformed inputでnetwork/file/DB/domain/provider callが0 | `NOT RUN` |
| Resource | caseごとのbounded allocation/time、no frame-sized response heap | `NOT RUN` |

test vectorはこの独立仕様からsemanticに作り、KonomiTV/EDCB serializerまたはcaptured bytesを
生成器として使わない。exact oracleの独立review方法とfuzz段階は人間判断が必要である。

## 9. Acceptance record and remaining completion gates

- `CTL-A01`–`CTL-A11`とstrictnessは2026-07-31にprovisional hard capとして採用した。
- `framing-and-primitives.md`とのextent/minimum/version一致を採用条件とする。
- Umbrella `limits.md` Group Aはreference-onlyであり、この文書が単一normative定義である。
- Unit/boundary/property-style deterministic/bounded mutationをHF-02必須とし、
  coverage-guided fuzzは後続decisionへ残す。
- 256 MiBはwire envelopeであり、frame-sized contiguous allocationを許可しない。Measurementは
  exact product commitのCompletion gateである。
- Fixture/live/black-boxはHF-02 pure codecの安全性完了に必須とせず、互換性claimには使わない。

## References

- [`framing-and-primitives.md`](framing-and-primitives.md)
- [`limits.md`](limits.md)
- [`Plan 0026`](../../docs/plans/0026-hf-02-authority-and-test-gates.md)
