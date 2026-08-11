package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestAutomaticRuleRoundTripAndDelete(t *testing.T) {
	root, store := openMigratedStore(t)
	startMargin, endMargin := int32(-5), int32(10)
	rule := autoreservation.Rule{
		ID: testID(t, 151), Version: 1, CreatedAtUTCMS: 10, UpdatedAtUTCMS: 10,
		Search: autoreservation.SearchCondition{
			Enabled: true, CaseSensitive: true, Regex: true, TitleOnly: true,
			Keyword: "テスト.*", Exclude: "除外", FreeAccess: 1,
			Contents: []autoreservation.ContentRange{{Content: 0x01ff, User: 2}},
			Dates:    []autoreservation.DateRange{{StartDay: 1, StartHour: 2, StartMinute: 3, EndDay: 4, EndHour: 5, EndMinute: 6}},
			Services: []autoreservation.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
			Video:    []uint16{4}, Audio: []uint16{5}, MinimumMinutes: 10, MaximumMinutes: 90,
		},
		Recording: autoreservation.RecordingSettings{
			Mode: 1, Priority: 4, Follow: true, ServiceMode: 16, Batch: "after.bat",
			Folders:     []autoreservation.Folder{{Path: "recordings", Writer: "Write_Default.dll", Name: "name"}},
			StartMargin: &startMargin, EndMargin: &endMargin, TunerID: 7,
		},
	}
	created, err := store.CreateAutomaticRule(context.Background(), rule)
	if err != nil || created.Number != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	items, err := store.AutomaticRules(context.Background(), autoreservation.MaxPage, 0)
	if err != nil || len(items) != 1 || items[0].Search.Keyword != rule.Search.Keyword ||
		items[0].Recording.StartMargin == nil || *items[0].Recording.StartMargin != startMargin ||
		items[0].Recording.TunerID != 7 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	changedSearch := rule.Search
	changedSearch.Keyword = "変更"
	changedRecording := rule.Recording
	changedRecording.TunerID = 9
	if err := store.UpdateAutomaticRule(context.Background(), 1, changedSearch, changedRecording, 11); err != nil {
		t.Fatal(err)
	}
	items, err = store.AutomaticRules(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].Version != 2 || items[0].Search.Keyword != "変更" ||
		items[0].Recording.TunerID != 9 {
		t.Fatalf("changed=%+v err=%v", items, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err = reopened.AutomaticRules(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].Search.Keyword != "変更" || items[0].Recording.TunerID != 9 {
		t.Fatalf("restarted=%+v err=%v", items, err)
	}
	if err := reopened.DeleteAutomaticRule(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	items, err = reopened.AutomaticRules(context.Background(), 1, 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("deleted=%+v err=%v", items, err)
	}
}

func TestAutomaticReservationIsAtomicAndNeverDuplicatesHistory(t *testing.T) {
	_, store := openMigratedStore(t)
	rule, err := store.CreateAutomaticRule(context.Background(), autoreservation.Rule{
		ID: testID(t, 152), Version: 1, CreatedAtUTCMS: 1, UpdatedAtUTCMS: 1,
		Search:    autoreservation.SearchCondition{Enabled: true},
		Recording: autoreservation.RecordingSettings{Mode: 1, Priority: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	reservation := reservationForTest(t, store)
	created, err := store.CreateAutomaticReservation(context.Background(), rule.Number, reservation)
	if err != nil || created.Number != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	rules, err := store.AutomaticRules(context.Background(), 1, 0)
	if err != nil || len(rules) != 1 || rules[0].ReservationCount != 1 {
		t.Fatalf("rules=%+v err=%v", rules, err)
	}
	duplicate := reservation
	duplicate.ID = testID(t, 153)
	if _, err := store.CreateAutomaticReservation(context.Background(), rule.Number, duplicate); !errors.Is(err, ErrAutomaticReservationDuplicate) {
		t.Fatalf("duplicate err=%v", err)
	}
	var matches, reservations int
	if err := store.reader.QueryRow(`SELECT count(*) FROM automatic_reservation_matches`).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM reservations`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if matches != 1 || reservations != 1 {
		t.Fatalf("matches=%d reservations=%d", matches, reservations)
	}
	if err := store.CancelReservation(context.Background(), created.Number, created.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAutomaticRule(context.Background(), rule.Number); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.CreateAutomaticRule(context.Background(), autoreservation.Rule{
		ID: testID(t, 154), Version: 1, CreatedAtUTCMS: 2, UpdatedAtUTCMS: 2,
		Search:    autoreservation.SearchCondition{Enabled: true},
		Recording: autoreservation.RecordingSettings{Mode: 1, Priority: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate.ID = testID(t, 155)
	if _, err := store.CreateAutomaticReservation(context.Background(), replacement.Number, duplicate); !errors.Is(err, ErrAutomaticReservationDuplicate) {
		t.Fatalf("finished history err=%v", err)
	}
}

func TestDisableAutomaticReservationOnlyBeforeRecordingStarts(t *testing.T) {
	createAutomatic := func(t *testing.T, store *Store) recording.Reservation {
		t.Helper()
		rule, err := store.CreateAutomaticRule(context.Background(), autoreservation.Rule{
			ID: testID(t, 210), Version: 1, CreatedAtUTCMS: 1, UpdatedAtUTCMS: 1,
			Search:    autoreservation.SearchCondition{Enabled: true},
			Recording: autoreservation.RecordingSettings{Mode: 1, Priority: 3},
		})
		if err != nil {
			t.Fatal(err)
		}
		created, err := store.CreateAutomaticReservation(context.Background(), rule.Number, reservationForTest(t, store))
		if err != nil {
			t.Fatal(err)
		}
		return created
	}

	t.Run("before recording", func(t *testing.T) {
		_, store := openMigratedStore(t)
		created := createAutomatic(t, store)
		now := created.CreatedAt.Add(time.Second)
		changed, err := store.DisableAutomaticReservation(context.Background(), created.Program.ProgramInstanceID, now)
		if err != nil || !changed {
			t.Fatalf("changed=%t err=%v", changed, err)
		}
		changed, err = store.DisableAutomaticReservation(context.Background(), created.Program.ProgramInstanceID, now.Add(time.Second))
		if err != nil || changed {
			t.Fatalf("second changed=%t err=%v", changed, err)
		}
		items, err := store.ActiveReservations(context.Background(), 1, 0)
		if err != nil || len(items) != 1 || !items[0].Disabled {
			t.Fatalf("items=%+v err=%v", items, err)
		}
	})

	t.Run("recording started", func(t *testing.T) {
		_, store := openMigratedStore(t)
		created := createAutomatic(t, store)
		now := created.CreatedAt.Add(time.Minute)
		if _, err := store.ClaimRecording(context.Background(), recording.ClaimRequest{
			ReservationID: created.ID, AttemptID: testID(t, 211), SegmentID: testID(t, 212),
			OwnerID: testID(t, 213), OwnerGeneration: 1, Now: now,
			Plan: recording.FilePlan{PartialPath: "automatic/started.ts.partial", FinalPath: "automatic/started.ts"},
		}); err != nil {
			t.Fatal(err)
		}
		changed, err := store.DisableAutomaticReservation(context.Background(), created.Program.ProgramInstanceID, now.Add(time.Second))
		if err != nil || changed {
			t.Fatalf("changed=%t err=%v", changed, err)
		}
		items, err := store.ActiveReservations(context.Background(), 1, 0)
		if err != nil || len(items) != 1 || items[0].Disabled {
			t.Fatalf("items=%+v err=%v", items, err)
		}
	})

	t.Run("finished reservation", func(t *testing.T) {
		_, store := openMigratedStore(t)
		created := createAutomatic(t, store)
		now := created.CreatedAt.Add(time.Minute)
		if err := store.CancelReservation(context.Background(), created.Number, now); err != nil {
			t.Fatal(err)
		}
		changed, err := store.DisableAutomaticReservation(context.Background(), created.Program.ProgramInstanceID,
			now.Add(time.Second))
		if err != nil || changed {
			t.Fatalf("changed=%t err=%v", changed, err)
		}
	})

	t.Run("manual reservation", func(t *testing.T) {
		_, store := openMigratedStore(t)
		created, err := store.CreateReservation(context.Background(), reservationForTest(t, store))
		if err != nil {
			t.Fatal(err)
		}
		changed, err := store.DisableAutomaticReservation(context.Background(), created.Program.ProgramInstanceID,
			created.CreatedAt.Add(time.Second))
		if err != nil || changed {
			t.Fatalf("changed=%t err=%v", changed, err)
		}
		items, err := store.ActiveReservations(context.Background(), 1, 0)
		if err != nil || len(items) != 1 || items[0].Disabled {
			t.Fatalf("items=%+v err=%v", items, err)
		}
	})
}

func TestAutomaticRuleLimitAndNumbersAreNotReused(t *testing.T) {
	_, store := openMigratedStore(t)
	for index := 0; index < autoreservation.MaxRules; index++ {
		created, err := store.CreateAutomaticRule(context.Background(), autoreservation.Rule{
			ID: testID(t, byte(index+1)), Version: 1, CreatedAtUTCMS: int64(index), UpdatedAtUTCMS: int64(index),
			Search:    autoreservation.SearchCondition{Enabled: true},
			Recording: autoreservation.RecordingSettings{Mode: 1, Priority: 3},
		})
		if err != nil || created.Number != int32(index+1) {
			t.Fatalf("index=%d number=%d err=%v", index, created.Number, err)
		}
	}
	over := autoreservation.Rule{
		ID: testID(t, 200), Version: 1, CreatedAtUTCMS: 200, UpdatedAtUTCMS: 200,
		Search: autoreservation.SearchCondition{Enabled: true}, Recording: autoreservation.RecordingSettings{Mode: 1, Priority: 3},
	}
	if _, err := store.CreateAutomaticRule(context.Background(), over); !errors.Is(err, ErrAutomaticRuleLimit) {
		t.Fatalf("limit err=%v", err)
	}
	if err := store.DeleteAutomaticRule(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	over.ID = testID(t, 201)
	created, err := store.CreateAutomaticRule(context.Background(), over)
	if err != nil || created.Number != autoreservation.MaxRules+1 {
		t.Fatalf("number=%d err=%v", created.Number, err)
	}
}

func TestAutomaticRuleRejectsCorruptPersistentJSON(t *testing.T) {
	_, store := openMigratedStore(t)
	rule := autoreservation.Rule{
		ID: testID(t, 202), Version: 1, CreatedAtUTCMS: 1, UpdatedAtUTCMS: 1,
		Search: autoreservation.SearchCondition{Enabled: true}, Recording: autoreservation.RecordingSettings{Mode: 1, Priority: 3},
	}
	created, err := store.CreateAutomaticRule(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := json.Marshal(rule.Search)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"unknown":   `{"Enabled":true,"Unknown":1}`,
		"duplicate": `{"Enabled":true,"Enabled":false}`,
		"overflow":  `{"CheckRecordedDays":65536}`,
		"trailing":  `{}{}`,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := store.writer.Exec(`UPDATE automatic_reservation_rules SET search_json=? WHERE number=?`,
				document, created.Number); err != nil {
				t.Fatal(err)
			}
			if items, err := store.AutomaticRules(context.Background(), 1, 0); err == nil || len(items) != 0 {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			if _, err := store.writer.Exec(`UPDATE automatic_reservation_rules SET search_json=? WHERE number=?`,
				string(valid), created.Number); err != nil {
				t.Fatal(err)
			}
		})
	}
}
