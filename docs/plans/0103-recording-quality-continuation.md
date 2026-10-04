# 録画品質継続 Implementation Plan 0103

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans for the preserved Native execution method. Use superpowers:subagent-driven-development only if the human changes the method. Steps use checkbox syntax.
>
> Status: In Progress。2026-10-04にProject ownerが本計画を確認し「いいよ」と承認した。書き込み制限の解除後に実装を開始した。

**Goal:** 受信TSが乱れても録画を続け、安全に確定できた劣化完録・通信部分録画を再生できるようにして公開する。

**Architecture:** 録画専用の有界packet処理で品質を集約し、既存の安全なfile確定とDBへ渡す。品質と保存状態を分け、履歴・復旧・HTTP・CtrlCmdで同じ公開条件を使う。共有parser、provider接続方式、旧録画、本番環境は変えない。

**Tech Stack:** Go 1.26.6、既存SQLite adapter、標準library、既存MPEG-TS parser、合成TS／HTTP／SQLite／loopback。

**Spec:** [品質継続仕様v1](../../spec/recording/quality-tolerant-recording-v1.md)、[ADR-0077](../adr/0077-recording-quality-continuation.md)。

**Baseline:** 公開v1.3.6 / `b36891cb4016a7d0a7242c3d6b65a380457d1ea1`。文書同期commit `bdb4ffdd14e5fe6601b28f05d3fd2189757b3869`、planning採用source `785537535d7df788cea6099546fcc15bbfaa80cf`。実装時の正本は文書同期commit内の二文書。

**Execution:** Native。親が依存順に実装し、必要な読み取り調査だけLuna Maxへ分担する。全体の独立レビューを一回受ける。2026-10-04に本計画への直接の承認を受領した。

## Global Constraints

- 188-byte packet、同期回復は五候補、探索保持64 KiB以下、初期選別保持1 MiB以下、PSI section 1,024 bytes以下、PMT entry 64件以下、CC追跡64 PID以下。
- 件数は0〜2,147,483,647へ飽和。fallbackはsegmentごと0〜1、追加OpenStreamは0〜3。
- 再接続は追加三回、1／2／4秒、残り60秒以上、同時一接続。各接続のtail／不完全PSIを持ち越さず、同一segmentの品質とfallbackだけを引き継ぐ。
- 通信partialの公開reasonはSTREAM_ENDED_EARLY、STREAM_TIMEOUT、STREAM_RECONNECT_EXHAUSTEDだけ。STREAM_UNAVAILABLEを拡張しない。
- 新規通信partialは188 bytes以上・188倍数・実時間1秒以上・既存の全公開証跡が必要。品質警告だけでは再生公開しない。
- file、DB、保存先、上書き禁止、停止、期限の保護と、共有ParsePacket／PSICollector／probe／HLSのstrict既定を維持する。
- 旧録画、旧partial、過去の設定を修復・削除・再解析しない。schema15はbackup／restore確認付きの明示migrationだけで、serveに自動migrationを加えない。
- Native RESTの固定quality objectだけを追加し、CtrlCmdのwire、drop／scramble、未確認status、client UIは変更しない。
- 新依存、daemon、汎用queue、packet単位ログ、無制限buffer・再試行を加えない。通常品質と短いLinux lifecycleを使う。
- 大きいtest／buildのTMPDIR・GOTMPDIR・cacheは、許可された外部ディスクの今回専用領域を使う。実装前に設定し、root・広域cacheを削除しない。
- Feature PRが取り込まれるまでは製品版1.3.6を維持する。ADR-0053の挙動変更・まとまった改修として、専用release-prepで1.4.0へ更新する。

## Review Focus

