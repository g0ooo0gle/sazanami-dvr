// Package reservationは番組予約をCtrlCmd 2011／2013の通信形式へ変換する。
package reservation

import (
	"context"
	"io"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	// CommandListはKonomiTVが未完了予約の取得に使うCtrlCmd番号である。
	CommandList int32 = 2011
	// CommandAddはKonomiTVが番組予約の追加に使うCtrlCmd番号である。
	CommandAdd int32 = 2013
	// Versionは今回受理するCmd2の版である。
	Version uint16 = 5
	// ResultSuccessは予約一覧の取得または予約追加が完了したことを表す。
	ResultSuccess int32 = 1
	// ResultFailureは対応済み操作の入力不正または保存失敗を表す。
	ResultFailure int32 = 0

	pageSize           = 256
	maxReservations    = 16_384
	listResponseCap    = 64 * 1024 * 1024
	minimumReserveSize = 133
)

// Operationsは予約の確定保存と未完了一覧だけを提供する。
type Operations interface {
	Add(context.Context, recording.ReservationRequest) (recording.Reservation, error)
	Active(context.Context, int, int32) ([]recording.Reservation, error)
}

// Handlerは対応済みの予約操作だけをapplication層へ渡す。
type Handler struct {
	Operations Operations
	Limits     codec.Limits
}

// Handleは2011／2013を振り分け、対応済み操作の失敗理由を応答へ含めない。
func (handler Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	frame, err := codec.ParseRequestFrame(request, handler.Limits)
	if err != nil {
		return err
	}
	if ctx == nil || destination == nil || handler.Operations == nil {
		return failure(codec.Internal, "missing-reservation-dependency", 0)
	}
	switch frame.Code {
	case CommandList:
		return handler.list(ctx, frame.Body, destination)
	case CommandAdd:
		return handler.add(ctx, frame.Body, destination)
	default:
		return failure(codec.Unsupported, "command-out-of-profile", int64(frame.Code))
	}
}

func (handler Handler) list(ctx context.Context, body []byte, destination io.Writer) error {
	if len(body) != 2 {
		return writeFailure(ctx, destination, handler.Limits)
	}
	reader, err := codec.NewReader(body, handler.Limits)
	if err != nil {
		return err
	}
	version, err := reader.U16()
	if err != nil || version != Version || reader.Exact() != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	limits := handler.Limits
	if limits.ResponseBody == 0 || limits.ResponseBody > listResponseCap {
		limits.ResponseBody = listResponseCap
	}
	count, reservationsSize, err := measureReservations(ctx, handler.Operations, limits)
	if err != nil {
		return err
	}
	bodySize := int64(2 + 8 + reservationsSize)
	return codec.WriteFrame(reservationDestination{ctx: ctx, destination: destination}, ResultSuccess, bodySize, limits, func(writer *codec.Writer) error {
		if err := writer.U16(Version); err != nil {
			return err
		}
		if err := writer.I32(int32(8 + reservationsSize)); err != nil {
			return err
		}
		if err := writer.I32(int32(count)); err != nil {
			return err
		}
		return writeReservations(ctx, writer, handler.Operations, limits, count)
	})
}

func (handler Handler) add(ctx context.Context, body []byte, destination io.Writer) error {
	request, err := decodeAdd(body, handler.Limits)
	if err != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	if _, err := handler.Operations.Add(ctx, request); err != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return codec.WriteFrame(reservationDestination{ctx: ctx, destination: destination}, ResultSuccess, 2, handler.Limits, func(writer *codec.Writer) error {
		return writer.U16(Version)
	})
}

func decodeAdd(body []byte, limits codec.Limits) (recording.ReservationRequest, error) {
	reader, err := codec.NewReader(body, limits)
	if err != nil {
		return recording.ReservationRequest{}, err
	}
	version, err := reader.U16()
	if err != nil || version != Version {
		return recording.ReservationRequest{}, failure(codec.Malformed, "reservation-version", int64(version))
	}
	var request recording.ReservationRequest
	count := 0
	err = reader.Vector(4, 1, func(item *codec.Reader, _ int) error {
		count++
		return item.Structure(func(value *codec.Reader) error {
			return decodeReservation(value, &request)
		})
	})
	if err != nil || count != 1 {
		return recording.ReservationRequest{}, failure(codec.Malformed, "reservation-vector", int64(count))
	}
	if err := reader.Exact(); err != nil {
		return recording.ReservationRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return recording.ReservationRequest{}, failure(codec.Malformed, "reservation-value", 0)
	}
	return request, nil
}

