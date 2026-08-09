// Command sazanami-dvrは、明示したサブコマンドだけを実行する小さなプロセス入口である。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	ctrlcmdruntime "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/runtime"
	mirakurunadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/provider/mirakurun"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordingfs"
	recordinghttpadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordinghttp"
	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	webuiadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/webui"
	autoreservationapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogrefresh"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogsync"
	ctrlcmdapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/ctrlcmd"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/liverelay"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/opsui"
	recordingapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	corerecording "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

var (
	version       = "0.0.16"
	productCommit = ""
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), arguments, stdout, stderr)
}

func runContext(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) == 0 {
		fmt.Fprintln(stdout, "Sazanami DVR: 自動的な待受やDB変更は開始しません")
		return 0
	}
	if arguments[0] == "--version" || arguments[0] == "-version" || arguments[0] == "version" {
		fmt.Fprintf(stdout, "sazanami-dvr %s\n", version)
		return 0
	}
	if arguments[0] == "ui" {
		if len(arguments) < 2 || arguments[1] != "serve" {
			fmt.Fprintln(stderr, "使用方法: sazanami-dvr ui serve --data-root <dir> [--listen 127.0.0.1:40772]")
			return 2
		}
		if err := runUICommand(ctx, arguments[2:], stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "UI起動に失敗しました: %v\n", err)
			return 1
		}
		return 0
	}
	if arguments[0] == "catalog" {
		if len(arguments) < 2 || arguments[1] != "sync" {
			fmt.Fprintln(stderr, "使用方法: sazanami-dvr catalog sync --data-root <dir> --provider mirakurun --base-url <url>")
			return 2
		}
		if err := runCatalogSyncCommand(ctx, arguments[2:], stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "カタログ同期に失敗しました: %v\n", err)
			return 1
		}
		return 0
	}
	if arguments[0] == "ctrlcmd" {
		if len(arguments) < 2 || arguments[1] != "validate" && arguments[1] != "serve" {
			fmt.Fprintln(stderr, "使用方法: sazanami-dvr ctrlcmd <validate|serve> --data-root <dir> --channel-map <file>")
			return 2
		}
		if err := runCtrlCmdCommand(ctx, arguments[1], arguments[2:], stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "CtrlCmd操作に失敗しました: %v\n", err)
			return 1
		}
		return 0
	}
	if arguments[0] == "recording" {
		if len(arguments) < 2 || arguments[1] != "serve" {
			fmt.Fprintln(stderr, "使用方法: sazanami-dvr recording serve --data-root <dir> --recording-root <dir> --channel-map <file> --provider mirakurun --base-url <url> [--http-listen 127.0.0.1:40773] [--max-concurrent-recordings 1]")
			return 2
		}
		if err := runRecordingCommand(ctx, arguments[2:], stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "録画プロセスの起動に失敗しました: %v\n", err)
			return 1
		}
		return 0
	}
	if arguments[0] != "db" || len(arguments) < 2 {
		fmt.Fprintln(stderr, "使用方法: sazanami-dvr <catalog|ctrlcmd|db|recording|ui> ...")
		return 2
	}
	if err := runDatabaseCommand(arguments[1], arguments[2:], stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "DB操作に失敗しました: %v\n", err)
		return 1
	}
	return 0
}

func runRecordingCommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("recording serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataRoot := flags.String("data-root", "", "owner-onlyのデータディレクトリ")
	recordingRootPath := flags.String("recording-root", "", "owner-onlyの録画ディレクトリ")
	channelMap := flags.String("channel-map", "", "data root直下のチャンネル設定JSON")
	providerName := flags.String("provider", "", "stream provider")
	baseURL := flags.String("base-url", "", "Mirakurunのoperator設定URL")
	listenAddress := flags.String("listen", ctrlcmdapp.DefaultAddress, "loopback、private IPまたは全interfaceのCtrlCmd待受アドレス")
	httpListenAddress := flags.String("http-listen", recordinghttpadapter.DefaultAddress, "loopback、private IPまたは全interfaceのHTTP待受アドレス")
	refreshInterval := flags.Duration("catalog-refresh-interval", catalogrefresh.DefaultInterval, "番組表を更新する間隔")
	maximumRecordings := flags.Int("max-concurrent-recordings", recordingapp.DefaultMaximumConcurrentRecordings, "同時録画数（1～8）")
	if err := flags.Parse(arguments); err != nil {
		return errorsStable("invalid-command-arguments")
	}
	if *dataRoot == "" || *recordingRootPath == "" || *channelMap == "" || *providerName != "mirakurun" ||
		*baseURL == "" || *refreshInterval < catalogrefresh.MinimumInterval || *refreshInterval > catalogrefresh.MaximumInterval ||
		*maximumRecordings < recordingapp.DefaultMaximumConcurrentRecordings ||
		*maximumRecordings > recordingapp.MaximumConcurrentRecordings || flags.NArg() != 0 {
		return errorsStable("recording-arguments-required")
	}
	config := ctrlcmdapp.RecordingConfig()
	config.Address = *listenAddress
	if err := validateCtrlCmdListen(config); err != nil {
		return errorsStable("local-ctrlcmd-listen-required")
	}
	if err := recordinghttpadapter.ValidateListenAddress(*httpListenAddress, false); err != nil {
		return errorsStable("local-http-listen-required")
	}
	startupContext, startupCancel := context.WithTimeout(ctx, 10*time.Second)
	defer startupCancel()
	inspection, err := sqliteadapter.InspectDatabase(startupContext, *dataRoot)
	if err != nil || inspection.State != sqliteadapter.StateCurrent {
		return errorsStable("current-database-required")
	}
	store, err := sqliteadapter.OpenStore(startupContext, *dataRoot)
	if err != nil {
		return errorsStable("database-owner-unavailable")
	}
	defer store.Close()
	clock := wallClock{}
	if _, err := (catalogsync.RecoveryService{Repository: store, Clock: clock}).Reconcile(startupContext); err != nil {
		return errorsStable("startup-recovery-failed")
	}
	recordingRoot, err := recordingfs.OpenRoot(*recordingRootPath)
	if err != nil {
		return errorsStable("recording-root-unavailable")
	}
	defer recordingRoot.Close()
	snapshot, err := ctrlcmdruntime.BuildSnapshot(startupContext, *dataRoot, *channelMap, store)
	if err != nil {
		return err
	}
	snapshots, err := ctrlcmdruntime.NewSnapshotHolder(snapshot)
	if err != nil {
		return errorsStable("recording-snapshot-failed")
	}
	streamAdapter, err := mirakurunadapter.NewStreamWithLimit(*baseURL, *maximumRecordings)
	if err != nil {
		return errorsStable("provider-configuration-invalid")
	}
	defer streamAdapter.CloseIdleConnections()
	liveStreamAdapter, err := mirakurunadapter.NewStreamWithLimit(*baseURL, liverelay.MaximumSessions)
	if err != nil {
		return errorsStable("provider-configuration-invalid")
	}
	defer liveStreamAdapter.CloseIdleConnections()
	catalogAdapter, err := mirakurunadapter.New(*baseURL)
	if err != nil {
		return errorsStable("provider-configuration-invalid")
	}
	defer catalogAdapter.CloseIdleConnections()
	recordingClock := recordingapp.SystemClock{}
	ownerID, err := catalogmodel.NewID()
	if err != nil {
		return errorsStable("recording-owner-id-generation-failed")
	}
	files := recordingapp.FileOperations{
		CreatePartial: func(plan corerecording.FilePlan) (recordingapp.PartialFile, error) {
			return recordingRoot.CreatePartial(plan)
		},
		LinkFinal: recordingRoot.LinkFinal, SyncDirectory: recordingRoot.SyncDirectory,
		RemovePartial: recordingRoot.RemovePartial,
	}
	executor := recordingapp.Executor{
		Store: store, Stream: streamAdapter, Files: files, Clock: recordingClock,
		NewID: catalogmodel.NewID, OwnerID: ownerID, Generation: 1,
	}
	recovery := recordingapp.Recovery{
		Store: store, Clock: recordingClock,
		Files: recordingapp.RecoveryFiles{FileOperations: files, Inspect: recordingRoot.Inspect},
	}
	if err := recovery.Run(startupContext); err != nil {
		return errorsStable("recording-recovery-failed")
	}
	scheduler, err := recordingapp.NewScheduler(store, executor, recordingClock, *maximumRecordings)
	if err != nil {
		return errorsStable("recording-scheduler-invalid")
	}
	reservations := recordingapp.ReservationService{
		Catalog: snapshots, Store: store, Clock: recordingClock, NewID: catalogmodel.NewID,
		OnAdded: scheduler.Notify, OnStop: scheduler.NotifyStop,
	}
	automaticRules := autoreservationapp.RuleService{Store: store, Clock: recordingClock, NewID: catalogmodel.NewID}
	liveManager, err := liverelay.NewManager(snapshots, liveStreamAdapter)
	if err != nil {
		return errorsStable("live-manager-invalid")
	}
	defer liveManager.CloseAll()
	router, err := ctrlcmdruntime.NewRecordingRouterWithLive(snapshots, reservations, automaticRules, store, liveManager,
		ctrlcmdruntime.SystemClock{}, codec.DefaultLimits())
	if err != nil {
		return errorsStable("recording-router-failed")
	}
	server, err := ctrlcmdapp.NewServer(config, router)
	if err != nil {
		return errorsStable("recording-listener-invalid")
	}
	listener, err := server.Listen()
	if err != nil {
		return errorsStable("recording-listen-failed")
	}
	defer listener.Close()
	serviceContext, serviceCancel := context.WithCancel(ctx)
	defer serviceCancel()
	httpHandler, err := recordinghttpadapter.NewHandler(store, recordingHTTPFiles{root: recordingRoot})
	if err != nil {
		return errorsStable("recording-http-handler-invalid")
	}
	httpServer := recordinghttpadapter.NewServer(*httpListenAddress, httpHandler)
	httpServer.BaseContext = func(net.Listener) context.Context { return serviceContext }
	httpListener, err := (&net.ListenConfig{}).Listen(startupContext, "tcp", *httpListenAddress)
	if err != nil || recordinghttpadapter.ValidateListener(httpListener, false) != nil {
		if httpListener != nil {
			_ = httpListener.Close()
		}
		return errorsStable("recording-http-listen-failed")
	}
	defer httpListener.Close()
	serverDone := make(chan error, 1)
	schedulerDone := make(chan error, 1)
	refreshDone := make(chan error, 1)
	httpDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(serviceContext, listener) }()
	go func() { schedulerDone <- scheduler.Run(serviceContext) }()
	go func() {
		err := httpServer.Serve(httpListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		httpDone <- err
	}()
	refreshOperation := &recordingCatalogRefresh{
		dataRoot: *dataRoot, channelMap: *channelMap, provider: catalogAdapter,
		store: store, holder: snapshots, clock: clock,
		follow: (recordingapp.FollowService{
			Store: store, Clock: recordingClock, OnUpdated: scheduler.Notify,
		}).Run,
		automatic: func(evaluationContext context.Context) (autoreservationapp.Result, error) {
			return (autoreservationapp.Evaluator{
				Store: store, Catalog: snapshots.Load(), Clock: recordingClock, NewID: catalogmodel.NewID,
				IsDuplicate: func(err error) bool { return errors.Is(err, sqliteadapter.ErrAutomaticReservationDuplicate) },
				OnCreated:   scheduler.Notify,
			}).Run(evaluationContext)
		},
		observeAutomatic: observeAutomaticReservation(stdout, stderr),
	}
	refresher := catalogrefresh.Runner{
		Interval: *refreshInterval, Sync: refreshOperation.sync, Observe: observeCatalogRefresh(stdout, stderr),
	}
	go func() { refreshDone <- refresher.Run(serviceContext) }()
	fmt.Fprintf(stdout, "録画プロセスを開始しました: ctrlcmd_scope=%s http_scope=%s services=%d catalog_refresh_interval=%s max_concurrent_recordings=%d\n",
		recordingListenScope(*listenAddress), recordingListenScope(*httpListenAddress), snapshot.Count(), refreshInterval.String(), *maximumRecordings)
	var serverErr, schedulerErr, refreshErr, httpErr error
	serverFinished, schedulerFinished, refreshFinished, httpFinished := false, false, false, false
	select {
	case serverErr = <-serverDone:
		serverFinished = true
	case schedulerErr = <-schedulerDone:
		schedulerFinished = true
	case refreshErr = <-refreshDone:
		refreshFinished = true
	case httpErr = <-httpDone:
		httpFinished = true
	case <-ctx.Done():
	}
	serviceCancel()
	liveManager.CloseAll()
	_ = listener.Close()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = httpServer.Shutdown(shutdownContext)
	shutdownCancel()
	_ = httpListener.Close()
	shutdown := time.NewTimer(30 * time.Second)
	defer shutdown.Stop()
	for !serverFinished || !schedulerFinished || !refreshFinished || !httpFinished {
		select {
		case serverErr = <-serverDone:
			serverFinished = true
		case schedulerErr = <-schedulerDone:
			schedulerFinished = true
		case refreshErr = <-refreshDone:
			refreshFinished = true
		case httpErr = <-httpDone:
			httpFinished = true
		case <-shutdown.C:
			return errorsStable("recording-shutdown-timeout")
		}
	}
	server.Wait()
	if serverErr != nil {
		return errorsStable("recording-listen-failed")
	}
	if schedulerErr != nil {
		return errorsStable("recording-scheduler-failed")
	}
	if refreshErr != nil {
		return errorsStable("recording-catalog-refresh-failed")
	}
	if httpErr != nil {
		return errorsStable("recording-http-listen-failed")
	}
	return nil
}

