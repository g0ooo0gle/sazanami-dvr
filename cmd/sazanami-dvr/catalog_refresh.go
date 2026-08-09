package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	ctrlcmdruntime "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/runtime"
	mirakurunadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/provider/mirakurun"
	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	autoreservationapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogrefresh"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogsync"
	recordingapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// recordingCatalogRefreshは一回分のMirakurun取得、候補検証、完了、公開を順に実行する。
type recordingCatalogRefresh struct {
	dataRoot         string
	channelMap       string
	provider         *mirakurunadapter.Adapter
	store            *sqliteadapter.Store
	holder           *ctrlcmdruntime.SnapshotHolder
	clock            wallClock
	follow           func(context.Context) (recordingapp.FollowResult, error)
	automatic        func(context.Context) (autoreservationapp.Result, error)
	observeAutomatic func(autoreservationapp.Result, error, time.Duration)
}

func (operation *recordingCatalogRefresh) sync(ctx context.Context) (catalogrefresh.Result, string, error) {
	if operation == nil || ctx == nil || operation.provider == nil || operation.store == nil || operation.holder == nil {
		return catalogrefresh.Result{}, "catalog-refresh-internal", errors.New("catalog refresh is not configured")
	}
	identityHash := operation.provider.IdentityHash()
	backendID := stableBackendID(identityHash)
	var reportedVersion *string
	if observed, err := operation.provider.ObserveVersion(ctx); err == nil {
		reportedVersion = &observed.Current
	}
	correlationID, err := catalogmodel.NewID()
	if err != nil {
		return catalogrefresh.Result{}, "catalog-refresh-internal", err
	}
	sourceRef := "mirakurun-http-json-v1"
	var candidate *ctrlcmdruntime.Snapshot
	result, err := (catalogsync.Service{
		Provider: operation.provider, Repository: operation.store, Clock: operation.clock,
	}).SyncValidated(ctx, catalogsync.Request{
		Backend: catalogmodel.Backend{
			ID: backendID, Kind: "MIRAKURUN", IdentityHash: identityHash,
			ReportedVersion: reportedVersion, SourceRef: &sourceRef,
		},
		CorrelationID: correlationID.String(), ServicePageLimit: 256, ProgramPageLimit: 256,
		VerifiedFakeLineage: false,
	}, func(validationContext context.Context, generationID catalogmodel.ID) error {
		var buildErr error
		candidate, buildErr = ctrlcmdruntime.BuildCandidateSnapshot(validationContext, operation.dataRoot,
			operation.channelMap, backendID, generationID, operation.store)
		return buildErr
	})
	if err != nil {
		return catalogrefresh.Result{}, refreshFailureReason(err), err
	}
	if candidate == nil {
		return catalogrefresh.Result{}, "catalog-refresh-internal", errors.New("catalog refresh produced no candidate")
	}
	if err := operation.holder.Store(candidate); err != nil {
		return catalogrefresh.Result{}, "catalog-refresh-internal", err
	}
	if operation.follow != nil {
		if _, err := operation.follow(ctx); err != nil {
			return catalogrefresh.Result{}, "catalog-refresh-follow-failed", err
		}
	}
	if operation.automatic != nil {
		started := time.Now()
		automaticResult, automaticErr := operation.automatic(ctx)
		if operation.observeAutomatic != nil {
			operation.observeAutomatic(automaticResult, automaticErr, time.Since(started))
		}
	}
	return catalogrefresh.Result{Services: result.Services, Programs: result.Programs}, "", nil
}

func refreshFailureReason(err error) string {
	switch catalogsync.StageOf(err) {
	case catalogsync.FailureProvider:
		return "catalog-refresh-provider-failed"
	case catalogsync.FailureStore:
		return "catalog-refresh-store-failed"
	case catalogsync.FailureValidation:
		return "catalog-refresh-channel-mismatch"
	default:
		return "catalog-refresh-internal"
	}
}

func observeAutomaticReservation(stdout, stderr io.Writer) func(autoreservationapp.Result, error, time.Duration) {
	return func(result autoreservationapp.Result, err error, duration time.Duration) {
		if err != nil {
			fmt.Fprintf(stderr, "automatic_reservation result=failed reason=evaluation-failed duration_ms=%d\n", duration.Milliseconds())
			return
		}
		fmt.Fprintf(stdout, "automatic_reservation result=completed rules=%d programs=%d matched=%d created=%d duplicates=%d unavailable_rules=%d limit_reached=%t duration_ms=%d\n",
			result.Rules, result.Programs, result.Matched, result.Created, result.Duplicates,
			result.UnavailableRules, result.LimitReached, duration.Milliseconds())
	}
}

func observeCatalogRefresh(stdout, stderr io.Writer) func(catalogrefresh.Event) {
	return func(event catalogrefresh.Event) {
		if event.Completed {
			fmt.Fprintf(stdout, "catalog_refresh result=completed services=%d programs=%d duration_ms=%d\n",
				event.Services, event.Programs, event.DurationMS)
			return
		}
		fmt.Fprintf(stderr, "catalog_refresh result=failed reason=%s duration_ms=%d\n", event.Reason, event.DurationMS)
	}
}
