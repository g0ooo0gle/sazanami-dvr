package reservation

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type fakeOperations struct {
	reservations []recording.Reservation
	added        []recording.ReservationRequest
	changed      []recording.ReservationChange
	deleted      []int32
	addErr       error
	changeErr    error
	deleteErr    error
	recording    bool
	reads        int
}

func (operations *fakeOperations) Add(_ context.Context, request recording.ReservationRequest) (recording.Reservation, error) {
	operations.added = append(operations.added, request)
	return recording.Reservation{}, operations.addErr
}

func (operations *fakeOperations) Active(_ context.Context, limit int, after int32) ([]recording.Reservation, error) {
	operations.reads++
	result := make([]recording.Reservation, 0, limit)
	for _, reservation := range operations.reservations {
		if reservation.Number > after && len(result) < limit {
			result = append(result, reservation)
		}
	}
	return result, nil
}

func (operations *fakeOperations) Change(_ context.Context, change recording.ReservationChange) error {
	operations.changed = append(operations.changed, change)
	return operations.changeErr
}

func (operations *fakeOperations) Delete(_ context.Context, number int32) error {
	operations.deleted = append(operations.deleted, number)
	return operations.deleteErr
}

func (operations *fakeOperations) Recording(_ context.Context, _ int32) (bool, error) {
	return operations.recording, nil
}