type recordingHTTPFiles struct{ root *recordingfs.Root }

// OpenFinalは録画保存先adapterの完成fileをHTTP境界の最小interfaceへ渡す。
func (files recordingHTTPFiles) OpenFinal(plan corerecording.FilePlan, size int64) (recordinghttpadapter.FinalFile, error) {
	if files.root == nil {
		return nil, errors.New("recording file root unavailable")
	}
	return files.root.OpenFinal(plan, size)
}

func recordingListenScope(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil && net.ParseIP(host).IsLoopback() {
		return "loopback"
	}
	return "private-lan"
}

func runCtrlCmdCommand(ctx context.Context, command string, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ctrlcmd "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataRoot := flags.String("data-root", "", "owner-onlyのデータディレクトリ")
	channelMap := flags.String("channel-map", "", "data root直下のチャンネル設定JSON")
	listenAddress := ctrlcmdapp.DefaultAddress
	if command == "serve" {
		flags.StringVar(&listenAddress, "listen", ctrlcmdapp.DefaultAddress, "numeric loopbackの待受アドレス")
	}
	if err := flags.Parse(arguments); err != nil {
		return errorsStable("invalid-command-arguments")
	}
	if *dataRoot == "" || *channelMap == "" || flags.NArg() != 0 {
		return errorsStable("ctrlcmd-arguments-required")
	}
	config := ctrlcmdapp.DefaultConfig()
	config.Address = listenAddress
	if command == "serve" {
		if err := validateCtrlCmdListen(config); err != nil {
			return errorsStable("loopback-listen-required")
		}
	}

	startupContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	inspection, err := sqliteadapter.InspectDatabase(startupContext, *dataRoot)
	if err != nil || inspection.State != sqliteadapter.StateCurrent {
		return errorsStable("current-database-required")
	}
	store, err := sqliteadapter.OpenStore(startupContext, *dataRoot)
	if err != nil {
		return errorsStable("database-owner-unavailable")
	}
	defer store.Close()
	snapshot, err := ctrlcmdruntime.BuildSnapshot(startupContext, *dataRoot, *channelMap, store)
	if err != nil {
		return err
	}
	if command == "validate" {
		fmt.Fprintf(stdout, "CtrlCmd設定を確認しました: services=%d\n", snapshot.Count())
		return nil
	}
	router, err := ctrlcmdruntime.NewRouter(snapshot, ctrlcmdruntime.SystemClock{}, codec.DefaultLimits())
	if err != nil {
		return errorsStable("channel-snapshot-failed")
	}
	server, err := ctrlcmdapp.NewServer(config, router)
	if err != nil {
		return errorsStable("channel-listen-failed")
	}
	listener, err := server.Listen()
	if err != nil {
		return errorsStable("channel-listen-failed")
	}
	defer listener.Close()
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(ctx, listener) }()
	fmt.Fprintf(stdout, "CtrlCmd待受をloopback限定で開始しました: services=%d\n", snapshot.Count())
	select {
	case serveErr := <-serveErrors:
		server.Wait()
		if serveErr != nil {
			return errorsStable("channel-listen-failed")
		}
		return nil
	case <-ctx.Done():
		_ = listener.Close()
		serveErr := <-serveErrors
		server.Wait()
		if serveErr != nil {
			return errorsStable("channel-listen-failed")
		}
		return nil
	}
}

