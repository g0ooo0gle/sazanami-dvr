// Package programguideは完成済み番組表をCtrlCmd 1029の応答へ変換する。
package programguide

import (
	"context"
	"io"
	"math"
	"sort"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	// CommandはKonomiTVが番組表の取得に使うCtrlCmd番号である。
	Command int32 = 1029
	// ResultSuccessは番組表を最後まで出力できた応答を表す。
	ResultSuccess int32 = 1

	maxServices = 4_096
	maxPrograms = 262_144
	pageSize    = 256
	responseCap = 256 * 1024 * 1024

	fileTimeUnixEpoch = int64(116_444_736_000_000_000)
	fileTimeJSTOffset = int64(9 * time.Hour / (100 * time.Nanosecond))
)

var acceptedSelectors = [...]int64{0xffffffffffff, 0xffffffffffff, 1, 0x7fffffffffffffff}
var japanStandardTime = time.FixedZone("Asia/Tokyo", 9*60*60)

type programQuery struct {
	exactService bool
	serviceKey   uint64
	start        int64
	end          int64
}

// Sourceは起動時に固定したチャンネルと完成済み番組表だけを返す。
type Source interface {
	Current(context.Context) (channel.Snapshot, error)
	CurrentProgramsForService(context.Context, string, int, string) ([]catalogmodel.CurrentProgram, error)
}

// Handlerは番組表を二回走査し、全番組をメモリへ保持せずに応答する。
type Handler struct {
	Source Source
	Limits codec.Limits
	heavy  chan struct{}
}

// NewHandlerは同時に一件だけ番組表を生成するHandlerを返す。
func NewHandler(source Source, limits codec.Limits) (*Handler, error) {
	if source == nil {
		return nil, failure(codec.Internal, "missing-program-source", 0)
	}
	return &Handler{Source: source, Limits: limits, heavy: make(chan struct{}, 1)}, nil
}

// Handleは完全一致する検索条件だけを受け、応答サイズの確定後に逐次出力する。
func (handler *Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	if handler == nil || ctx == nil {
		return failure(codec.Internal, "missing-program-handler", 0)
	}
	frame, err := codec.ParseRequestFrame(request, handler.Limits)
	if err != nil {
		return err
	}
	if frame.Code != Command || len(frame.Body) != 40 {
		return failure(codec.Malformed, "program-request-shape", int64(len(frame.Body)))
	}
	reader, err := codec.NewReader(frame.Body, handler.Limits)
	if err != nil {
		return err
	}
	selectors := [4]int64{}
	if err := reader.Vector(8, len(selectors), func(item *codec.Reader, index int) error {
		value, readErr := item.I64()
		selectors[index] = value
		return readErr
	}); err != nil {
		return err
	}
	if err := reader.Exact(); err != nil {
		return err
	}
	query, accepted := parseProgramQuery(selectors)
	if !accepted {
		return failure(codec.Unsupported, "program-selector-out-of-profile", 0)
	}
	if destination == nil {
		return failure(codec.Internal, "nil-response-writer", 0)
	}
	select {
	case handler.heavy <- struct{}{}:
		defer func() { <-handler.heavy }()
	default:
		return failure(codec.OverLimit, "program-generation-busy", 1)
	}

	snapshot, err := handler.Source.Current(ctx)
	if err != nil {
		return failure(codec.Internal, "program-source-failed", 0)
	}
	services, err := channel.ValidateSnapshot(ctx, snapshot)
	if err != nil || len(services) > maxServices {
		return failure(codec.OverLimit, "program-service-count", int64(len(services)))
	}
	services = selectServices(services, query)
	sort.Slice(services, func(i, j int) bool { return services[i].ProviderLocator < services[j].ProviderLocator })
	limits := handler.Limits
	if limits.ResponseBody == 0 || limits.ResponseBody > responseCap {
		limits.ResponseBody = responseCap
	}
	stats, bodySize, err := measure(ctx, handler.Source, services, query, limits)
	if err != nil {
		return err
	}
	return codec.WriteFrame(contextDestination{ctx: ctx, destination: destination}, ResultSuccess, bodySize, limits, func(writer *codec.Writer) error {
		if err := writer.I32(int32(bodySize)); err != nil {
			return err
		}
		if err := writer.I32(int32(len(services))); err != nil {
			return err
		}
		return writePrograms(ctx, writer, handler.Source, services, stats, query, limits)
	})
}

// parseProgramQueryは、KonomiTVが使う全件取得と単一サービス・開始時刻範囲の二形式だけを受理する。
func parseProgramQuery(selectors [4]int64) (programQuery, bool) {
	if selectors == acceptedSelectors {
		return programQuery{}, true
	}
	if selectors[0] != 0 || selectors[1] < 0 || selectors[1] > 0xffffffffffff ||
		selectors[2] < 0 || selectors[2] >= selectors[3] {
		return programQuery{}, false
	}
	return programQuery{
		exactService: true,
		serviceKey:   uint64(selectors[1]),
		start:        selectors[2],
		end:          selectors[3],
	}, true
}

