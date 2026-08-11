package reservation

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
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

type fakeScriptValidator struct{ allowed string }

func (validator fakeScriptValidator) Validate(path string) error {
	if path != validator.allowed {
		return errors.New("script not allowed")
	}
	return nil
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
	reservation.Disabled = true
	reservation.Margins = &recording.RecordingMargins{Start: -time.Hour, End: time.Hour}
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

func TestAddAcceptsBasicSettingsAndRejectsUnsupportedMode(t *testing.T) {
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	var response bytes.Buffer
	request := addRequest(t, Version, 1, true, false, 1)
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(frame.Body) != 4 || len(operations.added) != 1 ||
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
	if frame.Code != ResultSuccess || len(operations.added) != 2 || operations.added[1].Margins == nil ||
		*operations.added[1].Margins != (recording.RecordingMargins{}) {
		t.Fatalf("margin frame=%+v calls=%d", frame, len(operations.added))
	}
}

func TestAddRejectsForcedTunerWithoutPartialAcceptance(t *testing.T) {
	operations := &fakeOperations{}
	request := reservationRequestSettingsWithTuner(t, CommandAdd, Version, 0, 1, 3, true,
		0, 0, 0, 1, 0, recording.OutputSettings{}, recording.PostRecordingSettings{}, nil, 7)
	var response bytes.Buffer
	if err := (Handler{Operations: operations, Limits: codec.DefaultLimits()}).Handle(
		context.Background(), request, &response,
	); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultFailure || len(operations.added) != 0 {
		t.Fatalf("frame=%+v added=%+v err=%v", frame, operations.added, err)
	}
}

func TestAddAndChangeAcceptOneSegOutput(t *testing.T) {
	for _, command := range []int32{CommandAdd, CommandChange} {
		t.Run(fmt.Sprintf("command-%d", command), func(t *testing.T) {
			operations := &fakeOperations{}
			reserveID := int32(1)
			if command == CommandAdd {
				reserveID = 0
			}
			request := reservationRequestSettingsWithOneSeg(t, command, Version, reserveID, 1,
				[]oneSegWireFolder{{path: "mobile", writer: "Write_Default.dll", name: "RecName_Macro.dll?$Title$.ts"}})
			var response bytes.Buffer
			if err := (Handler{Operations: operations, Limits: codec.DefaultLimits()}).Handle(
				context.Background(), request, &response,
			); err != nil {
				t.Fatal(err)
			}
			frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
			if err != nil || frame.Code != ResultSuccess {
				t.Fatalf("frame=%+v err=%v", frame, err)
			}
			var got *recording.OutputSettings
			if command == CommandAdd && len(operations.added) == 1 {
				got = operations.added[0].OneSegOutput
			}
			if command == CommandChange && len(operations.changed) == 1 {
				got = operations.changed[0].Request.OneSegOutput
			}
			want := recording.OutputSettings{Folder: "mobile", Template: "$Title$.ts"}
			if got == nil || *got != want {
				t.Fatalf("one_seg=%+v", got)
			}
		})
	}
}

func TestDecodeOneSegSettingsProfile(t *testing.T) {
	tests := []struct {
		name     string
		flag     uint8
		folders  []oneSegWireFolder
		want     *recording.OutputSettings
		wantFail bool
	}{
		{name: "disabled", flag: 0},
		{name: "inherit", flag: 1, want: &recording.OutputSettings{}},
		{name: "empty row normalizes", flag: 1, folders: []oneSegWireFolder{{}}, want: &recording.OutputSettings{}},
		{name: "dedicated", flag: 1, folders: []oneSegWireFolder{{
			path: "mobile", writer: "Write_Default.dll", name: "RecName_Macro.dll?$Title$.ts",
		}}, want: &recording.OutputSettings{Folder: "mobile", Template: "$Title$.ts"}},
		{name: "mode two", flag: 2, wantFail: true},
		{name: "mode 255", flag: 255, wantFail: true},
		{name: "disabled with folder", folders: []oneSegWireFolder{{path: "mobile"}}, wantFail: true},
		{name: "two folders", flag: 1, folders: []oneSegWireFolder{{path: "a"}, {path: "b"}}, wantFail: true},
		{name: "wrong writer", flag: 1, folders: []oneSegWireFolder{{writer: "Other.dll"}}, wantFail: true},
		{name: "writer case", flag: 1, folders: []oneSegWireFolder{{writer: "write_default.dll"}}, wantFail: true},
		{name: "wrong name", flag: 1, folders: []oneSegWireFolder{{name: "Other.dll"}}, wantFail: true},
		{name: "empty template argument", flag: 1, folders: []oneSegWireFolder{{name: "RecName_Macro.dll?"}}, wantFail: true},
		{name: "reserved", flag: 1, folders: []oneSegWireFolder{{reserved: "x"}}, wantFail: true},
		{name: "absolute", flag: 1, folders: []oneSegWireFolder{{path: "/outside"}}, wantFail: true},
		{name: "parent", flag: 1, folders: []oneSegWireFolder{{path: "../outside"}}, wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer, _ := codec.NewWriter(&body, codec.DefaultLimits())
			writeInputSettingsWireFull(t, writer, 1, 3, true, 0, 0, 0, 0,
				recording.OutputSettings{}, "", 0, false, 0, test.flag, test.folders)
			reader, err := codec.NewReader(body.Bytes(), codec.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			settings, decodeErr := decodeSettings(reader)
			if test.wantFail {
				if decodeErr == nil {
					t.Fatalf("settings=%+v", settings)
				}
				return
			}
			if decodeErr != nil || reader.Exact() != nil || !sameOutputPointer(settings.oneSegOutput, test.want) {
				t.Fatalf("settings=%+v err=%v", settings, decodeErr)
			}
		})
	}
}

func TestAddAcceptsDisabledAndMarginBoundariesAtomically(t *testing.T) {
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	for _, test := range []struct {
		name       string
		mode       uint8
		useMargins uint8
		start, end int32
		wantOK     bool
	}{
		{name: "disabled default", mode: 5, wantOK: true},
		{name: "negative positive boundary", mode: 1, useMargins: 1, start: -3600, end: 3600, wantOK: true},
		{name: "positive negative boundary", mode: 1, useMargins: 1, start: 3600, end: -3600, wantOK: true},
		{name: "one over", mode: 1, useMargins: 1, start: 3601, wantOK: false},
		{name: "invalid flag", mode: 1, useMargins: 2, wantOK: false},
		{name: "default with value", mode: 1, start: 1, wantOK: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(operations.added)
			request := reservationRequestSettings(t, CommandAdd, Version, 0, test.mode, 3, true,
				test.useMargins, test.start, test.end, 1)
			var response bytes.Buffer
			if err := handler.Handle(context.Background(), request, &response); err != nil {
				t.Fatal(err)
			}
			frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if test.wantOK {
				if frame.Code != ResultSuccess || len(operations.added) != before+1 {
					t.Fatalf("frame=%+v calls=%d", frame, len(operations.added))
				}
				added := operations.added[len(operations.added)-1]
				if added.Disabled != (test.mode == 5) || added.RequestedFollow != true ||
					(test.useMargins == 0) != (added.Margins == nil) {
					t.Fatalf("added=%+v", added)
				}
			} else if frame.Code != ResultFailure || len(operations.added) != before {
				t.Fatalf("partial acceptance frame=%+v calls=%d", frame, len(operations.added))
			}
		})
	}
}