func TestListWritesOneReservationInTwoPasses(t *testing.T) {
	reservation := listedReservation(1)
	operations := &fakeOperations{reservations: []recording.Reservation{reservation}}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), listRequest(Version), &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
	reader, err := codec.NewReader(frame.Body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	version, err := reader.U16()
	if err != nil || version != Version {
		t.Fatalf("version=%d err=%v", version, err)
	}
	count := 0
	if err := reader.Vector(4, maxReservations, func(items *codec.Reader, _ int) error {
		count++
		return readListedReservation(items, reservation)
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Exact(); err != nil || count != 1 || operations.reads != 2 {
		t.Fatalf("count=%d reads=%d err=%v", count, operations.reads, err)
	}
}

func TestListAcceptsUpdatedReservationVersion(t *testing.T) {
	reservation := listedReservation(1)
	reservation.Version = 2
	handler := Handler{
		Operations: &fakeOperations{reservations: []recording.Reservation{reservation}},
		Limits:     codec.DefaultLimits(),
	}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), listRequest(Version), &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
}

func TestAddAcceptsFollowButRejectsUnsupportedSettings(t *testing.T) {
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	var response bytes.Buffer
	request := addRequest(t, Version, 1, true, false, 1)
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(frame.Body) != 2 || len(operations.added) != 1 ||
		!operations.added[0].RequestedFollow || operations.added[0].Priority != 3 {
		t.Fatalf("frame=%+v added=%+v err=%v", frame, operations.added, err)
	}

	response.Reset()
	if err := handler.Handle(context.Background(), addRequest(t, Version, 2, true, false, 1), &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultFailure || len(operations.added) != 1 {
		t.Fatalf("frame=%+v calls=%d err=%v", frame, len(operations.added), err)
	}

	response.Reset()
	if err := handler.Handle(context.Background(), addRequest(t, Version, 1, true, true, 1), &response); err != nil {
		t.Fatal(err)
	}
	frame, _ = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if frame.Code != ResultFailure || len(operations.added) != 1 {
		t.Fatalf("margin frame=%+v calls=%d", frame, len(operations.added))
	}
}

func TestAddFailureIsGenericAndVersionIsExact(t *testing.T) {
	operations := &fakeOperations{addErr: errors.New("private database detail")}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	for _, request := range [][]byte{
		addRequest(t, Version, 1, false, false, 1),
		addRequest(t, Version+1, 1, false, false, 1),
		addRequest(t, Version, 1, false, false, 0),
		addRequest(t, Version, 1, false, false, 2),
	} {
		var response bytes.Buffer
		if err := handler.Handle(context.Background(), request, &response); err != nil {
			t.Fatal(err)
		}
		frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
		if err != nil || frame.Code != ResultFailure || len(frame.Body) != 0 || bytes.Contains(response.Bytes(), []byte("private")) {
			t.Fatalf("frame=%+v response=%x err=%v", frame, response.Bytes(), err)
		}
	}
	if len(operations.added) != 1 {
		t.Fatalf("add calls=%d", len(operations.added))
	}

	var response bytes.Buffer
	if err := handler.Handle(context.Background(), listRequest(Version+1), &response); err != nil {
		t.Fatal(err)
	}
	frame, _ := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if frame.Code != ResultFailure {
		t.Fatalf("list frame=%+v", frame)
	}
}

func TestChangeDeleteAndRecordingStatus(t *testing.T) {
	operations := &fakeOperations{recording: true}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), changeRequest(t, 7, 4), &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(frame.Body) != 2 || len(operations.changed) != 1 ||
		operations.changed[0].Number != 7 || operations.changed[0].Request.Priority != 4 {
		t.Fatalf("frame=%+v changes=%+v err=%v", frame, operations.changed, err)
	}

	response.Reset()
	if err := handler.Handle(context.Background(), numberRequest(t, CommandDelete, 7, true), &response); err != nil {
		t.Fatal(err)
	}
	frame, _ = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if frame.Code != ResultSuccess || len(frame.Body) != 0 || len(operations.deleted) != 1 || operations.deleted[0] != 7 {
		t.Fatalf("delete frame=%+v deleted=%v", frame, operations.deleted)
	}

	response.Reset()
	if err := handler.Handle(context.Background(), numberRequest(t, CommandRecordingOpen, 7, false), &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess {
		t.Fatalf("open frame=%+v err=%v", frame, err)
	}
	reader, _ := codec.NewReader(frame.Body, codec.DefaultLimits())
	var control int32
	var path string
	if err := reader.Structure(func(item *codec.Reader) error {
		var readErr error
		control, readErr = item.I32()
		if readErr == nil {
			path, readErr = item.String()
		}
		return readErr
	}); err != nil || reader.Exact() != nil || control != 7 || path != recordingPath {
		t.Fatalf("control=%d path=%q err=%v", control, path, err)
	}

	response.Reset()
	if err := handler.Handle(context.Background(), numberRequest(t, CommandRecordingClose, control, false), &response); err != nil {
		t.Fatal(err)
	}
	frame, _ = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if frame.Code != ResultSuccess || len(frame.Body) != 0 {
		t.Fatalf("close frame=%+v", frame)
	}
}

func TestDeleteAcceptsFixedKonomiTVWireRequest(t *testing.T) {
	request := []byte{
		0xf6, 0x03, 0x00, 0x00, 0x0c, 0x00, 0x00, 0x00,
		0x0c, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x07, 0x00, 0x00, 0x00,
	}
	if built := numberRequest(t, CommandDelete, 7, true); !bytes.Equal(built, request) {
		t.Fatalf("request=%x", built)
	}
	operations := &fakeOperations{}
	var response bytes.Buffer
	if err := (Handler{Operations: operations, Limits: codec.DefaultLimits()}).Handle(
		context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(operations.deleted) != 1 || operations.deleted[0] != 7 {
		t.Fatalf("frame=%+v deleted=%v err=%v", frame, operations.deleted, err)
	}
}

func TestMutationAndStatusRejectMalformedOrInactiveInput(t *testing.T) {
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	excess := append(numberRequest(t, CommandDelete, 1, true), 0)
	binary.LittleEndian.PutUint32(excess[4:8], 13)
	truncated := numberRequest(t, CommandDelete, 1, true)[:19]
	binary.LittleEndian.PutUint32(truncated[4:8], 11)
	multiple := []byte{
		0xf6, 0x03, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00,
		0x10, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00,
	}
	requests := [][]byte{
		changeRequest(t, 0, 3),
		numberRequest(t, CommandDelete, 0, true),
		numberRequest(t, CommandDelete, -1, true),
		multiple,
		truncated,
		excess,
		numberRequest(t, CommandRecordingOpen, 1, false),
		numberRequest(t, CommandRecordingClose, 0, false),
	}
	for _, request := range requests {
		var response bytes.Buffer
		if err := handler.Handle(context.Background(), request, &response); err != nil {
			t.Fatal(err)
		}
		frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
		if err != nil || frame.Code != ResultFailure || len(frame.Body) != 0 {
			t.Fatalf("frame=%+v err=%v", frame, err)
		}
	}
}

func TestMeasureReservationCountBoundary(t *testing.T) {
	for _, test := range []struct {
		count   int
		wantErr bool
	}{
		{count: maxReservations},
		{count: maxReservations + 1, wantErr: true},
	} {
		operations := &generatedOperations{count: test.count}
		count, _, err := measureReservations(context.Background(), operations, codec.DefaultLimits())
		if (err != nil) != test.wantErr {
			t.Fatalf("input=%d measured=%d err=%v", test.count, count, err)
		}
		if operations.maximumLimit != pageSize {
			t.Fatalf("page limit=%d", operations.maximumLimit)
		}
	}
}

type generatedOperations struct {
	count        int
	maximumLimit int
}

func (*generatedOperations) Add(context.Context, recording.ReservationRequest) (recording.Reservation, error) {
	return recording.Reservation{}, nil
}

func (*generatedOperations) Change(context.Context, recording.ReservationChange) error { return nil }
func (*generatedOperations) Delete(context.Context, int32) error                       { return nil }
func (*generatedOperations) Recording(context.Context, int32) (bool, error)            { return false, nil }

func (operations *generatedOperations) Active(_ context.Context, limit int, after int32) ([]recording.Reservation, error) {
	if limit > operations.maximumLimit {
		operations.maximumLimit = limit
	}
	end := min(int(after)+limit, operations.count)
	result := make([]recording.Reservation, 0, end-int(after))
	for number := int(after) + 1; number <= end; number++ {
		result = append(result, listedReservation(int32(number)))
	}
	return result, nil
}

func listRequest(version uint16) []byte {
	request := make([]byte, codec.HeaderSize+2)
	binary.LittleEndian.PutUint32(request[0:4], uint32(CommandList))
	binary.LittleEndian.PutUint32(request[4:8], 2)
	binary.LittleEndian.PutUint16(request[8:10], version)
	return request
}

func addRequest(t *testing.T, version uint16, recordingMode uint8, follow, margins bool, count int) []byte {
	return reservationRequest(t, CommandAdd, version, 0, recordingMode, 3, follow, margins, count)
}

func changeRequest(t *testing.T, reserveID int32, priority uint8) []byte {
	return reservationRequest(t, CommandChange, Version, reserveID, 1, priority, false, false, 1)
}

func reservationRequest(t *testing.T, command int32, version uint16, reserveID int32, recordingMode, priority uint8, follow, margins bool, count int) []byte {
	t.Helper()
	var itemBody bytes.Buffer
	item, err := codec.NewWriter(&itemBody, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	if err := item.String("client title"); err != nil {
		t.Fatal(err)
	}
	if err := item.SystemTime(start); err != nil {
		t.Fatal(err)
	}
	if err := item.U32(1800); err != nil {
		t.Fatal(err)
	}
	if err := item.String("client station"); err != nil {
		t.Fatal(err)
	}
	for _, value := range [...]uint16{1, 2, 3, 4} {
		if err := item.U16(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := item.String("ignored comment"); err != nil {
		t.Fatal(err)
	}
	if err := item.I32(reserveID); err != nil {
		t.Fatal(err)
	}
	if err := item.U8(0); err != nil {
		t.Fatal(err)
	}
	if err := item.U8(0); err != nil {
		t.Fatal(err)
	}
	if err := item.String(""); err != nil {
		t.Fatal(err)
	}
	if err := item.SystemTime(start); err != nil {
		t.Fatal(err)
	}
	writeInputSettings(t, item, recordingMode, priority, follow, margins)
	if err := item.I32(0); err != nil {
		t.Fatal(err)
	}
	writeTestVector(t, item, nil, 0)
	if err := item.I32(0); err != nil {
		t.Fatal(err)
	}

	var structure bytes.Buffer
	structureWriter, _ := codec.NewWriter(&structure, codec.DefaultLimits())
	_ = structureWriter.I32(int32(4 + itemBody.Len()))
	_ = structureWriter.Bytes(itemBody.Bytes())
	var body bytes.Buffer
	bodyWriter, _ := codec.NewWriter(&body, codec.DefaultLimits())
	_ = bodyWriter.U16(version)
	writeTestVector(t, bodyWriter, structure.Bytes(), count)
	request := make([]byte, codec.HeaderSize+body.Len())
	binary.LittleEndian.PutUint32(request[0:4], uint32(command))
	binary.LittleEndian.PutUint32(request[4:8], uint32(body.Len()))
	copy(request[8:], body.Bytes())
	return request
}

func writeInputSettings(t *testing.T, writer *codec.Writer, mode, priority uint8, follow, margins bool) {
	t.Helper()
	if err := writer.I32(51); err != nil {
		t.Fatal(err)
	}
	for _, value := range []uint8{mode, priority, boolByte(follow)} {
		if err := writer.U8(value); err != nil {
			t.Fatal(err)
		}
	}
	_ = writer.U32(0)
	_ = writer.U8(0)
	_ = writer.String("")
	writeTestVector(t, writer, nil, 0)
	_ = writer.U8(0)
	_ = writer.U8(0)
	_ = writer.U8(boolByte(margins))
	_ = writer.I32(0)
	_ = writer.I32(0)
	_ = writer.U8(0)
	_ = writer.U8(0)
	_ = writer.U32(0)
	writeTestVector(t, writer, nil, 0)
}

func numberRequest(t *testing.T, command, number int32, vector bool) []byte {
	t.Helper()
	var body bytes.Buffer
	writer, err := codec.NewWriter(&body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if vector {
		if err := writer.I32(12); err != nil {
			t.Fatal(err)
		}
		if err := writer.I32(1); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.I32(number); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, codec.HeaderSize+body.Len())
	binary.LittleEndian.PutUint32(request[0:4], uint32(command))
	binary.LittleEndian.PutUint32(request[4:8], uint32(body.Len()))
	copy(request[8:], body.Bytes())
	return request
}

func writeTestVector(t *testing.T, writer *codec.Writer, item []byte, count int) {
	t.Helper()
	if err := writer.I32(int32(8 + len(item)*count)); err != nil {
		t.Fatal(err)
	}
	if err := writer.I32(int32(count)); err != nil {
		t.Fatal(err)
	}
	for range count {
		if err := writer.Bytes(item); err != nil {
			t.Fatal(err)
		}
	}
}

func readListedReservation(reader *codec.Reader, want recording.Reservation) error {
	return reader.Structure(func(item *codec.Reader) error {
		title, err := item.String()
		if err != nil || title != want.Program.Title {
			return fmt.Errorf("title=%q err=%v", title, err)
		}
		if _, err := item.SystemTime(); err != nil {
			return err
		}
		if _, err := item.U32(); err != nil {
			return err
		}
		if _, err := item.String(); err != nil {
			return err
		}
		for range 4 {
			if _, err := item.U16(); err != nil {
				return err
			}
		}
		if _, err := item.String(); err != nil {
			return err
		}
		number, err := item.I32()
		if err != nil || number != want.Number {
			return fmt.Errorf("number=%d err=%v", number, err)
		}
		for range 2 {
			if _, err := item.U8(); err != nil {
				return err
			}
		}
		if _, err := item.String(); err != nil {
			return err
		}
		if _, err := item.SystemTime(); err != nil {
			return err
		}
		priority, follow, err := decodeSettings(item)
		if err != nil || priority != want.Priority || follow != want.EffectiveFollow {
			return fmt.Errorf("priority=%d follow=%v err=%v", priority, follow, err)
		}
		if _, err := item.I32(); err != nil {
			return err
		}
		files := 0
		if err := item.Vector(6, 1, func(file *codec.Reader, _ int) error {
			files++
			_, err := file.String()
			return err
		}); err != nil || files != 0 {
			return fmt.Errorf("files=%d err=%v", files, err)
		}
		_, err = item.I32()
		return err
	})
}

func listedReservation(number int32) recording.Reservation {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	return recording.Reservation{
		Number: number, Version: 1, State: recording.ReservationActive, Priority: 3,
		RequestedFollow: true, EffectiveFollow: true,
		Program: recording.ProgramSnapshot{
			NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4,
			Title: "server title", StationName: "server station", Start: start, Duration: 30 * time.Minute,
		},
	}
}

func boolByte(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}
