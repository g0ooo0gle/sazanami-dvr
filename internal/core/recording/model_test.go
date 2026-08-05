package recording

import (
	"bytes"
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
	finalize := FinalizeRequest{AttemptID: claim.AttemptID, Token: idForTest(t, 9), ByteCount: 188, Now: now}
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
	finish.ByteCount = 187
	if err := finish.Validate(); err == nil {
		t.Fatal("188 bytes未満の成功が受理されました")
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
