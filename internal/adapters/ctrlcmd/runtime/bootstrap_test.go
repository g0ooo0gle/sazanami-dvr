package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestPublishGeneratedChannelMapCreatesDeterministicValidatedFile(t *testing.T) {
	root := t.TempDir()
	backendID := testID(200)
	catalog := &fakeCatalog{
		backends: []catalogmodel.CurrentBackend{{ID: backendID, Kind: "MIRAKURUN"}},
		services: []catalogmodel.CurrentService{
			observedService(1, "1004", 1, 4, "1"),
			observedService(2, "1003", 1, 3, "1"),
		},
	}
	services := []GeneratedService{
		{ProviderLocator: "1004", NetworkID: 1, ServiceID: 4, TransportStreamID: 8, RemoteControlKey: 4},
		{ProviderLocator: "1003", NetworkID: 1, ServiceID: 3, TransportStreamID: 2, RemoteControlKey: 3},
	}

	result, err := PublishGeneratedChannelMap(context.Background(), root, backendID, services, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceCount != 2 || result.Status != PublishStatusCreated {
		t.Fatalf("result=%+v", result)
	}
	path := filepath.Join(root, "channels.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"format\": \"sazanami-channel-map-v1\",\n" +
		"  \"backend_id\": \"" + backendID.String() + "\",\n" +
		"  \"services\": [\n" +
		"    {\n" +
		"      \"provider_locator\": \"1003\",\n" +
		"      \"network_id\": 1,\n" +
		"      \"service_id\": 3,\n" +
		"      \"transport_stream_id\": 2,\n" +
		"      \"provider_name\": \"\",\n" +
		"      \"network_name\": \"\",\n" +
		"      \"transport_stream_name\": \"\",\n" +
		"      \"remote_control_key_id\": 3,\n" +
		"      \"partial_reception\": false,\n" +
		"      \"epg_capture\": true,\n" +
		"      \"search\": true\n" +
		"    },\n" +
		"    {\n" +
		"      \"provider_locator\": \"1004\",\n" +
		"      \"network_id\": 1,\n" +
		"      \"service_id\": 4,\n" +
		"      \"transport_stream_id\": 8,\n" +
		"      \"provider_name\": \"\",\n" +
		"      \"network_name\": \"\",\n" +
		"      \"transport_stream_name\": \"\",\n" +
		"      \"remote_control_key_id\": 4,\n" +
		"      \"partial_reception\": false,\n" +
		"      \"epg_capture\": true,\n" +
		"      \"search\": true\n" +
		"    }\n" +
		"  ]\n" +
		"}\n"
	if string(data) != want {
		t.Fatalf("map=%q, want=%q", data, want)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if _, err := BuildSnapshot(context.Background(), root, path, catalog); err != nil {
		t.Fatalf("generated map does not load: %v", err)
	}

	result, err = PublishGeneratedChannelMap(context.Background(), root, backendID, services, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceCount != 2 || result.Status != PublishStatusUnchanged {
		t.Fatalf("second result=%+v", result)
	}
	leftovers, err := filepath.Glob(filepath.Join(root, ".channels.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func TestPublishGeneratedChannelMapRejectsInvalidOrUnverifiedCandidate(t *testing.T) {
	root := t.TempDir()
	backendID := testID(201)
	catalog := &fakeCatalog{
		backends: []catalogmodel.CurrentBackend{{ID: backendID, Kind: "MIRAKURUN"}},
		services: []catalogmodel.CurrentService{observedService(1, "1003", 1, 3, "1")},
	}
	cases := map[string][]GeneratedService{
		"empty":                nil,
		"noncanonical locator": {{ProviderLocator: "01003", NetworkID: 1, ServiceID: 3, TransportStreamID: 2}},
		"duplicate locator": {
			{ProviderLocator: "1003", NetworkID: 1, ServiceID: 3, TransportStreamID: 2},
			{ProviderLocator: "1003", NetworkID: 1, ServiceID: 4, TransportStreamID: 2},
		},
		"orphan": {{ProviderLocator: "1004", NetworkID: 1, ServiceID: 4, TransportStreamID: 2}},
	}
	for name, services := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PublishGeneratedChannelMap(context.Background(), root, backendID, services, catalog)
			if err == nil {
				t.Fatal("expected failure")
			}
			if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "1004") {
				t.Fatalf("private value leaked: %v", err)
			}
			if _, statErr := os.Lstat(filepath.Join(root, "channels.json")); !os.IsNotExist(statErr) {
				t.Fatalf("final map changed: %v", statErr)
			}
		})
	}
}

func TestPublishGeneratedChannelMapPreservesExistingObjects(t *testing.T) {
	backendID := testID(202)
	services := []GeneratedService{{ProviderLocator: "1003", NetworkID: 1, ServiceID: 3, TransportStreamID: 2}}
	newCatalog := func() *fakeCatalog {
		return &fakeCatalog{
			backends: []catalogmodel.CurrentBackend{{ID: backendID, Kind: "MIRAKURUN"}},
			services: []catalogmodel.CurrentService{observedService(1, "1003", 1, 3, "1")},
		}
	}

	t.Run("different regular file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "channels.json")
		original := []byte("operator file\n")
		if err := os.WriteFile(path, original, 0o640); err != nil {
			t.Fatal(err)
		}
		_, err := PublishGeneratedChannelMap(context.Background(), root, backendID, services, newCatalog())
		requireReason(t, err, "channel-map-exists")
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != string(original) {
			t.Fatalf("existing=%q err=%v", got, readErr)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "operator.json")
		if err := os.WriteFile(target, []byte("operator\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "channels.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		_, err := PublishGeneratedChannelMap(context.Background(), root, backendID, services, newCatalog())
		requireReason(t, err, "channel-map-not-regular")
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink changed: info=%v err=%v", info, statErr)
		}
	})

	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "channels.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := PublishGeneratedChannelMap(context.Background(), root, backendID, services, newCatalog())
		requireReason(t, err, "channel-map-not-regular")
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("directory changed: info=%v err=%v", info, statErr)
		}
	})
}

