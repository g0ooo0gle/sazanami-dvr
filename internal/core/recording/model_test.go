package recording

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestReservationValidateNew(t *testing.T) {
	valid := validReservation(t)
	if err := valid.ValidateNew(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Reservation)
	}{
		{name: "zero id", change: func(value *Reservation) { value.ID = catalogmodel.ID{} }},
		{name: "assigned number", change: func(value *Reservation) { value.Number = 1 }},
		{name: "priority", change: func(value *Reservation) { value.Priority = 0 }},
		{name: "effective follow", change: func(value *Reservation) { value.EffectiveFollow = true }},
		{name: "local time", change: func(value *Reservation) { value.Program.Start = value.Program.Start.In(time.FixedZone("JST", 9*60*60)) }},
		{name: "one second over", change: func(value *Reservation) { value.Program.Duration = 24*time.Hour + time.Second }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.change(&value)
			if err := value.ValidateNew(); err == nil {
				t.Fatal("不正な予約が受理されました")
			}
		})
	}
}

func TestReservationMarginsAndEffectiveDurationBoundaries(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	request := ReservationRequest{Start: start, Duration: 30 * time.Minute, Priority: 3}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	reservation := Reservation{Program: ProgramSnapshot{Start: start, Duration: request.Duration}}
	if !reservation.PlannedStart().Equal(start.Add(-5*time.Second)) ||
		!reservation.PlannedEnd().Equal(start.Add(30*time.Minute+2*time.Second)) {
		t.Fatalf("default start=%s end=%s", reservation.PlannedStart(), reservation.PlannedEnd())
	}
	zero := &RecordingMargins{}
	request.Margins = zero
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	reservation.Margins = zero
	if !reservation.PlannedStart().Equal(start) || !reservation.PlannedEnd().Equal(start.Add(30*time.Minute)) {
		t.Fatalf("zero start=%s end=%s", reservation.PlannedStart(), reservation.PlannedEnd())
	}
	for _, duration := range []time.Duration{time.Second, 24 * time.Hour} {
		request.Duration = duration
		if err := request.Validate(); err != nil {
			t.Fatalf("duration=%s: %v", duration, err)
		}
	}
	invalid := []ReservationRequest{
		{Start: start, Duration: time.Second, Priority: 3, Margins: &RecordingMargins{End: -time.Second}},
		{Start: start, Duration: 24 * time.Hour, Priority: 3},
		{Start: start, Duration: time.Hour, Priority: 3, Margins: &RecordingMargins{Start: MaxRecordingMargin + time.Second}},
		{Start: time.Unix(1, 0).UTC(), Duration: time.Hour, Priority: 3, Margins: &RecordingMargins{Start: 2 * time.Second}},
	}
	for index, candidate := range invalid {
		if err := candidate.Validate(); err == nil {
			t.Fatalf("invalid %d was accepted", index)
		}
	}
}

func TestPostRecordingSettingsValidation(t *testing.T) {
	for _, settings := range []PostRecordingSettings{
		{},
		{Mode: PostRecordingNothing},
		{Mode: PostRecordingDefault, Script: "/data/post-recording-scripts/after.sh"},
	} {
		if err := settings.Validate(); err != nil {
			t.Fatalf("settings=%+v err=%v", settings, err)
		}
	}
	for _, settings := range []PostRecordingSettings{
		{Mode: PostRecordingMode(2)},
		{Script: strings.Repeat("a", MaxPostRecordingScriptBytes+1)},
		{Script: "bad\x00script"},
		{Script: "bad\nscript"},
		{Script: string([]byte{0xff})},
	} {
		if err := settings.Validate(); err == nil {
			t.Fatalf("不正な録画後設定が受理されました: %+v", settings)
		}
	}
}