- 同じpacketの再送と同CC別内容を取り違え、PSIを二重組立てする：Task 1で原packetとcollectorの両方を検査する。
- read途中のPMT待ち・同期喪失が、予定終了やgarbage連続入力を妨げる：Task 1・3で容量、前進、終了後readなしを検査する。
- 終了と親cancelが重なり、任意の取消しを利用者停止として公開する：Task 3でDB停止要求あり／なしとCAS順を固定する。
- 最終品質の保存やhistory／recoveryのscanを落とし、再起動後に品質が正常化する：Task 2・4でroundtrip、crash境界、旧UNKNOWNを検査する。
- 固定v0.5.0のschema13からcandidate15へ直接migrationしてCIだけ失敗する：Task 5で固定helper14、restore13後の再更新、helper失敗の停止を検査する。

---

## Task 1: 固定品質型と録画専用のpacket回復

**Files:** Create `internal/core/recording/quality.go`、`quality_test.go`、`internal/app/recording/ts_framing.go`、`ts_quality.go`、`ts_quality_test.go`。Modify `ts_filter.go`、`ts_filter_test.go`、`executor.go`のfilter接続点、`executor_test.go`の合成packet helper。

**Interfaces:**

- `type QualityStatus uint8`。zeroはQualityUnknown、続いてQualityNoIssuesObserved、QualityDegraded。`String() string`は仕様の三値へ写し、`ParseQualityStatus(string) (QualityStatus, error)`は未知のTEXTを拒否する。
- `QualitySummary`はStatus、SelectionUnverified、ObservationLimited、CountersSaturatedと、仕様の15個の件数fieldをint64で持つ。`Validate() error`はenumと全値域を検証する。zero valueは旧未測定のUNKNOWN／0として有効。
- Go fieldはCCGapEvents、CCDuplicateEvents、TEIPackets、MalformedPacketEvents、PSIContinuityEvents、PSICRCEvents、PSIStructureEvents、PSILimitEvents、SyncLossEvents、SyncRecoveredEvents、SyncDiscardedBytes、TrailingIncompleteBytes、UnfinishedPSIEvents、FallbackEvents、ReconnectCount。
- `newTSComponentFilter(file PartialFile, keepCaptions, keepData bool, initial core.QualitySummary) *tsComponentFilter`。
- `Write([]byte) (int64, error)`、`Finish(allowBufferedWrite bool) (int64, error)`、`Quality() core.QualitySummary`。Finishは今回書いた追加byte数を返し、品質だけではerrorを返さない。file errorは返す。
- `streamCopyResult.Quality core.QualitySummary`を追加する。constructor／Finish変更のcall siteはこのTaskでcompile可能にそろえる。Finishが返した追加byteと品質は全return経路で結果へ反映し、品質だけのerrorは返さない。親contextの分離と終了優先順位はTask 3で変更する。

- [x] **Step 1: 失敗する品質・buffer・filterテストを書く。** 既存tsBufferFileとtestTransportStreamを使い、TestQualityArbitrarySplits、TestQualityTEILeavesOriginalPacket、TestQualityPSIRecoveryAndFallback、TestQualitySyncRecoveryBounds、TestQualityCountersSaturateを追加する。split 1／187／188／189、gap／duplicate／同CC別内容／discontinuity／payloadなし、TEI、CRC／pointer／更新PMT／新PID、1 MiB・1,024・64 entry・64 PIDの境界を含める。assertionは次を固定する。

```go
if !bytes.Equal(got, want) || len(got)%188 != 0 { t.Fatalf("output mismatch") }
if q.FallbackEvents != 1 || !q.SelectionUnverified || q.Status != core.QualityDegraded { t.Fatalf("fallback mismatch") }
if q.CCGapEvents == 0 || q.Validate() != nil { t.Fatalf("quality mismatch") }
```

