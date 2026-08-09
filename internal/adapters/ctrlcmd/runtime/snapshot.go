package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"sort"
	"strconv"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const catalogPageSize = 256

// CatalogReaderは完成済みカタログからチャンネル応答に必要な項目だけを読む境界である。
type CatalogReader interface {
	CurrentBackends(context.Context, int, catalogmodel.ID) ([]catalogmodel.CurrentBackend, error)
	LatestCompletedGeneration(context.Context, catalogmodel.ID) (catalogmodel.ID, error)
	ServicesForGeneration(context.Context, catalogmodel.ID, catalogmodel.ID, catalogmodel.GenerationState,
		int, catalogmodel.ID) ([]catalogmodel.CurrentService, error)
}

type programCatalogReader interface {
	ProgramsByServiceForGeneration(context.Context, catalogmodel.ID, catalogmodel.ID,
		int, catalogmodel.ProgramCursor) ([]catalogmodel.CurrentProgram, error)
	ProgramsForServiceForGeneration(context.Context, catalogmodel.ID, catalogmodel.ID,
		string, int, string) ([]catalogmodel.CurrentProgram, error)
	ProgramsMatchingGeneration(context.Context, catalogmodel.ID, catalogmodel.ID,
		string, int64, int64, int64) ([]catalogmodel.CurrentProgram, error)
}

// Snapshotは一つの完了済み番組表世代と、その世代に対応するチャンネル集合を保持する。
type Snapshot struct {
	value        channel.Snapshot
	backendID    catalogmodel.ID
	generationID catalogmodel.ID
	programs     programCatalogReader
}

// BuildSnapshotは設定と最新の完成済み番組表を照合し、世代を固定したスナップショットを作る。
func BuildSnapshot(ctx context.Context, dataRoot, path string, reader CatalogReader) (*Snapshot, error) {
	if ctx == nil || reader == nil {
		return nil, stable("channel-snapshot-failed")
	}
	configuration, err := loadChannelMap(dataRoot, path)
	if err != nil {
		return nil, err
	}
	backendID, err := catalogmodel.ParseID(configuration.BackendID)
	if err != nil {
		return nil, stable("channel-map-field-invalid")
	}
	backend, err := findBackend(ctx, reader, backendID)
	if err != nil {
		return nil, err
	}
	if backend.Kind != "MIRAKURUN" {
		return nil, stable("channel-backend-unavailable")
	}
	generationID, err := reader.LatestCompletedGeneration(ctx, backendID)
	if err != nil {
		return nil, stable("channel-catalog-unavailable")
	}
	return buildSnapshot(ctx, configuration, backendID, generationID, catalogmodel.GenerationCompleted, reader)
}

// BuildCandidateSnapshotは完了直前のRUNNING世代をチャンネル設定へ照合する。
// 戻り値は呼び出し側が同じ世代をCOMPLETEDにできた場合だけ公開してよい。
func BuildCandidateSnapshot(ctx context.Context, dataRoot, path string, backendID, generationID catalogmodel.ID,
	reader CatalogReader,
) (*Snapshot, error) {
	if ctx == nil || reader == nil {
		return nil, stable("channel-snapshot-failed")
	}
	configuration, err := loadChannelMap(dataRoot, path)
	if err != nil {
		return nil, err
	}
	configuredBackend, err := catalogmodel.ParseID(configuration.BackendID)
	if err != nil {
		return nil, stable("channel-map-field-invalid")
	}
	if configuredBackend != backendID {
		return nil, stable("channel-backend-unavailable")
	}
	return buildSnapshot(ctx, configuration, backendID, generationID, catalogmodel.GenerationRunning, reader)
}

func buildSnapshot(ctx context.Context, configuration channelMap, backendID, generationID catalogmodel.ID,
	state catalogmodel.GenerationState, reader CatalogReader,
) (*Snapshot, error) {
	catalog, err := readCatalog(ctx, reader, backendID, generationID, state)
	if err != nil {
		return nil, err
	}
	services, err := matchServices(configuration.Services, catalog)
	if err != nil {
		return nil, err
	}
	sortServices(services)
	key := snapshotKey(configuration.Hash, services)
	value := channel.Snapshot{Key: key, Services: services}
	verified, err := channel.ValidateSnapshot(ctx, value)
	if err != nil || len(verified) != len(services) {
		return nil, stable("channel-snapshot-failed")
	}
	value.Services = verified
	programs, _ := reader.(programCatalogReader)
	return &Snapshot{value: value, backendID: backendID, generationID: generationID, programs: programs}, nil
}

