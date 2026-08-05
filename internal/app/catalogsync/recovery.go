// Package catalogsyncはprovider cursorをtransaction外でboundedに消費し、catalog repositoryへ保存する。
package catalogsync

import (
	"context"
	"errors"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// RecoveryServiceは通常のcatalog同期を始める前に、未完了generationを回復する。
type RecoveryService struct {
	Repository catalogmodel.SyncRecovery
	Clock      Clock
}

// Reconcileは現在のUTC時刻でstale RUNNING generationをFAILEDへ閉じ、件数を返す。
func (service RecoveryService) Reconcile(ctx context.Context) (int, error) {
	if ctx == nil || service.Repository == nil || service.Clock == nil {
		return 0, errors.New("catalogsync: missing recovery dependency")
	}
	now := service.Clock.Now()
	if now.IsZero() {
		return 0, errors.New("catalogsync: zero recovery time")
	}
	return service.Repository.ReconcileRunningSyncs(ctx, now.UTC().UnixMilli())
}