- [x] **Step 2: REDを確認する。** Run `go test -count=1 ./internal/core/recording ./internal/app/recording -run 'TestQuality'`。現行の型不在または即停止の期待差で失敗し、fixture自体の誤りではないことを記録する。
- [x] **Step 3: 最小実装を行う。** framingは録画専用に分離し、同期喪失後の前進走査と固定保持を使う。TEIは固定188-byte scratchでだけmaskして構造確認し、原byteを変更しない。PSIの長さ・連番・CRC・構造・上限を固定分類し、次のPUSIから取り直す。共通parserは変更しない。初期・更新選別が保証できなければsegmentで一度だけraw保存へ切り替える。Write／Finishとも実際の書込byteだけを累積し、短いwriteはfatalとする。bufferはlenだけでなくcapacityも上限内にする。
- [x] **Step 4: GREENと既存回帰を確認する。** Run `go test -count=1 ./internal/mpegts ./internal/app/recording ./internal/core/recording`。故意の不正TS fixtureは残し、lifecycleだけを検査する旧fixtureの全0x47 packetを、構造が読める合成188-byte packetへ替える。正常選別のPMT再生成・PCR・interleavingとfile障害のassertionを弱めない。
- [x] **Step 5: Commit。** このTaskの型・filter・接続点・テストだけをcommitする。

## Task 2: schema15と品質・公開証跡の永続化

**Files:** Create `internal/adapters/sqlite/migrations/0015_recording_quality_continuation.sql`、`recording_quality_repository_test.go`。Modify `internal/core/recording/model.go`とテスト、`internal/adapters/sqlite/{migration,recording_repository,recording_history_repository,recording_recovery_repository}.go`、`store_test.go`、`setup_store_test.go`、`catalog_retention_test.go`、`recording_atomic_recovery_test.go`、`internal/app/recording/{executor,executor_oneseg,recovery,reconciler}.go`と対応mock。

**Interfaces:**

- `IsCommunicationPartialReason(TerminalReason) bool`は許可通信三値だけを返す。利用者停止・正常完了は既存の判定を組み合わせる。
- `FinalizeRequest`、`FinishRequest`、`OneSegResult`、`HistoryItem`、`RecoveryItem`、`RecoverySegment`へQualityを通す。HistoryItemにFinalizationToken・PlannedState・PlannedReasonを追加し、新通信partialだけはそれらと公開証跡の一致を要求する。旧完成／利用者停止へ新条件を遡及しない。
- 進捗portは `UpdateRecordingProgress(context.Context, catalogmodel.ID, int64, time.Time, core.QualitySummary) (time.Time, error)`と、同じ署名のUpdateOneSegProgressへ拡張する。本文、全call site、oneSeg adapter、mockを同時にそろえる。
- SQLは仕様の固定19列だけをqualityとして追加する。historyColumns／scanHistory、RecoveryAttemptsのSELECT／scan、SetRecordingAvailability／SetOneSegAvailability、completedReconcileTargetをそろえる。SQL enumとdomain enumは明示変換する。

- [x] **Step 1: TestQualityMigrationPreservesOldRecordings、TestQualityPersistenceRoundTrip、TestQualityFinalizationPlanReasons、TestQualityRecoveryKeepsWarningsを書く。** schema14の旧完録・旧partial・予約・main／oneSeg・FK行を作り、状態・reason・token・path・byteの同値とUNKNOWN／0を確認する。各列の負値／上限超過、bool 2、未知enum、999／1000 ms、0／187／188／189 bytes、三許可／拒否reasonを含める。

```go
if after.State != before.State || after.Reason != before.Reason || after.Plan != before.Plan { t.Fatalf("old recording changed") }
if q.Status != core.QualityUnknown || q.Validate() != nil { t.Fatalf("legacy quality mismatch") }
if got.Playable() != wantPlayable { t.Fatalf("publication boundary mismatch") }
```

