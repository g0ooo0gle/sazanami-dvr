package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	autoreservationadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/filecopy2"
	liveadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/live"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/programguide"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/programsearch"
	recordedadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/recorded"
	reservationadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/reservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/status"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/liverelay"
	coreautoreservation "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type fakeCatalog struct {
	backends   []catalogmodel.CurrentBackend
	services   []catalogmodel.CurrentService
	backendErr error
	serviceErr error
}

type recordingCatalog struct {
	*fakeCatalog
	programs []catalogmodel.CurrentProgram
}

type emptyReservationOperations struct{}

type emptyAutomaticReservationOperations struct{}

type emptyRecordedOperations struct{}

type emptyLiveOperations struct{}

func (emptyLiveOperations) Select(context.Context, liverelay.Service, int32) (int32, error) {
	return 1, nil
}

func (emptyLiveOperations) Open(context.Context, int32) (liverelay.Stream, error) {
	return nil, errors.New("not opened in router test")
}

func (emptyLiveOperations) Close(int32) {}

func (emptyRecordedOperations) CompletedRecordings(context.Context, int, int32) ([]recording.HistoryItem, error) {
	return nil, nil
}

func (emptyRecordedOperations) RecordingHistoryItem(context.Context, int32) (*recording.HistoryItem, error) {
	return nil, nil
}

func (emptyAutomaticReservationOperations) Add(context.Context, coreautoreservation.SearchCondition,
	coreautoreservation.RecordingSettings,
) (coreautoreservation.Rule, error) {
	return coreautoreservation.Rule{}, nil
}

func (emptyAutomaticReservationOperations) List(context.Context, int, int32) ([]coreautoreservation.Rule, error) {
	return nil, nil
}

func (emptyAutomaticReservationOperations) Change(context.Context, int32, coreautoreservation.SearchCondition,
	coreautoreservation.RecordingSettings,
) error {
	return nil
}

func (emptyAutomaticReservationOperations) Delete(context.Context, int32) error { return nil }

func (emptyReservationOperations) Add(context.Context, recording.ReservationRequest) (recording.Reservation, error) {
	return recording.Reservation{}, nil
}

func (emptyReservationOperations) Active(context.Context, int, int32) ([]recording.Reservation, error) {
	return nil, nil
}

func (emptyReservationOperations) Change(context.Context, recording.ReservationChange) error {
	return nil
}
func (emptyReservationOperations) Delete(context.Context, int32) error            { return nil }
func (emptyReservationOperations) Recording(context.Context, int32) (bool, error) { return false, nil }

