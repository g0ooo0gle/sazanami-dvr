# ADR-0074: サービス未解決の番組を記録して同期を続ける

- Status: Accepted
- Decision date: 2026-09-16
- Deciders: Project owner
- Authorization: 実機で見つかった同期失敗について、サービス一覧にない番組を未解決として記録し、録画対象にせず、ほかの番組の同期を続ける提案をProject ownerが「よい」と承認した。
- Related: [未解決番組仕様 v1](../../spec/provider/unresolved-program-service-v1.md)
- Partially supersedes: catalog-schema-v1とHF-05B provider契約で未定義だった、サービス未解決の番組観測の扱い
- Preserves: schema、入力検証・上限、既存予約と録画履歴、PAT確認と静的channel mapの全件検証

## 判断

Mirakurun互換のサービス一覧と番組一覧は、一つの時点で揃うとは限らない。
今回の同期世代で確認できないサービスの番組は、録画可能な番組へ変換せず、未解決の観測として記録する。
その行だけを理由付きで記録し、ほかの有効な番組の同期を続ける。

既存の`program_observations`に`classification=INVALID`、
`validation_reason=service-not-in-current-catalog`として保存し、instance/revisionの参照は両方NULLとする。
ここでのINVALIDは、放送番組自体が不正という意味ではなく、現在のサービス一覧から録画対象へ解決できないことを示す。
サービスやTSIDは推測しない。生の番組応答全体も保存しない。

照合には同じ同期世代のサービス観測を使う。過去のサービス行がDBに残っているかどうかで結果を変えない。
次回のサービス一覧に現れた場合は通常の番組として処理する。既存予約や録画履歴の参照を付け替えたり削除したりはしない。

この例外は、構文と必須項目を検証済みの番組について、サービスが当該世代にない場合だけに適用する。
不正入力、重複、上限超過、取消し、通信・DB障害は従来どおり同期全体の失敗とし、前回の完了世代を保持する。
未解決行も受信件数へ数え、既存GCで整理する。

## 影響

新規DBでも、確認できたチャンネルの初期設定を進められる。
未解決番組は通常の番組表や新規録画候補に出ないため、対象サービスが一覧に現れるまではその番組を予約できない。
schema変更や利用者向けの追加設定は必要ない。

## 製品への引き渡し

本ADRと未解決番組仕様v1だけをHandoff 0070のcopy manifestへ列挙し、対象製品base
`eaceb40282d85c2d0ad23447cf85ecbb6091dfe4`へ文書を複製したcommitを確認後に実装する。