func selectServices(services []channel.Service, query programQuery) []channel.Service {
	if !query.exactService {
		return services
	}
	selected := services[:0]
	for _, service := range services {
		key := uint64(service.NetworkID)<<32 | uint64(service.TransportStreamID)<<16 | uint64(service.ServiceID)
		if key == query.serviceKey {
			selected = append(selected, service)
		}
	}
	return selected
}

type serviceStats struct {
	structureSize int64
	eventBytes    int64
	eventCount    int
}

func measure(ctx context.Context, source Source, services []channel.Service, query programQuery, limits codec.Limits) ([]serviceStats, int64, error) {
	stats := make([]serviceStats, len(services))
	byLocator := make(map[string]int, len(services))
	bodySize := int64(8)
	for index, service := range services {
		serviceSize, err := channel.ServiceStructureSize(service, limits)
		if err != nil {
			return nil, 0, err
		}
		stats[index].structureSize = 4 + serviceSize + 8
		byLocator[service.ProviderLocator] = index
		bodySize, err = addSize(bodySize, stats[index].structureSize, int64(limits.ResponseBody), "program-response-body")
		if err != nil {
			return nil, 0, err
		}
	}
	programCount := 0
	err := forEachProgram(ctx, source, services, func(program catalogmodel.CurrentProgram) error {
		programCount++
		if programCount > maxPrograms {
			return failure(codec.OverLimit, "program-count", int64(programCount))
		}
		index, selected := byLocator[program.ServiceLocator]
		if !programMatchesQuery(program, query) {
			return nil
		}
		eventSize, eligible, err := serializedEventSize(program, limits)
		if err != nil || !selected || !eligible {
			return err
		}
		stats[index].eventCount++
		stats[index].eventBytes, err = addSize(stats[index].eventBytes, eventSize, int64(limits.StructureExtent), "program-service-events")
		if err != nil {
			return err
		}
		stats[index].structureSize, err = addSize(stats[index].structureSize, eventSize, int64(limits.StructureExtent), "program-service-structure")
		if err != nil {
			return err
		}
		bodySize, err = addSize(bodySize, eventSize, int64(limits.ResponseBody), "program-response-body")
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return stats, bodySize, nil
}

func programMatchesQuery(program catalogmodel.CurrentProgram, query programQuery) bool {
	if !query.exactService {
		return true
	}
	start := program.Material.StartUTCMS
	if start == nil || *start < 0 || *start > (math.MaxInt64-fileTimeUnixEpoch-fileTimeJSTOffset)/10_000 {
		return false
	}
	fileTime := *start*10_000 + fileTimeUnixEpoch + fileTimeJSTOffset
	return query.start <= fileTime && fileTime < query.end
}

func serializedEventSize(program catalogmodel.CurrentProgram, limits codec.Limits) (int64, bool, error) {
	material := program.Material
	if program.RawEventID == nil || *program.RawEventID < 0 || *program.RawEventID > 65_535 ||
		material.StartUTCMS == nil || *material.StartUTCMS < 0 || material.DurationMS == nil ||
		*material.DurationMS < 1_000 || *material.DurationMS > int64((24*time.Hour)/time.Millisecond) ||
		*material.DurationMS%1_000 != 0 ||
		(material.FreeAccess != catalogmodel.FreeNo && material.FreeAccess != catalogmodel.FreeYes) ||
		material.Validation != catalogmodel.ValidationValid {
		return 0, false, nil
	}
	localStart := time.UnixMilli(*material.StartUTCMS).In(japanStandardTime)
	if localStart.Year() < 1 || localStart.Year() > 65_535 {
		return 0, false, nil
	}
	title, description := "", ""
	if material.Title != nil {
		title = *material.Title
	}
	if material.Description != nil {
		description = *material.Description
	}
	titleSize, err := codec.StringSize(title, limits)
	if err != nil {
		return 0, false, err
	}
	descriptionSize, err := codec.StringSize(description, limits)
	if err != nil {
		return 0, false, err
	}
	return 63 + titleSize + descriptionSize, true, nil
}

func writePrograms(ctx context.Context, writer *codec.Writer, source Source, services []channel.Service, stats []serviceStats, query programQuery, limits codec.Limits) error {
	byLocator := make(map[string]int, len(services))
	for index, service := range services {
		byLocator[service.ProviderLocator] = index
	}
	nextService := 0
	activeService := -1
	written := make([]int, len(services))
	writeHeader := func(index int) error {
		if err := writer.I32(int32(stats[index].structureSize)); err != nil {
			return err
		}
		if err := channel.WriteServiceStructure(writer, services[index], limits); err != nil {
			return err
		}
		if err := writer.I32(int32(8 + stats[index].eventBytes)); err != nil {
			return err
		}
		return writer.I32(int32(stats[index].eventCount))
	}
	err := forEachProgram(ctx, source, services, func(program catalogmodel.CurrentProgram) error {
		index, selected := byLocator[program.ServiceLocator]
		if !programMatchesQuery(program, query) {
			return nil
		}
		_, eligible, sizeErr := serializedEventSize(program, limits)
		if sizeErr != nil || !selected || !eligible {
			return sizeErr
		}
		if index < activeService || index < nextService-1 {
			return failure(codec.Internal, "program-order-changed", int64(index))
		}
		if activeService != index {
			for nextService < index {
				if err := writeHeader(nextService); err != nil {
					return err
				}
				nextService++
			}
			if nextService != index {
				return failure(codec.Internal, "program-order-changed", int64(index))
			}
			if err := writeHeader(index); err != nil {
				return err
			}
			nextService++
			activeService = index
		}
		if err := writeEvent(writer, services[index], program, limits); err != nil {
			return err
		}
		written[index]++
		return nil
	})
	if err != nil {
		return err
	}
	for nextService < len(services) {
		if err := writeHeader(nextService); err != nil {
			return err
		}
		nextService++
	}
	for index := range stats {
		if written[index] != stats[index].eventCount {
			return failure(codec.Internal, "program-generation-changed", int64(written[index]))
		}
	}
	return nil
}

func writeEvent(writer *codec.Writer, service channel.Service, program catalogmodel.CurrentProgram, limits codec.Limits) error {
	eventSize, eligible, err := serializedEventSize(program, limits)
	if err != nil || !eligible {
		return failure(codec.Internal, "invalid-program-during-write", 0)
	}
	material := program.Material
	if err := writer.I32(int32(eventSize)); err != nil {
		return err
	}
	for _, value := range [...]uint16{service.NetworkID, service.TransportStreamID, service.ServiceID, uint16(*program.RawEventID)} {
		if err := writer.U16(value); err != nil {
			return err
		}
	}
	if err := writer.U8(1); err != nil {
		return err
	}
	if err := writer.SystemTime(time.UnixMilli(*material.StartUTCMS).UTC()); err != nil {
		return err
	}
	if err := writer.U8(1); err != nil {
		return err
	}
	if err := writer.I32(int32(*material.DurationMS / 1_000)); err != nil {
		return err
	}
	title, description := "", ""
	if material.Title != nil {
		title = *material.Title
	}
	if material.Description != nil {
		description = *material.Description
	}
	titleSize, _ := codec.StringSize(title, limits)
	descriptionSize, _ := codec.StringSize(description, limits)
	if err := writer.I32(int32(4 + titleSize + descriptionSize)); err != nil {
		return err
	}
	if err := writer.String(title); err != nil {
		return err
	}
	if err := writer.String(description); err != nil {
		return err
	}
	for range 6 {
		if err := writer.I32(4); err != nil {
			return err
		}
	}
	freeCA := uint8(1)
	if material.FreeAccess == catalogmodel.FreeYes {
		freeCA = 0
	}
	return writer.U8(freeCA)
}

func forEachProgram(ctx context.Context, source Source, services []channel.Service, visit func(catalogmodel.CurrentProgram) error) error {
	for _, service := range services {
		afterEvent := ""
		for {
			if err := ctx.Err(); err != nil {
				return failure(codec.Timeout, "request-context-ended", 0)
			}
			page, err := source.CurrentProgramsForService(ctx, service.ProviderLocator, pageSize, afterEvent)
			if err != nil || len(page) > pageSize {
				return failure(codec.Internal, "program-source-failed", int64(len(page)))
			}
			for _, program := range page {
				if program.ServiceLocator != service.ProviderLocator || program.EventLocator <= afterEvent {
					return failure(codec.Internal, "program-source-order", 0)
				}
				afterEvent = program.EventLocator
				if err := visit(program); err != nil {
					return err
				}
			}
			if len(page) < pageSize {
				break
			}
		}
	}
	return nil
}

func addSize(current, addition, limit int64, reason string) (int64, error) {
	if current < 0 || addition < 0 || current > limit || addition > limit-current {
		return 0, failure(codec.OverLimit, reason, current)
	}
	return current + addition, nil
}

type contextDestination struct {
	ctx         context.Context
	destination io.Writer
}

// Writeは応答を書き込む直前にrequestの有効期限を確認する。
func (destination contextDestination) Write(data []byte) (int, error) {
	if err := destination.ctx.Err(); err != nil {
		return 0, failure(codec.Timeout, "request-context-ended", 0)
	}
	written, err := destination.destination.Write(data)
	if err != nil {
		return written, failure(codec.PeerDisconnect, "response-write-failed", int64(written))
	}
	return written, nil
}

func failure(category codec.Category, reason string, size int64) error {
	return &codec.Error{Category: category, Reason: reason, Size: size}
}