func decodeReservation(reader *codec.Reader, request *recording.ReservationRequest) error {
	if _, err := reader.String(); err != nil {
		return err
	}
	start, err := reader.SystemTime()
	if err != nil {
		return err
	}
	duration, err := reader.U32()
	if err != nil {
		return err
	}
	if _, err := reader.String(); err != nil {
		return err
	}
	networkID, err := reader.U16()
	if err != nil {
		return err
	}
	transportID, err := reader.U16()
	if err != nil {
		return err
	}
	serviceID, err := reader.U16()
	if err != nil {
		return err
	}
	eventID, err := reader.U16()
	if err != nil {
		return err
	}
	if _, err := reader.String(); err != nil {
		return err
	}
	reserveID, err := reader.I32()
	if err != nil {
		return err
	}
	unknown, err := reader.U8()
	if err != nil {
		return err
	}
	overlap, err := reader.U8()
	if err != nil {
		return err
	}
	unused, err := reader.String()
	if err != nil {
		return err
	}
	epgStart, err := reader.SystemTime()
	if err != nil {
		return err
	}
	priority, follow, err := decodeSettings(reader)
	if err != nil {
		return err
	}
	trailingOne, err := reader.I32()
	if err != nil {
		return err
	}
	fileNames := 0
	if err := reader.Vector(6, 1, func(item *codec.Reader, _ int) error {
		fileNames++
		_, readErr := item.String()
		return readErr
	}); err != nil {
		return err
	}
	trailingTwo, err := reader.I32()
	if err != nil {
		return err
	}
	if reserveID != 0 || unknown != 0 || overlap != 0 || unused != "" || !epgStart.Equal(start) ||
		trailingOne != 0 || fileNames != 0 || trailingTwo != 0 || duration < 1 || duration > 86_400 {
		return failure(codec.Malformed, "reservation-server-field", 0)
	}
	*request = recording.ReservationRequest{
		NetworkID: networkID, TransportStreamID: transportID, ServiceID: serviceID, EventID: eventID,
		Start: start, Duration: time.Duration(duration) * time.Second, Priority: priority, RequestedFollow: follow,
	}
	return nil
}

func decodeSettings(reader *codec.Reader) (uint8, bool, error) {
	var priority uint8
	var follow bool
	err := reader.Structure(func(item *codec.Reader) error {
		recordingMode, err := item.U8()
		if err != nil {
			return err
		}
		priority, err = item.U8()
		if err != nil {
			return err
		}
		followValue, err := item.U8()
		if err != nil {
			return err
		}
		serviceMode, err := item.U32()
		if err != nil {
			return err
		}
		exact, err := item.U8()
		if err != nil {
			return err
		}
		batch, err := item.String()
		if err != nil {
			return err
		}
		folders, err := decodeFolderVector(item)
		if err != nil {
			return err
		}
		suspend, err := item.U8()
		if err != nil {
			return err
		}
		reboot, err := item.U8()
		if err != nil {
			return err
		}
		useMargins, err := item.U8()
		if err != nil {
			return err
		}
		startMargin, err := item.I32()
		if err != nil {
			return err
		}
		endMargin, err := item.I32()
		if err != nil {
			return err
		}
		continued, err := item.U8()
		if err != nil {
			return err
		}
		partial, err := item.U8()
		if err != nil {
			return err
		}
		tuner, err := item.U32()
		if err != nil {
			return err
		}
		partialFolders, err := decodeFolderVector(item)
		if err != nil {
			return err
		}
		if recordingMode != 1 || priority < 1 || priority > 5 || followValue > 1 || serviceMode != 0 ||
			exact != 0 || batch != "" || folders != 0 || suspend != 0 || reboot != 0 || useMargins != 0 ||
			startMargin != 0 || endMargin != 0 || continued != 0 || partial != 0 || tuner != 0 || partialFolders != 0 {
			return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
		}
		follow = followValue == 1
		return nil
	})
	return priority, follow, err
}

func decodeFolderVector(reader *codec.Reader) (int, error) {
	count := 0
	err := reader.Vector(4, 1, func(item *codec.Reader, _ int) error {
		count++
		return item.Structure(func(folder *codec.Reader) error {
			for range 4 {
				if _, err := folder.String(); err != nil {
					return err
				}
			}
			return nil
		})
	})
	return count, err
}

func measureReservations(ctx context.Context, operations Operations, limits codec.Limits) (int, int64, error) {
	count := 0
	var size int64
	err := forEachReservation(ctx, operations, func(reservation recording.Reservation) error {
		count++
		if count > maxReservations {
			return failure(codec.OverLimit, "reservation-count", int64(count))
		}
		itemSize, err := reservationSize(reservation, limits)
		if err != nil {
			return err
		}
		size, err = addReservationSize(size, itemSize, int64(limits.ResponseBody)-10)
		return err
	})
	return count, size, err
}

func reservationSize(reservation recording.Reservation, limits codec.Limits) (int64, error) {
	if err := validateStoredReservation(reservation); err != nil {
		return 0, err
	}
	title, err := codec.StringSize(reservation.Program.Title, limits)
	if err != nil {
		return 0, err
	}
	station, err := codec.StringSize(reservation.Program.StationName, limits)
	if err != nil {
		return 0, err
	}
	return minimumReserveSize + title + station, nil
}