- [x] **Step 2: REDを確認する。** Run `go test -count=1 ./internal/adapters/sqlite ./internal/core/recording -run 'TestQuality'`。品質列不在または旧公開制限による失敗を記録する。
- [x] **Step 3: migrationとrepositoryを実装する。** 過去migrationは変えない。親tableをDROP／再作成せず、旧planned_terminal_reasonをlegacy_planned_terminal_reasonへrenameし、新しい同名canonical列へ六つの許可reasonのCHECKを作って同値をコピーする。旧列は互換値の保全用で新しい計画の判定には使わない。既存stop trigger二つを必要な条件だけ再作成し、利用者停止のstop_requested制約、planned state／reason、tokenと公開段階を維持する。FKを無効化しない。
- [x] **Step 4: schema形状とbackup gateをそろえる。** migration.goの既存shape照合をversion引数のprivate helperへまとめ、12→13の挙動を保ったまま14→15でも列・CHECK・triggerを確認する。backup／restore、integrity／FK、失敗transaction、future／drift／再適用拒否を検査する。進捗・確定予定・終端とqualityを同じ短いtransactionへ保存する。
- [x] **Step 5: GREENと再起動回帰を確認する。** Run `go test -count=1 ./internal/adapters/sqlite ./internal/core/recording ./internal/app/recording`。planned state前のcrashは既知DEGRADEDを維持し他はUNKNOWN、plan後は品質と予定結果を維持する。旧partialの新規計画・renameは一切発生しない。
- [x] **Step 6: Commit。** schema、model、port、永続化とmockをcompile可能な一単位でcommitする。

## Task 3: 終了優先順位・安全な部分確定・警告ログ

**Files:** Modify `internal/app/recording/{executor,executor_oneseg}.go`、`executor_test.go`。Create `executor_quality_test.go`、`cmd/sazanami-dvr/recording_quality_log.go`、`recording_quality_log_test.go`。Modify `cmd/sazanami-dvr/main.go`のobserver接続点だけ（versionはまだ変えない）。

**Interfaces:**

- `copy(parentCtx, streamCtx context.Context, lease providerstream.Lease, file PartialFile, attempt core.Attempt, componentMode core.ComponentMode, result streamCopyResult, oneSeg bool) streamCopyResult`。親取消しとexecutor所有期限を別に渡す。
- mainのexecuteClaimedとoneSegのstart／runOneSegは元親contextを保持する。補助joinによる局所cancelは既存のSTREAM_CANCELLEDとして扱い、主のPROCESS_SHUTDOWNと混同しない。
- `finishCommunicationPartial(ctx context.Context, file PartialFile, attempt core.Attempt, copyResult streamCopyResult) (Result, error)`。許可reasonだけをSync／Close→確定予定→既存publishFinalへ通す。fileを明示して既存の同期・closeを使い、後処理へ渡さない。失敗時は非公開の既存終端へ戻る。
- `QualityObservation`は`Ordinal int`、`Phase string`、`Quality core.QualitySummary`だけを持つ。Ordinalは0／1、Phaseはstart／continue／fallback／reconnect／connection-end／finalの固定値。`Executor.ObserveQuality func(QualityObservation)`と`observeRecordingQuality(io.Writer) func(recordingapp.QualityObservation)`を追加する。

- [x] **Step 1: TestQualityFinishPreservesTermination、TestQualityParentCancelAtEndDoesNotPublish、TestQualityExplicitStopCASOrder、TestQualityReconnectAndFallbackState、TestQualityOneSegIsolation、TestQualityLogBoundsを書く。** mutableClock、fakeLease、attemptMemoryのstopとoperation履歴で、最後のreadのn>0／tail／PSI途中とEOF・timeout・peer・0 progress・予定終了・親cancel・親deadlineを組み合わせる。明示DB停止あり／なしとFINALIZINGの前後、補助joinのcancelを別ケースにする。独立性のfixtureは主だけ初期選別上限へ達し、補助は正常なPAT／PMTで選別を完了するものを使う。

```go
if result.Reason != core.ReasonProcessShutdown || finalizationCalled { t.Fatalf("cancel published") }
if readsAfterEnd != 0 || opens > 4 { t.Fatalf("termination/reconnect bound") }
if !mainQuality.SelectionUnverified || oneSegQuality.SelectionUnverified { t.Fatalf("segment state leaked") }
```