func validateCtrlCmdListen(config ctrlcmdapp.Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(config.Address)
	portNumber, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || portNumber < 1 || portNumber > 65_535 {
		return errors.New("invalid CtrlCmd listen address")
	}
	return nil
}

func runCatalogSyncCommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("catalog sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataRoot := flags.String("data-root", "", "owner-onlyのデータディレクトリ")
	providerName := flags.String("provider", "", "catalog provider")
	baseURL := flags.String("base-url", "", "Mirakurunのoperator設定URL")
	if err := flags.Parse(arguments); err != nil {
		return errorsStable("invalid-command-arguments")
	}
	if *dataRoot == "" || *providerName != "mirakurun" || *baseURL == "" || flags.NArg() != 0 {
		return errorsStable("catalog-sync-arguments-required")
	}
	syncContext, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	inspection, err := sqliteadapter.InspectDatabase(syncContext, *dataRoot)
	if err != nil || inspection.State != sqliteadapter.StateCurrent {
		return errorsStable("current-database-required")
	}
	store, err := sqliteadapter.OpenStore(syncContext, *dataRoot)
	if err != nil {
		return errorsStable("database-owner-unavailable")
	}
	defer store.Close()
	clock := wallClock{}
	if _, err := (catalogsync.RecoveryService{Repository: store, Clock: clock}).Reconcile(syncContext); err != nil {
		return errorsStable("startup-recovery-failed")
	}
	adapter, err := mirakurunadapter.New(*baseURL)
	if err != nil {
		return errorsStable("provider-configuration-invalid")
	}
	defer adapter.CloseIdleConnections()

	versionText := "unavailable"
	var reportedVersion *string
	if observed, versionErr := adapter.ObserveVersion(syncContext); versionErr == nil {
		versionText = observed.Current
		reportedVersion = &observed.Current
	}
	identityHash := adapter.IdentityHash()
	backendID := stableBackendID(identityHash)
	correlationID, err := catalogmodel.NewID()
	if err != nil {
		return errorsStable("correlation-id-generation-failed")
	}
	sourceRef := "mirakurun-http-json-v1"
	started := time.Now()
	result, err := (catalogsync.Service{Provider: adapter, Repository: store, Clock: clock}).Sync(syncContext, catalogsync.Request{
		Backend: catalogmodel.Backend{
			ID: backendID, Kind: "MIRAKURUN", IdentityHash: identityHash,
			ReportedVersion: reportedVersion, SourceRef: &sourceRef,
		},
		CorrelationID: correlationID.String(), ServicePageLimit: 256, ProgramPageLimit: 256,
		VerifiedFakeLineage: false,
	})
	if err != nil {
		return errorsStable("catalog-sync-failed")
	}
	durationMS := time.Since(started).Milliseconds()
	fmt.Fprintf(stdout, "backend_id=%s result=completed services=%d programs=%d duration_ms=%d version=%s\n",
		backendID.String(), result.Services, result.Programs, durationMS, strconv.Quote(versionText))
	return nil
}

