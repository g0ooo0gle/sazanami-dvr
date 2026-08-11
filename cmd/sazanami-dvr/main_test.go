package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/provider/fake"
	mirakurunadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/provider/mirakurun"
	recordinghttpadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordinghttp"
	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	webuiadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/webui"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogsync"
	ctrlcmdapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/ctrlcmd"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providercatalog "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

func TestVersion(t *testing.T) {
	for _, argument := range []string{"--version", "-version", "version"} {
		t.Run(argument, func(t *testing.T) {
			var output, diagnostic bytes.Buffer
			if code := run([]string{argument}, &output, &diagnostic); code != 0 {
				t.Fatalf("code=%d err=%q", code, diagnostic.String())
			}
			if got, want := output.String(), "sazanami-dvr 0.1.2\n"; got != want {
				t.Fatalf("version=%q want=%q", got, want)
			}
		})
	}
}

func TestDefaultListenerAddressesAndLegacyExplicitPorts(t *testing.T) {
	if ctrlcmdapp.DefaultAddress != "0.0.0.0:4520" ||
		recordinghttpadapter.DefaultAddress != "127.0.0.1:4521" ||
		defaultWebUIAddress != "127.0.0.1:4522" {
		t.Fatalf("defaults ctrlcmd=%q http=%q webui=%q", ctrlcmdapp.DefaultAddress, recordinghttpadapter.DefaultAddress, defaultWebUIAddress)
	}
	ctrlcmdConfig := ctrlcmdapp.DefaultConfig()
	ctrlcmdConfig.Address = "127.0.0.1:4510"
	if err := ctrlcmdConfig.Validate(); err != nil {
		t.Fatalf("legacy CtrlCmd port rejected: %v", err)
	}
	if err := recordinghttpadapter.ValidateListenAddress("127.0.0.1:40773", false); err != nil {
		t.Fatalf("legacy recording HTTP port rejected: %v", err)
	}
	if err := webuiadapter.ValidateListenAddress("127.0.0.1:40772", false); err != nil {
		t.Fatalf("legacy WebUI port rejected: %v", err)
	}
	for _, test := range []struct {
		arguments []string
		current   string
		legacy    string
	}{
		{[]string{"ui"}, defaultWebUIAddress, "127.0.0.1:40772"},
		{[]string{"recording"}, recordinghttpadapter.DefaultAddress, "127.0.0.1:40773"},
	} {
		var output, diagnostic bytes.Buffer
		if code := run(test.arguments, &output, &diagnostic); code != 2 ||
			!strings.Contains(diagnostic.String(), test.current) || strings.Contains(diagnostic.String(), test.legacy) {
			t.Fatalf("arguments=%v code=%d usage=%q", test.arguments, code, diagnostic.String())
		}
	}
}

func TestDatabaseCommandTimeout(t *testing.T) {
	for _, command := range []string{"migrate", "backup", "restore", "recover"} {
		if got := databaseCommandTimeout(command); got != time.Hour {
			t.Fatalf("command=%s timeout=%s", command, got)
		}
	}
	for _, command := range []string{"status", "unknown"} {
		if got := databaseCommandTimeout(command); got != 10*time.Second {
			t.Fatalf("command=%s timeout=%s", command, got)
		}
	}
}