func TestReservationOutputSettingsRoundTrip(t *testing.T) {
	output := recording.OutputSettings{Folder: "ドラマ/新作", Template: "$SDYYYY$-$Title$-$ReserveID$"}
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	request := reservationRequestSettingsWithOutput(t, CommandAdd, Version, 0, 1, 3, true, 0, 0, 0, 1, output)
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(operations.added) != 1 || operations.added[0].Output != output {
		t.Fatalf("frame=%+v added=%+v err=%v", frame, operations.added, err)
	}

	listed := listedReservation(42)
	listed.Output = output
	operations.reservations = []recording.Reservation{listed}
	response.Reset()
	if err := handler.Handle(context.Background(), listRequest(Version), &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
	reader, _ := codec.NewReader(frame.Body, codec.DefaultLimits())
	if version, err := reader.U16(); err != nil || version != Version {
		t.Fatalf("version=%d err=%v", version, err)
	}
	count := 0
	if err := reader.Vector(4, 1, func(items *codec.Reader, _ int) error {
		count++
		return readListedReservation(items, listed)
	}); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}

	response.Reset()
	request = reservationRequestSettingsWithFileNames(t, CommandChange, Version, 42, 1, 4, true,
		0, 0, 0, 1, 0, output, recording.PostRecordingSettings{}, []string{"/untrusted/echo.ts"})
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(operations.changed) != 1 ||
		operations.changed[0].Number != 42 || operations.changed[0].Request.Output != output {
		t.Fatalf("frame=%+v changed=%+v err=%v", frame, operations.changed, err)
	}

	for _, test := range []struct {
		name      string
		command   int32
		reserveID int32
		fileNames []string
		reason    string
	}{
		{name: "add rejects echoed name", command: CommandAdd, fileNames: []string{"echo.ts"}, reason: "reservation-vector"},
		{name: "change rejects two names", command: CommandChange, reserveID: 42, fileNames: []string{"one.ts", "two.ts"}, reason: "reservation-vector"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response.Reset()
			request := reservationRequestSettingsWithFileNames(t, test.command, Version, test.reserveID, 1, 3, true,
				0, 0, 0, 1, 0, output, recording.PostRecordingSettings{}, test.fileNames)
			_, decodeErr := decodeReservationRequest(request[codec.HeaderSize:], codec.DefaultLimits(), test.command == CommandChange)
			var codecErr *codec.Error
			if !errors.As(decodeErr, &codecErr) || codecErr.Reason != test.reason {
				t.Fatalf("decode error=%v want reason=%s", decodeErr, test.reason)
			}
			if err := handler.Handle(context.Background(), request, &response); err != nil {
				t.Fatal(err)
			}
			frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
			if err != nil || frame.Code != ResultFailure || len(operations.added) != 1 || len(operations.changed) != 1 {
				t.Fatalf("frame=%+v added=%d changed=%d err=%v", frame, len(operations.added), len(operations.changed), err)
			}
		})
	}
}