func TestPublishGeneratedChannelMapCancellationCleansCandidate(t *testing.T) {
	root := t.TempDir()
	backendID := testID(203)
	catalog := &fakeCatalog{
		backends: []catalogmodel.CurrentBackend{{ID: backendID, Kind: "MIRAKURUN"}},
		services: []catalogmodel.CurrentService{observedService(1, "1003", 1, 3, "1")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := PublishGeneratedChannelMap(ctx, root, backendID,
		[]GeneratedService{{ProviderLocator: "1003", NetworkID: 1, ServiceID: 3, TransportStreamID: 2}}, catalog)
	requireReason(t, err, "channel-context-ended")
	leftovers, globErr := filepath.Glob(filepath.Join(root, ".channels.json.tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func TestPublishGeneratedChannelMapConcurrentPublishDoesNotClobber(t *testing.T) {
	root := t.TempDir()
	backendID := testID(204)
	services := []GeneratedService{{ProviderLocator: "1003", NetworkID: 1, ServiceID: 3, TransportStreamID: 2}}
	newCatalog := func() *fakeCatalog {
		return &fakeCatalog{
			backends: []catalogmodel.CurrentBackend{{ID: backendID, Kind: "MIRAKURUN"}},
			services: []catalogmodel.CurrentService{observedService(1, "1003", 1, 3, "1")},
		}
	}
	results := make([]PublishResult, 2)
	errors := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errors[index] = PublishGeneratedChannelMap(context.Background(), root, backendID, services, newCatalog())
		}(index)
	}
	wait.Wait()
	for index, err := range errors {
		if err != nil {
			t.Fatalf("publisher %d: %v", index, err)
		}
		if results[index].Status != PublishStatusCreated && results[index].Status != PublishStatusUnchanged {
			t.Fatalf("publisher %d result=%+v", index, results[index])
		}
	}
	if _, err := BuildSnapshot(context.Background(), root, filepath.Join(root, "channels.json"), newCatalog()); err != nil {
		t.Fatalf("final map invalid: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(root, ".channels.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}
