package sqlite

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestProgramMetadataPersistsCanonicallyAndSurvivesRestart(t *testing.T) {
	root, store := openMigratedStore(t)
	backendID := testID(t, 200)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("metadata")), ObservedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	video := catalogmodel.Video{StreamContent: 1, ComponentType: 0xb3}
	metadata := catalogmodel.ProgramMetadata{
		Extended: []catalogmodel.ExtendedItem{{Heading: "B", Body: "二"}, {Heading: "A", Body: "一"}},
		Genres:   []catalogmodel.Genre{{Level1: 5, Level2: 2}, {Level1: 1, Level2: 3}},
		Video:    &video,
		Audios:   []catalogmodel.Audio{{ComponentType: 3, ComponentTag: 1, Main: true, SamplingRate: 48_000, Languages: []string{"jpn"}}},
	}
	storeMetadataGeneration(t, store, backendID, testID(t, 201), 10, "1", metadata)

	var stored []byte
	if err := store.reader.QueryRow(`SELECT metadata FROM program_revisions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	canonical, err := catalogmodel.EncodeMetadataV1(metadata)
	if err != nil || !bytesEqual(stored, canonical) {
		t.Fatalf("保存値がcanonicalではありません: bytes=%d err=%v", len(stored), err)
	}
	items, err := store.CurrentPrograms(context.Background(), backendID, 256, catalogmodel.ID{})
	if err != nil || len(items) != 1 || items[0].Material.Metadata.Video == nil ||
		items[0].Material.Metadata.Extended[0].Heading != "A" {
		t.Fatalf("items=%+v err=%v", items, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err = reopened.CurrentPrograms(context.Background(), backendID, 256, catalogmodel.ID{})
	if err != nil || len(items) != 1 || len(items[0].Material.Metadata.Genres) != 2 {
		t.Fatalf("再起動後=%+v err=%v", items, err)
	}

	metadata.Extended[0], metadata.Extended[1] = metadata.Extended[1], metadata.Extended[0]
	metadata.Genres = append(metadata.Genres, metadata.Genres[0])
	storeMetadataGeneration(t, reopened, backendID, testID(t, 202), 20, "1", metadata)
	var revisions int
	if err := reopened.reader.QueryRow(`SELECT count(*) FROM program_revisions`).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatalf("同じ意味の詳細でrevisionが増えました: count=%d err=%v", revisions, err)
	}
}

func TestProgramMetadataNullInsertOnlyAndCorruption(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID := testID(t, 203)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("metadata-null")), ObservedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	storeMetadataGeneration(t, store, backendID, testID(t, 204), 10, "1", catalogmodel.ProgramMetadata{})
	var metadata []byte
	if err := store.reader.QueryRow(`SELECT metadata FROM program_revisions`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata != nil {
		t.Fatalf("空metadataがNULLではありません: %x", metadata)
	}
	var nullCount int
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_revisions WHERE metadata IS NULL`).Scan(&nullCount); err != nil || nullCount != 1 {
		t.Fatalf("NULL count=%d err=%v", nullCount, err)
	}
	if _, err := store.writer.Exec(`UPDATE program_revisions SET metadata=x'00'`); err == nil {
		t.Fatal("insert-only revisionのmetadataが更新されました")
	}
	if _, err := store.writer.Exec(`DELETE FROM program_revisions`); err == nil {
		t.Fatal("insert-only revisionが削除されました")
	}

	// 読取り境界が破損を空情報へ丸めないことを、隔離したテストDBで確認する。
	if _, err := store.writer.Exec(`DROP TRIGGER program_revisions_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`UPDATE program_revisions SET metadata=x'00'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CurrentPrograms(context.Background(), backendID, 256, catalogmodel.ID{}); err == nil {
		t.Fatal("破損metadataが空情報として読まれました")
	}
}

func storeMetadataGeneration(t *testing.T, store *Store, backendID, syncID catalogmodel.ID, started int64, eventLocator string, metadata catalogmodel.ProgramMetadata) {
	t.Helper()
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: backendID, StartedAtMS: started, CorrelationID: "metadata-test",
	}); err != nil {
		t.Fatal(err)
	}
	network, transport, service := int64(1), int64(2), int64(3)
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: "service", NetworkID: &network, TransportID: &transport, ServiceID: &service,
		DisplayName: "局", Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	start, duration, eventID := int64(1_000), int64(60_000), int64(1)
	title := "番組"
	if err := store.StorePrograms(context.Background(), syncID, true, []catalogmodel.ProgramObservation{{
		ServiceLocator: "service", EventLocator: eventLocator, RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &start, DurationMS: &duration, Title: &title,
			FreeAccess: catalogmodel.FreeYes, Validation: catalogmodel.ValidationValid, Metadata: metadata},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, started+1, 1, 1); err != nil {
		t.Fatal(err)
	}
}