func TestReservationPostRecordingSettingsRoundTrip(t *testing.T) {
	post := recording.PostRecordingSettings{Mode: recording.PostRecordingNothing, Script: "/allowed/finish.sh"}
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, ScriptValidator: fakeScriptValidator{allowed: post.Script}, Limits: codec.DefaultLimits()}
	request := reservationRequestSettingsWithPostRecording(t, CommandAdd, Version, 0, 1, 3, true,
		0, 0, 0, 1, 0, recording.OutputSettings{}, post)
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(operations.added) != 1 || operations.added[0].PostRecording != post {
		t.Fatalf("frame=%+v added=%+v err=%v", frame, operations.added, err)
	}
	response.Reset()
	request = reservationRequestSettingsWithPostRecording(t, CommandChange, Version, 42, 1, 3, true,
		0, 0, 0, 1, 0, recording.OutputSettings{}, post)
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(operations.changed) != 1 || operations.changed[0].Request.PostRecording != post {
		t.Fatalf("frame=%+v changed=%+v err=%v", frame, operations.changed, err)
	}

	listed := listedReservation(42)
	listed.PostRecording = post
	operations.reservations = []recording.Reservation{listed}
	response.Reset()
	if err := handler.Handle(context.Background(), listRequest(Version), &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	reader, readerErr := codec.NewReader(frame.Body, codec.DefaultLimits())
	if err != nil || readerErr != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v parse=%v reader=%v", frame, err, readerErr)
	}
	if _, err := reader.U16(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Vector(4, 1, func(item *codec.Reader, _ int) error { return readListedReservation(item, listed) }); err != nil {
		t.Fatal(err)
	}

	rejected := post
	rejected.Script = "/outside/finish.sh"
	response.Reset()
	request = reservationRequestSettingsWithPostRecording(t, CommandAdd, Version, 0, 1, 3, true,
		0, 0, 0, 1, 0, recording.OutputSettings{}, rejected)
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, _ = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if frame.Code != ResultFailure || len(operations.added) != 1 {
		t.Fatalf("frame=%+v calls=%d", frame, len(operations.added))
	}
}

func TestPostRecordingWireModeMatrixAndPathBoundaries(t *testing.T) {
	type wireMode struct {
		suspend uint8
		reboot  bool
	}
	accepted := map[wireMode]recording.PostRecordingMode{
		{0, false}: recording.PostRecordingDefault,
		{4, false}: recording.PostRecordingNothing,
		{1, false}: recording.PostRecordingStandby,
		{1, true}:  recording.PostRecordingStandbyReboot,
		{2, false}: recording.PostRecordingSuspend,
		{2, true}:  recording.PostRecordingSuspendReboot,
		{3, false}: recording.PostRecordingShutdown,
	}
	for rawSuspend := 0; rawSuspend <= 255; rawSuspend++ {
		suspend := uint8(rawSuspend)
		for _, reboot := range []bool{false, true} {
			var body bytes.Buffer
			writer, _ := codec.NewWriter(&body, codec.DefaultLimits())
			writeInputSettingsWire(t, writer, 1, 3, true, 0, 0, 0, 0, recording.OutputSettings{}, "", suspend, reboot)
			reader, _ := codec.NewReader(body.Bytes(), codec.DefaultLimits())
			settings, err := decodeSettings(reader)
			wantMode, wantOK := accepted[wireMode{suspend, reboot}]
			if wantOK && (err != nil || reader.Exact() != nil || settings.postRecording.Mode != wantMode) {
				t.Fatalf("suspend=%d reboot=%v settings=%+v err=%v", suspend, reboot, settings, err)
			}
			if !wantOK && err == nil {
				t.Fatalf("suspend=%d reboot=%v を受理しました", suspend, reboot)
			}
		}
	}
	for _, test := range []struct {
		name, path string
		wantOK     bool
	}{
		{name: "1024 bytes", path: strings.Repeat("a", recording.MaxPostRecordingScriptBytes), wantOK: true},
		{name: "1025 bytes", path: strings.Repeat("a", recording.MaxPostRecordingScriptBytes+1)},
		{name: "control", path: "bad\npath"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer, _ := codec.NewWriter(&body, codec.DefaultLimits())
			writeInputSettingsWire(t, writer, 1, 3, true, 0, 0, 0, 0, recording.OutputSettings{}, test.path, 0, false)
			reader, _ := codec.NewReader(body.Bytes(), codec.DefaultLimits())
			_, err := decodeSettings(reader)
			if (err == nil) != test.wantOK {
				t.Fatalf("len=%d err=%v", len(test.path), err)
			}
		})
	}
}