- [x] **Step 2: REDを確認する。** Run `go test -count=1 ./internal/app/recording ./cmd/sazanami-dvr -run 'TestQuality'`。Finish上書き、ReachedEnd先行、ctx.Errの利用者停止化、通信partial非公開という現行との差を確認する。
- [x] **Step 3: 終了分類を分離する。** 親取消し／DB停止→更新済み終了→通信分類→Finish品質の順にし、品質はreason・retryabilityを変えない。Finishのbuffer書込みは予定終了・通信終了・明示停止だけに許可し、file／DB失敗やprocess取消しでは強行しない。DB停止と通常確定のCAS順を維持する。BeginFinalization前とoneSeg wrapperの任意ctx.ErrからUSER_REQUESTED_STOPへの変換を除く。
- [x] **Step 4: 同じ安全確定へ通す。** 追加OpenStreamの試行時点でReconnectCountを累積し、失敗した試行も数える。Qualityはsegment内で引継ぎ、filterのtail／collectorは捨てる。許可通信partialにだけ安全確定を使う。主の通信partialで補助を確定できる条件は共通predicateへそろえるが、主失敗の取消し、第二の再生URL、後処理・電源動作の条件は広げない。
- [x] **Step 5: bounded observerを接続する。** rate stateは既存segmentの処理内へ置き、30秒以下の頻度で警告を出さない。開始一回、接続終了最大四回、最終一回、分類64 bytes以下、一行2 KiB以下。packet、PID、path、ID、番組情報、接続先、生errorを出さない。共有Writerは一行単位に直列化し、観測の失敗で録画を止めない。
- [x] **Step 6: GREENとfailure injectionを確認する。** Run `go test -count=1 ./internal/app/recording ./cmd/sazanami-dvr`。create／short write／sync／close／DB／rename／directory sync／衝突、oneSeg独立、lease・FD・goroutine解放、予定終了後のread・retryなしを確認する。STREAM_UNAVAILABLEは元partialのまま保全する。
- [x] **Step 7: Commit。** executor、警告、関連テストだけをcommitする。

## Task 4: 全公開projectionと統合回帰をそろえる

**Files:** Modify `internal/adapters/recordinghttp/{handler,handler_test}.go`、`internal/adapters/ctrlcmd/recorded/handler_test.go`、`internal/app/recording/{recovery,reconciler}_test.go`、`internal/adapters/sqlite/{recording_e2e,recording_atomic_recovery}_test.go`。必要なクライアント合成fixtureは既存test内で拡張し、実TSは使わない。

**Interfaces:**

- `recordingResponse.Quality qualityResponse`をJSONのquality objectとして追加する。`projectQuality(core.QualitySummary) qualityResponse`を用意する。
- objectのstatusは三値、selection_unverified／observation_limited／counters_saturatedと仕様の15 counterは固定field。旧UNKNOWN／0も省略せず、未測定を正常と説明しない。内部token、planned field、pathは公開しない。
- HTTP、REST、resolver、CtrlCmd 2017／2024はHistoryItem.Playableと同じ判定。CtrlCmd fixedItemSize=73とwire writer、drop／scramble値を変更しない。main品質だけをNative履歴へ投影し、補助品質は独立保存のままにする。

- [x] **Step 1: TestQualityAllReadSurfacesAgree、TestQualityFinalizeCrashReadback、TestQualityConcurrentActorsを書く。** 安全に確定した正常・劣化・通信partial・停止と、証跡欠落・旧partialを同じfixtureでHTTP GET／HEAD／full・先頭／中間／末尾Range、REST、resolver、2017／2024へ通す。予約の2013→2011→2015→1014も維持する。

```go
if rest.Playable != item.Playable() || ctrlcmdCount != wantCount { t.Fatalf("surface mismatch") }
if !bytes.Equal(actualRange, expectedRange) { t.Fatalf("range mismatch") }
if restored.Quality != persisted.Quality { t.Fatalf("quality lost") }
```

