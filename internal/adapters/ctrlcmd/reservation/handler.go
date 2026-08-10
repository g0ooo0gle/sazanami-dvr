// Package reservationは番組予約と録画中確認をKonomiTV向けCtrlCmd形式へ変換する。
package reservation

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	// CommandListはKonomiTVが未完了予約の取得に使うCtrlCmd番号である。
	CommandList int32 = 2011
	// CommandAddはKonomiTVが番組予約の追加に使うCtrlCmd番号である。
	CommandAdd int32 = 2013
	// CommandChangeはKonomiTVが録画開始前の予約設定を更新するCtrlCmd番号である。
	CommandChange int32 = 2015
	// CommandDeleteはKonomiTVが録画開始前の予約を取り消すCtrlCmd番号である。
	CommandDelete int32 = 1014
	// CommandRecordingOpenはKonomiTVが予約の録画中状態を確認するCtrlCmd番号である。
	CommandRecordingOpen int32 = 1087
	// CommandRecordingCloseは録画中確認の直後にKonomiTVが送るCtrlCmd番号である。
	CommandRecordingClose int32 = 1081
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
	recordingPath      = "sazanami-recording.ts"
)

// Operationsは予約の保存、変更、取消し、録画中照合を提供する。
type Operations interface {
	Add(context.Context, recording.ReservationRequest) (recording.Reservation, error)
	Active(context.Context, int, int32) ([]recording.Reservation, error)
	Change(context.Context, recording.ReservationChange) error
	Delete(context.Context, int32) error
	Recording(context.Context, int32) (bool, error)
}

// ScriptValidatorは予約へ保存する録画後スクリプトが専用ディレクトリ内にあるかを確認する。
type ScriptValidator interface {
	Validate(string) error
}

// Handlerは対応済みの予約操作だけをapplication層へ渡す。
type Handler struct {
	Operations      Operations
	ScriptValidator ScriptValidator
	Limits          codec.Limits
}

// Handleは対応済みの予約commandを振り分け、失敗理由を応答へ含めない。
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
	case CommandChange:
		return handler.change(ctx, frame.Body, destination)
	case CommandDelete:
		return handler.delete(ctx, frame.Body, destination)
	case CommandRecordingOpen:
		return handler.recordingOpen(ctx, frame.Body, destination)
	case CommandRecordingClose:
		return handler.recordingClose(ctx, frame.Body, destination)
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
	change, err := decodeReservationRequest(body, handler.Limits, false)
	if err != nil || !handler.validScript(change.Request.PostRecording.Script) {
		return writeFailure(ctx, destination, handler.Limits)
	}
	if _, err := handler.Operations.Add(ctx, change.Request); err != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return writeMutationSuccess(ctx, destination, handler.Limits)
}

func (handler Handler) change(ctx context.Context, body []byte, destination io.Writer) error {
	change, err := decodeReservationRequest(body, handler.Limits, true)
	if err != nil || !handler.validScript(change.Request.PostRecording.Script) || handler.Operations.Change(ctx, change) != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return writeMutationSuccess(ctx, destination, handler.Limits)
}

func (handler Handler) validScript(path string) bool {
	return path == "" || handler.ScriptValidator != nil && handler.ScriptValidator.Validate(path) == nil
}

func (handler Handler) delete(ctx context.Context, body []byte, destination io.Writer) error {
	number, err := decodeOneNumber(body, handler.Limits, true)
	if err != nil || handler.Operations.Delete(ctx, number) != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return writeMutationSuccess(ctx, destination, handler.Limits)
}