func stableBackendID(identity [32]byte) catalogmodel.ID {
	var result catalogmodel.ID
	copy(result[:], identity[:16])
	result[6] = (result[6] & 0x0f) | 0x40
	result[8] = (result[8] & 0x3f) | 0x80
	return result
}

func runUICommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ui serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataRoot := flags.String("data-root", "", "owner-onlyのデータディレクトリ")
	listenAddress := flags.String("listen", "127.0.0.1:40772", "numeric loopbackの待受アドレス")
	if err := flags.Parse(arguments); err != nil {
		return errorsStable("invalid-command-arguments")
	}
	if *dataRoot == "" || flags.NArg() != 0 {
		return errorsStable("data-root-required")
	}
	if err := webuiadapter.ValidateListenAddress(*listenAddress, false); err != nil {
		return errorsStable("loopback-listen-required")
	}
	startupContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	inspection, err := sqliteadapter.InspectDatabase(startupContext, *dataRoot)
	if err != nil || inspection.State != sqliteadapter.StateCurrent {
		return errorsStable("current-database-required")
	}
	store, err := sqliteadapter.OpenStore(startupContext, *dataRoot)
	if err != nil {
		return errorsStable("database-owner-unavailable")
	}
	defer store.Close()
	clock := wallClock{}
	if _, err := (catalogsync.RecoveryService{Repository: store, Clock: clock}).Reconcile(startupContext); err != nil {
		return errorsStable("startup-recovery-failed")
	}
	commit := resolvedProductCommit()
	displayCommit := "unavailable"
	if len(commit) == 40 {
		displayCommit = commit[:16]
	}
	application, err := opsui.New(store, &uiBackup{store: store, clock: clock}, clock, opsui.Settings{
		ProductVersion: version, ProductCommit: displayCommit,
		SchemaCurrent: inspection.CurrentVersion, SchemaTarget: inspection.TargetVersion,
		ListenScope: "loopback-only",
	})
	if err != nil {
		return errorsStable("ui-composition-failed")
	}
	handler, err := webuiadapter.NewHandler(application, stderr)
	if err != nil {
		return errorsStable("ui-security-initialization-failed")
	}
	listener, err := (&net.ListenConfig{}).Listen(startupContext, "tcp", *listenAddress)
	if err != nil {
		return errorsStable("loopback-listen-failed")
	}
	defer listener.Close()
	server := webuiadapter.NewServer(*listenAddress, handler)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	fmt.Fprintln(stdout, "Sazanami DVR UI: loopback限定で待受を開始しました")
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errorsStable("ui-server-failed")
	case <-ctx.Done():
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return errorsStable("ui-shutdown-failed")
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errorsStable("ui-server-failed")
		}
		return nil
	}
}

