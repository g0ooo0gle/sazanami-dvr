// Package autoreservationは自動予約条件の保存と完成済み番組表の評価を組み立てる。
package autoreservation

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// RuleStoreは自動予約条件の永続化操作を提供する。
type RuleStore interface {
	CreateAutomaticRule(context.Context, autoreservation.Rule) (autoreservation.Rule, error)
	AutomaticRules(context.Context, int, int32) ([]autoreservation.Rule, error)
	UpdateAutomaticRule(context.Context, int32, autoreservation.SearchCondition, autoreservation.RecordingSettings, int64) error
	DeleteAutomaticRule(context.Context, int32) error
}

// Clockは条件の作成・更新時刻をテストから指定できるようにする。
type Clock interface{ Now() time.Time }

// RuleServiceはCtrlCmd操作を自動予約条件の永続化へ接続する。
type RuleService struct {
	Store RuleStore
	Clock Clock
	NewID func() (catalogmodel.ID, error)
}

// Addは検証済み条件へIDと作成時刻を付けて保存する。
func (service RuleService) Add(ctx context.Context, search autoreservation.SearchCondition,
	settings autoreservation.RecordingSettings,
) (autoreservation.Rule, error) {
	if ctx == nil || service.Store == nil || service.Clock == nil || service.NewID == nil ||
		autoreservation.ValidateChange(1, search, settings) != nil {
		return autoreservation.Rule{}, errors.New("autoreservation: invalid add operation")
	}
	now := service.Clock.Now().UTC()
	if now.IsZero() || now.UnixMilli() < 0 {
		return autoreservation.Rule{}, errors.New("autoreservation: invalid clock")
	}
	id, err := service.NewID()
	if err != nil {
		return autoreservation.Rule{}, errors.New("autoreservation: id generation failed")
	}
	return service.Store.CreateAutomaticRule(ctx, autoreservation.Rule{
		ID: id, Version: 1, Search: search, Recording: settings,
		CreatedAtUTCMS: now.UnixMilli(), UpdatedAtUTCMS: now.UnixMilli(),
	})
}

// ListはCtrlCmd番号順の条件を上限付きで返す。
func (service RuleService) List(ctx context.Context, limit int, after int32) ([]autoreservation.Rule, error) {
	if ctx == nil || service.Store == nil {
		return nil, errors.New("autoreservation: invalid list operation")
	}
	return service.Store.AutomaticRules(ctx, limit, after)
}

// Changeは既存番号の検索条件と録画設定を一度に置き換える。
func (service RuleService) Change(ctx context.Context, number int32, search autoreservation.SearchCondition,
	settings autoreservation.RecordingSettings,
) error {
	if ctx == nil || service.Store == nil || service.Clock == nil ||
		autoreservation.ValidateChange(number, search, settings) != nil {
		return errors.New("autoreservation: invalid change operation")
	}
	now := service.Clock.Now().UTC()
	if now.IsZero() || now.UnixMilli() < 0 {
		return errors.New("autoreservation: invalid clock")
	}
	return service.Store.UpdateAutomaticRule(ctx, number, search, settings, now.UnixMilli())
}

// Deleteは条件を削除し、すでに作った通常予約は維持する。
func (service RuleService) Delete(ctx context.Context, number int32) error {
	if ctx == nil || service.Store == nil || number < 1 {
		return errors.New("autoreservation: invalid delete operation")
	}
	return service.Store.DeleteAutomaticRule(ctx, number)
}
