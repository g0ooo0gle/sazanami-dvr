// Package opsuiは運用WebUIと将来のNative RESTが共有できるread／backup use caseを提供する。
package opsui

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	MaxBackends    = 16
	MaxServices    = catalogmodel.MaxQueryPage
	MaxPrograms    = catalogmodel.MaxQueryPage
	MaximumEPGSpan = 8 * 24 * time.Hour
	DefaultEPGPast = 3 * time.Hour
	DefaultEPGNext = 24 * time.Hour
)

var (
	ErrInvalidRequest  = errors.New("opsui: invalid request")
	ErrBackendNotFound = errors.New("opsui: backend not found")
	ErrBackupBusy      = errors.New("opsui: backup busy")
)

// Clockはdefault EPG windowとbackup時刻をtestで固定する境界である。
type Clock interface{ Now() time.Time }

// BackupPortはSQLite manifest typeやpathをapplicationから隠すonline backup境界である。
type BackupPort interface {
	Create(context.Context, time.Time) (BackupResult, error)
}

// BackupResultは画面へ表示できる秘密を含まない完了factである。
type BackupResult struct {
	ID            string
	State         string
	SchemaVersion int
}

// Settingsは起動時に固定したredacted済み実効設定である。
type Settings struct {
	ProductVersion string
	ProductCommit  string
	SchemaCurrent  int
	SchemaTarget   int
	ListenScope    string
}

// Backendは画面選択に必要な最小backend情報である。
type Backend struct {
	ID              catalogmodel.ID
	Kind            string
	ReportedVersion *string
}

// ProgramはEPG一覧へ渡すdescription／pathを含まない表示modelである。
type Program struct {
	InstanceID     catalogmodel.ID
	ServiceName    string
	ServiceLocator string
	EventLocator   string
	Start          time.Time
	End            *time.Time
	Title          *string
	Revision       int64
	Classification catalogmodel.Classification
}

// Guideは1 backend、1 time windowのbounded EPG結果である。
type Guide struct {
	Backends  []Backend
	Selected  catalogmodel.ID
	From      time.Time
	To        time.Time
	Programs  []Program
	Truncated bool
}

// GuideRequestはoptionalなbackend／window選択である。Zero値は安全なdefaultへ解決する。
type GuideRequest struct {
	Backend *catalogmodel.ID
	From    *time.Time
	To      *time.Time
}

// Overviewはoverview pageへ渡すbounded状態である。
type Overview struct {
	Settings Settings
	Backends []Backend
}

// Serviceはcatalog readとonline backupのapplication境界を所有する。
type Service struct {
	reader     catalogmodel.CatalogReader
	backup     BackupPort
	clock      Clock
	settings   Settings
	backupGate chan struct{}
}

// Newは依存を検証し、同時backup 1件のnon-blocking gateを作る。
func New(reader catalogmodel.CatalogReader, backup BackupPort, clock Clock, settings Settings) (*Service, error) {
	if reader == nil || backup == nil || clock == nil || settings.ListenScope != "loopback-only" ||
		settings.SchemaCurrent < 1 || settings.SchemaTarget < settings.SchemaCurrent {
		return nil, ErrInvalidRequest
	}
	return &Service{reader: reader, backup: backup, clock: clock, settings: settings, backupGate: make(chan struct{}, 1)}, nil
}

// Overviewはcompleted backendとredacted settingsだけを返す。
func (service *Service) Overview(ctx context.Context) (Overview, error) {
	backends, err := service.backends(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{Settings: service.settings, Backends: backends}, nil
}

// Settingsはprivate path／endpoint／environmentを持たない起動時設定を返す。
func (service *Service) Settings() Settings { return service.settings }

// Guideは選択backendのcompleted catalogをbounded windowで構成する。
func (service *Service) Guide(ctx context.Context, request GuideRequest) (Guide, error) {
	backends, err := service.backends(ctx)
	if err != nil {
		return Guide{}, err
	}
	now := service.clock.Now().UTC()
	from := now.Add(-DefaultEPGPast)
	to := now.Add(DefaultEPGNext)
	if request.From != nil {
		from = request.From.UTC()
	}
	if request.To != nil {
		to = request.To.UTC()
	}
	if from.IsZero() || to.IsZero() || !to.After(from) || to.Sub(from) > MaximumEPGSpan {
		return Guide{}, ErrInvalidRequest
	}
	result := Guide{Backends: backends, From: from, To: to}
	if len(backends) == 0 {
		return result, nil
	}
	selected := backends[0].ID
	if request.Backend != nil {
		selected = *request.Backend
	}
	found := false
	for _, backend := range backends {
		if backend.ID == selected {
			found = true
			break
		}
	}
	if !found {
		return Guide{}, ErrBackendNotFound
	}
	result.Selected = selected
	services, err := service.reader.CurrentServices(ctx, selected, MaxServices, catalogmodel.ID{})
	if err != nil {
		return Guide{}, err
	}
	serviceNames := make(map[string]string, len(services))
	for _, item := range services {
		serviceNames[item.ProviderLocator] = item.DisplayName
	}
	programs, truncated, err := service.reader.CurrentProgramsInWindow(ctx, selected,
		from.UnixMilli(), to.UnixMilli(), MaxPrograms)
	if err != nil {
		return Guide{}, err
	}
	result.Truncated = truncated
	result.Programs = make([]Program, 0, len(programs))
	for _, item := range programs {
		if item.Material.StartUTCMS == nil {
			continue
		}
		start := time.UnixMilli(*item.Material.StartUTCMS).UTC()
		var end *time.Time
		if item.Material.DurationMS != nil {
			value := start.Add(time.Duration(*item.Material.DurationMS) * time.Millisecond)
			end = &value
		}
		name := serviceNames[item.ServiceLocator]
		if name == "" {
			name = item.ServiceLocator
		}
		result.Programs = append(result.Programs, Program{
			InstanceID: item.InstanceID, ServiceName: name, ServiceLocator: item.ServiceLocator,
			EventLocator: item.EventLocator, Start: start, End: end, Title: item.Material.Title,
			Revision: item.RevisionNumber, Classification: item.Classification,
		})
	}
	return result, nil
}

// Backupは同時実行を待たせずbusyにし、Portのbounded結果だけを返す。
func (service *Service) Backup(ctx context.Context) (BackupResult, error) {
	select {
	case service.backupGate <- struct{}{}:
		defer func() { <-service.backupGate }()
	default:
		return BackupResult{}, ErrBackupBusy
	}
	result, err := service.backup.Create(ctx, service.clock.Now().UTC())
	if err != nil {
		return BackupResult{}, err
	}
	if result.ID == "" || result.State != "complete" || result.SchemaVersion < 1 {
		return BackupResult{}, ErrInvalidRequest
	}
	return result, nil
}

func (service *Service) backends(ctx context.Context) ([]Backend, error) {
	items, err := service.reader.CurrentBackends(ctx, MaxBackends, catalogmodel.ID{})
	if err != nil {
		return nil, err
	}
	result := make([]Backend, len(items))
	for index, item := range items {
		result[index] = Backend{ID: item.ID, Kind: item.Kind, ReportedVersion: item.ReportedVersion}
	}
	return result, nil
}