type wallClock struct{}

// Nowはprocessの現在UTC時刻を返す。
func (wallClock) Now() time.Time { return time.Now().UTC() }

type uiBackup struct {
	store *sqliteadapter.Store
	clock wallClock
}

// Createはbuild provenanceを検証して既存Storeのonline backupを実行する。
func (backup *uiBackup) Create(ctx context.Context, started time.Time) (opsui.BackupResult, error) {
	commit := resolvedProductCommit()
	if commit == "" {
		return opsui.BackupResult{}, errorsStable("product-commit-unavailable")
	}
	id, err := catalogmodel.NewID()
	if err != nil {
		return opsui.BackupResult{}, errorsStable("backup-id-generation-failed")
	}
	manifest, err := backup.store.CreateBackup(ctx, sqliteadapter.BackupRequest{
		ID: id, Purpose: "manual", StartedAt: started, ProductVersion: version,
		ProductCommit: commit, Now: backup.clock.Now,
	})
	if err != nil {
		return opsui.BackupResult{}, errorsStable("backup-failed")
	}
	return opsui.BackupResult{ID: manifest.BackupID, State: manifest.State, SchemaVersion: manifest.SchemaVersion}, nil
}

func runDatabaseCommand(command string, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("db "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataRoot := flags.String("data-root", "", "owner-onlyのデータディレクトリ")
	backupIDText := flags.String("backup-id", "", "復元するbackup UUID")
	operationIDText := flags.String("operation-id", "", "復旧するrestore operation UUID")
	if err := flags.Parse(arguments); err != nil {
		return errorsStable("invalid-command-arguments")
	}
	if *dataRoot == "" || flags.NArg() != 0 {
		return errorsStable("data-root-required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	switch command {
	case "status":
		inspection, err := sqliteadapter.InspectDatabase(ctx, *dataRoot)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "state=%s current=%d target=%d", inspection.State, inspection.CurrentVersion, inspection.TargetVersion)
		if inspection.Reason != "" {
			fmt.Fprintf(stdout, " reason=%s", inspection.Reason)
		}
		fmt.Fprintln(stdout)
		return nil
	case "migrate":
		backupID, err := catalogmodel.NewID()
		if err != nil {
			return errorsStable("backup-id-generation-failed")
		}
		result, err := sqliteadapter.MigrateDatabaseWithBackup(ctx, *dataRoot, sqliteadapter.MigrationRequest{
			AppliedAt: time.Now().UTC(), BackupID: backupID, ProductVersion: version,
			ProductCommit: resolvedProductCommit(), Now: time.Now,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "state=%s schema=%d", result.Inspection.State, result.Inspection.CurrentVersion)
		if result.Backup != nil {
			fmt.Fprintf(stdout, " backup_id=%s", result.Backup.BackupID)
		}
		fmt.Fprintln(stdout)
		return nil
	case "backup":
		commit := resolvedProductCommit()
		if commit == "" {
			return errorsStable("product-commit-unavailable")
		}
		store, err := sqliteadapter.OpenStore(ctx, *dataRoot)
		if err != nil {
			return err
		}
		defer store.Close()
		id, err := catalogmodel.NewID()
		if err != nil {
			return errorsStable("backup-id-generation-failed")
		}
		started := time.Now().UTC()
		manifest, err := store.CreateBackup(ctx, sqliteadapter.BackupRequest{
			ID: id, Purpose: "manual", StartedAt: started, ProductVersion: version,
			ProductCommit: commit, Now: time.Now,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "backup_id=%s state=%s schema=%d\n", manifest.BackupID, manifest.State, manifest.SchemaVersion)
		return nil
	case "restore":
		backupID, err := catalogmodel.ParseID(*backupIDText)
		if err != nil {
			return errorsStable("valid-backup-id-required")
		}
		manifest, err := sqliteadapter.FindBackupManifest(*dataRoot, backupID)
		if err != nil {
			return err
		}
		operationID, err := catalogmodel.NewID()
		if err != nil {
			return errorsStable("restore-id-generation-failed")
		}
		operation, err := sqliteadapter.RestoreOffline(ctx, *dataRoot, sqliteadapter.RestoreRequest{
			OperationID: operationID, BackupManifest: manifest, CreatedAt: time.Now().UTC(), Now: time.Now,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "operation_id=%s phase=%s\n", operation.OperationID, operation.Phase)
		return nil
	case "recover":
		operationID, err := catalogmodel.ParseID(*operationIDText)
		if err != nil {
			return errorsStable("valid-operation-id-required")
		}
		operation, err := sqliteadapter.RecoverRestoreOffline(ctx, *dataRoot,
			sqliteadapter.RestoreOperationBasename(operationID), time.Now)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "operation_id=%s phase=%s\n", operation.OperationID, operation.Phase)
		return nil
	default:
		return errorsStable("unknown-db-command")
	}
}

type stableError string

// Errorはprivate pathやraw driver errorを含まないstable reasonを返す。
func (err stableError) Error() string { return string(err) }

func errorsStable(reason string) error { return stableError(reason) }

func resolvedProductCommit() string {
	if len(productCommit) == 40 {
		return productCommit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	revision := ""
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if modified || len(revision) != 40 {
		return ""
	}
	for _, character := range revision {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return ""
		}
	}
	return revision
}
