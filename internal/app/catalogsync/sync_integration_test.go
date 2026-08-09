package catalogsync_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/provider/fake"
	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogsync"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providercatalog "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

type advancingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *advancingClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.now
	clock.now = clock.now.Add(time.Second)
	return value
}

func TestFakeCatalogPersistsRevisionsAndRestart(t *testing.T) {
	root, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID, err := catalogmodel.NewIDFrom(zeroReader{})
	if err != nil {
		t.Fatal(err)
	}
	backend := catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:test"))}

	first := newHarness(t, "最初の番組", nil)
	firstResult, err := (catalogsync.Service{Provider: first.Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), catalogsync.Request{
			Backend: backend, CorrelationID: "sync-1", ServicePageLimit: 16, ProgramPageLimit: 16,
			VerifiedFakeLineage: true,
		})
	if err != nil || firstResult.Services != 1 || firstResult.Programs != 1 {
		t.Fatalf("first=%+v err=%v", firstResult, err)
	}
	programs, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 {
		t.Fatalf("programs=%+v err=%v", programs, err)
	}
	if programs[0].RevisionNumber != 1 || programs[0].Classification != catalogmodel.NewInstance || *programs[0].Material.Title != "最初の番組" {
		t.Fatalf("first current=%+v", programs[0])
	}
	instanceID := programs[0].InstanceID

	second := newHarness(t, "延長された番組", nil)
	if _, err := (catalogsync.Service{Provider: second.Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), catalogsync.Request{
			Backend: backend, CorrelationID: "sync-2", ServicePageLimit: 16, ProgramPageLimit: 16,
			VerifiedFakeLineage: true,
		}); err != nil {
		t.Fatal(err)
	}
	programs, err = store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 {
		t.Fatalf("programs=%+v err=%v", programs, err)
	}
	if programs[0].InstanceID != instanceID || programs[0].RevisionNumber != 2 || programs[0].Classification != catalogmodel.VerifiedSuccessor {
		t.Fatalf("second current=%+v", programs[0])
	}
	if *programs[0].Material.Title != "延長された番組" {
		t.Fatalf("title=%q", *programs[0].Material.Title)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	programs, err = restarted.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 || programs[0].RevisionNumber != 2 || programs[0].InstanceID != instanceID {
		t.Fatalf("restart programs=%+v err=%v", programs, err)
	}
}

func TestIdenticalReplayConvergesToSameRevision(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID := mustCatalogID(t, 43)
	backend := catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:replay"))}
	request := catalogsync.Request{
		Backend: backend, CorrelationID: "first", ServicePageLimit: 16, ProgramPageLimit: 16,
		VerifiedFakeLineage: true,
	}
	providerHarness := newHarness(t, "同一番組", nil)
	if _, err := (catalogsync.Service{Provider: providerHarness.Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	first, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	request.CorrelationID = "replay"
	providerHarness = newHarness(t, "同一番組", nil)
	if _, err := (catalogsync.Service{Provider: providerHarness.Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	second, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(second) != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if second[0].InstanceID != first[0].InstanceID || second[0].RevisionID != first[0].RevisionID ||
		second[0].RevisionNumber != 1 || second[0].Classification != catalogmodel.SameContent {
		t.Fatalf("同一観測が収束しませんでした: first=%+v second=%+v", first[0], second[0])
	}
}

func TestFailedGenerationDoesNotReplaceCompletedCatalog(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID, err := catalogmodel.NewIDFrom(zeroReader{})
	if err != nil {
		t.Fatal(err)
	}
	backend := catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:failure"))}
	initial := newHarness(t, "正本", nil)
	service := catalogsync.Service{Provider: initial.Catalog, Repository: store, Clock: clock}
	request := catalogsync.Request{Backend: backend, CorrelationID: "good", ServicePageLimit: 16, ProgramPageLimit: 16, VerifiedFakeLineage: true}
	if _, err := service.Sync(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	failing := newHarness(t, "未完了", func(config *fake.Config) {
		config.Faults = []fake.Fault{{Point: fake.FaultProgramBefore, Index: 0, Reason: provider.ReasonUnavailable, Diagnostic: "synthetic"}}
	})
	service.Provider = failing.Catalog
	request.CorrelationID = "failed"
	if _, err := service.Sync(context.Background(), request); err == nil {
		t.Fatal("fault付きsyncが成功しました")
	}
	programs, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 || *programs[0].Material.Title != "正本" {
		t.Fatalf("current=%+v err=%v", programs, err)
	}
}

func TestValidationRunsBeforeCompletionAndKeepsPreviousCatalog(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID := mustCatalogID(t, 88)
	backend := catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:validation"))}
	request := catalogsync.Request{
		Backend: backend, CorrelationID: "initial", ServicePageLimit: 16, ProgramPageLimit: 16,
		VerifiedFakeLineage: true,
	}
	service := catalogsync.Service{Provider: newHarness(t, "正本", nil).Catalog, Repository: store, Clock: clock}
	if _, err := service.Sync(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	want := errors.New("candidate rejected")
	request.CorrelationID = "candidate"
	service.Provider = newHarness(t, "公開しない番組", nil).Catalog
	called := 0
	_, err := service.SyncValidated(context.Background(), request, func(ctx context.Context, generationID catalogmodel.ID) error {
		called++
		services, readErr := store.ServicesForGeneration(ctx, backendID, generationID,
			catalogmodel.GenerationRunning, 16, catalogmodel.ID{})
		if readErr != nil || len(services) != 1 {
			t.Fatalf("candidate services=%+v err=%v", services, readErr)
		}
		return want
	})
	if !errors.Is(err, want) || called != 1 {
		t.Fatalf("validation calls=%d err=%v", called, err)
	}
	programs, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 || *programs[0].Material.Title != "正本" {
		t.Fatalf("current=%+v err=%v", programs, err)
	}
}

func TestUnverifiedEIDReuseRemainsAmbiguous(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID := mustCatalogID(t, 40)
	backend := catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:eid-reuse"))}
	request := catalogsync.Request{
		Backend: backend, CorrelationID: "original", ServicePageLimit: 16, ProgramPageLimit: 16,
		VerifiedFakeLineage: true,
	}
	if _, err := (catalogsync.Service{Provider: newHarness(t, "元の番組", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.CorrelationID = "reuse"
	request.VerifiedFakeLineage = false
	if _, err := (catalogsync.Service{Provider: newHarness(t, "別世代の番組", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	programs, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 || programs[0].Classification != catalogmodel.Ambiguous ||
		programs[0].RevisionNumber != 1 || *programs[0].Material.Title != "元の番組" {
		t.Fatalf("曖昧な変更で元revisionを維持できませんでした: programs=%+v err=%v", programs, err)
	}
	request.CorrelationID = "original-again"
	request.VerifiedFakeLineage = true
	if _, err := (catalogsync.Service{Provider: newHarness(t, "元の番組", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	programs, err = store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 || programs[0].RevisionNumber != 1 || programs[0].Classification != catalogmodel.SameContent {
		t.Fatalf("元revisionが維持されませんでした: programs=%+v err=%v", programs, err)
	}
}

func TestMirakurunCannotClaimVerifiedSuccessor(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID := mustCatalogID(t, 43)
	backend := catalogmodel.Backend{ID: backendID, Kind: "MIRAKURUN", IdentityHash: sha256.Sum256([]byte("mirakurun:test"))}
	request := catalogsync.Request{
		Backend: backend, CorrelationID: "original", ServicePageLimit: 16, ProgramPageLimit: 16,
		VerifiedFakeLineage: false,
	}
	if _, err := (catalogsync.Service{Provider: newHarness(t, "元の番組", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.CorrelationID = "changed"
	if _, err := (catalogsync.Service{Provider: newHarness(t, "別世代の番組", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	programs, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range programs {
		if program.Classification == catalogmodel.VerifiedSuccessor {
			t.Fatalf("実providerをVerifiedSuccessorへ昇格しました: %+v", program)
		}
	}
	request.CorrelationID = "invalid-lineage"
	request.VerifiedFakeLineage = true
	if _, err := (catalogsync.Service{Provider: newHarness(t, "不正", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err == nil {
		t.Fatal("実providerがFake専用lineageを受理しました")
	}
}

func TestMirakurunBoundedChangeCreatesSuccessor(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID := mustCatalogID(t, 44)
	backend := catalogmodel.Backend{ID: backendID, Kind: "MIRAKURUN", IdentityHash: sha256.Sum256([]byte("mirakurun:bounded"))}
	request := catalogsync.Request{Backend: backend, CorrelationID: "original", ServicePageLimit: 16, ProgramPageLimit: 16}
	eventID := uint16(10)
	withEvent := func(config *fake.Config) { config.ProgramPages[0].Items[0].EventID = &eventID }
	if _, err := (catalogsync.Service{Provider: newHarness(t, "元の番組", withEvent).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.CorrelationID = "bounded-change"
	changed := func(config *fake.Config) {
		withEvent(config)
		start := config.ProgramPages[0].Items[0].Start.Add(10 * time.Minute)
		duration := 35 * time.Minute
		config.ProgramPages[0].Items[0].Start = &start
		config.ProgramPages[0].Items[0].Duration = &duration
	}
	if _, err := (catalogsync.Service{Provider: newHarness(t, "変更後の番組", changed).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	programs, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(programs) != 1 || programs[0].RevisionNumber != 2 ||
		programs[0].Classification != catalogmodel.VerifiedSuccessor || *programs[0].Material.Title != "変更後の番組" {
		t.Fatalf("bounded successor=%+v err=%v", programs, err)
	}
	services, err := store.CurrentServices(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(services) != 1 || services[0].Validation == catalogmodel.ValidationInvalid {
		t.Fatalf("services=%+v err=%v", services, err)
	}
}

func TestServiceMoveCreatesDistinctInstance(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID := mustCatalogID(t, 41)
	backend := catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:service-move"))}
	request := catalogsync.Request{
		Backend: backend, CorrelationID: "before-move", ServicePageLimit: 16, ProgramPageLimit: 16,
		VerifiedFakeLineage: true,
	}
	if _, err := (catalogsync.Service{Provider: newHarness(t, "移動番組", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(before) != 1 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	moved := newHarness(t, "移動番組", func(config *fake.Config) {
		config.ServicePages[0].Items[0].Locator = "service:2"
		config.ServicePages[0].Items[0].TuningTarget.Opaque = "target:2"
		config.ProgramPages[0].Items[0].ServiceLocator = "service:2"
	})
	request.CorrelationID = "after-move"
	if _, err := (catalogsync.Service{Provider: moved.Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	after, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(after) != 1 {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	if after[0].InstanceID == before[0].InstanceID || after[0].ServiceLocator != "service:2" || after[0].Classification != catalogmodel.NewInstance {
		t.Fatalf("service moveが別instanceになりませんでした: before=%+v after=%+v", before[0], after[0])
	}
}

func TestUnknownTimeBecomesRevisionOnlyWithVerifiedFakeLineage(t *testing.T) {
	_, store := migratedStore(t)
	clock := &advancingClock{now: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}
	backendID := mustCatalogID(t, 42)
	backend := catalogmodel.Backend{ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:unknown-time"))}
	unknown := newHarness(t, "時刻未定", func(config *fake.Config) {
		config.ProgramPages[0].Items[0].Start = nil
		config.ProgramPages[0].Items[0].Duration = nil
	})
	request := catalogsync.Request{
		Backend: backend, CorrelationID: "unknown", ServicePageLimit: 16, ProgramPageLimit: 16,
		VerifiedFakeLineage: true,
	}
	if _, err := (catalogsync.Service{Provider: unknown.Catalog, Repository: store, Clock: clock}).Sync(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(before) != 1 || before[0].Material.StartUTCMS != nil || before[0].Material.DurationMS != nil {
		t.Fatalf("unknown=%+v err=%v", before, err)
	}
	request.CorrelationID = "known"
	if _, err := (catalogsync.Service{Provider: newHarness(t, "時刻未定", nil).Catalog, Repository: store, Clock: clock}).Sync(
		context.Background(), request); err != nil {
		t.Fatal(err)
	}
	after, err := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	if err != nil || len(after) != 1 || after[0].InstanceID != before[0].InstanceID || after[0].RevisionNumber != 2 ||
		after[0].Material.StartUTCMS == nil || after[0].Material.DurationMS == nil {
		t.Fatalf("known=%+v err=%v", after, err)
	}
}

func migratedStore(t *testing.T) (string, *sqliteadapter.Store) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteadapter.MigrateDatabase(context.Background(), root, 1785628800000); err != nil {
		t.Fatal(err)
	}
	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return root, store
}

func newHarness(t *testing.T, title string, mutate func(*fake.Config)) *fake.Harness {
	t.Helper()
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	duration := 30 * time.Minute
	config := fake.Config{
		ID: "catalog-sync", Seed: 1, WallTime: start, InitialNanos: 1,
		ServicePages: []providercatalog.ServicePage{{Items: []providercatalog.ServiceObservation{{
			Provenance: provider.Provenance{Backend: "fake", Revision: "1"}, Locator: "service:1",
			Broadcast: "GR", NetworkID: 1, ServiceID: 2, DisplayName: "サービス",
			TuningTarget: provider.TuningTarget{Opaque: "target:1"}, Validation: provider.ValidationValid,
		}}, End: true}},
		ProgramPages: []providercatalog.ProgramPage{{Items: []providercatalog.ProgramObservation{{
			Provenance: provider.Provenance{Backend: "fake", Revision: "1"}, ServiceLocator: "service:1",
			EventLocator: "event:10", Start: &start, Duration: &duration, Title: title,
			Description: "合成データ", Validation: provider.ValidationValid,
		}}, End: true}},
		Expected: fake.ExpectedRequests{ServiceLimit: 16, ProgramLimit: 16}, HistoryLimit: 64,
	}
	if mutate != nil {
		mutate(&config)
	}
	scenario, err := fake.NewScenario(config)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := fake.NewHarness(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

type zeroReader struct{}

func (zeroReader) Read(destination []byte) (int, error) {
	for index := range destination {
		destination[index] = 0
	}
	return len(destination), nil
}

func mustCatalogID(t *testing.T, marker byte) catalogmodel.ID {
	t.Helper()
	id, err := catalogmodel.NewIDFrom(markerReader(marker))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