func (handler Handler) recordingOpen(ctx context.Context, body []byte, destination io.Writer) error {
	number, err := decodeOneNumber(body, handler.Limits, false)
	if err != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	active, err := handler.Operations.Recording(ctx, number)
	if err != nil || !active {
		return writeFailure(ctx, destination, handler.Limits)
	}
	stringSize, err := codec.StringSize(recordingPath, handler.Limits)
	if err != nil {
		return err
	}
	structureSize := int64(8) + stringSize
	return codec.WriteFrame(reservationDestination{ctx: ctx, destination: destination}, ResultSuccess, structureSize, handler.Limits, func(writer *codec.Writer) error {
		if err := writer.I32(int32(structureSize)); err != nil {
			return err
		}
		if err := writer.I32(number); err != nil {
			return err
		}
		return writer.String(recordingPath)
	})
}

func (handler Handler) recordingClose(ctx context.Context, body []byte, destination io.Writer) error {
	if _, err := decodeOneNumber(body, handler.Limits, false); err != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return writeEmptySuccess(ctx, destination, handler.Limits)
}

func decodeReservationRequest(body []byte, limits codec.Limits, requireNumber bool) (recording.ReservationChange, error) {
	reader, err := codec.NewReader(body, limits)
	if err != nil {
		return recording.ReservationChange{}, err
	}
	version, err := reader.U16()
	if err != nil || version != Version {
		return recording.ReservationChange{}, failure(codec.Malformed, "reservation-version", int64(version))
	}
	var change recording.ReservationChange
	count := 0
	err = reader.Vector(4, 1, func(item *codec.Reader, _ int) error {
		count++
		return item.Structure(func(value *codec.Reader) error {
			return decodeReservation(value, &change, requireNumber)
		})
	})
	if err != nil || count != 1 {
		return recording.ReservationChange{}, failure(codec.Malformed, "reservation-vector", int64(count))
	}
	if err := reader.Exact(); err != nil {
		return recording.ReservationChange{}, err
	}
	if change.Request.Validate() != nil || requireNumber && change.Validate() != nil || !requireNumber && change.Number != 0 {
		return recording.ReservationChange{}, failure(codec.Malformed, "reservation-value", 0)
	}
	return change, nil
}

func decodeReservation(reader *codec.Reader, change *recording.ReservationChange, requireNumber bool) error {
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
	settings, err := decodeSettings(reader)
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
		// KonomiTVは2011で受け取った予定名を2015へ送り返す。値は保存先として使わない。
		_, readErr := item.String()
		return readErr
	}); err != nil {
		return err
	}
	trailingTwo, err := reader.I32()
	if err != nil {
		return err
	}
	if (!requireNumber && reserveID != 0) || (requireNumber && reserveID < 1) || unknown != 0 || overlap != 0 || unused != "" || !epgStart.Equal(start) ||
		trailingOne != 0 || (!requireNumber && fileNames != 0) || trailingTwo != 0 || duration < 1 || duration > 86_400 {
		return failure(codec.Malformed, "reservation-server-field", 0)
	}
	change.Number = reserveID
	change.Request = recording.ReservationRequest{
		NetworkID: networkID, TransportStreamID: transportID, ServiceID: serviceID, EventID: eventID,
		Start: start, Duration: time.Duration(duration) * time.Second, Priority: settings.priority,
		RequestedFollow: settings.follow, Disabled: settings.disabled, Margins: settings.margins, Output: settings.output,
		Components: settings.components, PostRecording: settings.postRecording,
	}
	return nil
}

func decodeOneNumber(body []byte, limits codec.Limits, vector bool) (int32, error) {
	reader, err := codec.NewReader(body, limits)
	if err != nil {
		return 0, err
	}
	var number int32
	if vector {
		count := 0
		err = reader.Vector(4, 1, func(item *codec.Reader, _ int) error {
			count++
			value, readErr := item.I32()
			number = value
			return readErr
		})
		if err != nil || count != 1 || len(body) != 12 {
			return 0, failure(codec.Malformed, "reservation-id-vector", int64(count))
		}
	} else {
		if len(body) != 4 {
			return 0, failure(codec.Malformed, "reservation-id-body", int64(len(body)))
		}
		number, err = reader.I32()
	}
	if err != nil || number < 1 || reader.Exact() != nil {
		return 0, failure(codec.Malformed, "reservation-id", int64(number))
	}
	return number, nil
}

