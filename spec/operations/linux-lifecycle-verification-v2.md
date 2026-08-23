# Linux lifecycle検証仕様 v2

- Status: Accepted
- Date: 2026-08-23
- Applies to: Sazanami DVR v0.5.0以後。lifecycle実行はLinux amd64、配布物確認はamd64／arm64
- Decisions: ADR-0068、ADR-0069
- Base specification: `spec/operations/linux-installation-lifecycle-v3.md`
- Supersedes: `spec/operations/linux-lifecycle-verification-v1.md`

## 目的

Linux導入・更新・削除仕様v3の主要操作を、fresh Ubuntuで短く一回確認する。長時間運転、厳しいresource指標、
利用者向けchecksum照合を品質条件にせず、導入、更新、復元、切り戻し、通常削除、purgeの実用操作を確認する。

## 証拠区分

| 区分 | 確認する対象 | 必須 |
|---|---|---:|
| Standard CI | 標準利用者、標準path、標準unit、既定port、主要lifecycle | Yes |
| Release archive | 版、VCS revision、収録ファイル、OS／architecture | Yes |
| LAB smoke | 実Mirakurunとのcatalog、tuner取得、サービス起動 | 必要な変更がある場合だけ |

Standard CIと公開物は同じ製品candidateへ結び付ける。`SHA256SUMS`や利用者のhash照合結果は証拠にしない。
LABを実施しない場合は未実施と記録し、Standard CIの成功と混同しない。

## LVC2-001: Candidateをcommitで固定する

更新元は公開v0.5.0、製品commit `0b90ee4d5cdd137c23cdb966649e436db0170ea8`のLinux amd64 archiveとする。
Candidateはworkflowのcheckout完全SHAで固定し、そのcheckoutから一回だけarchiveを作る。実行ファイルの
`--version`、VCS revision、dirty状態、OS、architectureを確認する。Branch名、tag名、archiveのファイル名だけを
identityに使わない。

## LVC2-002: Fresh resourceだけを使う

Standard CIはGitHub-hosted `ubuntu-24.04` x64のfresh VMで行う。標準利用者、group、path、unit、
4520／4521／4522のいずれかが既に存在または使用中なら、何も停止、変更、削除せず失敗する。

Cleanupとpurgeは、今回作成した固定absolute pathだけを対象にする。`/`、`/var`、`/home`、glob、symlink、
未解決変数、外部録画先を削除対象にしない。

## LVC2-003: Synthetic providerを最小にする

Synthetic Mirakurunはloopback限定とし、Python標準ライブラリだけで`/api/version`、`/api/services`、
`/api/programs`、`/api/tuners`へ固定応答を返す。1サービス、1番組、2チューナーを上限とし、
実番組、実接続先、credential、TSを含めない。

## LVC2-004: Lifecycleを一回通す

次を一つのjobで順番に確認する。

1. 公開v0.5.0を標準pathへ導入し、DBを`CURRENT`にする。
2. Catalog sync、channel map検査、systemd起動、4520／4521待受を確認する。
3. サービスを停止し、手動バックアップを作る。
4. Candidateへ更新し、必要なら明示migrationを行い、再起動する。
5. 更新前backupをrestoreし、旧版へ切り戻して起動する。
6. Candidateへ再更新して起動する。
7. 通常削除後に設定、channel map、DB、バックアップ、録画先、利用者、groupが残ることを確認する。
8. 独立した明示purgeで、今回作成した標準resourceだけを削除する。

各操作の成功を確認すればよい。運転時間、繰返し回数、周期数、RSS、goroutine、ファイルディスクリプタ数の固定合格値は
持たない。

## LVC2-005: Archive内容を確認する

Linux amd64／arm64 archiveに、実行ファイル、license、README、CHANGELOG、third-party notice、`docs/`、
`packaging/`が含まれることを確認する。実行ファイルの版、VCS revision、CGO無効、OS、architectureを確認する。
Checksum一覧ファイルの生成、parser、照合テストは追加しない。

## LVC2-006: 公開文書を確認する

READMEは最短セットアップから目的別ガイドへ進める構成にする。詳細な更新、切り戻し、通常削除、purgeは
`docs/linux-installation.md`へ置き、版履歴は`CHANGELOG.md`へ置く。利用者必須のchecksum手順と、
時間指定の耐久試験を現在の操作として案内しない。

変更した公開文書へnatural-japaneseのfull検査を行い、構造、読みやすさ、guide適合を独立reviewする。

## LVC2-007: 通常品質を使う

Full test、shuffle、race、vet、module検証、既知脆弱性検査、主要buildを最終candidateで成功させる。
Lifecycle jobは、この通常品質を置き換えない。長時間専用workflowと試験成果物は作らない。

## LVC2-008: LABは必要な変更だけ確認する

Provider接続、標準path以外の隔離profile、実Mirakurun固有の挙動を変更した場合は、認可済みLABで短いsmokeを
実施できる。別名の利用者、path、unit、loopback portを使い、既存Compose、標準導入、KonomiTV、予約、録画へ
触れない。今回の文書とrelease workflowだけの変更では、LAB smokeを必須にしない。

## 必須テスト

| Layer | Required case | Environment | Evidence |
|---|---|---|---|
| Script safety | Resource／port競合、symlink、保持差分、purge範囲外で作成または削除前に失敗 | Shell／fresh CI | Caseと終了段階 |
| Archive | Version、revision、CGO、OS／architecture、必須ファイル | Product CI／Release | Candidate SHAと結果 |
| Lifecycle | 導入、起動、update、restore、rollback、re-update、normal uninstall、purge | `ubuntu-24.04` x64 | Jobと各段階 |
| DB | `CURRENT`、backup ID、restore／recover terminal phase | Standard CI | 状態とphase |
| Retention | 通常削除後も保持対象のowner、mode、件数が同じ | Standard CI | 差分なし |
| Documentation | README構成、目的別リンク、CHANGELOG、archive収録 | Product CI | 対象ファイルと結果 |
| Full quality | Full、shuffle、race、vet、module、脆弱性、主要build | Product CI | Candidate／main run |

## 完了条件

- [ ] Accepted文書を製品へ複製した。
- [ ] READMEとCHANGELOGの既存文書整理を引き継いだ。
- [ ] Release workflowから`SHA256SUMS`生成、公開、照合を外した。
- [ ] Fresh Ubuntuで主要lifecycleを一回完了した。
- [ ] 通常削除の保持と明示purgeの固定範囲を確認した。
- [ ] 通常品質、candidate CI、独立review、製品PR、main CIが成功した。
- [ ] 長時間専用test、厳しいresource gate、必須LAB成果物を追加していない。
- [ ] Final product implementation commitをplanningへ読み戻した。
