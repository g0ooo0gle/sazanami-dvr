package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
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

func TestRecordingCatalogRefreshPublishesOnlyValidatedGeneration(t *testing.T) {
	const (
		networkID  = 1
		serviceID  = 3
		eventID    = 4
		serviceKey = 100003
		programKey = 10000300004
	)
	var state struct {
		sync.Mutex
		service string
		title   string
		fail    bool
		paths   []string
	}
	state.service, state.title = "更新前の局", "更新前の番組"
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		state.Lock()
		defer state.Unlock()
		state.paths = append(state.paths, request.URL.Path)
		if state.fail {
			http.Error(writer, "private", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/version":
			fmt.Fprint(writer, `{"current":"unknown-compatible","latest":"unknown-compatible"}`)
		case "/api/services":
			fmt.Fprintf(writer, `[{"type":1,"name":%q,"serviceId":%d,"id":%d,"networkId":%d}]`,
				state.service, serviceID, serviceKey, networkID)
		case "/api/programs":
			fmt.Fprintf(writer, `[{"id":%d,"networkId":%d,"serviceId":%d,"eventId":%d,"startAt":1786237200000,"duration":1800000,"isFree":true,"name":%q,"description":""}]`,
				programKey, networkID, serviceID, eventID, state.title)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer providerServer.Close()

	root := migratedRoot(t)
	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider, err := mirakurunadapter.New(providerServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.CloseIdleConnections()
	identityHash := provider.IdentityHash()
	backendID := stableBackendID(identityHash)
	sourceRef := "mirakurun-http-json-v1"
	initialCorrelation, _ := catalogmodel.NewID()
	request := catalogsync.Request{
		Backend:       catalogmodel.Backend{ID: backendID, Kind: "MIRAKURUN", IdentityHash: identityHash, SourceRef: &sourceRef},
		CorrelationID: initialCorrelation.String(), ServicePageLimit: 256, ProgramPageLimit: 256,
	}
	if _, err := (catalogsync.Service{Provider: provider, Repository: store, Clock: wallClock{}}).Sync(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	channelMap := filepath.Join(root, "channels.json")
	document := fmt.Sprintf(`{"format":"sazanami-channel-map-v1","backend_id":%q,"services":[{"provider_locator":"100003","network_id":1,"service_id":3,"transport_stream_id":2,"provider_name":"","network_name":"テスト","transport_stream_name":"テスト","remote_control_key_id":1,"partial_reception":false,"epg_capture":true,"search":true}]}`,
		backendID.String())
	if err := os.WriteFile(channelMap, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	initial, err := ctrlcmdruntime.BuildSnapshot(context.Background(), root, channelMap, store)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := ctrlcmdruntime.NewSnapshotHolder(initial)
	if err != nil {
		t.Fatal(err)
	}

	state.Lock()
	state.service = "更新後の局"
	state.Unlock()
	followCalls := 0
	automaticCalls := 0
	observedAutomaticError := false
	operation := &recordingCatalogRefresh{
		dataRoot: root, channelMap: channelMap, provider: provider, store: store, holder: holder, clock: wallClock{},
		follow: func(context.Context) (recordingapp.FollowResult, error) {
			followCalls++
			return recordingapp.FollowResult{}, nil
		},
		automatic: func(context.Context) (autoreservationapp.Result, error) {
			automaticCalls++
			return autoreservationapp.Result{}, errors.New("private automatic failure")
		},
		observeAutomatic: func(_ autoreservationapp.Result, err error, _ time.Duration) {
			observedAutomaticError = err != nil
		},
	}
	result, reason, err := operation.sync(context.Background())
	if err != nil || reason != "" || result.Services != 1 || result.Programs != 1 || holder.Load() == initial ||
		followCalls != 1 || automaticCalls != 1 || !observedAutomaticError {
		t.Fatalf("result=%+v reason=%q switched=%v follows=%d automatic=%d observed=%v err=%v",
			result, reason, holder.Load() != initial, followCalls, automaticCalls, observedAutomaticError, err)
	}
	operation.automatic = nil
	value, err := holder.Load().Current(context.Background())
	if err != nil || len(value.Services) != 1 || value.Services[0].ServiceName != "更新後の局" {
		t.Fatalf("snapshot=%+v err=%v", value, err)
	}
	programs, err := holder.Load().CurrentProgramsForService(context.Background(), "100003", 16, "")
	if err != nil || len(programs) != 1 || programs[0].Material.Title == nil || *programs[0].Material.Title != "更新前の番組" {
		t.Fatalf("programs=%+v err=%v", programs, err)
	}
	accepted := holder.Load()
	badMap := strings.Replace(document, `"provider_locator":"100003"`, `"provider_locator":"999"`, 1)
	if err := os.WriteFile(channelMap, []byte(badMap), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := operation.sync(context.Background()); err == nil || reason != "catalog-refresh-channel-mismatch" || holder.Load() != accepted {
		t.Fatalf("mismatch reason=%q kept=%v err=%v", reason, holder.Load() == accepted, err)
	}
	if followCalls != 1 {
		t.Fatalf("不正な世代で追従しました: calls=%d", followCalls)
	}
	state.Lock()
	state.fail = true
	state.Unlock()
	if _, reason, err := operation.sync(context.Background()); err == nil || reason != "catalog-refresh-provider-failed" || holder.Load() != accepted {
		t.Fatalf("provider reason=%q kept=%v err=%v", reason, holder.Load() == accepted, err)
	}
	if followCalls != 1 {
		t.Fatalf("取得失敗後に追従しました: calls=%d", followCalls)
	}
	state.Lock()
	state.fail = false
	state.Unlock()
	if err := os.WriteFile(channelMap, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeGoroutines := runtime.NumGoroutine()
	beforeFDs, measureFDs := openFileDescriptorCount()
	previous := holder.Load()
	for cycle := range 100 {
		if _, reason, err := operation.sync(context.Background()); err != nil || reason != "" || holder.Load() == previous {
			t.Fatalf("cycle=%d reason=%q switched=%v err=%v", cycle+1, reason, holder.Load() != previous, err)
		}
		previous = holder.Load()
	}
	if followCalls != 101 {
		t.Fatalf("follow calls=%d", followCalls)
	}
	provider.CloseIdleConnections()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > beforeGoroutines+8 {
		t.Fatalf("goroutines before=%d after=%d", beforeGoroutines, after)
	}
	if after, supported := openFileDescriptorCount(); measureFDs && supported && after > beforeFDs+4 {
		t.Fatalf("file descriptors before=%d after=%d", beforeFDs, after)
	}
	state.Lock()
	paths := append([]string(nil), state.paths...)
	state.Unlock()
	for _, path := range paths {
		if path != "/api/version" && path != "/api/services" && path != "/api/programs" {
			t.Fatalf("unexpected provider path=%q", path)
		}
	}
}

func openFileDescriptorCount() (int, bool) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

func TestCatalogRefreshOutputIsBoundedAndRedacted(t *testing.T) {
	var output, diagnostic bytes.Buffer
	observe := observeCatalogRefresh(&output, &diagnostic)
	observe(catalogrefresh.Event{Completed: true, Services: 2, Programs: 3, DurationMS: 4})
	observe(catalogrefresh.Event{Reason: "catalog-refresh-provider-failed", DurationMS: 5})
	wantOutput := "catalog_refresh result=completed services=2 programs=3 duration_ms=4\n"
	wantDiagnostic := "catalog_refresh result=failed reason=catalog-refresh-provider-failed duration_ms=5\n"
	if output.String() != wantOutput || diagnostic.String() != wantDiagnostic {
		t.Fatalf("output=%q diagnostic=%q", output.String(), diagnostic.String())
	}
	for _, private := range []string{"http://", "/home/", "番組", "private"} {
		if strings.Contains(output.String(), private) || strings.Contains(diagnostic.String(), private) {
			t.Fatalf("private value=%q", private)
		}
	}
}

func TestAutomaticReservationOutputIsBoundedAndRedacted(t *testing.T) {
	completed := "automatic_reservation result=completed rules=2 programs=3 matched=4 created=5 duplicates=6 recorded_title_matches=7 unavailable_rules=8 limit_reached=true duration_ms=8\n"
	for _, test := range []struct {
		name           string
		result         autoreservationapp.Result
		err            error
		wantOutput     string
		wantDiagnostic string
	}{
		{name: "zero", result: autoreservationapp.Result{
			Rules: 2, Programs: 3, Matched: 4, Created: 5, Duplicates: 6, RecordedTitleMatches: 7,
			UnavailableRules: 8, LimitReached: true,
		}, wantOutput: completed},
		{name: "one", result: autoreservationapp.Result{
			Rules: 2, Programs: 3, Matched: 4, Created: 5, Duplicates: 6, RecordedTitleMatches: 7,
			UnavailableRules: 8, ForcedTunerUnavailableRules: 1, LimitReached: true,
		}, wantOutput: completed + "automatic_reservation_unavailable reason=forced-tuner-not-supported-by-provider rules=1\n"},
		{name: "multiple", result: autoreservationapp.Result{
			Rules: 2, Programs: 3, Matched: 4, Created: 5, Duplicates: 6, RecordedTitleMatches: 7,
			UnavailableRules: 8, ForcedTunerUnavailableRules: 3, LimitReached: true,
		}, wantOutput: completed + "automatic_reservation_unavailable reason=forced-tuner-not-supported-by-provider rules=3\n"},
		{name: "one-seg", result: autoreservationapp.Result{
			Rules: 2, Programs: 3, Matched: 4, Created: 5, Duplicates: 6, RecordedTitleMatches: 7,
			UnavailableRules: 8, OneSegUnavailableRules: 2, OneSegUnresolvedPrograms: 3, LimitReached: true,
		}, wantOutput: completed +
			"automatic_reservation_unavailable reason=one-seg-profile-unsupported rules=2\n" +
			"automatic_reservation_unavailable reason=one-seg-service-unresolved programs=3\n"},
		{name: "failure", result: autoreservationapp.Result{ForcedTunerUnavailableRules: 3},
			err:            errors.New("private program and path"),
			wantDiagnostic: "automatic_reservation result=failed reason=evaluation-failed duration_ms=8\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output, diagnostic bytes.Buffer
			observeAutomaticReservation(&output, &diagnostic)(test.result, test.err, 8*time.Millisecond)
			if output.String() != test.wantOutput || diagnostic.String() != test.wantDiagnostic {
				t.Fatalf("output=%q diagnostic=%q", output.String(), diagnostic.String())
			}
			for _, private := range []string{"http://", "/home/", "番組", "private"} {
				if strings.Contains(output.String(), private) || strings.Contains(diagnostic.String(), private) {
					t.Fatalf("private value=%q", private)
				}
			}
		})
	}
}