func TestPostRecordingListReturnsAllSevenModes(t *testing.T) {
	operations := &fakeOperations{}
	for mode := recording.PostRecordingDefault; mode <= recording.PostRecordingShutdown; mode++ {
		item := listedReservation(int32(mode + 1))
		item.PostRecording.Mode = mode
		operations.reservations = append(operations.reservations, item)
	}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), listRequest(Version), &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	reader, readerErr := codec.NewReader(frame.Body, codec.DefaultLimits())
	if err != nil || readerErr != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v parse=%v reader=%v", frame, err, readerErr)
	}
	if _, err := reader.U16(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Vector(4, len(operations.reservations), func(item *codec.Reader, index int) error {
		return readListedReservation(item, operations.reservations[index])
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Exact(); err != nil {
		t.Fatal(err)
	}
}

func TestReservationComponentSettingsNormalizeAndRoundTrip(t *testing.T) {
	tests := []struct {
		wire uint32
		mode recording.ComponentMode
	}{
		{0x00, recording.ComponentDefault},
		{0x10, recording.ComponentDefault},
		{0x20, recording.ComponentDefault},
		{0x30, recording.ComponentDefault},
		{0x01, recording.ComponentNeither},
		{0x11, recording.ComponentCaptionsOnly},
		{0x21, recording.ComponentDataOnly},
		{0x31, recording.ComponentBoth},
	}
	for _, test := range tests {
		operations := &fakeOperations{}
		handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
		request := reservationRequestSettingsWithServiceMode(t, CommandAdd, Version, 0, 1, 3, true,
			0, 0, 0, 1, test.wire, recording.OutputSettings{})
		var response bytes.Buffer
		if err := handler.Handle(context.Background(), request, &response); err != nil {
			t.Fatal(err)
		}
		frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
		if err != nil || frame.Code != ResultSuccess || len(operations.added) != 1 ||
			operations.added[0].Components != test.mode {
			t.Fatalf("wire=%#x frame=%+v added=%+v err=%v", test.wire, frame, operations.added, err)
		}
	}

	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	request := reservationRequestSettingsWithServiceMode(t, CommandAdd, Version, 0, 1, 3, true,
		0, 0, 0, 1, 0x02, recording.OutputSettings{})
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultFailure || len(operations.added) != 0 {
		t.Fatalf("unknown frame=%+v calls=%d err=%v", frame, len(operations.added), err)
	}

	change := reservationRequestSettingsWithServiceMode(t, CommandChange, Version, 7, 1, 3, true,
		0, 0, 0, 1, 0x31, recording.OutputSettings{})
	response.Reset()
	if err := handler.Handle(context.Background(), change, &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(operations.changed) != 1 ||
		operations.changed[0].Request.Components != recording.ComponentBoth {
		t.Fatalf("change frame=%+v changed=%+v err=%v", frame, operations.changed, err)
	}

	for _, mode := range []recording.ComponentMode{recording.ComponentDefault, recording.ComponentNeither,
		recording.ComponentCaptionsOnly, recording.ComponentDataOnly, recording.ComponentBoth} {
		listed := listedReservation(42)
		listed.Components = mode
		operations.reservations = []recording.Reservation{listed}
		response.Reset()
		if err := handler.Handle(context.Background(), listRequest(Version), &response); err != nil {
			t.Fatal(err)
		}
		frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
		if err != nil || frame.Code != ResultSuccess {
			t.Fatalf("mode=%d frame=%+v err=%v", mode, frame, err)
		}
		reader, _ := codec.NewReader(frame.Body, codec.DefaultLimits())
		_, _ = reader.U16()
		if err := reader.Vector(4, 1, func(items *codec.Reader, _ int) error {
			return readListedReservation(items, listed)
		}); err != nil {
			t.Fatalf("mode=%d err=%v", mode, err)
		}
	}
}

func TestReservationOutputRejectsUnsafePathMacroAndPluginsAtomically(t *testing.T) {
	valid := recording.OutputSettings{Folder: "safe", Template: "$Title$"}
	base := reservationRequestSettingsWithOutput(t, CommandAdd, Version, 0, 1, 3, true, 0, 0, 0, 1, valid)
	tests := map[string][]byte{
		"absolute-folder": reservationRequestSettingsWithOutput(t, CommandAdd, Version, 0, 1, 3, true, 0, 0, 0, 1,
			recording.OutputSettings{Folder: "/unsafe", Template: "$Title$"}),
		"parent-folder": reservationRequestSettingsWithOutput(t, CommandAdd, Version, 0, 1, 3, true, 0, 0, 0, 1,
			recording.OutputSettings{Folder: "../unsafe", Template: "$Title$"}),
		"unknown-macro": reservationRequestSettingsWithOutput(t, CommandAdd, Version, 0, 1, 3, true, 0, 0, 0, 1,
			recording.OutputSettings{Folder: "safe", Template: "$Unknown$"}),
		"write-plugin": replaceWireString(t, base, "Write_Default.dll", "Write_Invalid.dll"),
		"name-plugin":  replaceWireString(t, base, "RecName_Macro.dll?$Title$", "RecName_Other.dll?$Title$"),
	}
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			var response bytes.Buffer
			if err := handler.Handle(context.Background(), request, &response); err != nil {
				t.Fatal(err)
			}
			frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
			if err != nil || frame.Code != ResultFailure || len(operations.added) != 0 {
				t.Fatalf("frame=%+v calls=%d err=%v", frame, len(operations.added), err)
			}
		})
	}
}