// FindProgramは放送ID、開始時刻、継続時間が一致する完成済み番組を一件だけ返す。
func (snapshot *Snapshot) FindProgram(ctx context.Context, request recording.ReservationRequest) (recording.ProgramSnapshot, error) {
	if snapshot == nil || snapshot.programs == nil || ctx == nil || request.Validate() != nil {
		return recording.ProgramSnapshot{}, stable("program-not-reservable")
	}
	var selected *channel.Service
	for index := range snapshot.value.Services {
		service := &snapshot.value.Services[index]
		if service.NetworkID == request.NetworkID && service.TransportStreamID == request.TransportStreamID &&
			service.ServiceID == request.ServiceID {
			if selected != nil {
				return recording.ProgramSnapshot{}, stable("program-not-reservable")
			}
			selected = service
		}
	}
	if selected == nil || !canonicalServiceLocator(selected.ProviderLocator) {
		return recording.ProgramSnapshot{}, stable("program-not-reservable")
	}
	durationMS := request.Duration.Milliseconds()
	programs, err := snapshot.programs.ProgramsMatchingGeneration(ctx, snapshot.backendID, snapshot.generationID,
		selected.ProviderLocator, int64(request.EventID), request.Start.UnixMilli(), durationMS)
	if err != nil || len(programs) != 1 {
		return recording.ProgramSnapshot{}, stable("program-not-reservable")
	}
	program := programs[0]
	material := program.Material
	if program.ServiceLocator != selected.ProviderLocator || program.RawEventID == nil || *program.RawEventID != int64(request.EventID) ||
		material.StartUTCMS == nil || *material.StartUTCMS != request.Start.UnixMilli() ||
		material.DurationMS == nil || *material.DurationMS != durationMS || material.Validation != catalogmodel.ValidationValid {
		return recording.ProgramSnapshot{}, stable("program-not-reservable")
	}
	title := ""
	if material.Title != nil {
		title = *material.Title
	}
	return recording.ProgramSnapshot{
		ProgramInstanceID: program.InstanceID, ProgramRevisionID: program.RevisionID, BackendID: snapshot.backendID,
		ProviderServiceLocator: selected.ProviderLocator, TuningTarget: selected.ProviderLocator,
		NetworkID: selected.NetworkID, TransportStreamID: selected.TransportStreamID, ServiceID: selected.ServiceID,
		EventID: request.EventID, Title: title, StationName: selected.ServiceName,
		Start: request.Start, Duration: request.Duration,
	}, nil
}

func canonicalServiceLocator(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 63)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

// Currentは同じ完成済みスナップショットを返す。返却後に内部データを更新しない。
func (snapshot *Snapshot) Current(ctx context.Context) (channel.Snapshot, error) {
	if snapshot == nil || ctx == nil {
		return channel.Snapshot{}, stable("channel-snapshot-failed")
	}
	if err := ctx.Err(); err != nil {
		return channel.Snapshot{}, stable("channel-context-ended")
	}
	return snapshot.value, nil
}

// Countは起動時に確定したチャンネル数を返す。
func (snapshot *Snapshot) Count() int {
	if snapshot == nil {
		return 0
	}
	return len(snapshot.value.Services)
}

// ResolveLiveServiceは放送IDの組を一つの完成済みスナップショットで照合し、
// Mirakurun固有のservice locatorをopaqueな接続先として返す。
func (snapshot *Snapshot) ResolveLiveService(ctx context.Context, networkID, transportStreamID, serviceID uint16) (provider.TuningTarget, error) {
	if snapshot == nil || ctx == nil || ctx.Err() != nil {
		return provider.TuningTarget{}, stable("live-service-unavailable")
	}
	locator := ""
	for _, service := range snapshot.value.Services {
		if service.NetworkID == networkID && service.TransportStreamID == transportStreamID && service.ServiceID == serviceID {
			if locator != "" {
				return provider.TuningTarget{}, stable("live-service-unavailable")
			}
			locator = service.ProviderLocator
		}
	}
	target, err := provider.NewTuningTarget(locator)
	if err != nil {
		return provider.TuningTarget{}, stable("live-service-unavailable")
	}
	return target, nil
}

// Loadは固定スナップショット自身を返し、動的保持器と同じrouter境界で利用できるようにする。
func (snapshot *Snapshot) Load() *Snapshot { return snapshot }

// CurrentProgramsByServiceは完成済み番組表を放送サービス順に読み進める。
func (snapshot *Snapshot) CurrentProgramsByService(ctx context.Context, limit int, after catalogmodel.ProgramCursor) ([]catalogmodel.CurrentProgram, error) {
	if snapshot == nil || snapshot.programs == nil || ctx == nil {
		return nil, stable("program-catalog-unavailable")
	}
	return snapshot.programs.ProgramsByServiceForGeneration(ctx, snapshot.backendID, snapshot.generationID, limit, after)
}

