// Package catalogsyncはprovider cursorをtransaction外でboundedに消費し、catalog repositoryへ保存する。
package catalogsync

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providercatalog "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

const (
	maxServiceCount = 4_096
	maxProgramCount = 262_144
	maxServicePages = maxServiceCount + 1
	maxProgramPages = maxProgramCount + 1
)

// Clockはsyncのdurable UTC時刻をtestで注入する境界である。
type Clock interface {
	Now() time.Time
}

// Requestは1回のcatalog syncに必要な固定identityと上限をまとめる。
type Request struct {
	Backend             catalogmodel.Backend
	CorrelationID       string
	ServicePageLimit    int
	ProgramPageLimit    int
	VerifiedFakeLineage bool
}

// Resultはcompleted generationのIDと確定件数を返す。
type Result struct {
	SyncID      catalogmodel.ID
	Services    int
	Programs    int
	CompletedAt time.Time
}

// FailureStageは定期更新が秘密情報を出さずに失敗箇所を分類するための段階である。
type FailureStage uint8

const (
	// FailureInternalは依存や要求自体が不正な段階である。
	FailureInternal FailureStage = iota + 1
	// FailureProviderは外部providerの接続、応答、観測値を処理する段階である。
	FailureProvider
	// FailureStoreは永続化または世代確定の段階である。
	FailureStore
	// FailureValidationは保存済み候補を完了前に検証する段階である。
	FailureValidation
)

// Failureは元のエラーを保ったまま、番組表更新の失敗段階だけを追加する。
type Failure struct {
	Stage FailureStage
	err   error
}

// Errorは外部へ追加情報を出さず、元のエラーメッセージを返す。
func (failure *Failure) Error() string { return failure.err.Error() }

// Unwrapは既存のエラー判定を維持するため、元のエラーを返す。
func (failure *Failure) Unwrap() error { return failure.err }

// StageOfはcatalog syncの失敗段階を返す。分類できないエラーはFailureInternalとする。
func StageOf(err error) FailureStage {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Stage
	}
	return FailureInternal
}

// Serviceはprovider I/Oと短いrepository transactionの順序を所有する。
type Service struct {
	Provider   providercatalog.CatalogProvider
	Repository catalogmodel.Repository
	Clock      Clock
}

// Syncはservices、programsを順にnormal endまで読み、最後にだけgenerationをCOMPLETEDへ遷移させる。
func (service Service) Sync(ctx context.Context, request Request) (result Result, returnErr error) {
	return service.sync(ctx, request, nil)
}

// SyncValidatedは全データの保存後、COMPLETEDへ遷移する直前に候補世代を検証する。
// validateが失敗した世代はFAILEDへ閉じ、以前の完了世代を維持する。
func (service Service) SyncValidated(ctx context.Context, request Request,
	validate func(context.Context, catalogmodel.ID) error,
) (Result, error) {
	if validate == nil {
		return Result{}, stage(FailureInternal, errors.New("catalogsync: missing validation"))
	}
	return service.sync(ctx, request, validate)
}