func (catalog *recordingCatalog) ProgramsByServiceForGeneration(_ context.Context, _, _ catalogmodel.ID, limit int, after catalogmodel.ProgramCursor) ([]catalogmodel.CurrentProgram, error) {
	result := make([]catalogmodel.CurrentProgram, 0, limit)
	for _, program := range catalog.programs {
		if program.ServiceLocator > after.ServiceLocator ||
			(program.ServiceLocator == after.ServiceLocator && program.EventLocator > after.EventLocator) {
			result = append(result, program)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (catalog *recordingCatalog) ProgramsForServiceForGeneration(_ context.Context, _, _ catalogmodel.ID, serviceLocator string, limit int, afterEvent string) ([]catalogmodel.CurrentProgram, error) {
	result := make([]catalogmodel.CurrentProgram, 0, limit)
	for _, program := range catalog.programs {
		if program.ServiceLocator != serviceLocator || program.EventLocator <= afterEvent {
			continue
		}
		result = append(result, program)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (catalog *recordingCatalog) ProgramsMatchingGeneration(_ context.Context, _, _ catalogmodel.ID, locator string, eventID, start, duration int64) ([]catalogmodel.CurrentProgram, error) {
	result := make([]catalogmodel.CurrentProgram, 0, 2)
	for _, program := range catalog.programs {
		if program.ServiceLocator == locator && program.RawEventID != nil && *program.RawEventID == eventID &&
			program.Material.StartUTCMS != nil && *program.Material.StartUTCMS == start &&
			program.Material.DurationMS != nil && *program.Material.DurationMS == duration {
			result = append(result, program)
		}
	}
	return result, nil
}

func (catalog *fakeCatalog) CurrentBackends(_ context.Context, limit int, after catalogmodel.ID) ([]catalogmodel.CurrentBackend, error) {
	if catalog.backendErr != nil {
		return nil, catalog.backendErr
	}
	result := make([]catalogmodel.CurrentBackend, 0, limit)
	for _, backend := range catalog.backends {
		if bytes.Compare(backend.ID[:], after[:]) > 0 && len(result) < limit {
			result = append(result, backend)
		}
	}
	return result, nil
}

func (catalog *fakeCatalog) LatestCompletedGeneration(context.Context, catalogmodel.ID) (catalogmodel.ID, error) {
	return testID(101), nil
}

func (catalog *fakeCatalog) ServicesForGeneration(_ context.Context, _, _ catalogmodel.ID,
	_ catalogmodel.GenerationState, limit int, after catalogmodel.ID,
) ([]catalogmodel.CurrentService, error) {
	if catalog.serviceErr != nil {
		return nil, catalog.serviceErr
	}
	result := make([]catalogmodel.CurrentService, 0, limit)
	for _, service := range catalog.services {
		if bytes.Compare(service.ID[:], after[:]) > 0 && len(result) < limit {
			result = append(result, service)
		}
	}
	return result, nil
}

func testID(number uint32) catalogmodel.ID {
	var id catalogmodel.ID
	id[6] = 0x40
	id[8] = 0x80
	binary.BigEndian.PutUint32(id[12:], number)
	return id
}

func int64Pointer(value int64) *int64    { return &value }
func stringPointer(value string) *string { return &value }

func observedService(number uint32, locator string, networkID, serviceID int64, serviceType string) catalogmodel.CurrentService {
	return catalogmodel.CurrentService{
		ID: testID(number), ProviderLocator: locator, DisplayName: "サービス" + locator,
		NetworkID: int64Pointer(networkID), ServiceID: int64Pointer(serviceID), BroadcastKind: stringPointer(serviceType),
		Validation: catalogmodel.ValidationProvisional,
	}
}

func serviceMapping(locator string, networkID, serviceID, transportID int) string {
	return fmt.Sprintf(`{"provider_locator":%q,"network_id":%d,"service_id":%d,"transport_stream_id":%d,"provider_name":"","network_name":"ネット","transport_stream_name":"TS","remote_control_key_id":1,"partial_reception":false,"epg_capture":true,"search":true}`,
		locator, networkID, serviceID, transportID)
}

func mapDocument(backendID catalogmodel.ID, services string) string {
	return fmt.Sprintf(`{"format":"sazanami-channel-map-v1","backend_id":%q,"services":[%s]}`, backendID.String(), services)
}

func writeMap(t *testing.T, root, document string) string {
	t.Helper()
	path := filepath.Join(root, "channels.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validFixture(t *testing.T) (string, string, *fakeCatalog, catalogmodel.ID) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backendID := testID(100)
	path := writeMap(t, root, mapDocument(backendID, serviceMapping("service:1", 1, 3, 2)))
	catalog := &fakeCatalog{
		backends: []catalogmodel.CurrentBackend{{ID: backendID, Kind: "MIRAKURUN"}},
		services: []catalogmodel.CurrentService{observedService(1, "service:1", 1, 3, "1")},
	}
	return root, path, catalog, backendID
}

func requireReason(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil || err.Error() != reason {
		t.Fatalf("error=%v, want=%s", err, reason)
	}
}

func TestBuildSnapshotMatchesAndSortsWithoutChangingCatalog(t *testing.T) {
	root, path, catalog, backendID := validFixture(t)
	catalog.services = append(catalog.services, observedService(2, "service:2", 1, 4, "255"))
	document := mapDocument(backendID, strings.Join([]string{
		serviceMapping("service:2", 1, 4, 8), serviceMapping("service:1", 1, 3, 2),
	}, ","))
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	originalFirst := catalog.services[0]
	snapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	current, err := snapshot.Current(context.Background())
	if err != nil || snapshot.Count() != 2 || len(current.Services) != 2 {
		t.Fatalf("count=%d services=%d err=%v", snapshot.Count(), len(current.Services), err)
	}
	if current.Services[0].ProviderLocator != "service:1" || current.Services[1].ServiceType != 255 ||
		!strings.HasPrefix(current.Key, "v1:") || len(current.Key) != 67 {
		t.Fatalf("snapshot=%+v", current)
	}
	if catalog.services[0] != originalFirst {
		t.Fatal("入力カタログが変更されました")
	}
	again, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	againValue, _ := again.Current(context.Background())
	if current.Key != againValue.Key {
		t.Fatalf("key is not deterministic: %s != %s", current.Key, againValue.Key)
	}
	changed := strings.Replace(document, `"network_name":"ネット"`, `"network_name":"別"`, 1)
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	changedSnapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	changedValue, _ := changedSnapshot.Current(context.Background())
	if current.Key == changedValue.Key {
		t.Fatal("設定変更後もsnapshot keyが同一です")
	}
}

func TestCandidateSnapshotSwitchKeepsOldValueImmutable(t *testing.T) {
	root, path, catalog, backendID := validFixture(t)
	initial, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := NewSnapshotHolder(initial)
	if err != nil {
		t.Fatal(err)
	}
	oldValue, _ := initial.Current(context.Background())
	catalog.services[0].DisplayName = "更新後のサービス"
	candidateID := testID(102)
	candidate, err := BuildCandidateSnapshot(context.Background(), root, path, backendID, candidateID, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.generationID != candidateID || initial.generationID == candidate.generationID {
		t.Fatalf("initial=%s candidate=%s", initial.generationID.String(), candidate.generationID.String())
	}
	if err := holder.Store(candidate); err != nil {
		t.Fatal(err)
	}
	newValue, _ := holder.Load().Current(context.Background())
	oldAgain, _ := initial.Current(context.Background())
	if newValue.Services[0].ServiceName != "更新後のサービス" || oldAgain.Services[0].ServiceName != oldValue.Services[0].ServiceName {
		t.Fatalf("old=%+v new=%+v", oldAgain.Services[0], newValue.Services[0])
	}
	if err := holder.Store(nil); err == nil || holder.Load() != candidate {
		t.Fatalf("nil store err=%v", err)
	}
	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				if holder.Load() == nil {
					t.Error("holder returned nil")
				}
			}
		}()
	}
	for range 100 {
		if err := holder.Store(initial); err != nil {
			t.Fatal(err)
		}
		if err := holder.Store(candidate); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}

func TestSnapshotFindsOneReservableProgram(t *testing.T) {
	root, _, base, backendID := validFixture(t)
	path := writeMap(t, root, mapDocument(backendID, serviceMapping("1003", 1, 3, 2)))
	base.services = []catalogmodel.CurrentService{observedService(1, "1003", 1, 3, "1")}
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	startMS, durationMS, eventID := start.UnixMilli(), int64((30*time.Minute)/time.Millisecond), int64(4)
	title := "番組"
	catalog := &recordingCatalog{fakeCatalog: base, programs: []catalogmodel.CurrentProgram{{
		InstanceID: testID(201), RevisionID: testID(202), ServiceLocator: "1003", EventLocator: "4", RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &startMS, DurationMS: &durationMS,
			Title: &title, Validation: catalogmodel.ValidationValid},
	}}}
	snapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	program, err := snapshot.FindProgram(context.Background(), coreReservationRequest(start))
	if err != nil || program.BackendID != backendID || program.Title != title || program.StationName != "サービス1003" ||
		program.ProviderServiceLocator != "1003" {
		t.Fatalf("program=%+v err=%v", program, err)
	}
	request := coreReservationRequest(start)
	request.EventID++
	if _, err := snapshot.FindProgram(context.Background(), request); err == nil {
		t.Fatal("一致しないeventが予約候補になりました")
	}
}

func TestRecordingRouterDispatchesReservationList(t *testing.T) {
	root, path, base, _ := validFixture(t)
	catalog := &recordingCatalog{fakeCatalog: base}
	snapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRecordingRouter(snapshot, emptyReservationOperations{}, SystemClock{}, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := make([]byte, 10)
	binary.LittleEndian.PutUint32(request[0:4], uint32(reservationadapter.CommandList))
	binary.LittleEndian.PutUint32(request[4:8], 2)
	binary.LittleEndian.PutUint16(request[8:10], reservationadapter.Version)
	var response bytes.Buffer
	if err := router.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != reservationadapter.ResultSuccess {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
}

func TestRecordingRouterDispatchesAutomaticReservationList(t *testing.T) {
	root, path, base, _ := validFixture(t)
	catalog := &recordingCatalog{fakeCatalog: base}
	snapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRecordingRouterWithAutomatic(snapshot, emptyReservationOperations{},
		emptyAutomaticReservationOperations{}, SystemClock{}, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := make([]byte, 10)
	binary.LittleEndian.PutUint32(request[0:4], uint32(autoreservationadapter.CommandList))
	binary.LittleEndian.PutUint32(request[4:8], 2)
	binary.LittleEndian.PutUint16(request[8:10], autoreservationadapter.Version)
	var response bytes.Buffer
	if err := router.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != 1 {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
}

func TestRecordingRouterDispatchesCompletedRecordingList(t *testing.T) {
	root, path, base, _ := validFixture(t)
	snapshot, err := BuildSnapshot(context.Background(), root, path, &recordingCatalog{fakeCatalog: base})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRecordingRouterComplete(snapshot, emptyReservationOperations{},
		emptyAutomaticReservationOperations{}, emptyRecordedOperations{}, SystemClock{}, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := make([]byte, 10)
	binary.LittleEndian.PutUint32(request[0:4], uint32(recordedadapter.CommandList))
	binary.LittleEndian.PutUint32(request[4:8], 2)
	binary.LittleEndian.PutUint16(request[8:10], recordedadapter.Version)
	var response bytes.Buffer
	if err := router.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != 1 {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
}

func TestRecordingRouterMarksOnlyLiveRelayAsLongLived(t *testing.T) {
	root, path, base, _ := validFixture(t)
	snapshot, err := BuildSnapshot(context.Background(), root, path, &recordingCatalog{fakeCatalog: base})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRecordingRouterWithLive(snapshot, emptyReservationOperations{},
		emptyAutomaticReservationOperations{}, emptyRecordedOperations{}, emptyLiveOperations{},
		SystemClock{}, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := make([]byte, 12)
	binary.LittleEndian.PutUint32(request[:4], uint32(liveadapter.CommandRelay))
	binary.LittleEndian.PutUint32(request[4:8], 4)
	binary.LittleEndian.PutUint32(request[8:12], 1)
	if !router.LongLived(request) {
		t.Fatal("301を長時間接続として識別しません")
	}
	binary.LittleEndian.PutUint32(request[:4], uint32(liveadapter.CommandSelect))
	if router.LongLived(request) || router.LongLived(request[:4]) {
		t.Fatal("301以外を長時間接続として識別しました")
	}
}

func TestSnapshotResolvesOneLiveService(t *testing.T) {
	root, path, base, _ := validFixture(t)
	snapshot, err := BuildSnapshot(context.Background(), root, path, base)
	if err != nil {
		t.Fatal(err)
	}
	target, err := snapshot.ResolveLiveService(context.Background(), 1, 2, 3)
	if err != nil || target.Opaque != "service:1" {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	if _, err := snapshot.ResolveLiveService(context.Background(), 1, 2, 9); err == nil {
		t.Fatal("未知serviceを解決しました")
	}
}

func coreReservationRequest(start time.Time) recording.ReservationRequest {
	return recording.ReservationRequest{
		NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4,
		Start: start, Duration: 30 * time.Minute, Priority: 3,
	}
}

func TestDecodeRejectsInvalidJSONAndFields(t *testing.T) {
	_, _, _, backendID := validFixture(t)
	validService := serviceMapping("service:1", 1, 3, 2)
	valid := mapDocument(backendID, validService)
	cases := map[string]string{
		"missing":  strings.Replace(valid, `"format":"sazanami-channel-map-v1",`, "", 1),
		"unknown":  strings.Replace(valid, `"format":`, `"extra":1,"format":`, 1),
		"null":     strings.Replace(valid, `"network_id":1`, `"network_id":null`, 1),
		"wrong":    strings.Replace(valid, `"network_id":1`, `"network_id":"1"`, 1),
		"overflow": strings.Replace(valid, `"network_id":1`, `"network_id":65536`, 1),
		"trailing": valid + `{}`,
		"empty":    mapDocument(backendID, ""),
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeChannelMap([]byte(document))
			if err == nil {
				t.Fatal("expected failure")
			}
		})
	}
	if _, err := decodeChannelMap([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeChannelMap(append([]byte{0xef, 0xbb, 0xbf}, []byte(valid)...)); err == nil {
		t.Fatal("decode helper itself may accept BOM, file loader test must reject it")
	}
	mappings := make([]string, maxServices+1)
	for index := range mappings {
		mappings[index] = validService
	}
	if _, err := decodeChannelMap([]byte(mapDocument(backendID, strings.Join(mappings[:maxServices], ",")))); err != nil {
		t.Fatalf("4,096 services: %v", err)
	}
	_, err := decodeChannelMap([]byte(mapDocument(backendID, strings.Join(mappings, ","))))
	requireReason(t, err, "channel-map-count")
}

func TestFileBoundariesAndPaths(t *testing.T) {
	root, path, catalog, _ := validFixture(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exact := append(data, bytes.Repeat([]byte{' '}, maxChannelMap-len(data))...)
	if err := os.WriteFile(path, exact, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSnapshot(context.Background(), root, path, catalog); err != nil {
		t.Fatalf("exact limit and relaxed file mode: %v", err)
	}
	if err := os.WriteFile(path, append(exact, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildSnapshot(context.Background(), root, path, catalog)
	requireReason(t, err, "channel-map-over-limit")

	outside := writeMap(t, t.TempDir(), string(data))
	_, err = BuildSnapshot(context.Background(), root, outside, catalog)
	requireReason(t, err, "channel-map-path-invalid")
	directory := filepath.Join(root, "directory.json")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = BuildSnapshot(context.Background(), root, directory, catalog)
	requireReason(t, err, "channel-map-not-regular")
	symlink := filepath.Join(root, "link.json")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	_, err = BuildSnapshot(context.Background(), root, symlink, catalog)
	requireReason(t, err, "channel-map-not-regular")
	bom := append([]byte{0xef, 0xbb, 0xbf}, data...)
	if err := os.WriteFile(path, bom, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildSnapshot(context.Background(), root, path, catalog)
	requireReason(t, err, "channel-map-json-invalid")
	invalidUTF8 := append(append([]byte(nil), data...), 0xff)
	if err := os.WriteFile(path, invalidUTF8, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildSnapshot(context.Background(), root, path, catalog)
	requireReason(t, err, "channel-map-json-invalid")
}

func TestCatalogAndMatchingFailures(t *testing.T) {
	root, path, catalog, backendID := validFixture(t)
	tests := map[string]struct {
		mutate func(*fakeCatalog) string
		reason string
	}{
		"backend absent": {func(c *fakeCatalog) string {
			c.backends = nil
			return mapDocument(backendID, serviceMapping("service:1", 1, 3, 2))
		}, "channel-backend-unavailable"},
		"backend kind": {func(c *fakeCatalog) string {
			c.backends[0].Kind = "FAKE"
			return mapDocument(backendID, serviceMapping("service:1", 1, 3, 2))
		}, "channel-backend-unavailable"},
		"orphan":           {func(c *fakeCatalog) string { return mapDocument(backendID, serviceMapping("missing", 1, 3, 2)) }, "channel-service-orphan"},
		"network mismatch": {func(c *fakeCatalog) string { return mapDocument(backendID, serviceMapping("service:1", 9, 3, 2)) }, "channel-service-mismatch"},
		"service mismatch": {func(c *fakeCatalog) string { return mapDocument(backendID, serviceMapping("service:1", 1, 9, 2)) }, "channel-service-mismatch"},
		"locator duplicate": {func(c *fakeCatalog) string {
			return mapDocument(backendID, serviceMapping("service:1", 1, 3, 2)+","+serviceMapping("service:1", 1, 3, 4))
		}, "channel-service-duplicate"},
		"identity duplicate": {func(c *fakeCatalog) string {
			c.services = append(c.services, observedService(2, "service:2", 1, 3, "1"))
			return mapDocument(backendID, serviceMapping("service:1", 1, 3, 2)+","+serviceMapping("service:2", 1, 3, 2))
		}, "channel-service-duplicate"},
		"catalog read": {func(c *fakeCatalog) string {
			c.serviceErr = errors.New("private")
			return mapDocument(backendID, serviceMapping("service:1", 1, 3, 2))
		}, "channel-catalog-unavailable"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			copyCatalog := &fakeCatalog{backends: append([]catalogmodel.CurrentBackend(nil), catalog.backends...), services: append([]catalogmodel.CurrentService(nil), catalog.services...)}
			document := test.mutate(copyCatalog)
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := BuildSnapshot(context.Background(), root, path, copyCatalog)
			requireReason(t, err, test.reason)
			if strings.Contains(fmt.Sprint(err), "private") {
				t.Fatal("private error leaked")
			}
		})
	}
}

func TestServiceTypeAndCatalogCountBoundaries(t *testing.T) {
	root, path, catalog, _ := validFixture(t)
	for _, value := range []string{"", "x", "-1", "+1", "256"} {
		t.Run("type "+value, func(t *testing.T) {
			copyCatalog := *catalog
			copyCatalog.services = append([]catalogmodel.CurrentService(nil), catalog.services...)
			copyCatalog.services[0].BroadcastKind = stringPointer(value)
			_, err := BuildSnapshot(context.Background(), root, path, &copyCatalog)
			requireReason(t, err, "channel-service-mismatch")
		})
	}
	for _, value := range []string{"0", "255"} {
		copyCatalog := *catalog
		copyCatalog.services = append([]catalogmodel.CurrentService(nil), catalog.services...)
		copyCatalog.services[0].BroadcastKind = stringPointer(value)
		if _, err := BuildSnapshot(context.Background(), root, path, &copyCatalog); err != nil {
			t.Fatalf("type %s: %v", value, err)
		}
	}

	many := make([]catalogmodel.CurrentService, maxServices+1)
	for index := range many {
		many[index] = observedService(uint32(index+1), fmt.Sprintf("s:%d", index), 1, int64(index%65_536), "1")
	}
	many[0] = observedService(1, "service:1", 1, 3, "1")
	catalog.services = many[:maxServices]
	if _, err := BuildSnapshot(context.Background(), root, path, catalog); err != nil {
		t.Fatalf("4,096 catalog services: %v", err)
	}
	catalog.services = many
	_, err := BuildSnapshot(context.Background(), root, path, catalog)
	requireReason(t, err, "channel-catalog-over-limit")
}

type fixedClock struct{ instant time.Time }

func (clock fixedClock) Now() time.Time { return clock.instant }

type countingLoader struct {
	snapshot *Snapshot
	loads    int
}

func (loader *countingLoader) Load() *Snapshot {
	loader.loads++
	return loader.snapshot
}

func TestRouterLoadsSnapshotOncePerCatalogRequest(t *testing.T) {
	root, path, base, _ := validFixture(t)
	catalog := &recordingCatalog{fakeCatalog: base}
	snapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	loader := &countingLoader{snapshot: snapshot}
	router, err := NewRecordingRouter(loader, emptyReservationOperations{}, SystemClock{}, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	loader.loads = 0
	request := make([]byte, 8)
	binary.LittleEndian.PutUint32(request, uint32(channel.CommandEnumService))
	if err := router.Handle(context.Background(), request, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if loader.loads != 1 {
		t.Fatalf("channel loads=%d", loader.loads)
	}

	loader.loads = 0
	programBody := make([]byte, 40)
	binary.LittleEndian.PutUint32(programBody[0:4], 40)
	binary.LittleEndian.PutUint32(programBody[4:8], 4)
	for index, selector := range [...]uint64{0xffffffffffff, 0xffffffffffff, 1, 0x7fffffffffffffff} {
		binary.LittleEndian.PutUint64(programBody[8+index*8:16+index*8], selector)
	}
	programRequest := make([]byte, 8+len(programBody))
	binary.LittleEndian.PutUint32(programRequest[0:4], uint32(programguide.Command))
	binary.LittleEndian.PutUint32(programRequest[4:8], uint32(len(programBody)))
	copy(programRequest[8:], programBody)
	if err := router.Handle(context.Background(), programRequest, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if loader.loads != 1 {
		t.Fatalf("program loads=%d", loader.loads)
	}

	loader.loads = 0
	if err := router.Handle(context.Background(), commandFrame(programsearch.Command, emptyProgramSearchBody()), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if loader.loads != 1 {
		t.Fatalf("search loads=%d", loader.loads)
	}
}

func TestRouterDispatchesOnlyAcceptedCommands(t *testing.T) {
	root, path, catalog, _ := validFixture(t)
	snapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(snapshot, fixedClock{time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)}, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	statusRequest := make([]byte, 14)
	binary.LittleEndian.PutUint32(statusRequest[0:4], uint32(status.Command))
	binary.LittleEndian.PutUint32(statusRequest[4:8], status.RequestBodySize)
	binary.LittleEndian.PutUint16(statusRequest[8:10], status.Version)
	enumRequest := make([]byte, 8)
	binary.LittleEndian.PutUint32(enumRequest[0:4], 1021)
	fileRequest := make([]byte, 34)
	binary.LittleEndian.PutUint32(fileRequest[0:4], 1060)
	binary.LittleEndian.PutUint32(fileRequest[4:8], 26)
	binary.LittleEndian.PutUint32(fileRequest[8:12], 26)
	position := 12
	for _, character := range []byte("ChSet5.txt") {
		binary.LittleEndian.PutUint16(fileRequest[position:position+2], uint16(character))
		position += 2
	}
	for _, request := range [][]byte{statusRequest, enumRequest, fileRequest} {
		var response bytes.Buffer
		if err := router.Handle(context.Background(), request, &response); err != nil {
			t.Fatal(err)
		}
		if response.Len() < 8 || binary.LittleEndian.Uint32(response.Bytes()[:4]) != 1 {
			t.Fatalf("response=%x", response.Bytes())
		}
	}
	unsupported := make([]byte, 8)
	binary.LittleEndian.PutUint32(unsupported[:4], 999)
	var codecError *codec.Error
	if err := router.Handle(context.Background(), unsupported, &bytes.Buffer{}); !errors.As(err, &codecError) || codecError.Category != codec.Unsupported {
		t.Fatalf("unsupported error=%v", err)
	}
	if err := router.Handle(context.Background(), unsupported[:7], &bytes.Buffer{}); !errors.As(err, &codecError) || codecError.Category != codec.Truncated {
		t.Fatalf("truncated error=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := router.Handle(canceled, enumRequest, &bytes.Buffer{}); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestRecordingRouterKeepsKonomiTVCoreProfile(t *testing.T) {
	root, path, base, _ := validFixture(t)
	catalog := &recordingCatalog{fakeCatalog: base}
	snapshot, err := BuildSnapshot(context.Background(), root, path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRecordingRouter(
		snapshot,
		emptyReservationOperations{},
		fixedClock{time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)},
		codec.DefaultLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}

	statusBody := make([]byte, status.RequestBodySize)
	binary.LittleEndian.PutUint16(statusBody[0:2], status.Version)

	fileBody := make([]byte, 26)
	binary.LittleEndian.PutUint32(fileBody[0:4], 26)
	position := 4
	for _, character := range []byte("ChSet5.txt") {
		binary.LittleEndian.PutUint16(fileBody[position:position+2], uint16(character))
		position += 2
	}

	programBody := make([]byte, 40)
	binary.LittleEndian.PutUint32(programBody[0:4], 40)
	binary.LittleEndian.PutUint32(programBody[4:8], 4)
	for index, selector := range [...]uint64{0xffffffffffff, 0xffffffffffff, 1, 0x7fffffffffffffff} {
		binary.LittleEndian.PutUint64(programBody[8+index*8:16+index*8], selector)
	}

	listBody := make([]byte, 2)
	binary.LittleEndian.PutUint16(listBody, reservationadapter.Version)
	fileCopy2Body := make([]byte, 38)
	binary.LittleEndian.PutUint16(fileCopy2Body[0:2], filecopy2.Version)
	binary.LittleEndian.PutUint32(fileCopy2Body[2:6], 36)
	binary.LittleEndian.PutUint32(fileCopy2Body[6:10], 1)
	binary.LittleEndian.PutUint32(fileCopy2Body[10:14], 28)
	position = 14
	for _, character := range []byte("Bitrate.ini") {
		binary.LittleEndian.PutUint16(fileCopy2Body[position:position+2], uint16(character))
		position += 2
	}
	requests := []struct {
		name    string
		command int32
		body    []byte
	}{
		{name: "起動確認", command: status.Command, body: statusBody},
		{name: "チャンネル設定", command: channel.CommandFileCopy, body: fileBody},
		{name: "サービス一覧", command: channel.CommandEnumService},
		{name: "番組表", command: programguide.Command, body: programBody},
		{name: "番組検索", command: programsearch.Command, body: emptyProgramSearchBody()},
		{name: "予約一覧", command: reservationadapter.CommandList, body: listBody},
		{name: "録画設定", command: filecopy2.Command, body: fileCopy2Body},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			var response bytes.Buffer
			if err := router.Handle(context.Background(), commandFrame(test.command, test.body), &response); err != nil {
				t.Fatal(err)
			}
			frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
			if err != nil || frame.Code != 1 {
				t.Fatalf("response=%x err=%v", response.Bytes(), err)
			}
		})
	}

	var addResponse bytes.Buffer
	if err := router.Handle(context.Background(), commandFrame(reservationadapter.CommandAdd, nil), &addResponse); err != nil {
		t.Fatal(err)
	}
	addFrame, err := codec.ParseRequestFrame(addResponse.Bytes(), codec.DefaultLimits())
	if err != nil || addFrame.Code != reservationadapter.ResultFailure {
		t.Fatalf("予約追加の不正入力応答=%x err=%v", addResponse.Bytes(), err)
	}

	var codecError *codec.Error
	if err := router.Handle(context.Background(), commandFrame(2061, nil), &bytes.Buffer{}); !errors.As(err, &codecError) || codecError.Category != codec.Unsupported {
		t.Fatalf("対象外命令error=%v", err)
	}
}

func commandFrame(command int32, body []byte) []byte {
	request := make([]byte, codec.HeaderSize+len(body))
	binary.LittleEndian.PutUint32(request[0:4], uint32(command))
	binary.LittleEndian.PutUint32(request[4:8], uint32(len(body)))
	copy(request[codec.HeaderSize:], body)
	return request
}

func emptyProgramSearchBody() []byte {
	condition := make([]byte, 68)
	binary.LittleEndian.PutUint32(condition[0:4], uint32(len(condition)))
	position := 4
	for range 2 {
		binary.LittleEndian.PutUint32(condition[position:position+4], 6)
		position += 6
	}
	position += 8
	for range 5 {
		binary.LittleEndian.PutUint32(condition[position:position+4], 8)
		position += 8
	}
	// 最後の4 byteはfuzzy、分類除外、時間帯除外、無料条件のzero値である。
	body := make([]byte, 8+len(condition))
	binary.LittleEndian.PutUint32(body[0:4], uint32(len(body)))
	binary.LittleEndian.PutUint32(body[4:8], 1)
	copy(body[8:], condition)
	return body
}