// CurrentProgramsForServiceは、選択済みの一つのサービスについて、イベント識別子順に番組を読み進める。
func (snapshot *Snapshot) CurrentProgramsForService(ctx context.Context, serviceLocator string, limit int, afterEvent string) ([]catalogmodel.CurrentProgram, error) {
	if snapshot == nil || snapshot.programs == nil || ctx == nil {
		return nil, stable("program-catalog-unavailable")
	}
	return snapshot.programs.ProgramsForServiceForGeneration(ctx, snapshot.backendID, snapshot.generationID,
		serviceLocator, limit, afterEvent)
}

// ReservationRequestForProgramは固定世代の番組を既存予約照合に使う放送IDへ変換する。
func (snapshot *Snapshot) ReservationRequestForProgram(program catalogmodel.CurrentProgram, priority uint8,
	follow bool,
) (recording.ReservationRequest, error) {
	if snapshot == nil || program.RawEventID == nil || *program.RawEventID < 0 || *program.RawEventID > 65_535 ||
		program.Material.StartUTCMS == nil || program.Material.DurationMS == nil ||
		*program.Material.StartUTCMS < 0 || *program.Material.DurationMS < 1_000 ||
		*program.Material.DurationMS > int64((24*time.Hour)/time.Millisecond) ||
		*program.Material.DurationMS%1_000 != 0 || program.Material.Validation != catalogmodel.ValidationValid {
		return recording.ReservationRequest{}, stable("program-not-reservable")
	}
	var selected *channel.Service
	for index := range snapshot.value.Services {
		service := &snapshot.value.Services[index]
		if service.ProviderLocator == program.ServiceLocator {
			if selected != nil {
				return recording.ReservationRequest{}, stable("program-not-reservable")
			}
			selected = service
		}
	}
	if selected == nil {
		return recording.ReservationRequest{}, stable("program-not-reservable")
	}
	request := recording.ReservationRequest{
		NetworkID: selected.NetworkID, TransportStreamID: selected.TransportStreamID,
		ServiceID: selected.ServiceID, EventID: uint16(*program.RawEventID),
		Start:    time.UnixMilli(*program.Material.StartUTCMS).UTC(),
		Duration: time.Duration(*program.Material.DurationMS) * time.Millisecond,
		Priority: priority, RequestedFollow: follow,
	}
	if request.Validate() != nil {
		return recording.ReservationRequest{}, stable("program-not-reservable")
	}
	return request, nil
}

func findBackend(ctx context.Context, reader CatalogReader, target catalogmodel.ID) (catalogmodel.CurrentBackend, error) {
	var after catalogmodel.ID
	for {
		page, err := reader.CurrentBackends(ctx, 16, after)
		if err != nil || len(page) > 16 {
			return catalogmodel.CurrentBackend{}, stable("channel-catalog-unavailable")
		}
		previous := after
		for _, backend := range page {
			if bytes.Compare(backend.ID[:], previous[:]) <= 0 {
				return catalogmodel.CurrentBackend{}, stable("channel-catalog-unavailable")
			}
			previous = backend.ID
			if backend.ID == target {
				return backend, nil
			}
		}
		if len(page) < 16 {
			return catalogmodel.CurrentBackend{}, stable("channel-backend-unavailable")
		}
		next := page[len(page)-1].ID
		if next == after {
			return catalogmodel.CurrentBackend{}, stable("channel-catalog-unavailable")
		}
		after = next
	}
}

func readCatalog(ctx context.Context, reader CatalogReader, backendID, generationID catalogmodel.ID,
	state catalogmodel.GenerationState,
) ([]catalogmodel.CurrentService, error) {
	result := make([]catalogmodel.CurrentService, 0, catalogPageSize)
	locators := make(map[string]struct{})
	var after catalogmodel.ID
	for {
		page, err := reader.ServicesForGeneration(ctx, backendID, generationID, state, catalogPageSize, after)
		if err != nil || len(page) > catalogPageSize {
			return nil, stable("channel-catalog-unavailable")
		}
		previous := after
		for _, service := range page {
			if bytes.Compare(service.ID[:], previous[:]) <= 0 {
				return nil, stable("channel-catalog-unavailable")
			}
			previous = service.ID
			if len(result) == maxServices {
				return nil, stable("channel-catalog-over-limit")
			}
			if _, duplicate := locators[service.ProviderLocator]; duplicate {
				return nil, stable("channel-service-duplicate")
			}
			locators[service.ProviderLocator] = struct{}{}
			result = append(result, service)
		}
		if len(page) < catalogPageSize {
			return result, nil
		}
		next := page[len(page)-1].ID
		if next == after {
			return nil, stable("channel-catalog-unavailable")
		}
		after = next
	}
}