func (service Service) sync(ctx context.Context, request Request,
	validate func(context.Context, catalogmodel.ID) error,
) (result Result, returnErr error) {
	if ctx == nil || service.Provider == nil || service.Repository == nil || service.Clock == nil {
		return result, stage(FailureInternal, errors.New("catalogsync: missing dependency"))
	}
	if request.Backend.Kind != "FAKE" && request.Backend.Kind != "MIRAKURUN" {
		return result, stage(FailureInternal, errors.New("catalogsync: backend kind is not accepted"))
	}
	if request.Backend.Kind == "MIRAKURUN" && request.VerifiedFakeLineage {
		return result, stage(FailureInternal, errors.New("catalogsync: real backend cannot use Fake lineage"))
	}
	if request.CorrelationID == "" || len(request.CorrelationID) > 128 {
		return result, stage(FailureInternal, errors.New("catalogsync: invalid correlation"))
	}
	if request.ServicePageLimit < 1 || request.ServicePageLimit > provider.MaxCatalogPage ||
		request.ProgramPageLimit < 1 || request.ProgramPageLimit > provider.MaxCatalogPage {
		return result, stage(FailureInternal, errors.New("catalogsync: page limit outside accepted range"))
	}

	started := service.Clock.Now().UTC()
	request.Backend.ObservedAtMS = started.UnixMilli()
	if err := service.Repository.EnsureBackend(ctx, request.Backend); err != nil {
		return result, stage(FailureStore, err)
	}
	syncID, err := catalogmodel.NewID()
	if err != nil {
		return result, stage(FailureInternal, errors.New("catalogsync: generate sync id"))
	}
	result.SyncID = syncID
	syncRecord := catalogmodel.Sync{
		ID: syncID, BackendID: request.Backend.ID, StartedAtMS: started.UnixMilli(),
		CorrelationID: request.CorrelationID, VerifiedFakeLineage: request.VerifiedFakeLineage,
	}
	if err := service.Repository.BeginSync(ctx, syncRecord); err != nil {
		return result, stage(FailureStore, err)
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		failureContext := context.WithoutCancel(ctx)
		if err := service.Repository.FailSync(failureContext, syncID, service.Clock.Now().UTC().UnixMilli(), failureReason(returnErr)); err != nil && returnErr == nil {
			returnErr = stage(FailureStore, err)
		}
	}()

	services, err := service.Provider.OpenServices(ctx, providercatalog.ServiceRequest{
		CorrelationID: request.CorrelationID + "-services", Limit: request.ServicePageLimit,
	})
	if err != nil {
		return result, stage(FailureProvider, err)
	}
	if err := service.consumeServices(ctx, syncID, services, &result.Services); err != nil {
		return result, err
	}

	programs, err := service.Provider.OpenPrograms(ctx, providercatalog.ProgramRequest{
		CorrelationID: request.CorrelationID + "-programs", Limit: request.ProgramPageLimit,
	})
	if err != nil {
		return result, stage(FailureProvider, err)
	}
	if err := service.consumePrograms(ctx, syncID, request.VerifiedFakeLineage, programs, &result.Programs); err != nil {
		return result, err
	}
	if validate != nil {
		if err := validate(ctx, syncID); err != nil {
			return result, stage(FailureValidation, err)
		}
	}

	finished := service.Clock.Now().UTC()
	if err := service.Repository.CompleteSync(ctx, syncID, finished.UnixMilli(), result.Services, result.Programs); err != nil {
		return result, stage(FailureStore, err)
	}
	completed = true
	result.CompletedAt = finished
	return result, nil
}

func (service Service) consumeServices(ctx context.Context, syncID catalogmodel.ID, cursor providercatalog.ServiceCursor, total *int) (err error) {
	defer func() {
		if closeErr := cursor.Close(); err == nil && closeErr != nil {
			err = stage(FailureProvider, closeErr)
		}
	}()
	for pages := 0; pages < maxServicePages; pages++ {
		page, nextErr := cursor.Next(ctx)
		if nextErr != nil {
			return stage(FailureProvider, nextErr)
		}
		if len(page.Items) > provider.MaxCatalogPage || *total > maxServiceCount-len(page.Items) {
			return stage(FailureProvider, errors.New("catalogsync: service operation exceeds accepted limit"))
		}
		converted := make([]catalogmodel.ServiceObservation, len(page.Items))
		for index, observation := range page.Items {
			converted[index], err = convertService(observation)
			if err != nil {
				return stage(FailureProvider, err)
			}
		}
		if err := storeServiceChunks(ctx, service.Repository, syncID, converted); err != nil {
			return stage(FailureStore, err)
		}
		*total += len(converted)
		if page.End {
			return nil
		}
	}
	return stage(FailureProvider, errors.New("catalogsync: service page limit exceeded"))
}

func (service Service) consumePrograms(ctx context.Context, syncID catalogmodel.ID, verified bool, cursor providercatalog.ProgramCursor, total *int) (err error) {
	defer func() {
		if closeErr := cursor.Close(); err == nil && closeErr != nil {
			err = stage(FailureProvider, closeErr)
		}
	}()
	for pages := 0; pages < maxProgramPages; pages++ {
		page, nextErr := cursor.Next(ctx)
		if nextErr != nil {
			return stage(FailureProvider, nextErr)
		}
		if len(page.Items) > provider.MaxCatalogPage || *total > maxProgramCount-len(page.Items) {
			return stage(FailureProvider, errors.New("catalogsync: program operation exceeds accepted limit"))
		}
		converted := make([]catalogmodel.ProgramObservation, len(page.Items))
		for index, observation := range page.Items {
			converted[index], err = convertProgram(observation)
			if err != nil {
				return stage(FailureProvider, err)
			}
		}
		if err := storeProgramChunks(ctx, service.Repository, syncID, verified, converted); err != nil {
			return stage(FailureStore, err)
		}
		*total += len(converted)
		if page.End {
			return nil
		}
	}
	return stage(FailureProvider, errors.New("catalogsync: program page limit exceeded"))
}