func TestFilePlanAndRecordingRequests(t *testing.T) {
	plan := FilePlan{PartialPath: "2026/08/attempt.ts.partial", FinalPath: "2026/08/attempt.ts"}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []FilePlan{
		{PartialPath: "/tmp/a", FinalPath: "2026/08/a.ts"},
		{PartialPath: "../a", FinalPath: "../a.ts"},
		{PartialPath: "a/x.partial", FinalPath: "b/x.ts"},
		{PartialPath: "a/x", FinalPath: "a/x"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("不正なpathが受理されました: %+v", invalid)
		}
	}
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	claim := ClaimRequest{
		ReservationID: idForTest(t, 5), AttemptID: idForTest(t, 6), SegmentID: idForTest(t, 7),
		OwnerID: idForTest(t, 8), OwnerGeneration: 1, Now: now, Plan: plan,
	}
	if err := claim.Validate(); err != nil {
		t.Fatal(err)
	}
	finalize := FinalizeRequest{
		AttemptID: claim.AttemptID, Token: idForTest(t, 9), ByteCount: 188,
		State: AttemptSucceeded, Reason: ReasonCompleted, Now: now,
	}
	if err := finalize.Validate(); err != nil {
		t.Fatal(err)
	}
	finish := FinishRequest{
		AttemptID: claim.AttemptID, State: AttemptSucceeded, Reason: ReasonCompleted,
		ByteCount: 188, Availability: AvailabilityFinal, Now: now,
	}
	if err := finish.Validate(); err != nil {
		t.Fatal(err)
	}
	finish.Reason = ReasonCompletedAfterReconnect
	if err := finish.Validate(); err != nil {
		t.Fatal(err)
	}
	finish.ByteCount = 187
	if err := finish.Validate(); err == nil {
		t.Fatal("188 bytes未満の成功が受理されました")
	}
	finish = FinishRequest{
		AttemptID: claim.AttemptID, State: AttemptPartial, Reason: ReasonUserRequestedStop,
		ByteCount: 188, Availability: AvailabilityFinal, Now: now,
	}
	if err := finish.Validate(); err != nil {
		t.Fatal(err)
	}
	finish.Reason = ReasonStreamEndedEarly
	if err := finish.Validate(); err == nil {
		t.Fatal("利用者停止以外の部分録画が完成ファイル扱いになりました")
	}
}

func TestHistoryItemRequiresEveryCompletedRecordingCondition(t *testing.T) {
	start := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	valid := HistoryItem{Number: 1, State: AttemptSucceeded, Reason: ReasonCompleted, Title: "番組", StationName: "放送局",
		NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4, PlannedStart: start, PlannedEnd: end,
		ActualStart: &start, ActualEnd: &end, ByteCount: 188,
		Plan:         FilePlan{PartialPath: "2026/08/x.ts.partial", FinalPath: "2026/08/x.ts"},
		SegmentState: SegmentFinalized, Availability: AvailabilityFinal, FileSynced: true, FinalPublished: true, DirectorySynced: true}
	if err := valid.Validate(); err != nil || !valid.Playable() {
		t.Fatalf("valid=%+v err=%v", valid, err)
	}
	changes := []func(*HistoryItem){
		func(item *HistoryItem) { item.State = AttemptPartial },
		func(item *HistoryItem) { item.Reason = ReasonStreamUnavailable },
		func(item *HistoryItem) { item.ActualStart, item.ActualEnd = nil, nil },
		func(item *HistoryItem) { same := start; item.ActualEnd = &same },
		func(item *HistoryItem) { item.ByteCount = 187 },
		func(item *HistoryItem) { item.SegmentState = SegmentPartial },
		func(item *HistoryItem) { item.Availability = AvailabilityMissing },
		func(item *HistoryItem) { item.FileSynced = false },
		func(item *HistoryItem) { item.FinalPublished = false },
		func(item *HistoryItem) { item.DirectorySynced = false },
	}
	for index, change := range changes {
		item := valid
		change(&item)
		if item.Playable() {
			t.Fatalf("condition %d without required value is playable: %+v", index, item)
		}
	}
	for _, state := range []AttemptState{AttemptSucceeded, AttemptPartial, AttemptFailed, AttemptCancelled, AttemptMissed} {
		item := valid
		item.State = state
		if err := item.Validate(); err != nil {
			t.Fatalf("terminal state %s: %v", state, err)
		}
	}
}

func validReservation(t *testing.T) Reservation {
	t.Helper()
	created := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	return Reservation{
		ID: idForTest(t, 1), Version: 1, State: ReservationActive, Priority: 3,
		RequestedFollow: true, CreatedAt: created, UpdatedAt: created,
		Program: ProgramSnapshot{
			ProgramInstanceID: idForTest(t, 2), ProgramRevisionID: idForTest(t, 3), BackendID: idForTest(t, 4),
			ProviderServiceLocator: "1", TuningTarget: "1", NetworkID: 1, TransportStreamID: 2,
			ServiceID: 3, EventID: 4, Title: "テスト番組", StationName: "テスト局",
			Start: created.Add(time.Hour), Duration: 30 * time.Minute,
		},
	}
}

func idForTest(t *testing.T, marker byte) catalogmodel.ID {
	t.Helper()
	id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{marker}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