- [x] **Step 2: REDを確認する。** Run `go test -count=1 ./internal/adapters/recordinghttp ./internal/adapters/ctrlcmd/recorded ./internal/adapters/sqlite ./internal/app/recording -run 'TestQuality'`。summary不足・判定差を記録する。
- [x] **Step 3: projectionと回帰をそろえる。** rename／directory sync／終端の前後で再起動し、新規FINALIZINGだけをplanned resultへ冪等収束させる。周期reconcileはavailabilityだけを更新し、no-opのChanged=0を維持する。偽providerに視聴・EPG・tuner不足・priority拒否を注入し、他actorの解放で予約録画が取消されないことを検査する。
- [x] **Step 4: 全体GREENを確認する。** Run `go test -count=1 ./...`、`go test -count=1 -shuffle=on ./...`、`go test -count=1 -race -p 1 ./...`、`go vet ./...`、`go mod verify`、`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。CGO無効でlinux／darwin各amd64／arm64をbuildする。command・exit・最終SHAを保存し、実機成功へ読み替えない。
- [x] **Step 5: Commit。** projectionと統合回帰をcommitする。この時点ではfeature PRを統合しない。schema15を含むcandidateにはTask 5の旧版更新helperが必要なため、全体独立review・PR・main CIはその変更もそろってから行う。

## Task 5: 旧版更新試験・公開文書・v1.4.0配布

**Files:** Modify `packaging/lifecycle/{verify.sh,verify_test.sh}`、`.github/workflows/ci.yml`、`docs/operations/update-and-remove.md`、`docs/guides/recording.md`、`docs/reference/compatibility.md`、`docs/troubleshooting.md`。専用release-prepで `cmd/sazanami-dvr/{main,main_test}.go`のversion、`README.md`の版と配布リンク、`CHANGELOG.md`、`packaging/compose/.env.example`を更新する。Dockerfile／release workflowの機構は変更しない。

**Interfaces:**

- LVC2-001のbaselineはv0.5.0 / 0b90ee4d5cdd137c23cdb966649e436db0170ea8のまま維持する。helperはv1.3.6 / b36891cb4016a7d0a7242c3d6b65a380457d1ea1。候補の一段migration制約を緩めない。
- verify.shの通常実行は `baseline-archive helper-archive candidate-archive candidate-sha candidate-version`の五引数へそろえる。preflight-onlyは維持する。helperも既存validate_archiveで版／revision／内容を検査し、今回作るinstall root内へ展開する。
- `advance_to_candidate()`はensure_current helper→candidateの順だけを行い、失敗したら先へ進まない。初回更新と旧backup restore後の再更新で使う。cleanup／purgeの範囲は広げない。

- [x] **Step 1: helper順序の失敗条件を固定する。** verify_test.shでensure_currentをstubし、呼出し順がhelper→candidate、helper失敗時にcandidate未実行、旧baseline SHA・URLが不変であることを確認する。成功時のtraceは二行の`helper`／`candidate`、helper失敗時のtraceは一行の`helper`と非zero終了を要求する。schema13→14→15、restore13→14→15はfresh Ubuntu jobで実行する。
- [x] **Step 2: REDを確認する。** Run `sh packaging/lifecycle/verify_test.sh`。旧scriptがhelperを使わないため順序または入口のassertionで失敗することを記録する。
- [x] **Step 3: scriptとCIを最小更新する。** 公開v0.5.0 archive取得は残し、固定v1.3.6 archive取得を追加する。未検証helper、引数不正、既存resource／port、symlinkは作成・変更前に拒否する。helper用の個別削除や本番用SSHを加えない。
- [x] **Step 4: 更新説明を仕上げる。** schema14→15の停止、事前backupとrestore確認、明示db migrate、CURRENT確認、旧binaryでschema15を開かないことを既存更新ガイドへ記す。schema13の場合は公開v1.3.6で14へ進めてから15へ進む。品質警告・selection_unverifiedと字幕／データ混入、通信partial三reason、Native REST限定の品質、画面再生未確認を簡潔に説明する。READMEの詳細手順は増やさず、更新ガイドへリンクする。natural-japaneseのfull検査で構造・読みやすさ・guide適合を確認する。
- [x] **Step 5: GREENと文書参照を確認する。** Run `sh packaging/install_test.sh`、`sh packaging/lifecycle/verify_test.sh`、`sh packaging/compose/prepare_test.sh`、`git diff --check`。公開文書の参照先と今回の差分を確認してcommitする。Macではsystemd/rootを使うlifecycleを実行しない。
- [x] **Step 6: 全体独立reviewを行う。** spec QRC-001〜012、handoffの必須matrix、旧版更新helper、禁止範囲を最終branchで一回レビューし、指摘を直す。修正した範囲の検証を再実行する。
- [ ] **Step 7: feature PRを統合する。** PRを作成・添付し、PRのtest・container・fresh Ubuntu lifecycle、通常削除の保持、明示purgeの固定範囲を読み戻す。CI成功後だけ統合し、main CIと取り込みSHAを確認する。版は1.3.6のままにする。
- [ ] **Step 8: 専用release-prepを作る。** feature取り込み後のmainから版・CHANGELOG・READMEリンク・Compose imageを1.4.0へそろえる。 `test "$(go run ./cmd/sazanami-dvr --version)" = "sazanami-dvr 1.4.0"`と最終CIを確認してPRを作成・添付・統合する。公開済みtagを動かさない。
- [ ] **Step 9: 配布を読み戻す。** 正確なmain SHAへv1.4.0 tagを作り、Release CI、amd64／arm64 archive、version・revision・CGO・収録file、OCI tag・digest・multiarchを確認する。ユーザー向けchecksum工程や時間指定試験は加えない。
- [ ] **Step 10: 実機担当へ送る。** 公開物のreadback後だけ、ユーザーが指定した別タスク「Ubuntu録画サーバーを移行」へ更新版・main SHA・image／digest・schema14→15・backup／restore・停止時間の配慮・未実施の実機項目を送る。この開発task自身は本番へ接続しない。送信先の内部識別子は公開文書へ載せない。
- [ ] **Step 11: 最終記録。** Handoff0077へ文書同期SHA・最終製品SHA・PR／CI／配布結果・NOT RUNを読み戻し、完了条件がそろった場合だけCompletedへ進める。

## Scope / Authority / Rollback

この計画は採用判断を増やさない。技術手順が採用仕様から逸脱する必要が出たら止め、差分をplanningへ戻す。旧partialを改名して救済しない。新しいcolumn・triggerを使うため、旧binaryへの切戻しは更新前backupの復元が必要である。更新後の予約・履歴を古いbackupで失わない判断は本番担当の別作業とする。

実受信、実クライアント画面、NAS、arm64実機は未実施ならNOT RUN: hardware required。合成TS・loopback・CIが成功しても受信設備や実再生の合格とはしない。

## Execution / Completion record

- Implementation plan review: 2026-10-04にProject ownerが直接承認
- Plan self-review: 2026-10-04に仕様全項目、型・呼出し署名、五つのReview Focus、文量を照合した。旧版更新helperより先にPRを統合する依存順を修正し、main／oneSegの独立性fixtureを具体化した。
- Baseline full test: `4eebb9d`で`go test -count=1 ./...`成功
- Product changes so far: Task 1は`259b910`、Task 2は`a312f12`、Task 3は`5bcce02`。Task 4でNative履歴の品質object、CtrlCmdの既存wire維持、21ケースの確定中断・再起動、視聴・EPGと録画の独立性を確認した。
- New behavior RED / GREEN: 型・品質保存・公開条件の不足、取消しの誤公開、再接続時の停止・終了・件数、補助期限の誤分類、Native履歴の品質不足を再現後に修正。Task 4の最終Go sourceでfull、shuffle、race（`-p 1`）各36 package、vet、module verify、govulncheck、CGO無効のlinux／darwin各amd64／arm64 buildが成功。一回の独立レビュー後、writer待ち中の取消し、壊れたPAT／PMT更新後の選別、fallback後のPSI計測、helperの失敗伝播をRED→GREENで修正し、全36 packageとportable installer／lifecycle／Compose、branch全体のdiff確認が成功。Task 5のPR・公開工程は未完了。
- Feature / release-prep PR、main／Release SHA: UNCREATED
- Production update instruction: NOT SENT（新公開物のreadback後）