func writeReservations(ctx context.Context, writer *codec.Writer, operations Operations, limits codec.Limits, expected int) error {
	written := 0
	err := forEachReservation(ctx, operations, func(reservation recording.Reservation) error {
		if err := writeReservation(writer, reservation, limits); err != nil {
			return err
		}
		written++
		return nil
	})
	if err != nil {
		return err
	}
	if written != expected {
		return failure(codec.Internal, "reservation-generation-changed", int64(written))
	}
	return nil
}

func writeReservation(writer *codec.Writer, reservation recording.Reservation, limits codec.Limits) error {
	size, err := reservationSize(reservation, limits)
	if err != nil {
		return err
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	if err := writer.String(reservation.Program.Title); err != nil {
		return err
	}
	if err := writer.SystemTime(reservation.Program.Start); err != nil {
		return err
	}
	if err := writer.U32(uint32(reservation.Program.Duration / time.Second)); err != nil {
		return err
	}
	if err := writer.String(reservation.Program.StationName); err != nil {
		return err
	}
	for _, value := range [...]uint16{reservation.Program.NetworkID, reservation.Program.TransportStreamID, reservation.Program.ServiceID, reservation.Program.EventID} {
		if err := writer.U16(value); err != nil {
			return err
		}
	}
	if err := writer.String(""); err != nil {
		return err
	}
	if err := writer.I32(reservation.Number); err != nil {
		return err
	}
	if err := writer.U8(0); err != nil {
		return err
	}
	if err := writer.U8(0); err != nil {
		return err
	}
	if err := writer.String(""); err != nil {
		return err
	}
	if err := writer.SystemTime(reservation.Program.Start); err != nil {
		return err
	}
	if err := writeSettings(writer, reservation.Priority); err != nil {
		return err
	}
	if err := writer.I32(0); err != nil {
		return err
	}
	if err := writeEmptyVector(writer); err != nil {
		return err
	}
	return writer.I32(0)
}

func writeSettings(writer *codec.Writer, priority uint8) error {
	if err := writer.I32(51); err != nil {
		return err
	}
	for _, value := range [...]uint8{1, priority, 0} {
		if err := writer.U8(value); err != nil {
			return err
		}
	}
	if err := writer.U32(0); err != nil {
		return err
	}
	if err := writer.U8(0); err != nil {
		return err
	}
	if err := writer.String(""); err != nil {
		return err
	}
	if err := writeEmptyVector(writer); err != nil {
		return err
	}
	for range 3 {
		if err := writer.U8(0); err != nil {
			return err
		}
	}
	for range 2 {
		if err := writer.I32(0); err != nil {
			return err
		}
	}
	for range 2 {
		if err := writer.U8(0); err != nil {
			return err
		}
	}
	if err := writer.U32(0); err != nil {
		return err
	}
	return writeEmptyVector(writer)
}

func writeEmptyVector(writer *codec.Writer) error {
	if err := writer.I32(8); err != nil {
		return err
	}
	return writer.I32(0)
}

func validateStoredReservation(reservation recording.Reservation) error {
	if reservation.Number < 1 || reservation.Version != 1 || reservation.State != recording.ReservationActive ||
		reservation.EffectiveFollow || reservation.Priority < 1 || reservation.Priority > 5 ||
		reservation.Program.Start.IsZero() || reservation.Program.Start.Location() != time.UTC ||
		reservation.Program.Duration < time.Second || reservation.Program.Duration > 24*time.Hour ||
		reservation.Program.Duration%time.Second != 0 {
		return failure(codec.Internal, "invalid-stored-reservation", int64(reservation.Number))
	}
	return nil
}

func forEachReservation(ctx context.Context, operations Operations, visit func(recording.Reservation) error) error {
	after := int32(0)
	for {
		if err := ctx.Err(); err != nil {
			return failure(codec.Timeout, "request-context-ended", 0)
		}
		page, err := operations.Active(ctx, pageSize, after)
		if err != nil || len(page) > pageSize {
			return failure(codec.Internal, "reservation-source-failed", int64(len(page)))
		}
		for _, reservation := range page {
			if reservation.Number <= after {
				return failure(codec.Internal, "reservation-source-order", int64(reservation.Number))
			}
			after = reservation.Number
			if err := visit(reservation); err != nil {
				return err
			}
		}
		if len(page) < pageSize {
			return nil
		}
	}
}

func addReservationSize(current, addition, limit int64) (int64, error) {
	if current < 0 || addition < 0 || current > limit || addition > limit-current {
		return 0, failure(codec.OverLimit, "reservation-response-body", current)
	}
	return current + addition, nil
}

func writeFailure(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(reservationDestination{ctx: ctx, destination: destination}, ResultFailure, 0, limits, func(*codec.Writer) error { return nil })
}

type reservationDestination struct {
	ctx         context.Context
	destination io.Writer
}

// Writeは応答を書き込む直前にrequestの有効期限を確認する。
func (destination reservationDestination) Write(data []byte) (int, error) {
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