func matchServices(configured []configuredService, catalog []catalogmodel.CurrentService) ([]channel.Service, error) {
	byLocator := make(map[string]catalogmodel.CurrentService, len(catalog))
	for _, service := range catalog {
		byLocator[service.ProviderLocator] = service
	}
	selectedLocators := make(map[string]struct{}, len(configured))
	type identity struct{ network, transport, service uint16 }
	identities := make(map[identity]struct{}, len(configured))
	result := make([]channel.Service, 0, len(configured))
	for _, selected := range configured {
		if _, duplicate := selectedLocators[selected.ProviderLocator]; duplicate {
			return nil, stable("channel-service-duplicate")
		}
		selectedLocators[selected.ProviderLocator] = struct{}{}
		observed, found := byLocator[selected.ProviderLocator]
		if !found {
			return nil, stable("channel-service-orphan")
		}
		if observed.NetworkID == nil || observed.ServiceID == nil || *observed.NetworkID < 0 || *observed.NetworkID > 65_535 ||
			*observed.ServiceID < 0 || *observed.ServiceID > 65_535 || uint16(*observed.NetworkID) != selected.NetworkID ||
			uint16(*observed.ServiceID) != selected.ServiceID || observed.Validation == catalogmodel.ValidationInvalid {
			return nil, stable("channel-service-mismatch")
		}
		serviceType, err := parseServiceType(observed.BroadcastKind)
		if err != nil {
			return nil, err
		}
		key := identity{selected.NetworkID, selected.TransportStreamID, selected.ServiceID}
		if _, duplicate := identities[key]; duplicate {
			return nil, stable("channel-service-duplicate")
		}
		identities[key] = struct{}{}
		result = append(result, channel.Service{
			ProviderLocator: selected.ProviderLocator, ProviderName: selected.ProviderName,
			ServiceName: observed.DisplayName, NetworkName: selected.NetworkName,
			TransportStreamName: selected.TransportStreamName, NetworkID: selected.NetworkID,
			TransportStreamID: selected.TransportStreamID, ServiceID: selected.ServiceID,
			ServiceType: serviceType, RemoteControlKey: selected.RemoteControlKey,
			PartialReception: selected.PartialReception, EPGCapture: selected.EPGCapture, Search: selected.Search,
			Verified: true, Selected: true,
		})
	}
	return result, nil
}

func parseServiceType(value *string) (uint8, error) {
	if value == nil || *value == "" {
		return 0, stable("channel-service-mismatch")
	}
	for _, digit := range []byte(*value) {
		if digit < '0' || digit > '9' {
			return 0, stable("channel-service-mismatch")
		}
	}
	parsed, err := strconv.ParseUint(*value, 10, 8)
	if err != nil {
		return 0, stable("channel-service-mismatch")
	}
	return uint8(parsed), nil
}

func sortServices(services []channel.Service) {
	sort.Slice(services, func(i, j int) bool {
		left, right := services[i], services[j]
		if left.NetworkID != right.NetworkID {
			return left.NetworkID < right.NetworkID
		}
		if left.TransportStreamID != right.TransportStreamID {
			return left.TransportStreamID < right.TransportStreamID
		}
		if left.ServiceID != right.ServiceID {
			return left.ServiceID < right.ServiceID
		}
		return left.ProviderLocator < right.ProviderLocator
	})
}

func snapshotKey(fileHash [sha256.Size]byte, services []channel.Service) string {
	digest := sha256.New()
	_, _ = digest.Write(fileHash[:])
	for _, service := range services {
		writeString(digest, service.ProviderLocator)
		writeString(digest, service.ProviderName)
		writeString(digest, service.ServiceName)
		writeString(digest, service.NetworkName)
		writeString(digest, service.TransportStreamName)
		var fixed [11]byte
		binary.LittleEndian.PutUint16(fixed[0:2], service.NetworkID)
		binary.LittleEndian.PutUint16(fixed[2:4], service.TransportStreamID)
		binary.LittleEndian.PutUint16(fixed[4:6], service.ServiceID)
		fixed[6] = service.ServiceType
		fixed[7] = service.RemoteControlKey
		fixed[8] = boolByte(service.PartialReception)
		fixed[9] = boolByte(service.EPGCapture)
		fixed[10] = boolByte(service.Search)
		_, _ = digest.Write(fixed[:])
	}
	return "v1:" + hex.EncodeToString(digest.Sum(nil))
}

func writeString(destination hash.Hash, value string) {
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = destination.Write(size[:])
	_, _ = destination.Write([]byte(value))
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}
