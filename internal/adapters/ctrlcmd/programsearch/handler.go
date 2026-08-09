package programsearch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"io"
	"sort"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/programguide"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	// CommandはKonomiTVが番組検索に使うCtrlCmd番号である。
	Command int32 = 1025
	// ResultSuccessは検索結果を最後まで出力できた応答を表す。
	ResultSuccess int32 = 1

	maxServices = 4_096
	maxPrograms = 262_144
	pageSize    = 256
	responseCap = 256 * 1024 * 1024
)

// Sourceは一つの完成済みチャンネル・番組表世代をページ単位で返す。
type Source interface {
	Current(context.Context) (channel.Snapshot, error)
	CurrentProgramsForService(context.Context, string, int, string) ([]catalogmodel.CurrentProgram, error)
}

// Handlerは検索結果の大きさを測ってから同じ番組表を逐次出力する。
type Handler struct {
	Source Source
	Limits codec.Limits
	Gate   chan struct{}
}

// NewHandlerは同時生成を共有する固定gate付きの検索Handlerを返す。
func NewHandler(source Source, limits codec.Limits, gate chan struct{}) (*Handler, error) {
	if source == nil || gate == nil {
		return nil, failure(codec.Internal, "missing-program-search-source", 0)
	}
	return &Handler{Source: source, Limits: limits, Gate: gate}, nil
}

// HandleはKonomiTVの一条件を検証し、完成済み番組表だけを検索する。
func (handler *Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	if handler == nil || ctx == nil || destination == nil {
		return failure(codec.Internal, "missing-program-search-handler", 0)
	}
	frame, err := codec.ParseRequestFrame(request, handler.Limits)
	if err != nil {
		return err
	}
	if frame.Code != Command {
		return failure(codec.Malformed, "program-search-command", int64(frame.Code))
	}
	search, err := decodeRequest(frame.Body, handler.Limits)
	if err != nil {
		return err
	}
	condition, err := prepare(search)
	if err != nil {
		return err
	}
	select {
	case handler.Gate <- struct{}{}:
		defer func() { <-handler.Gate }()
	default:
		return failure(codec.OverLimit, "program-search-busy", 1)
	}
	snapshot, err := handler.Source.Current(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return failure(codec.Timeout, "request-context-ended", 0)
		}
		return failure(codec.Internal, "program-search-source-failed", 0)
	}
	services, err := selectedServices(ctx, snapshot, search)
	if err != nil {
		return err
	}
	limits := handler.Limits
	if limits.ResponseBody == 0 || limits.ResponseBody > responseCap {
		limits.ResponseBody = responseCap
	}
	measured, err := measure(ctx, handler.Source, services, condition, limits)
	if err != nil {
		return err
	}
	pending := &deferredTail{destination: contextDestination{ctx: ctx, destination: destination}}
	if err := codec.WriteFrame(pending, ResultSuccess, measured.bodySize, limits,
		func(writer *codec.Writer) error {
			if err := writer.I32(int32(measured.bodySize)); err != nil {
				return err
			}
			if err := writer.I32(int32(measured.count)); err != nil {
				return err
			}
			written, err := writeMatches(ctx, writer, handler.Source, services, condition, limits)
			if err != nil {
				return err
			}
			if written.count != measured.count || written.bodySize != measured.bodySize || written.digest != measured.digest {
				return failure(codec.Internal, "program-search-generation-changed", int64(written.count))
			}
			return nil
		}); err != nil {
		return err
	}
	return pending.Commit()
}

