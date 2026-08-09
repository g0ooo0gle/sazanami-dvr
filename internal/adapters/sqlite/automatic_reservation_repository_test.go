package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
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
			StartMargin: &startMargin, EndMargin: &endMargin,
		},
	}
	created, err := store.CreateAutomaticRule(context.Background(), rule)
	if err != nil || created.Number != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	items, err := store.AutomaticRules(context.Background(), autoreservation.MaxPage, 0)
	if err != nil || len(items) != 1 || items[0].Search.Keyword != rule.Search.Keyword ||
		items[0].Recording.StartMargin == nil || *items[0].Recording.StartMargin != startMargin {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	changedSearch := rule.Search
	changedSearch.Keyword = "変更"
	if err := store.UpdateAutomaticRule(context.Background(), 1, changedSearch, rule.Recording, 11); err != nil {
		t.Fatal(err)
	}
	items, err = store.AutomaticRules(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].Version != 2 || items[0].Search.Keyword != "変更" {
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
	if err != nil || len(items) != 1 || items[0].Search.Keyword != "変更" {
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
}