func TestDatabaseCommandUsesParentContext(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runDatabaseCommand(context.Background(), "migrate", []string{"--data-root", root}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runDatabaseCommand(ctx, "status", []string{"--data-root", root}, io.Discard, io.Discard); err == nil {
		t.Fatal("cancelled parent context was ignored")
	}
}

func TestRunRestrictsProcessFileCreation(t *testing.T) {
	previous := syscall.Umask(0o002)
	t.Cleanup(func() { syscall.Umask(previous) })
	var output, diagnostic bytes.Buffer
	if code := run([]string{"--version"}, &output, &diagnostic); code != 0 {
		t.Fatalf("code=%d err=%q", code, diagnostic.String())
	}
	if got := syscall.Umask(0o077); got != 0o077 {
		t.Fatalf("umask=%03o", got)
	}
}

func TestDatabaseOperatorJourney(t *testing.T) {
	previousCommit := productCommit
	productCommit = strings.Repeat("f", 40)
	t.Cleanup(func() { productCommit = previousCommit })
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	if code := run([]string{"db", "status", "--data-root", root}, &output, &diagnostic); code != 0 || !strings.Contains(output.String(), "state=EMPTY") {
		t.Fatalf("status code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	output.Reset()
	diagnostic.Reset()
	if code := run([]string{"db", "migrate", "--data-root", root}, &output, &diagnostic); code != 0 || !strings.Contains(output.String(), "state=CURRENT") {
		t.Fatalf("migrate code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	output.Reset()
	diagnostic.Reset()
	if code := run([]string{"db", "backup", "--data-root", root}, &output, &diagnostic); code != 0 {
		t.Fatalf("backup code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	fields := strings.Fields(output.String())
	if len(fields) < 1 || !strings.HasPrefix(fields[0], "backup_id=") {
		t.Fatalf("backup output=%q", output.String())
	}
	backupID := strings.TrimPrefix(fields[0], "backup_id=")
	if _, err := catalogmodel.ParseID(backupID); err != nil {
		t.Fatal(err)
	}

	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	backendID, err := catalogmodel.NewIDFrom(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	harness := operatorJourneyHarness(t)
	clock := &operatorClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	if _, err := (catalogsync.Service{Provider: harness.Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), catalogsync.Request{
			Backend:       catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:operator-journey"))},
			CorrelationID: "operator-journey", ServicePageLimit: 16, ProgramPageLimit: 16, VerifiedFakeLineage: true,
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	diagnostic.Reset()
	if code := run([]string{"db", "restore", "--data-root", root, "--backup-id", backupID}, &output, &diagnostic); code != 0 || !strings.Contains(output.String(), "phase=COMMITTED") {
		t.Fatalf("restore code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	restored, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	programs, err := restored.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 0 {
		t.Fatalf("restored programs=%+v err=%v", programs, err)
	}
}

func operatorJourneyHarness(t *testing.T) *fake.Harness {
	t.Helper()
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	duration := 30 * time.Minute
	scenario, err := fake.NewScenario(fake.Config{
		ID: "operator-journey", Seed: 1, WallTime: start, InitialNanos: 1,
		ServicePages: []providercatalog.ServicePage{{Items: []providercatalog.ServiceObservation{{
			Provenance: provider.Provenance{Backend: "fake", Revision: "1"}, Locator: "service:1",
			Broadcast: "GR", NetworkID: 1, ServiceID: 2, DisplayName: "合成サービス",
			TuningTarget: provider.TuningTarget{Opaque: "target:1"}, Validation: provider.ValidationValid,
		}}, End: true}},
		ProgramPages: []providercatalog.ProgramPage{{Items: []providercatalog.ProgramObservation{{
			Provenance: provider.Provenance{Backend: "fake", Revision: "1"}, ServiceLocator: "service:1",
			EventLocator: "event:1", Start: &start, Duration: &duration, Title: "合成番組",
			Validation: provider.ValidationValid,
		}}, End: true}},
		Expected: fake.ExpectedRequests{ServiceLimit: 16, ProgramLimit: 16}, HistoryLimit: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	harness, err := fake.NewHarness(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

type operatorClock struct{ now time.Time }

func (clock *operatorClock) Now() time.Time {
	value := clock.now
	clock.now = clock.now.Add(time.Second)
	return value
}

func TestCommandDoesNotStartListenerOrAcceptUnsafeArguments(t *testing.T) {
	var output, diagnostic bytes.Buffer
	if code := run(nil, &output, &diagnostic); code != 0 || !strings.Contains(output.String(), "自動的な待受やDB変更は開始しません") {
		t.Fatalf("code=%d out=%q", code, output.String())
	}
	output.Reset()
	diagnostic.Reset()
	if code := run([]string{"db", "restore", "--data-root", "/tmp", "--backup-id", "invalid"}, &output, &diagnostic); code != 1 || strings.Contains(diagnostic.String(), "/tmp") {
		t.Fatalf("code=%d diagnostic=%q", code, diagnostic.String())
	}
}

func TestCtrlCmdValidateServeRestartAndRollback(t *testing.T) {
	root := migratedRoot(t)
	backendID := seedCtrlCmdCatalog(t, root)
	channelMap := filepath.Join(root, "channels.json")
	validDocument := fmt.Sprintf(`{"format":"sazanami-channel-map-v1","backend_id":%q,"services":[{"provider_locator":"service:ctrlcmd","network_id":1,"service_id":3,"transport_stream_id":2,"provider_name":"","network_name":"合成ネットワーク","transport_stream_name":"合成TS","remote_control_key_id":1,"partial_reception":false,"epg_capture":true,"search":true}]}`, backendID.String())
	if err := os.WriteFile(channelMap, []byte(validDocument), 0o600); err != nil {
		t.Fatal(err)
	}

	var output, diagnostic bytes.Buffer
	validateArguments := []string{"ctrlcmd", "validate", "--data-root", root, "--channel-map", channelMap}
	if code := run(validateArguments, &output, &diagnostic); code != 0 || !strings.Contains(output.String(), "services=1") {
		t.Fatalf("validate code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	for _, private := range []string{root, channelMap, backendID.String(), "service:ctrlcmd"} {
		if strings.Contains(output.String(), private) || strings.Contains(diagnostic.String(), private) {
			t.Fatalf("validate出力に非公開値が含まれます: %q", private)
		}
	}

	address := unusedLoopbackAddress(t)
	for cycle := 0; cycle < 2; cycle++ {
		ctx, cancel := context.WithCancel(context.Background())
		serveOutput := newNotifyingWriter()
		serveDiagnostic := newNotifyingWriter()
		done := make(chan int, 1)
		go func() {
			done <- runContext(ctx, []string{"ctrlcmd", "serve", "--data-root", root, "--channel-map", channelMap, "--listen", address}, serveOutput, serveDiagnostic)
		}()
		select {
		case <-serveOutput.written:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatalf("CtrlCmd did not start, diagnostic=%q", serveDiagnostic.String())
		}
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		request := make([]byte, 8)
		binary.LittleEndian.PutUint32(request[:4], 1021)
		if _, err := connection.Write(request); err != nil {
			cancel()
			t.Fatal(err)
		}
		header := make([]byte, 8)
		if _, err := io.ReadFull(connection, header); err != nil {
			cancel()
			t.Fatal(err)
		}
		declared := binary.LittleEndian.Uint32(header[4:8])
		body := make([]byte, declared)
		if _, err := io.ReadFull(connection, body); err != nil {
			cancel()
			t.Fatal(err)
		}
		_ = connection.Close()
		if binary.LittleEndian.Uint32(header[:4]) != 1 || declared == 0 {
			cancel()
			t.Fatalf("response header=%x", header)
		}
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("shutdown code=%d diagnostic=%q", code, serveDiagnostic.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("CtrlCmd graceful shutdown timed out")
		}
		for _, private := range []string{root, channelMap, backendID.String(), "service:ctrlcmd"} {
			if strings.Contains(serveOutput.String(), private) || strings.Contains(serveDiagnostic.String(), private) {
				t.Fatalf("serve出力に非公開値が含まれます: %q", private)
			}
		}
	}

	invalidDocument := strings.Replace(validDocument, `"network_id":1`, `"network_id":9`, 1)
	if err := os.WriteFile(channelMap, []byte(invalidDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	diagnostic.Reset()
	if code := run(validateArguments, &output, &diagnostic); code != 1 || !strings.Contains(diagnostic.String(), "channel-service-mismatch") {
		t.Fatalf("invalid code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	if err := os.WriteFile(channelMap, []byte(validDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	diagnostic.Reset()
	if code := run(validateArguments, &output, &diagnostic); code != 0 {
		t.Fatalf("rollback code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
}

func TestCtrlCmdRejectsInvalidConfigAndOwnerLockBeforeListen(t *testing.T) {
	root := migratedRoot(t)
	backendID := seedCtrlCmdCatalog(t, root)
	channelMap := filepath.Join(root, "channels.json")
	document := fmt.Sprintf(`{"format":"sazanami-channel-map-v1","backend_id":%q,"services":[]}`, backendID.String())
	if err := os.WriteFile(channelMap, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	address := unusedLoopbackAddress(t)
	var output, diagnostic bytes.Buffer
	arguments := []string{"ctrlcmd", "serve", "--data-root", root, "--channel-map", channelMap, "--listen", address}
	if code := runContext(context.Background(), arguments, &output, &diagnostic); code != 1 || !strings.Contains(diagnostic.String(), "channel-map-count") {
		t.Fatalf("invalid code=%d diagnostic=%q", code, diagnostic.String())
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("設定失敗後にlistenerが残っています: %v", err)
	}
	_ = listener.Close()

	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	output.Reset()
	diagnostic.Reset()
	if code := run([]string{"ctrlcmd", "validate", "--data-root", root, "--channel-map", channelMap}, &output, &diagnostic); code != 1 || !strings.Contains(diagnostic.String(), "database-owner-unavailable") {
		t.Fatalf("owner lock code=%d diagnostic=%q", code, diagnostic.String())
	}
}

func TestCtrlCmdRejectsUnsafeListenBeforeDatabase(t *testing.T) {
	for _, address := range []string{"192.0.2.39:4510", "169.254.1.39:4510", "224.0.0.1:4510", "localhost:4510", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:not-a-port"} {
		var output, diagnostic bytes.Buffer
		code := runContext(context.Background(), []string{
			"ctrlcmd", "serve", "--data-root", "/private/not-for-output", "--channel-map", "/private/not-for-output/channels.json", "--listen", address,
		}, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "local-ctrlcmd-listen-required") || strings.Contains(diagnostic.String(), "/private") {
			t.Fatalf("address=%q code=%d diagnostic=%q", address, code, diagnostic.String())
		}
	}
}

func TestCtrlCmdAcceptsLANListenBeforeDatabase(t *testing.T) {
	for _, address := range []string{"0.0.0.0:4510", "[::]:4510", "10.254.254.39:4510", "[fd00::39]:4510"} {
		var output, diagnostic bytes.Buffer
		code := runContext(context.Background(), []string{
			"ctrlcmd", "serve", "--data-root", "/private/not-for-output", "--channel-map", "/private/not-for-output/channels.json", "--listen", address,
		}, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "current-database-required") || strings.Contains(diagnostic.String(), "/private") {
			t.Fatalf("address=%q code=%d diagnostic=%q", address, code, diagnostic.String())
		}
	}
}

func TestCtrlCmdLANNoticeDoesNotExposeAddress(t *testing.T) {
	var output bytes.Buffer
	writeCtrlCmdLANNotice(&output, "10.254.254.39:4510")
	text := output.String()
	if strings.Count(text, "認証なし") != 1 || !strings.Contains(text, "127.0.0.1:4520") || strings.Contains(text, "10.254.254.39") {
		t.Fatalf("notice=%q", text)
	}
	output.Reset()
	writeCtrlCmdLANNotice(&output, "127.0.0.1:4510")
	if output.Len() != 0 {
		t.Fatalf("loopback notice=%q", output.String())
	}
}

func TestRecordingServeRefreshesCatalogWithoutOpeningStreamBeforeReservationTime(t *testing.T) {
	root := migratedRoot(t)
	backendID := seedCtrlCmdCatalog(t, root)
	channelMap := filepath.Join(root, "channels.json")
	document := fmt.Sprintf(`{"format":"sazanami-channel-map-v1","backend_id":%q,"services":[{"provider_locator":"service:ctrlcmd","network_id":1,"service_id":3,"transport_stream_id":2,"provider_name":"","network_name":"合成ネットワーク","transport_stream_name":"合成TS","remote_control_key_id":1,"partial_reception":false,"epg_capture":true,"search":true}]}`, backendID.String())
	if err := os.WriteFile(channelMap, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	recordingRoot := filepath.Join(t.TempDir(), "recordings")
	var tunerCalls, catalogCalls, streamCalls atomic.Int32
	var calledOnce sync.Once
	catalogCalled := make(chan struct{})
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tuners" {
			tunerCalls.Add(1)
			writeCommandJSON(writer, `[{},{}]`)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/stream") {
			streamCalls.Add(1)
			return
		}
		catalogCalls.Add(1)
		calledOnce.Do(func() { close(catalogCalled) })
		http.Error(writer, "synthetic", http.StatusServiceUnavailable)
	}))
	defer providerServer.Close()
	address := unusedLoopbackAddress(t)
	httpAddress := unusedLoopbackAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	output := newNotifyingWriter()
	diagnostic := newNotifyingWriter()
	done := make(chan int, 1)
	arguments := []string{
		"recording", "serve", "--data-root", root, "--recording-root", recordingRoot,
		"--channel-map", channelMap, "--provider", "mirakurun", "--base-url", providerServer.URL,
		"--listen", address, "--http-listen", httpAddress,
	}
	go func() { done <- runContext(ctx, arguments, output, diagnostic) }()
	select {
	case <-output.written:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("録画processが起動しませんでした: %q", diagnostic.String())
	}
	select {
	case <-catalogCalled:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("起動直後の番組表更新が始まりませんでした")
	}
	if tunerCalls.Load() != 1 || catalogCalls.Load() == 0 || streamCalls.Load() != 0 {
		cancel()
		t.Fatalf("tuners=%d catalog=%d stream=%d", tunerCalls.Load(), catalogCalls.Load(), streamCalls.Load())
	}
	if !strings.Contains(output.String(), "max_concurrent_recordings=2 max_concurrent_source=mirakurun") {
		cancel()
		t.Fatalf("Mirakurunのチューナー数が出力されませんでした: %q", output.String())
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request := make([]byte, 10)
	binary.LittleEndian.PutUint32(request[0:4], 2011)
	binary.LittleEndian.PutUint32(request[4:8], 2)
	binary.LittleEndian.PutUint16(request[8:10], 5)
	if _, err := connection.Write(request); err != nil {
		cancel()
		t.Fatal(err)
	}
	header := make([]byte, 8)
	if _, err := io.ReadFull(connection, header); err != nil {
		cancel()
		t.Fatal(err)
	}
	body := make([]byte, binary.LittleEndian.Uint32(header[4:8]))
	if _, err := io.ReadFull(connection, body); err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = connection.Close()
	if binary.LittleEndian.Uint32(header[0:4]) != 1 || len(body) < 2 || binary.LittleEndian.Uint16(body[0:2]) != 5 {
		cancel()
		t.Fatalf("response header=%x body=%x", header, body)
	}
	if streamCalls.Load() != 0 {
		cancel()
		t.Fatalf("予約一覧取得でstreamへ%d回接続しました", streamCalls.Load())
	}
	httpResponse, err := http.Get("http://" + httpAddress + "/api/recordings")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	responseBytes, readErr := io.ReadAll(httpResponse.Body)
	closeErr := httpResponse.Body.Close()
	if readErr != nil || closeErr != nil || httpResponse.StatusCode != http.StatusOK ||
		!bytes.Contains(responseBytes, []byte(`"recordings":[]`)) {
		cancel()
		t.Fatalf("history status=%d body=%s read=%v close=%v", httpResponse.StatusCode, responseBytes, readErr, closeErr)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code=%d diagnostic=%q", code, diagnostic.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("録画processの終了がtimeoutしました")
	}
	postDirectory, err := os.Lstat(filepath.Join(root, "post-recording-scripts"))
	if err != nil || !postDirectory.IsDir() || postDirectory.Mode().Perm() != 0o700 {
		t.Fatalf("post recording directory=%v err=%v", postDirectory, err)
	}
	for _, private := range []string{root, recordingRoot, channelMap, providerServer.URL, httpAddress, backendID.String(), "service:ctrlcmd"} {
		if strings.Contains(output.String(), private) || strings.Contains(diagnostic.String(), private) {
			t.Fatalf("録画processの出力に非公開値が含まれます: %q", private)
		}
	}
}

func TestRecordingServeRejectsUnsafeListenBeforeOpeningRoots(t *testing.T) {
	var output, diagnostic bytes.Buffer
	private := "/private/not-for-output"
	code := runContext(context.Background(), []string{
		"recording", "serve", "--data-root", private, "--recording-root", private + "/recordings",
		"--channel-map", private + "/channels.json", "--provider", "mirakurun",
		"--base-url", "http://127.0.0.1:40773", "--listen", "203.0.113.39:4510",
	}, &output, &diagnostic)
	if code != 1 || !strings.Contains(diagnostic.String(), "local-ctrlcmd-listen-required") || strings.Contains(diagnostic.String(), private) {
		t.Fatalf("code=%d diagnostic=%q", code, diagnostic.String())
	}
}

func TestRecordingServeAcceptsLANAddressesBeforeOpeningRoots(t *testing.T) {
	for _, addresses := range [][2]string{{"10.254.254.39:4510", "10.254.254.39:40773"}, {"0.0.0.0:4510", "0.0.0.0:40773"}} {
		var output, diagnostic bytes.Buffer
		private := "/private/not-for-output"
		code := runContext(context.Background(), []string{
			"recording", "serve", "--data-root", private, "--recording-root", private + "/recordings",
			"--channel-map", private + "/channels.json", "--provider", "mirakurun",
			"--base-url", "http://127.0.0.1:40773", "--listen", addresses[0], "--http-listen", addresses[1],
		}, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "current-database-required") || strings.Contains(diagnostic.String(), private) {
			t.Fatalf("addresses=%v code=%d diagnostic=%q", addresses, code, diagnostic.String())
		}
	}
}

func TestRecordingServeValidatesCatalogRefreshIntervalBeforeOpeningRoots(t *testing.T) {
	base := []string{
		"recording", "serve", "--data-root", "/private/not-for-output",
		"--recording-root", "/private/not-for-output/recordings",
		"--channel-map", "/private/not-for-output/channels.json", "--provider", "mirakurun",
		"--base-url", "http://127.0.0.1:40773", "--listen", "127.0.0.1:4510",
	}
	for _, interval := range []string{"4m59.999999999s", "24h0m0.000000001s"} {
		var output, diagnostic bytes.Buffer
		arguments := append(append([]string(nil), base...), "--catalog-refresh-interval", interval)
		code := runContext(context.Background(), arguments, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "recording-arguments-required") ||
			strings.Contains(diagnostic.String(), "/private") {
			t.Fatalf("interval=%q code=%d diagnostic=%q", interval, code, diagnostic.String())
		}
	}
	for _, interval := range []string{"5m", "24h"} {
		var output, diagnostic bytes.Buffer
		arguments := append(append([]string(nil), base...), "--catalog-refresh-interval", interval)
		code := runContext(context.Background(), arguments, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "current-database-required") ||
			strings.Contains(diagnostic.String(), "/private") {
			t.Fatalf("interval=%q code=%d diagnostic=%q", interval, code, diagnostic.String())
		}
	}
}

func TestRecordingServeAcceptsExtensionOnlyFollowFlagBeforeOpeningRoots(t *testing.T) {
	base := []string{
		"recording", "serve", "--data-root", "/private/not-for-output",
		"--recording-root", "/private/not-for-output/recordings",
		"--channel-map", "/private/not-for-output/channels.json", "--provider", "mirakurun",
		"--base-url", "http://127.0.0.1:40773", "--listen", "127.0.0.1:4510",
	}
	var output, diagnostic bytes.Buffer
	arguments := append(append([]string(nil), base...), "--active-follow-extension-only")
	if code := runContext(context.Background(), arguments, &output, &diagnostic); code != 1 ||
		!strings.Contains(diagnostic.String(), "current-database-required") || strings.Contains(diagnostic.String(), "/private") {
		t.Fatalf("code=%d diagnostic=%q", code, diagnostic.String())
	}
	output.Reset()
	diagnostic.Reset()
	arguments = append(append([]string(nil), base...), "--active-follow-extension-only=invalid")
	if code := runContext(context.Background(), arguments, &output, &diagnostic); code != 1 ||
		!strings.Contains(diagnostic.String(), "invalid-command-arguments") || strings.Contains(diagnostic.String(), "/private") {
		t.Fatalf("code=%d diagnostic=%q", code, diagnostic.String())
	}
}

func TestRecordingServeValidatesConcurrentRecordingLimitBeforeOpeningRoots(t *testing.T) {
	var tunerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tunerCalls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/tuners" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("request=%s %s accept=%q", request.Method, request.URL.Path, request.Header.Get("Accept"))
		}
		writeCommandJSON(writer, `[{}]`)
	}))
	defer server.Close()
	base := []string{
		"recording", "serve", "--data-root", "/private/not-for-output",
		"--recording-root", "/private/not-for-output/recordings",
		"--channel-map", "/private/not-for-output/channels.json", "--provider", "mirakurun",
		"--base-url", server.URL, "--listen", "127.0.0.1:4510",
	}
	for _, maximum := range []string{"-1", "0"} {
		var output, diagnostic bytes.Buffer
		arguments := append(append([]string(nil), base...), "--max-concurrent-recordings", maximum)
		code := runContext(context.Background(), arguments, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "recording-arguments-required") ||
			strings.Contains(diagnostic.String(), "/private") {
			t.Fatalf("maximum=%q code=%d diagnostic=%q", maximum, code, diagnostic.String())
		}
	}
	if tunerCalls.Load() != 0 {
		t.Fatalf("不正な明示値で%d回接続しました", tunerCalls.Load())
	}
	for _, maximum := range []string{"1", "8", "9", "19", "20", "21", "1000000000"} {
		var output, diagnostic bytes.Buffer
		arguments := append(append([]string(nil), base...), "--max-concurrent-recordings", maximum)
		code := runContext(context.Background(), arguments, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "current-database-required") ||
			strings.Contains(diagnostic.String(), "/private") {
			t.Fatalf("maximum=%q code=%d diagnostic=%q", maximum, code, diagnostic.String())
		}
		if maximum == "20" || maximum == "21" || maximum == "1000000000" {
			if strings.Count(diagnostic.String(), "注意: 同時録画数が20件以上です") != 1 {
				t.Fatalf("maximum=%q warning=%q", maximum, diagnostic.String())
			}
		}
	}
	if tunerCalls.Load() != 0 {
		t.Fatalf("正常な明示値で%d回接続しました", tunerCalls.Load())
	}
	var output, diagnostic bytes.Buffer
	arguments := append(append([]string(nil), base...), "--max-concurrent-recordings", "not-a-number")
	code := runContext(context.Background(), arguments, &output, &diagnostic)
	if code != 1 || !strings.Contains(diagnostic.String(), "invalid-command-arguments") ||
		strings.Contains(diagnostic.String(), "/private") {
		t.Fatalf("non-number code=%d diagnostic=%q", code, diagnostic.String())
	}
	for _, invalid := range [][]string{
		append(append([]string(nil), base...), "--max-concurrent-recordings", "999999999999999999999999"),
		append(append([]string(nil), base...), "unexpected"),
	} {
		output.Reset()
		diagnostic.Reset()
		if code := runContext(context.Background(), invalid, &output, &diagnostic); code != 1 || tunerCalls.Load() != 0 {
			t.Fatalf("arguments=%v code=%d requests=%d diagnostic=%q", invalid[len(base):], code, tunerCalls.Load(), diagnostic.String())
		}
	}
	output.Reset()
	diagnostic.Reset()
	if code := runContext(context.Background(), base, &output, &diagnostic); code != 1 ||
		!strings.Contains(diagnostic.String(), "current-database-required") || tunerCalls.Load() != 1 {
		t.Fatalf("auto code=%d requests=%d diagnostic=%q", code, tunerCalls.Load(), diagnostic.String())
	}
}

func TestSelectRecordingMaximumWarnsFallsBackAndHonorsCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeCommandJSON(writer, "["+strings.Repeat("{},", 20)+"{}]")
	}))
	defer server.Close()
	adapter, err := mirakurunadapter.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	var diagnostic bytes.Buffer
	maximum, source, err := selectRecordingMaximum(context.Background(), adapter, 1, false, &diagnostic)
	if err != nil || maximum != 21 || source != "mirakurun" || calls.Load() != 1 ||
		strings.Count(diagnostic.String(), "注意: 同時録画数が20件以上です") != 1 {
		t.Fatalf("maximum=%d source=%s calls=%d diagnostic=%q err=%v", maximum, source, calls.Load(), diagnostic.String(), err)
	}

	diagnostic.Reset()
	maximum, source, err = selectRecordingMaximum(context.Background(), adapter, 20, true, &diagnostic)
	if err != nil || maximum != 20 || source != "explicit" || calls.Load() != 1 ||
		strings.Count(diagnostic.String(), "注意: 同時録画数が20件以上です") != 1 {
		t.Fatalf("explicit maximum=%d source=%s calls=%d diagnostic=%q err=%v", maximum, source, calls.Load(), diagnostic.String(), err)
	}

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeCommandJSON(writer, `[]`)
	}))
	defer fallbackServer.Close()
	fallbackAdapter, err := mirakurunadapter.New(fallbackServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer fallbackAdapter.CloseIdleConnections()
	diagnostic.Reset()
	maximum, source, err = selectRecordingMaximum(context.Background(), fallbackAdapter, 1, false, &diagnostic)
	if err != nil || maximum != 1 || source != "fallback" ||
		strings.Count(diagnostic.String(), "tuner-count-fallback") != 1 || strings.Contains(diagnostic.String(), fallbackServer.URL) {
		t.Fatalf("fallback maximum=%d source=%s diagnostic=%q err=%v", maximum, source, diagnostic.String(), err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	diagnostic.Reset()
	if _, _, err := selectRecordingMaximum(cancelled, adapter, 1, false, &diagnostic); err == nil ||
		err.Error() != "recording-startup-cancelled" || calls.Load() != 1 {
		t.Fatalf("cancel error=%v calls=%d diagnostic=%q", err, calls.Load(), diagnostic.String())
	}
}

func seedCtrlCmdCatalog(t *testing.T, root string) catalogmodel.ID {
	t.Helper()
	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backendID, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{1}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	syncID, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{2}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	networkID, serviceID := int64(1), int64(3)
	serviceType := "1"
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "MIRAKURUN", IdentityHash: sha256.Sum256([]byte("ctrlcmd-test")), ObservedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: backendID, StartedAtMS: 2, CorrelationID: "ctrlcmd-test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: "service:ctrlcmd", NetworkID: &networkID, ServiceID: &serviceID,
		BroadcastKind: &serviceType, DisplayName: "合成サービス", Validation: catalogmodel.ValidationProvisional,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, 3, 1, 0); err != nil {
		t.Fatal(err)
	}
	return backendID
}

func TestMirakurunCatalogSyncIsExplicitRedactedAndVisibleInUI(t *testing.T) {
	root := migratedRoot(t)
	startMS := time.Now().UTC().Add(time.Hour).UnixMilli()
	const (
		serviceID    = uint64(3_273_601_024)
		programID    = uint64(327_360_102_400_001)
		privateTitle = "実カタログ番組-非公開文字列"
	)
	var calls atomic.Int32
	var failPrograms atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		switch request.URL.Path {
		case "/private/api/version":
			writeCommandJSON(writer, `{"current":"unknown-compatible","latest":"99.0.0"}`)
		case "/private/api/services":
			writeCommandJSON(writer, `[{"id":3273601024,"networkId":32736,"serviceId":1024,"name":"実サービス","type":1}]`)
		case "/private/api/programs":
			if failPrograms.Load() {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(writer, `private upstream body `+privateTitle)
				return
			}
			writeCommandJSON(writer, fmt.Sprintf(`[{"id":%d,"networkId":32736,"serviceId":1024,"eventId":1,"startAt":%d,"duration":1800000,"isFree":true,"name":%q,"description":"説明"}]`, programID, startMS, privateTitle))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	var output, diagnostic bytes.Buffer
	if code := run(nil, &output, &diagnostic); code != 0 || calls.Load() != 0 {
		t.Fatalf("通常起動で通信しました: code=%d calls=%d", code, calls.Load())
	}
	output.Reset()
	diagnostic.Reset()
	baseURL := server.URL + "/private"
	arguments := []string{"catalog", "sync", "--data-root", root, "--provider", "mirakurun", "--base-url", baseURL}
	if code := run(arguments, &output, &diagnostic); code != 0 {
		t.Fatalf("sync code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	if calls.Load() != 3 || !strings.Contains(output.String(), "result=completed services=1 programs=1") {
		t.Fatalf("calls=%d output=%q", calls.Load(), output.String())
	}
	for _, secret := range []string{baseURL, root, "/private", privateTitle} {
		if strings.Contains(output.String(), secret) || strings.Contains(diagnostic.String(), secret) {
			t.Fatalf("同期結果に非公開値が含まれます: %q", secret)
		}
	}

	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	backends, err := store.CurrentBackends(context.Background(), 16, catalogmodel.ID{})
	if err != nil || len(backends) != 1 || backends[0].Kind != "MIRAKURUN" {
		_ = store.Close()
		t.Fatalf("backends=%+v err=%v", backends, err)
	}
	backendID := backends[0].ID
	services, err := store.CurrentServices(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(services) != 1 || services[0].ProviderLocator != fmt.Sprint(serviceID) || services[0].Validation != catalogmodel.ValidationProvisional {
		_ = store.Close()
		t.Fatalf("services=%+v err=%v", services, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	failPrograms.Store(true)
	output.Reset()
	diagnostic.Reset()
	if code := run(arguments, &output, &diagnostic); code != 1 || !strings.Contains(diagnostic.String(), "catalog-sync-failed") {
		t.Fatalf("failed sync code=%d out=%q err=%q", code, output.String(), diagnostic.String())
	}
	for _, secret := range []string{baseURL, root, "/private", privateTitle, "upstream body"} {
		if strings.Contains(output.String(), secret) || strings.Contains(diagnostic.String(), secret) {
			t.Fatalf("失敗結果に非公開値が含まれます: %q", secret)
		}
	}
	requestsAfterSync := calls.Load()

	address := unusedLoopbackAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	uiOutput := newNotifyingWriter()
	uiDiagnostic := newNotifyingWriter()
	done := make(chan int, 1)
	go func() {
		done <- runContext(ctx, []string{"ui", "serve", "--data-root", root, "--listen", address}, uiOutput, uiDiagnostic)
	}()
	select {
	case <-uiOutput.written:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("UI did not start, diagnostic=%q", uiDiagnostic.String())
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("http://" + address + "/epg")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status=%d read=%v close=%v", response.StatusCode, readErr, closeErr)
	}
	if !bytes.Contains(body, []byte(privateTitle)) || !bytes.Contains(body, []byte("実サービス")) {
		cancel()
		t.Fatalf("同期済みカタログが表示されません: %q", body)
	}
	if calls.Load() != requestsAfterSync {
		cancel()
		t.Fatalf("UI表示がprovider通信を開始しました: before=%d after=%d", requestsAfterSync, calls.Load())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("UI shutdown code=%d diagnostic=%q", code, uiDiagnostic.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UI graceful shutdown timed out")
	}
}

func writeCommandJSON(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(writer, body)
}

func TestUICommandRejectsUnsafeListenBeforeDatabase(t *testing.T) {
	for _, address := range []string{"0.0.0.0:40772", "192.0.2.39:40772", "localhost:40772", "127.0.0.1:0"} {
		var output, diagnostic bytes.Buffer
		code := runContext(context.Background(), []string{"ui", "serve", "--data-root", "/private/not-for-output", "--listen", address}, &output, &diagnostic)
		if code != 1 || !strings.Contains(diagnostic.String(), "loopback-listen-required") || strings.Contains(diagnostic.String(), "/private") {
			t.Fatalf("address=%q code=%d diagnostic=%q", address, code, diagnostic.String())
		}
	}
}

func TestUICommandFailsClosedForDatabaseStateAndOwnerLock(t *testing.T) {
	empty := ownerOnlyRoot(t)
	address := unusedLoopbackAddress(t)
	var output, diagnostic bytes.Buffer
	if code := runContext(context.Background(), []string{"ui", "serve", "--data-root", empty, "--listen", address}, &output, &diagnostic); code != 1 || !strings.Contains(diagnostic.String(), "current-database-required") {
		t.Fatalf("empty code=%d diagnostic=%q", code, diagnostic.String())
	}

	current := migratedRoot(t)
	store, err := sqliteadapter.OpenStore(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	output.Reset()
	diagnostic.Reset()
	if code := runContext(context.Background(), []string{"ui", "serve", "--data-root", current, "--listen", address}, &output, &diagnostic); code != 1 || !strings.Contains(diagnostic.String(), "database-owner-unavailable") {
		t.Fatalf("locked code=%d diagnostic=%q", code, diagnostic.String())
	}
}

func TestUICommandStartsExplicitlyAndShutsDownGracefully(t *testing.T) {
	previousCommit := productCommit
	productCommit = strings.Repeat("a", 40)
	t.Cleanup(func() { productCommit = previousCommit })
	root := migratedRoot(t)
	address := unusedLoopbackAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := newNotifyingWriter()
	diagnostic := newNotifyingWriter()
	done := make(chan int, 1)
	go func() {
		done <- runContext(ctx, []string{"ui", "serve", "--data-root", root, "--listen", address}, output, diagnostic)
	}()
	select {
	case <-output.written:
	case <-time.After(5 * time.Second):
		t.Fatalf("UI did not start, diagnostic=%q", diagnostic.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("shutdown code=%d diagnostic=%q", code, diagnostic.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UI graceful shutdown timed out")
	}
	if !strings.Contains(output.String(), "loopback限定") || strings.Contains(output.String(), root) {
		t.Fatalf("unsafe output=%q", output.String())
	}
}

func TestUIBackupRejectsUnavailableBuildProvenance(t *testing.T) {
	previousCommit := productCommit
	productCommit = "invalid"
	t.Cleanup(func() { productCommit = previousCommit })
	root := migratedRoot(t)
	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = (&uiBackup{store: store, clock: wallClock{}}).Create(context.Background(), time.Now().UTC())
	if err == nil || err.Error() != "product-commit-unavailable" {
		t.Fatalf("error=%v", err)
	}
}

func ownerOnlyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func migratedRoot(t *testing.T) string {
	t.Helper()
	root := ownerOnlyRoot(t)
	inspection, err := sqliteadapter.MigrateDatabase(context.Background(), root, time.Now().UTC().UnixMilli())
	if err != nil || inspection.State != sqliteadapter.StateCurrent {
		t.Fatalf("migrate state=%s err=%v", inspection.State, err)
	}
	return root
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

type notifyingWriter struct {
	mutex   sync.Mutex
	buffer  bytes.Buffer
	written chan struct{}
	once    sync.Once
}

func newNotifyingWriter() *notifyingWriter {
	return &notifyingWriter{written: make(chan struct{})}
}

func (writer *notifyingWriter) Write(value []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	count, err := writer.buffer.Write(value)
	writer.once.Do(func() { close(writer.written) })
	return count, err
}

func (writer *notifyingWriter) String() string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.buffer.String()
}

var _ io.Writer = (*notifyingWriter)(nil)
