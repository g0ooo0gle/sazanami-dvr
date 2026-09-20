package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	ctrlcmdapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/ctrlcmd"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// 実DB・通常のrouter・TCP接続を通し、全番組表がclientの10秒以内に届くことを確認する。
// race buildでは時間を比較せず、server側の小さい回帰テストで送信処理を検査する。
func TestWholeProgramGuideTransfer(t *testing.T) {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				t.Skip("transfer timing is measured without race instrumentation")
			}
		}
	}
	const serviceCount, programsPerService = 100, 300
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, _, err := sqlite.OpenStoreForSetup(ctx, root, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backendID, syncID := testID(400), testID(401)
	if err := store.EnsureBackend(ctx, catalogmodel.Backend{
		ID: backendID, Kind: "MIRAKURUN", IdentityHash: sha256.Sum256([]byte("synthetic-transfer")), ObservedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginSync(ctx, catalogmodel.Sync{ID: syncID, BackendID: backendID, StartedAtMS: 10, CorrelationID: "synthetic-transfer"}); err != nil {
		t.Fatal(err)
	}
	var mappings []string
	for service := 1; service <= serviceCount; service++ {
		locator := fmt.Sprint(service)
		if err := store.StoreServices(ctx, syncID, []catalogmodel.ServiceObservation{{
			ProviderLocator: locator, NetworkID: int64Pointer(1), TransportID: int64Pointer(1),
			BroadcastKind: stringPointer("1"),
			ServiceID:     int64Pointer(int64(service)), DisplayName: "合成放送局", Validation: catalogmodel.ValidationValid,
		}}); err != nil {
			t.Fatal(err)
		}
		mappings = append(mappings, serviceMapping(locator, 1, service, 1))
		for first := 0; first < programsPerService; first += catalogmodel.MaxWriteBatch {
			programs := make([]catalogmodel.ProgramObservation, 0, catalogmodel.MaxWriteBatch)
			for event := first; event < first+catalogmodel.MaxWriteBatch; event++ {
				programs = append(programs, catalogmodel.ProgramObservation{
					ServiceLocator: locator, EventLocator: fmt.Sprintf("%05d", event), RawEventID: int64Pointer(int64(event)),
					Material: catalogmodel.RevisionMaterial{
						StartUTCMS: int64Pointer(1_800_000_000_000 + int64(event)*1_800_000), DurationMS: int64Pointer(1_800_000),
						Title:       stringPointer(fmt.Sprintf("合成番組 %d %s", event, strings.Repeat("番組タイトル", 4))),
						Description: stringPointer(strings.Repeat("日本語の番組説明。", 6)),
						FreeAccess:  catalogmodel.FreeYes, Validation: catalogmodel.ValidationValid,
						Metadata: catalogmodel.ProgramMetadata{
							Extended: []catalogmodel.ExtendedItem{{Heading: "詳細", Body: strings.Repeat("合成した番組の詳しい内容です。", 4)}},
							Genres:   []catalogmodel.Genre{{Level1: 1, Level2: 3}},
							Video:    &catalogmodel.Video{StreamContent: 1, ComponentType: 0xb3},
							Audios:   []catalogmodel.Audio{{ComponentType: 3, ComponentTag: 1, Main: true, SamplingRate: 48000, Languages: []string{"jpn"}}},
						},
					},
				})
			}
			if err := store.StorePrograms(ctx, syncID, false, programs); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.CompleteSync(ctx, syncID, 11, serviceCount, serviceCount*programsPerService); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildSnapshot(ctx, root, writeMap(t, root, mapDocument(backendID, strings.Join(mappings, ","))), store)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRecordingRouter(snapshot, emptyReservationOperations{}, SystemClock{}, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var body [40]byte
	binary.LittleEndian.PutUint32(body[:4], uint32(len(body)))
	binary.LittleEndian.PutUint32(body[4:8], 4)
	for index, selector := range []uint64{0xffffffffffff, 0xffffffffffff, 1, 0x7fffffffffffffff} {
		binary.LittleEndian.PutUint64(body[8+index*8:], selector)
	}
	request := commandFrame(1029, body[:])
	// バッファを通さない同じ応答と全バイトを比較する。応答全体はメモリに保持しない。
	want := sha256.New()
	if err := router.Handle(ctx, request, want); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	config := ctrlcmdapp.RecordingConfig()
	config.Address = listener.Addr().String()
	server, err := ctrlcmdapp.NewServer(config, router)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, listener) }()
	defer func() {
		cancel()
		_ = listener.Close()
		if err := <-served; err != nil {
			t.Error(err)
		}
		server.Wait()
	}()
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	started := time.Now()
	if err := connection.SetDeadline(started.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(request); err != nil {
		t.Fatal(err)
	}
	var header [8]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		t.Fatal(err)
	}
	headerTime := time.Since(started)
	declared := int64(binary.LittleEndian.Uint32(header[4:]))
	if code := binary.LittleEndian.Uint32(header[:4]); code != 1 || declared < 8*1024*1024 {
		t.Fatalf("code=%d body=%d: expected a successful, realistic-size guide", code, declared)
	}
	got := sha256.New()
	_, _ = got.Write(header[:])
	received, err := io.CopyN(got, connection, declared)
	if err != nil {
		t.Fatalf("received %d/%d body bytes after %s: %v", received, declared, time.Since(started), err)
	}
	elapsed := time.Since(started)
	if elapsed >= 10*time.Second || !bytes.Equal(got.Sum(nil), want.Sum(nil)) {
		t.Fatalf("response mismatch or too slow: elapsed=%s", elapsed)
	}
	var extra [1]byte
	if n, err := connection.Read(extra[:]); n != 0 || err != io.EOF {
		t.Fatalf("response did not end cleanly: n=%d err=%v", n, err)
	}
	t.Logf("services=%d programs=%d body=%d header=%s complete=%s", serviceCount, serviceCount*programsPerService, declared, headerTime, elapsed)
}
