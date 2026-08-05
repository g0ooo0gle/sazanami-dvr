package opsui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestGuideDefaultsSelectsBackendAndRedactsSettings(t *testing.T) {
	backendID := opsID(1)
	start := time.Date(2026, 8, 3, 2, 0, 0, 0, time.UTC)
	title := "<script>合成</script>"
	duration := int64((30 * time.Minute) / time.Millisecond)
	reader := &fakeReader{
		backends: []catalogmodel.CurrentBackend{{ID: backendID, Kind: "FAKE"}},
		services: []catalogmodel.CurrentService{{ProviderLocator: "service:1", DisplayName: "合成サービス"}},
		programs: []catalogmodel.CurrentProgram{{InstanceID: opsID(2), ServiceLocator: "service:1",
			EventLocator: "event:1", RevisionNumber: 1, Material: catalogmodel.RevisionMaterial{
				StartUTCMS: int64Pointer(start.UnixMilli()), DurationMS: &duration, Title: &title,
				Validation: catalogmodel.ValidationValid,
			}}},
	}
	service := newTestService(t, reader, &fakeBackup{}, start)
	guide, err := service.Guide(context.Background(), GuideRequest{})
	if err != nil || guide.Selected != backendID || len(guide.Programs) != 1 {
		t.Fatalf("guide=%+v err=%v", guide, err)
	}
	if guide.From != start.Add(-DefaultEPGPast) || guide.To != start.Add(DefaultEPGNext) {
		t.Fatalf("window=%s..%s", guide.From, guide.To)
	}
	if guide.Programs[0].ServiceName != "合成サービス" || *guide.Programs[0].Title != title {
		t.Fatalf("program=%+v", guide.Programs[0])
	}
	settings := service.Settings()
	if settings.ListenScope != "loopback-only" || settings.ProductCommit != "0123456789abcdef" {
		t.Fatalf("settings=%+v", settings)
	}
}

func TestGuideRejectsUnknownBackendAndWideWindow(t *testing.T) {
	reader := &fakeReader{backends: []catalogmodel.CurrentBackend{{ID: opsID(1), Kind: "FAKE"}}}
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	service := newTestService(t, reader, &fakeBackup{}, now)
	unknown := opsID(9)
	if _, err := service.Guide(context.Background(), GuideRequest{Backend: &unknown}); !errors.Is(err, ErrBackendNotFound) {
		t.Fatalf("unknown err=%v", err)
	}
	from := now
	to := now.Add(MaximumEPGSpan + time.Second)
	if _, err := service.Guide(context.Background(), GuideRequest{From: &from, To: &to}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("wide err=%v", err)
	}
}

func TestBackupIsSingleFlight(t *testing.T) {
	backup := &blockingBackup{started: make(chan struct{}), release: make(chan struct{})}
	service := newTestService(t, &fakeReader{}, backup, time.Now().UTC())
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		if _, err := service.Backup(context.Background()); err != nil {
			t.Errorf("first backup: %v", err)
		}
	}()
	<-backup.started
	if _, err := service.Backup(context.Background()); !errors.Is(err, ErrBackupBusy) {
		t.Fatalf("second err=%v", err)
	}
	close(backup.release)
	wait.Wait()
}

func newTestService(t *testing.T, reader catalogmodel.CatalogReader, backup BackupPort, now time.Time) *Service {
	t.Helper()
	service, err := New(reader, backup, fixedClock{now: now}, Settings{
		ProductVersion: "development", ProductCommit: "0123456789abcdef", SchemaCurrent: 1,
		SchemaTarget: 1, ListenScope: "loopback-only",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fakeReader struct {
	backends  []catalogmodel.CurrentBackend
	services  []catalogmodel.CurrentService
	programs  []catalogmodel.CurrentProgram
	truncated bool
}

func (reader *fakeReader) CurrentBackends(context.Context, int, catalogmodel.ID) ([]catalogmodel.CurrentBackend, error) {
	return reader.backends, nil
}

func (reader *fakeReader) CurrentServices(context.Context, catalogmodel.ID, int, catalogmodel.ID) ([]catalogmodel.CurrentService, error) {
	return reader.services, nil
}

func (reader *fakeReader) CurrentProgramsInWindow(context.Context, catalogmodel.ID, int64, int64, int) ([]catalogmodel.CurrentProgram, bool, error) {
	return reader.programs, reader.truncated, nil
}

type fakeBackup struct{}

func (*fakeBackup) Create(context.Context, time.Time) (BackupResult, error) {
	return BackupResult{ID: "00000000-0000-4000-8000-000000000000", State: "complete", SchemaVersion: 1}, nil
}

type blockingBackup struct {
	started chan struct{}
	release chan struct{}
}

func (backup *blockingBackup) Create(context.Context, time.Time) (BackupResult, error) {
	close(backup.started)
	<-backup.release
	return BackupResult{ID: "00000000-0000-4000-8000-000000000000", State: "complete", SchemaVersion: 1}, nil
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func opsID(marker byte) catalogmodel.ID {
	var id catalogmodel.ID
	for index := range id {
		id[index] = marker
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

func int64Pointer(value int64) *int64 { return &value }
