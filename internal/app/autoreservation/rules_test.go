package autoreservation

import (
	"context"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

type ruleStore struct{ rule autoreservation.Rule }

func (store *ruleStore) CreateAutomaticRule(_ context.Context, rule autoreservation.Rule) (autoreservation.Rule, error) {
	rule.Number = 1
	store.rule = rule
	return rule, nil
}

func (store *ruleStore) AutomaticRules(_ context.Context, _ int, _ int32) ([]autoreservation.Rule, error) {
	return []autoreservation.Rule{store.rule}, nil
}

func (store *ruleStore) UpdateAutomaticRule(_ context.Context, number int32, search autoreservation.SearchCondition,
	settings autoreservation.RecordingSettings, now int64,
) error {
	store.rule.Number, store.rule.Search, store.rule.Recording, store.rule.UpdatedAtUTCMS = number, search, settings, now
	return nil
}

func (store *ruleStore) DeleteAutomaticRule(_ context.Context, _ int32) error {
	store.rule = autoreservation.Rule{}
	return nil
}

func TestRuleServiceLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	store := &ruleStore{}
	service := RuleService{Store: store, Clock: fixedClock{now}, NewID: func() (catalogmodel.ID, error) {
		return catalogmodel.ID{1}, nil
	}}
	settings := autoreservation.RecordingSettings{Mode: 1, Priority: 3}
	created, err := service.Add(context.Background(), autoreservation.SearchCondition{Enabled: true, Keyword: "番組"}, settings)
	if err != nil || created.Number != 1 || created.CreatedAtUTCMS != now.UnixMilli() {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if err := service.Change(context.Background(), 1, autoreservation.SearchCondition{Enabled: true, Keyword: "変更"}, settings); err != nil {
		t.Fatal(err)
	}
	if store.rule.Search.Keyword != "変更" {
		t.Fatalf("rule=%+v", store.rule)
	}
	if err := service.Delete(context.Background(), 1); err != nil || store.rule.ID != (catalogmodel.ID{}) {
		t.Fatalf("rule=%+v err=%v", store.rule, err)
	}
}