type decodedSettings struct {
	priority      uint8
	follow        bool
	disabled      bool
	margins       *recording.RecordingMargins
	output        recording.OutputSettings
	components    recording.ComponentMode
	postRecording recording.PostRecordingSettings
}

func decodeSettings(reader *codec.Reader) (decodedSettings, error) {
	var settings decodedSettings
	err := reader.Structure(func(item *codec.Reader) error {
		recordingMode, err := item.U8()
		if err != nil {
			return err
		}
		settings.priority, err = item.U8()
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
		settings.components, err = componentModeFromWire(serviceMode)
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
		settings.output, err = decodeOutputFolderVector(item)
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
		partialFolders, err := decodeFolderCount(item)
		if err != nil {
			return err
		}
		switch {
		case suspend == 0 && reboot == 0:
			settings.postRecording.Mode = recording.PostRecordingDefault
		case suspend == 4 && reboot == 0:
			settings.postRecording.Mode = recording.PostRecordingNothing
		default:
			return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
		}
		settings.postRecording.Script = batch
		if settings.postRecording.Validate() != nil {
			return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
		}
		if (recordingMode != 1 && recordingMode != 5) || settings.priority < 1 || settings.priority > 5 ||
			followValue > 1 ||
			exact != 0 || useMargins > 1 ||
			continued != 0 || partial != 0 || tuner != 0 || partialFolders != 0 {
			return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
		}
		if useMargins == 0 {
			if startMargin != 0 || endMargin != 0 {
				return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
			}
		} else if useMargins == 1 && startMargin >= -3600 && startMargin <= 3600 && endMargin >= -3600 && endMargin <= 3600 {
			settings.margins = &recording.RecordingMargins{
				Start: time.Duration(startMargin) * time.Second,
				End:   time.Duration(endMargin) * time.Second,
			}
		} else {
			return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
		}
		settings.follow = followValue == 1
		settings.disabled = recordingMode == 5
		return nil
	})
	return settings, err
}

func decodeOutputFolderVector(reader *codec.Reader) (recording.OutputSettings, error) {
	var result recording.OutputSettings
	count := 0
	err := reader.Vector(4, 1, func(item *codec.Reader, _ int) error {
		count++
		return item.Structure(func(folder *codec.Reader) error {
			pathValue, err := folder.String()
			if err != nil {
				return err
			}
			writer, err := folder.String()
			if err != nil {
				return err
			}
			name, err := folder.String()
			if err != nil {
				return err
			}
			reserved, err := folder.String()
			if err != nil {
				return err
			}
			const plugin = "RecName_Macro.dll"
			template := ""
			switch {
			case name == plugin:
			case strings.HasPrefix(name, plugin+"?") && len(name) > len(plugin)+1:
				template = name[len(plugin)+1:]
			default:
				return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
			}
			result = recording.OutputSettings{Folder: pathValue, Template: template}
			if writer != "Write_Default.dll" || reserved != "" || result.Validate() != nil {
				return failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
			}
			return nil
		})
	})
	if err != nil {
		return recording.OutputSettings{}, err
	}
	if count == 1 && result.Folder == "" && result.Template == "" {
		return recording.OutputSettings{}, nil
	}
	return result, nil
}

func decodeFolderCount(reader *codec.Reader) (int, error) {
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
	folders, err := reservationFoldersSize(reservation, limits)
	if err != nil {
		return 0, err
	}
	files, err := reservationFileNamesSize(reservation, limits)
	if err != nil {
		return 0, err
	}
	postScript, err := codec.StringSize(reservation.PostRecording.Script, limits)
	if err != nil {
		return 0, err
	}
	emptyScript, err := codec.StringSize("", limits)
	if err != nil {
		return 0, err
	}
	return minimumReserveSize + title + station + folders - 8 + files - 8 + postScript - emptyScript, nil
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
	if err := writeSettings(writer, reservation, limits); err != nil {
		return err
	}
	if err := writer.I32(0); err != nil {
		return err
	}
	if err := writeReservationFileNames(writer, reservation, limits); err != nil {
		return err
	}
	return writer.I32(0)
}