func TestChangeReplacesAllBasicSettingsTogether(t *testing.T) {
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	request := reservationRequestSettings(t, CommandChange, Version, 7, 5, 5, true, 1, -10, 20, 1)
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess || len(operations.changed) != 1 {
		t.Fatalf("frame=%+v changed=%+v err=%v", frame, operations.changed, err)
	}
	change := operations.changed[0]
	if change.Number != 7 || !change.Request.Disabled || change.Request.Priority != 5 ||
		!change.Request.RequestedFollow || change.Request.Margins == nil ||
		*change.Request.Margins != (recording.RecordingMargins{Start: -10 * time.Second, End: 20 * time.Second}) {
		t.Fatalf("change=%+v", change)
	}
	response.Reset()
	invalid := reservationRequestSettings(t, CommandChange, Version, 7, 1, 1, false, 1, -3601, 0, 1)
	if err := handler.Handle(context.Background(), invalid, &response); err != nil {
		t.Fatal(err)
	}
	frame, err = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultFailure || len(operations.changed) != 1 {
		t.Fatalf("partial change frame=%+v changed=%d err=%v", frame, len(operations.changed), err)
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
	if err != nil || frame.Code != ResultSuccess || len(frame.Body) != 4 || len(operations.changed) != 1 ||
		operations.changed[0].Number != 7 || operations.changed[0].Request.Priority != 4 {
		t.Fatalf("frame=%+v changes=%+v err=%v", frame, operations.changed, err)
	}

	response.Reset()
	if err := handler.Handle(context.Background(), numberRequest(t, CommandDelete, 7, true), &response); err != nil {
		t.Fatal(err)
	}
	frame, _ = codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if frame.Code != ResultSuccess || len(frame.Body) != 4 || len(operations.deleted) != 1 || operations.deleted[0] != 7 {
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

func TestReservationMutationsReturnSharedFixedSuccess(t *testing.T) {
	want := []byte{
		0x01, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00,
	}
	for _, test := range mutationTests(t) {
		t.Run(test.name, func(t *testing.T) {
			operations := &fakeOperations{}
			var response bytes.Buffer
			err := (Handler{Operations: operations, Limits: codec.DefaultLimits()}).Handle(
				context.Background(), test.request, &response)
			if err != nil || !bytes.Equal(response.Bytes(), want) || mutationCalls(operations) != 1 {
				t.Fatalf("response=%x calls=%d err=%v", response.Bytes(), mutationCalls(operations), err)
			}

			// Komorebiは本文を32-bit整数として読み、KonomiTVは外側の成功コードを読む。
			frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
			if err != nil || frame.Code != ResultSuccess || len(frame.Body) != 4 ||
				int32(binary.LittleEndian.Uint32(frame.Body)) != ResultSuccess {
				t.Fatalf("frame=%+v err=%v", frame, err)
			}
		})
	}
}

func TestKonomiTVMutationContractUsesOuterResult(t *testing.T) {
	for _, test := range mutationTests(t) {
		t.Run(test.name, func(t *testing.T) {
			var response bytes.Buffer
			err := (Handler{Operations: &fakeOperations{}, Limits: codec.DefaultLimits()}).Handle(
				context.Background(), test.request, &response)
			if err != nil || response.Len() < 4 || int32(binary.LittleEndian.Uint32(response.Bytes()[:4])) != ResultSuccess {
				t.Fatalf("response size=%d err=%v", response.Len(), err)
			}
		})
	}
}

func TestReservationMutationStorageFailureKeepsEmptyFailureResponse(t *testing.T) {
	for _, test := range mutationTests(t) {
		t.Run(test.name, func(t *testing.T) {
			storageErr := errors.New("private storage detail")
			operations := &fakeOperations{addErr: storageErr, changeErr: storageErr, deleteErr: storageErr}
			var response bytes.Buffer
			err := (Handler{Operations: operations, Limits: codec.DefaultLimits()}).Handle(
				context.Background(), test.request, &response)
			frame, parseErr := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
			if err != nil || parseErr != nil || frame.Code != ResultFailure || len(frame.Body) != 0 ||
				mutationCalls(operations) != 1 || bytes.Contains(response.Bytes(), []byte("private")) {
				t.Fatalf("frame=%+v calls=%d err=%v parse=%v", frame, mutationCalls(operations), err, parseErr)
			}
		})
	}
}

func TestReservationMutationWriteFailureDoesNotRepeatOperation(t *testing.T) {
	for _, test := range mutationTests(t) {
		for _, limit := range []int{1, 2, 3, 5, 6, 7, 9, 10, 11} {
			t.Run(fmt.Sprintf("%s/%d", test.name, limit), func(t *testing.T) {
				operations := &fakeOperations{}
				err := (Handler{Operations: operations, Limits: codec.DefaultLimits()}).Handle(
					context.Background(), test.request, &shortWriter{remaining: limit})
				var codecErr *codec.Error
				if !errors.As(err, &codecErr) || codecErr.Category != codec.PeerDisconnect || mutationCalls(operations) != 1 {
					t.Fatalf("calls=%d err=%v", mutationCalls(operations), err)
				}
			})
		}
	}
}

func TestReservationMutationCanceledResponseDoesNotRepeatOperation(t *testing.T) {
	for _, test := range mutationTests(t) {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			operations := &fakeOperations{}
			err := (Handler{Operations: operations, Limits: codec.DefaultLimits()}).Handle(
				ctx, test.request, &bytes.Buffer{})
			var codecErr *codec.Error
			if !errors.As(err, &codecErr) || codecErr.Category != codec.Timeout || mutationCalls(operations) != 1 {
				t.Fatalf("calls=%d err=%v", mutationCalls(operations), err)
			}
		})
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

type mutationTest struct {
	name    string
	request []byte
}

func mutationTests(t *testing.T) []mutationTest {
	t.Helper()
	return []mutationTest{
		{name: "追加", request: addRequest(t, Version, 1, false, false, 1)},
		{name: "変更", request: changeRequest(t, 7, 3)},
		{name: "取消し", request: numberRequest(t, CommandDelete, 7, true)},
	}
}

func mutationCalls(operations *fakeOperations) int {
	return len(operations.added) + len(operations.changed) + len(operations.deleted)
}

type shortWriter struct{ remaining int }

func (writer *shortWriter) Write(data []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	if len(data) > writer.remaining {
		written := writer.remaining
		writer.remaining = 0
		return written, io.ErrClosedPipe
	}
	writer.remaining -= len(data)
	return len(data), nil
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
	return reservationRequestSettings(t, command, version, reserveID, recordingMode, priority, follow,
		boolByte(margins), 0, 0, count)
}

func reservationRequestSettings(t *testing.T, command int32, version uint16, reserveID int32,
	recordingMode, priority uint8, follow bool, useMargins uint8, startMargin, endMargin int32, count int,
) []byte {
	return reservationRequestSettingsWithOutput(t, command, version, reserveID, recordingMode, priority, follow,
		useMargins, startMargin, endMargin, count, recording.OutputSettings{})
}

func reservationRequestSettingsWithOutput(t *testing.T, command int32, version uint16, reserveID int32,
	recordingMode, priority uint8, follow bool, useMargins uint8, startMargin, endMargin int32, count int,
	output recording.OutputSettings,
) []byte {
	return reservationRequestSettingsWithServiceMode(t, command, version, reserveID, recordingMode, priority, follow,
		useMargins, startMargin, endMargin, count, 0, output)
}

func reservationRequestSettingsWithServiceMode(t *testing.T, command int32, version uint16, reserveID int32,
	recordingMode, priority uint8, follow bool, useMargins uint8, startMargin, endMargin int32, count int,
	serviceMode uint32, output recording.OutputSettings,
) []byte {
	return reservationRequestSettingsWithPostRecording(t, command, version, reserveID, recordingMode, priority, follow,
		useMargins, startMargin, endMargin, count, serviceMode, output, recording.PostRecordingSettings{})
}

func reservationRequestSettingsWithPostRecording(t *testing.T, command int32, version uint16, reserveID int32,
	recordingMode, priority uint8, follow bool, useMargins uint8, startMargin, endMargin int32, count int,
	serviceMode uint32, output recording.OutputSettings, post recording.PostRecordingSettings,
) []byte {
	return reservationRequestSettingsWithFileNames(t, command, version, reserveID, recordingMode, priority, follow,
		useMargins, startMargin, endMargin, count, serviceMode, output, post, nil)
}

func reservationRequestSettingsWithFileNames(t *testing.T, command int32, version uint16, reserveID int32,
	recordingMode, priority uint8, follow bool, useMargins uint8, startMargin, endMargin int32, count int,
	serviceMode uint32, output recording.OutputSettings, post recording.PostRecordingSettings, fileNames []string,
) []byte {
	return reservationRequestSettingsWithTuner(t, command, version, reserveID, recordingMode, priority, follow,
		useMargins, startMargin, endMargin, count, serviceMode, output, post, fileNames, 0)
}

func reservationRequestSettingsWithTuner(t *testing.T, command int32, version uint16, reserveID int32,
	recordingMode, priority uint8, follow bool, useMargins uint8, startMargin, endMargin int32, count int,
	serviceMode uint32, output recording.OutputSettings, post recording.PostRecordingSettings, fileNames []string,
	tunerID uint32,
) []byte {
	return reservationRequestSettingsFull(t, command, version, reserveID, recordingMode, priority, follow,
		useMargins, startMargin, endMargin, count, serviceMode, output, post, fileNames, tunerID, 0, nil)
}

func reservationRequestSettingsWithOneSeg(t *testing.T, command int32, version uint16, reserveID int32,
	partial uint8, folders []oneSegWireFolder,
) []byte {
	return reservationRequestSettingsFull(t, command, version, reserveID, 1, 3, true,
		0, 0, 0, 1, 0, recording.OutputSettings{}, recording.PostRecordingSettings{}, nil, 0, partial, folders)
}

func reservationRequestSettingsFull(t *testing.T, command int32, version uint16, reserveID int32,
	recordingMode, priority uint8, follow bool, useMargins uint8, startMargin, endMargin int32, count int,
	serviceMode uint32, output recording.OutputSettings, post recording.PostRecordingSettings, fileNames []string,
	tunerID uint32, partial uint8, partialFolders []oneSegWireFolder,
) []byte {
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
	suspend, reboot, ok := encodePostRecordingMode(post.Mode)
	if !ok {
		t.Fatalf("invalid post recording mode: %d", post.Mode)
	}
	writeInputSettingsWireFull(t, item, recordingMode, priority, follow, useMargins, startMargin, endMargin,
		serviceMode, output, post.Script, suspend, reboot, tunerID, partial, partialFolders)
	if err := item.I32(0); err != nil {
		t.Fatal(err)
	}
	writeTestStringVector(t, item, fileNames)
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

func writeInputSettings(t *testing.T, writer *codec.Writer, mode, priority uint8, follow bool,
	useMargins uint8, startMargin, endMargin int32,
) {
	writeInputSettingsWithOutput(t, writer, mode, priority, follow, useMargins, startMargin, endMargin,
		recording.OutputSettings{})
}

func writeInputSettingsWithOutput(t *testing.T, writer *codec.Writer, mode, priority uint8, follow bool,
	useMargins uint8, startMargin, endMargin int32, output recording.OutputSettings,
) {
	writeInputSettingsWithServiceMode(t, writer, mode, priority, follow, useMargins, startMargin, endMargin, 0, output)
}

func writeInputSettingsWithServiceMode(t *testing.T, writer *codec.Writer, mode, priority uint8, follow bool,
	useMargins uint8, startMargin, endMargin int32, serviceMode uint32, output recording.OutputSettings,
) {
	writeInputSettingsWithPostRecording(t, writer, mode, priority, follow, useMargins, startMargin, endMargin,
		serviceMode, output, recording.PostRecordingSettings{})
}

func writeInputSettingsWithPostRecording(t *testing.T, writer *codec.Writer, mode, priority uint8, follow bool,
	useMargins uint8, startMargin, endMargin int32, serviceMode uint32, output recording.OutputSettings,
	post recording.PostRecordingSettings,
) {
	writeInputSettingsWithPostRecordingAndTuner(t, writer, mode, priority, follow, useMargins, startMargin, endMargin,
		serviceMode, output, post, 0)
}

func writeInputSettingsWithPostRecordingAndTuner(t *testing.T, writer *codec.Writer, mode, priority uint8,
	follow bool, useMargins uint8, startMargin, endMargin int32, serviceMode uint32, output recording.OutputSettings,
	post recording.PostRecordingSettings, tunerID uint32,
) {
	suspend, reboot, ok := encodePostRecordingMode(post.Mode)
	if !ok {
		t.Fatalf("invalid post recording mode: %d", post.Mode)
	}
	writeInputSettingsWireWithTuner(t, writer, mode, priority, follow, useMargins, startMargin, endMargin, serviceMode,
		output, post.Script, suspend, reboot, tunerID)
}

func writeInputSettingsWire(t *testing.T, writer *codec.Writer, mode, priority uint8, follow bool,
	useMargins uint8, startMargin, endMargin int32, serviceMode uint32, output recording.OutputSettings,
	batch string, suspend uint8, reboot bool,
) {
	writeInputSettingsWireWithTuner(t, writer, mode, priority, follow, useMargins, startMargin, endMargin, serviceMode,
		output, batch, suspend, reboot, 0)
}

func writeInputSettingsWireWithTuner(t *testing.T, writer *codec.Writer, mode, priority uint8, follow bool,
	useMargins uint8, startMargin, endMargin int32, serviceMode uint32, output recording.OutputSettings,
	batch string, suspend uint8, reboot bool, tunerID uint32,
) {
	writeInputSettingsWireFull(t, writer, mode, priority, follow, useMargins, startMargin, endMargin, serviceMode,
		output, batch, suspend, reboot, tunerID, 0, nil)
}

type oneSegWireFolder struct {
	path, writer, name, reserved string
}

func writeInputSettingsWireFull(t *testing.T, writer *codec.Writer, mode, priority uint8, follow bool,
	useMargins uint8, startMargin, endMargin int32, serviceMode uint32, output recording.OutputSettings,
	batch string, suspend uint8, reboot bool, tunerID uint32, partial uint8, partialFolders []oneSegWireFolder,
) {
	t.Helper()
	var folder bytes.Buffer
	if output != (recording.OutputSettings{}) {
		fields := &bytes.Buffer{}
		fieldWriter, err := codec.NewWriter(fields, codec.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		name := "RecName_Macro.dll"
		if output.Template != "" {
			name += "?" + output.Template
		}
		for _, value := range []string{output.Folder, "Write_Default.dll", name, ""} {
			if err := fieldWriter.String(value); err != nil {
				t.Fatal(err)
			}
		}
		folderWriter, _ := codec.NewWriter(&folder, codec.DefaultLimits())
		_ = folderWriter.I32(int32(4 + fields.Len()))
		_ = folderWriter.Bytes(fields.Bytes())
	}
	var body bytes.Buffer
	settingsWriter, err := codec.NewWriter(&body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []uint8{mode, priority, boolByte(follow)} {
		if err := settingsWriter.U8(value); err != nil {
			t.Fatal(err)
		}
	}
	_ = settingsWriter.U32(serviceMode)
	_ = settingsWriter.U8(0)
	_ = settingsWriter.String(batch)
	if output == (recording.OutputSettings{}) {
		writeTestVector(t, settingsWriter, nil, 0)
	} else {
		writeTestVector(t, settingsWriter, folder.Bytes(), 1)
	}
	_ = settingsWriter.U8(suspend)
	_ = settingsWriter.U8(boolByte(reboot))
	_ = settingsWriter.U8(useMargins)
	_ = settingsWriter.I32(startMargin)
	_ = settingsWriter.I32(endMargin)
	_ = settingsWriter.U8(0)
	_ = settingsWriter.U8(partial)
	_ = settingsWriter.U32(tunerID)
	var partialBody bytes.Buffer
	for _, value := range partialFolders {
		var fields bytes.Buffer
		fieldWriter, _ := codec.NewWriter(&fields, codec.DefaultLimits())
		for _, field := range []string{value.path, value.writer, value.name, value.reserved} {
			if err := fieldWriter.String(field); err != nil {
				t.Fatal(err)
			}
		}
		folderWriter, _ := codec.NewWriter(&partialBody, codec.DefaultLimits())
		_ = folderWriter.I32(int32(4 + fields.Len()))
		_ = folderWriter.Bytes(fields.Bytes())
	}
	writeTestVector(t, settingsWriter, partialBody.Bytes(), len(partialFolders))
	if err := writer.I32(int32(4 + body.Len())); err != nil {
		t.Fatal(err)
	}
	_ = writer.Bytes(body.Bytes())
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

func writeTestStringVector(t *testing.T, writer *codec.Writer, values []string) {
	t.Helper()
	var body bytes.Buffer
	bodyWriter, err := codec.NewWriter(&body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if err := bodyWriter.String(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.I32(int32(8 + body.Len())); err != nil {
		t.Fatal(err)
	}
	if err := writer.I32(int32(len(values))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Bytes(body.Bytes()); err != nil {
		t.Fatal(err)
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
		settings, err := decodeSettings(item)
		if err != nil || settings.priority != want.Priority || settings.follow != want.EffectiveFollow ||
			settings.disabled != want.Disabled || !sameMargins(settings.margins, want.Margins) || settings.output != want.Output ||
			settings.postRecording != want.PostRecording || !sameOneSegSettings(settings.oneSegOutput, want.OneSegOutput) {
			return fmt.Errorf("settings=%+v err=%v", settings, err)
		}
		if settings.components != want.Components {
			return fmt.Errorf("components=%d want=%d", settings.components, want.Components)
		}
		if _, err := item.I32(); err != nil {
			return err
		}
		expectedFile, expected, expectedErr := recording.ScheduledOutputPath(want)
		if expectedErr != nil {
			return expectedErr
		}
		files := 0
		if err := item.Vector(6, 1, func(file *codec.Reader, _ int) error {
			files++
			value, err := file.String()
			if err == nil && value != expectedFile {
				return fmt.Errorf("file=%q want=%q", value, expectedFile)
			}
			return err
		}); err != nil || files != boolCount(expected) {
			return fmt.Errorf("files=%d err=%v", files, err)
		}
		_, err = item.I32()
		return err
	})
}

func sameMargins(left, right *recording.RecordingMargins) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameOutputPointer(left, right *recording.OutputSettings) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameOneSegSettings(left *recording.OutputSettings, right *recording.OneSegOutput) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == right.Output
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

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func replaceWireString(t *testing.T, source []byte, before, after string) []byte {
	t.Helper()
	if len(before) != len(after) {
		t.Fatal("置換するwire文字列の長さが異なります")
	}
	wire := func(value string) []byte {
		var buffer bytes.Buffer
		writer, err := codec.NewWriter(&buffer, codec.DefaultLimits())
		if err != nil || writer.String(value) != nil {
			t.Fatalf("wire string: %v", err)
		}
		return buffer.Bytes()
	}
	result := bytes.Replace(append([]byte(nil), source...), wire(before), wire(after), 1)
	if bytes.Equal(result, source) {
		t.Fatal("wire文字列を置換できませんでした")
	}
	return result
}