func storeServiceChunks(ctx context.Context, repository catalogmodel.Repository, syncID catalogmodel.ID, values []catalogmodel.ServiceObservation) error {
	for start := 0; start < len(values); start += catalogmodel.MaxWriteBatch {
		end := min(start+catalogmodel.MaxWriteBatch, len(values))
		if err := repository.StoreServices(ctx, syncID, values[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func storeProgramChunks(ctx context.Context, repository catalogmodel.Repository, syncID catalogmodel.ID, verified bool, values []catalogmodel.ProgramObservation) error {
	for start := 0; start < len(values); start += catalogmodel.MaxWriteBatch {
		end := min(start+catalogmodel.MaxWriteBatch, len(values))
		if err := repository.StorePrograms(ctx, syncID, verified, values[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func convertService(observation providercatalog.ServiceObservation) (catalogmodel.ServiceObservation, error) {
	if observation.Locator == "" || len(observation.Locator) > 256 || len(observation.DisplayName) > 4096 ||
		!utf8.ValidString(observation.Locator) || !utf8.ValidString(observation.DisplayName) ||
		!utf8.ValidString(observation.Broadcast) || !utf8.ValidString(observation.TuningTarget.Opaque) {
		return catalogmodel.ServiceObservation{}, errors.New("catalogsync: invalid service observation")
	}
	network := int64(observation.NetworkID)
	serviceID := int64(observation.ServiceID)
	result := catalogmodel.ServiceObservation{
		ProviderLocator: observation.Locator, NetworkID: &network, ServiceID: &serviceID,
		DisplayName: observation.DisplayName, Validation: convertValidation(observation.Validation),
	}
	if observation.Broadcast != "" {
		result.BroadcastKind = pointer(observation.Broadcast)
	}
	if observation.TuningTarget.Opaque != "" {
		result.TuningTarget = pointer(observation.TuningTarget.Opaque)
	}
	return result, nil
}

func convertProgram(observation providercatalog.ProgramObservation) (catalogmodel.ProgramObservation, error) {
	if observation.ServiceLocator == "" || observation.EventLocator == "" || len(observation.Title) > 4096 || len(observation.Description) > 65536 ||
		!utf8.ValidString(observation.ServiceLocator) || !utf8.ValidString(observation.EventLocator) ||
		!utf8.ValidString(observation.Title) || !utf8.ValidString(observation.Description) {
		return catalogmodel.ProgramObservation{}, errors.New("catalogsync: invalid program observation")
	}
	material := catalogmodel.RevisionMaterial{
		Title: pointer(observation.Title), Description: pointer(observation.Description),
		Validation: convertValidation(observation.Validation), FreeAccess: catalogmodel.FreeUnknown,
	}
	if observation.FreeAccess != nil {
		if *observation.FreeAccess {
			material.FreeAccess = catalogmodel.FreeYes
		} else {
			material.FreeAccess = catalogmodel.FreeNo
		}
	}
	if observation.Start != nil {
		value := observation.Start.UTC().UnixMilli()
		material.StartUTCMS = &value
	}
	if observation.Duration != nil {
		value := observation.Duration.Milliseconds()
		if value <= 0 {
			material.Validation = catalogmodel.ValidationInvalid
		} else {
			material.DurationMS = &value
		}
	}
	result := catalogmodel.ProgramObservation{
		ServiceLocator: observation.ServiceLocator, EventLocator: observation.EventLocator, Material: material,
	}
	if observation.EventID != nil {
		value := int64(*observation.EventID)
		result.RawEventID = &value
	}
	return result, nil
}

func convertValidation(value provider.ValidationState) catalogmodel.Validation {
	switch value {
	case provider.ValidationValid:
		return catalogmodel.ValidationValid
	case provider.ValidationInvalid:
		return catalogmodel.ValidationInvalid
	default:
		return catalogmodel.ValidationProvisional
	}
}

func failureReason(err error) string {
	if err == nil {
		return "sync-incomplete"
	}
	var failure *provider.Failure
	if errors.As(err, &failure) {
		return "provider-" + string(failure.Reason)
	}
	return "sync-failed"
}

func stage(value FailureStage, err error) error {
	if err == nil {
		return nil
	}
	var failure *Failure
	if errors.As(err, &failure) {
		return err
	}
	return &Failure{Stage: value, err: err}
}

func pointer[T any](value T) *T { return &value }

// Stringはsync結果を秘密情報なしのbounded診断へ変換する。
func (result Result) String() string {
	return fmt.Sprintf("catalog sync completed: services=%d programs=%d", result.Services, result.Programs)
}