func writeSettings(writer *codec.Writer, reservation recording.Reservation, limits codec.Limits) error {
	folders, err := reservationFoldersSize(reservation, limits)
	if err != nil {
		return err
	}
	postScript, err := codec.StringSize(reservation.PostRecording.Script, limits)
	if err != nil {
		return err
	}
	emptyScript, err := codec.StringSize("", limits)
	if err != nil {
		return err
	}
	if err := writer.I32(int32(43 + folders + postScript - emptyScript)); err != nil {
		return err
	}
	followValue, recordingMode := uint8(0), uint8(1)
	if reservation.EffectiveFollow {
		followValue = 1
	}
	if reservation.Disabled {
		recordingMode = 5
	}
	for _, value := range [...]uint8{recordingMode, reservation.Priority, followValue} {
		if err := writer.U8(value); err != nil {
			return err
		}
	}
	serviceMode, err := componentModeToWire(reservation.Components)
	if err != nil {
		return failure(codec.Internal, "invalid-stored-reservation-components", int64(reservation.Number))
	}
	if err := writer.U32(serviceMode); err != nil {
		return err
	}
	if err := writer.U8(0); err != nil {
		return err
	}
	if err := writer.String(reservation.PostRecording.Script); err != nil {
		return err
	}
	if err := writeReservationFolders(writer, reservation, limits); err != nil {
		return err
	}
	suspendMode := uint8(0)
	if reservation.PostRecording.Mode == recording.PostRecordingNothing {
		suspendMode = 4
	} else if reservation.PostRecording.Mode != recording.PostRecordingDefault {
		return failure(codec.Internal, "invalid-stored-post-recording-settings", int64(reservation.Number))
	}
	if err := writer.U8(suspendMode); err != nil {
		return err
	}
	if err := writer.U8(0); err != nil {
		return err
	}
	useMargins := uint8(0)
	margins := recording.RecordingMargins{}
	if reservation.Margins != nil {
		useMargins = 1
		margins = *reservation.Margins
	}
	if err := writer.U8(useMargins); err != nil {
		return err
	}
	if err := writer.I32(int32(margins.Start / time.Second)); err != nil {
		return err
	}
	if err := writer.I32(int32(margins.End / time.Second)); err != nil {
		return err
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

// componentModeFromWireはKonomiTVの個別指定bitを予約domainの固定値へ正規化する。
func componentModeFromWire(value uint32) (recording.ComponentMode, error) {
	if value&^uint32(0x31) != 0 {
		return recording.ComponentDefault, failure(codec.Unsupported, "recording-setting-out-of-profile", 0)
	}
	if value&0x01 == 0 {
		return recording.ComponentDefault, nil
	}
	return recording.ExplicitComponentMode(value&0x10 != 0, value&0x20 != 0), nil
}

// componentModeToWireは保存した既定値または明示指定をKonomiTVのbitへ戻す。
func componentModeToWire(mode recording.ComponentMode) (uint32, error) {
	switch mode {
	case recording.ComponentDefault:
		return 0, nil
	case recording.ComponentNeither:
		return 0x01, nil
	case recording.ComponentCaptionsOnly:
		return 0x11, nil
	case recording.ComponentDataOnly:
		return 0x21, nil
	case recording.ComponentBoth:
		return 0x31, nil
	default:
		return 0, failure(codec.Internal, "invalid-stored-reservation-components", 0)
	}
}

func reservationFoldersSize(reservation recording.Reservation, limits codec.Limits) (int64, error) {
	if reservation.Output == (recording.OutputSettings{}) {
		return 8, nil
	}
	if reservation.Output.Validate() != nil {
		return 0, failure(codec.Internal, "invalid-stored-reservation-output", int64(reservation.Number))
	}
	values := []string{reservation.Output.Folder, "Write_Default.dll", "RecName_Macro.dll", ""}
	if reservation.Output.Template != "" {
		values[2] += "?" + reservation.Output.Template
	}
	size := int64(12)
	for _, value := range values {
		field, err := codec.StringSize(value, limits)
		if err != nil {
			return 0, err
		}
		size += field
	}
	return size, nil
}

func writeReservationFolders(writer *codec.Writer, reservation recording.Reservation, limits codec.Limits) error {
	size, err := reservationFoldersSize(reservation, limits)
	if err != nil {
		return err
	}
	count := int32(0)
	if reservation.Output != (recording.OutputSettings{}) {
		count = 1
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	if err := writer.I32(count); err != nil || count == 0 {
		return err
	}
	if err := writer.I32(int32(size - 8)); err != nil {
		return err
	}
	name := "RecName_Macro.dll"
	if reservation.Output.Template != "" {
		name += "?" + reservation.Output.Template
	}
	for _, value := range []string{reservation.Output.Folder, "Write_Default.dll", name, ""} {
		if err := writer.String(value); err != nil {
			return err
		}
	}
	return nil
}

func reservationFileNamesSize(reservation recording.Reservation, limits codec.Limits) (int64, error) {
	value, ok, err := recording.ScheduledOutputPath(reservation)
	if err != nil {
		return 0, failure(codec.Internal, "invalid-stored-reservation-output", int64(reservation.Number))
	}
	if !ok {
		return 8, nil
	}
	size, err := codec.StringSize(value, limits)
	if err != nil {
		return 0, err
	}
	return 8 + size, nil
}

func writeReservationFileNames(writer *codec.Writer, reservation recording.Reservation, limits codec.Limits) error {
	size, err := reservationFileNamesSize(reservation, limits)
	if err != nil {
		return err
	}
	value, ok, err := recording.ScheduledOutputPath(reservation)
	if err != nil {
		return err
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	count := int32(0)
	if ok {
		count = 1
	}
	if err := writer.I32(count); err != nil || !ok {
		return err
	}
	return writer.String(value)
}

func writeEmptyVector(writer *codec.Writer) error {
	if err := writer.I32(8); err != nil {
		return err
	}
	return writer.I32(0)
}

func validateStoredReservation(reservation recording.Reservation) error {
	if reservation.Number < 1 || reservation.Version < 1 || reservation.State != recording.ReservationActive ||
		reservation.EffectiveFollow != reservation.RequestedFollow || reservation.Priority < 1 || reservation.Priority > 5 ||
		reservation.Program.Start.IsZero() || reservation.Program.Start.Location() != time.UTC ||
		reservation.Program.Duration < time.Second || reservation.Program.Duration > 24*time.Hour ||
		reservation.Program.Duration%time.Second != 0 || !reservation.PlannedEnd().After(reservation.PlannedStart()) ||
		reservation.PlannedEnd().Sub(reservation.PlannedStart()) > recording.MaxEffectiveDuration || reservation.Output.Validate() != nil {
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

func writeEmptySuccess(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(reservationDestination{ctx: ctx, destination: destination}, ResultSuccess, 0, limits, func(*codec.Writer) error { return nil })
}

// writeMutationSuccessは予約の追加・変更・取消しに共通する成功値を返す。
// KonomiTVは外側の成功コードを使い、Komorebiは本文の32-bit値を使うため、両方を常に1へそろえる。
func writeMutationSuccess(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(reservationDestination{ctx: ctx, destination: destination}, ResultSuccess, 4, limits, func(writer *codec.Writer) error {
		return writer.I32(ResultSuccess)
	})
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
