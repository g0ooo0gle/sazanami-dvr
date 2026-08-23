# 実用リリース品質仕様 v1

- Status: Accepted
- Date: 2026-08-23
- Requirements: `PRQ-001`～`PRQ-008`
- Related decision: ADR-0069
- Target: Sazanami DVR v0.5.0以後
- Overrides: 製品版仕様v1の`VER-010`／`VER-011`、Linux lifecycle検証仕様v1のarchive checksum必須条項

## 目的

固定KonomiTVの実用バックエンドを、通常の自動テスト、短いLinux確認、簡潔な公開文書とともに公開する。
時間を固定した耐久試験と、利用者による配布アーカイブのチェックサム照合を、品質や成果物の条件にしない。

## PRQ-001: 実用導線を製品境界にする

固定KonomiTV v0.14.1 / commit `0a32188274b81c1e7bed642474b208bd2a543a6b`の日常導線を対象にする。
番組表、検索、予約、放送途中の録画、取消し、録画中停止、自然終了、ライブ、完成録画の一覧、通常取得、
Range取得、再起動後読戻し、再解析、サムネイル再生成、管理者削除後の整合を扱う。

固定クライアントから呼ばれないEDCB機能、別版KonomiTV、Komorebi全体、全画面と全検索条件の総当たりは、
このリリースの品質条件に含めない。

## PRQ-002: 通常CIを必須にする

最終候補で、通常テスト、shuffle、race、vet、module検証、既知脆弱性検査、CGOを使わない主要buildを行う。
既存のcontainer構成を変更した場合は、ComposeとOCI imageの検査も行う。

時間を固定した耐久試験、固定周期数、RSS増加率、goroutine数、ファイルディスクリプタ数の専用合格値は要求しない。
通常テストが失敗した場合は、長時間運転で合否を上書きせず、不具合として修正する。

## PRQ-003: Linux lifecycleは短い一回の確認にする

Fresh Ubuntuで、初回導入、systemd起動、DB更新、バックアップ、復元、旧版への切り戻し、再更新、通常削除、
明示purgeを一回通す。Synthetic Mirakurunを使ってよい。各操作の成功と保持範囲を確認し、実時間の長さ、
繰返し回数、長期resource推移を品質指標にしない。

実Mirakurunの隔離LAB smokeは、接続先変更やprovider差分を確認する必要がある場合に実施できるが、
通常CIを通したreleaseの必須成果物にはしない。未実施なら未実施と記録し、成功扱いにしない。

## PRQ-004: 公開成果物を簡潔にする

次版で必須とする公開面は次である。

- Linux amd64／arm64アーカイブ
- Multi-architecture OCI imageと、その参照情報
- `LICENSE`、`README.md`、`CHANGELOG.md`、必要な詳細文書とthird-party notice
- 製品版、tag、公開対象commit、binaryへ埋め込んだVCS revisionの一致

`SHA256SUMS`は生成、公開、利用者照合の必須成果物にしない。専用のチェックサム検証コマンド、ライブラリ、スクリプト、
文書手順も追加しない。既に公開済みのリリース資産は削除または差替えをしない。

## PRQ-005: 内部整合性検査を区別する

次のhashまたはchecksumは、利用者が配布archiveを確認する機能ではないため維持する。

- DB migrationのname／内容drift検出
- Backup manifestとrestore時の破損検出
- SQLite integrityとforeign key検査
- Go moduleと公式toolchain取得の来歴検査
- Git commit、OCI digestなど、既存platformが持つidentity

本仕様を根拠に、これらの安全検査を削除、無視、fail-openへ変更してはならない。

## PRQ-006: 既存証拠を再利用できる

固定KonomiTVの実行コードと公開契約がv0.5.0から変わっていない場合、v0.5.0のsource、契約テスト、black-box、
実験環境証拠を再利用できる。再利用する場合は、対象パスの差分がないことと、最終候補の通常CI成功を記録する。
同じ画面操作を形式的にやり直すことをrelease gateにしない。

## PRQ-007: 公開文書を現在の操作へ絞る

READMEは現在版、最短セットアップ、主な機能、注意事項、目的別ガイド、開発、Licenseの順を基本にする。
詳しい導入、更新、切り戻し、通常削除、purge、互換範囲、運用、版履歴は目的別文書へ分ける。

Activeな公開文書に、利用者必須のarchive checksum照合、時間指定の耐久試験、専用の試験成果物を置かない。
過去の変更履歴は`CHANGELOG.md`へ残せるが、現在の完了条件として読めないようにする。

## PRQ-008: v1.0.0の完了条件

v0.9.0を経由せず、次を同じ最終製品commitへそろえればv1.0.0を公開できる。

1. PRQ-001の固定KonomiTV実用導線に、未解決の重大な製品不具合がない。
2. PRQ-002の通常CIがcandidateと統合後mainで成功する。
3. PRQ-003の短いLinux lifecycle確認が成功する。
4. PRQ-004の公開成果物、版、tag、revisionが一致する。
5. PRQ-007の公開文書と互換表が、確認済み範囲と既知制限を正しく示す。
6. 独立reviewで、実行コード、DB、公開契約、依存に意図しない変更がない。

時間指定の耐久試験、長期resource表、チェックサム一覧ファイル、利用者の照合結果は完了条件に含めない。

## 必須確認

| 対象 | 確認 | 証拠 |
|---|---|---|
| KonomiTV | 実行時差分がないか、変更した導線の契約テストが成功 | Final candidate SHA、テスト結果、再利用した証拠 |
| 通常品質 | Test、shuffle、race、vet、module、脆弱性、主要build | Candidate／main CI |
| Linux | 一回の導入、起動、更新、復元、切り戻し、通常削除、purge | Fresh Ubuntu job |
| 公開物 | Archive内容、版、VCS revision、OCI参照、文書 | Release workflowとreadback |
| 文書 | README、詳細ガイド、CHANGELOG、互換表の役割とリンク | 文書検査と独立review |
| 内部安全 | Migration、backup、integrity、module、toolchain検査を維持 | 既存回帰テスト |

## 完了条件

- [ ] PRQ-001～PRQ-008が製品差分、CI、公開文書、release-prepへ対応付いている。
- [ ] 長時間専用の必須テスト、workflow、品質表、成果物を追加していない。
- [ ] `SHA256SUMS`と利用者向けchecksum手順を次版の必須成果物から外した。
- [ ] 内部整合性検査を変更していない。
- [ ] 通常CIと短いLinux lifecycle確認が成功した。
- [ ] Final product commit、tag、版、VCS revision、公開物を読み戻した。