func selectedServices(ctx context.Context, snapshot channel.Snapshot, search core.SearchCondition) ([]channel.Service, error) {
	services, err := channel.ValidateSnapshot(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	if len(services) > maxServices {
		return nil, failure(codec.OverLimit, "program-search-service-count", int64(len(services)))
	}
	requested := make(map[uint64]struct{}, len(search.Services))
	for _, service := range search.Services {
		key := uint64(service.NetworkID)<<32 | uint64(service.TransportStreamID)<<16 | uint64(service.ServiceID)
		requested[key] = struct{}{}
	}
	selected := services[:0]
	for _, service := range services {
		key := uint64(service.NetworkID)<<32 | uint64(service.TransportStreamID)<<16 | uint64(service.ServiceID)
		if _, found := requested[key]; found && service.Search {
			selected = append(selected, service)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ProviderLocator < selected[j].ProviderLocator })
	return selected, nil
}

type searchStats struct {
	count    int
	bodySize int64
	digest   [sha256.Size]byte
}

func measure(ctx context.Context, source Source, services []channel.Service, condition preparedCondition,
	limits codec.Limits,
) (searchStats, error) {
	stats := searchStats{bodySize: 8}
	digest := sha256.New()
	err := forEachProgram(ctx, source, services, func(_ channel.Service, program catalogmodel.CurrentProgram) error {
		if !condition.matches(program) {
			return nil
		}
		eventSize, eligible, err := programguide.EventStructureSize(program, limits)
		if err != nil || !eligible {
			return err
		}
		stats.count++
		if stats.count > maxPrograms {
			return failure(codec.OverLimit, "program-search-result-count", int64(stats.count))
		}
		stats.bodySize, err = addSize(stats.bodySize, eventSize, int64(limits.ResponseBody))
		if err == nil {
			digestProgram(digest, program, eventSize)
		}
		return err
	})
	if err != nil {
		return searchStats{}, err
	}
	copy(stats.digest[:], digest.Sum(nil))
	return stats, nil
}

func writeMatches(ctx context.Context, writer *codec.Writer, source Source, services []channel.Service,
	condition preparedCondition, limits codec.Limits,
) (searchStats, error) {
	stats := searchStats{bodySize: 8}
	digest := sha256.New()
	err := forEachProgram(ctx, source, services, func(service channel.Service, program catalogmodel.CurrentProgram) error {
		if !condition.matches(program) {
			return nil
		}
		eventSize, eligible, err := programguide.EventStructureSize(program, limits)
		if err != nil || !eligible {
			return err
		}
		stats.count++
		stats.bodySize, err = addSize(stats.bodySize, eventSize, int64(limits.ResponseBody))
		if err != nil {
			return err
		}
		digestProgram(digest, program, eventSize)
		return programguide.WriteEvent(writer, service, program, limits)
	})
	if err != nil {
		return searchStats{}, err
	}
	copy(stats.digest[:], digest.Sum(nil))
	return stats, nil
}

func forEachProgram(ctx context.Context, source Source, services []channel.Service,
	visit func(channel.Service, catalogmodel.CurrentProgram) error,
) error {
	seen := 0
	for _, service := range services {
		afterEvent := ""
		for {
			if err := ctx.Err(); err != nil {
				return failure(codec.Timeout, "request-context-ended", 0)
			}
			page, err := source.CurrentProgramsForService(ctx, service.ProviderLocator, pageSize, afterEvent)
			if err != nil {
				if ctx.Err() != nil {
					return failure(codec.Timeout, "request-context-ended", 0)
				}
				return failure(codec.Internal, "program-search-source-failed", 0)
			}
			if len(page) > pageSize {
				return failure(codec.Internal, "program-search-source-failed", int64(len(page)))
			}
			for _, program := range page {
				seen++
				if seen > maxPrograms || program.ServiceLocator != service.ProviderLocator || program.EventLocator <= afterEvent {
					return failure(codec.Internal, "program-search-source-order", int64(seen))
				}
				afterEvent = program.EventLocator
				if err := visit(service, program); err != nil {
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

func digestProgram(destination hash.Hash, program catalogmodel.CurrentProgram, eventSize int64) {
	writeDigestText(destination, program.ServiceLocator)
	writeDigestText(destination, program.EventLocator)
	destination.Write(program.Hash[:])
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(eventSize))
	destination.Write(size[:])
}

func writeDigestText(destination hash.Hash, value string) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	destination.Write(size[:])
	destination.Write([]byte(value))
}

func addSize(current, addition, limit int64) (int64, error) {
	if current < 0 || addition < 0 || current > limit || addition > limit-current {
		return 0, failure(codec.OverLimit, "program-search-response-body", current)
	}
	return current + addition, nil
}

type contextDestination struct {
	ctx         context.Context
	destination io.Writer
}

// deferredTailは二回目の走査を確認し終えるまで応答の最後の1 byteだけを保留する。
// 世代差が見つかった応答を、完全な成功frameとして相手へ渡さないための境界である。
type deferredTail struct {
	destination io.Writer
	tail        byte
	pending     bool
}

// Writeは以前に保留した1 byteと、今回受け取った末尾以外を順に出力する。
func (destination *deferredTail) Write(data []byte) (int, error) {
	if destination == nil || destination.destination == nil {
		return 0, failure(codec.Internal, "missing-program-search-destination", 0)
	}
	if len(data) == 0 {
		return 0, nil
	}
	if destination.pending {
		if err := writeExactly(destination.destination, []byte{destination.tail}); err != nil {
			return 0, err
		}
	}
	if len(data) > 1 {
		if err := writeExactly(destination.destination, data[:len(data)-1]); err != nil {
			return 0, err
		}
	}
	destination.tail = data[len(data)-1]
	destination.pending = true
	return len(data), nil
}

// Commitは完全な成功frameだと確認できた場合だけ、保留した最後の1 byteを出力する。
func (destination *deferredTail) Commit() error {
	if destination == nil || !destination.pending {
		return failure(codec.Internal, "missing-program-search-tail", 0)
	}
	destination.pending = false
	return writeExactly(destination.destination, []byte{destination.tail})
}

func writeExactly(destination io.Writer, data []byte) error {
	written, err := destination.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return failure(codec.PeerDisconnect, "program-search-short-write", int64(written))
	}
	return nil
}

// Writeは検索応答を書き込む直前に要求の取消しを確認する。
func (destination contextDestination) Write(data []byte) (int, error) {
	if destination.ctx.Err() != nil {
		return 0, failure(codec.Timeout, "request-context-ended", 0)
	}
	written, err := destination.destination.Write(data)
	if err != nil {
		return written, failure(codec.PeerDisconnect, "program-search-write-failed", int64(written))
	}
	return written, nil
}

func failure(category codec.Category, reason string, size int64) error {
	return &codec.Error{Category: category, Reason: reason, Size: size}
}
