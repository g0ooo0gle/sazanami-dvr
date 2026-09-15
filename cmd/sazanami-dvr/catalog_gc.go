package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/cataloggc"
)

// catalogGCRunnerはcatalog sync直前のGC境界をテスト可能にするfunctional portである。
type catalogGCRunner func(context.Context, *sqliteadapter.Store, cataloggc.Clock) (cataloggc.Result, error)

// newCatalogGCはSQLiteの結果をapplication-owned DTOへ写像したGC runnerを返す。
func newCatalogGC(store *sqliteadapter.Store, clock cataloggc.Clock) func(context.Context) (cataloggc.Result, error) {
	service := cataloggc.Service{
		Clock: clock,
		Prune: func(ctx context.Context, cutoffUTCMS int64) (cataloggc.BatchResult, error) {
			if store == nil {
				return cataloggc.BatchResult{}, cataloggc.ErrFailed
			}
			result, err := store.PruneCatalogBatch(ctx, cutoffUTCMS)
			return cataloggc.BatchResult{
				ProgramObservationsDeleted: result.ProgramObservationsDeleted,
				ServiceObservationsDeleted: result.ServiceObservationsDeleted,
				CatalogSyncsDeleted:        result.CatalogSyncsDeleted,
				ProgramRevisionsDeleted:    result.ProgramRevisionsDeleted,
				ProgramInstancesDeleted:    result.ProgramInstancesDeleted,
				ServicesDeleted:            result.ServicesDeleted,
				AllEmpty:                   result.AllEmpty,
			}, err
		},
	}
	return service.Run
}

// observeCatalogGCはcatalog refreshの結果と分離した、固定項目だけのGC観測を出力する。
func observeCatalogGC(stdout, stderr io.Writer) func(cataloggc.Result, error) {
	return func(result cataloggc.Result, err error) {
		reason := catalogGCReason(result, err)
		destination := stdout
		if err != nil {
			destination = stderr
		}
		fmt.Fprintf(destination, "catalog_gc program_observations_deleted=%d service_observations_deleted=%d catalog_syncs_deleted=%d program_revisions_deleted=%d program_instances_deleted=%d services_deleted=%d more=%t duration_ms=%d reason=%s\n",
			result.ProgramObservationsDeleted, result.ServiceObservationsDeleted, result.CatalogSyncsDeleted,
			result.ProgramRevisionsDeleted, result.ProgramInstancesDeleted, result.ServicesDeleted,
			result.More, result.DurationMS, reason)
	}
}

func catalogGCReason(result cataloggc.Result, err error) string {
	switch {
	case err == nil && result.Completed:
		return "completed"
	case err == nil && result.More:
		return "deadline"
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	default:
		return "catalog-gc-failed"
	}
}
