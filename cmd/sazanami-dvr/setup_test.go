package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

func TestSetupCreatesAndReusesChannelMapFromMirakurunURL(t *testing.T) {
	root := ownerOnlyRoot(t)
	startMS := time.Now().UTC().Add(time.Hour).UnixMilli()
	var servicesCalls atomic.Int32
	var streamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/version":
			writeCommandJSON(writer, `{"current":"test","latest":"test"}`)
		case "/api/services":
			servicesCalls.Add(1)
			writeCommandJSON(writer, `[`+
				`{"id":100003,"networkId":1,"serviceId":3,"name":"private station","type":1},`+
				`{"id":100004,"networkId":1,"serviceId":4,"name":"excluded radio","type":192}`+
				`]`)
		case "/api/programs":
			writeCommandJSON(writer, fmt.Sprintf(`[{"id":10000300005,"networkId":1,"serviceId":3,"eventId":5,"startAt":%d,"duration":1800000,"isFree":true,"name":"private program","description":""}]`, startMS))
		case "/api/services/100003/stream":
			streamCalls.Add(1)
			if request.URL.RawQuery != "decode=0" || request.Header.Get("Accept") != "video/MP2T" ||
				request.Header.Get("X-Mirakurun-Priority") != "0" {
				http.Error(writer, "bad request", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(setupPAT(2, 3))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	arguments := []string{"setup", "--mirakurun-url", server.URL, "--data-root", root}
	for runIndex, expectedState := range []string{"created", "unchanged"} {
		var output, diagnostic bytes.Buffer
		if code := runContext(context.Background(), arguments, &output, &diagnostic); code != 0 {
			t.Fatalf("run %d code=%d output=%q diagnostic=%q", runIndex, code, output.String(), diagnostic.String())
		}
		if output.String() != "setup result=completed services=1 channel_map="+expectedState+"\n" {
			t.Fatalf("run %d output=%q", runIndex, output.String())
		}
		for _, private := range []string{server.URL, root, "private station", "private program"} {
			if strings.Contains(output.String(), private) || strings.Contains(diagnostic.String(), private) {
				t.Fatalf("run %d leaked %q: output=%q diagnostic=%q", runIndex, private, output.String(), diagnostic.String())
			}
		}
	}
	if servicesCalls.Load() != 4 || streamCalls.Load() != 2 {
		t.Fatalf("services calls=%d stream calls=%d", servicesCalls.Load(), streamCalls.Load())
	}

	path := filepath.Join(root, "channels.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("channel map info=%v err=%v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Format   string `json:"format"`
		Services []struct {
			ProviderLocator   string `json:"provider_locator"`
			NetworkID         uint16 `json:"network_id"`
			ServiceID         uint16 `json:"service_id"`
			TransportStreamID uint16 `json:"transport_stream_id"`
			RemoteControlKey  uint8  `json:"remote_control_key_id"`
			EPGCapture        bool   `json:"epg_capture"`
			Search            bool   `json:"search"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Format != "sazanami-channel-map-v1" || len(document.Services) != 1 {
		t.Fatalf("document=%+v", document)
	}
	service := document.Services[0]
	if service.ProviderLocator != "100003" || service.NetworkID != 1 || service.ServiceID != 3 ||
		service.TransportStreamID != 2 || service.RemoteControlKey != 0 || !service.EPGCapture || !service.Search {
		t.Fatalf("service=%+v", service)
	}

	var output, diagnostic bytes.Buffer
	if code := runContext(context.Background(), []string{"ctrlcmd", "validate", "--data-root", root,
		"--channel-map", path}, &output, &diagnostic); code != 0 {
		t.Fatalf("validate code=%d output=%q diagnostic=%q", code, output.String(), diagnostic.String())
	}
}

func TestSetupRefusesOwnerLockBeforeNetwork(t *testing.T) {
	root := migratedRoot(t)
	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	var output, diagnostic bytes.Buffer
	code := runContext(context.Background(), []string{"setup", "--mirakurun-url", server.URL,
		"--data-root", root}, &output, &diagnostic)
	if code != 1 || !strings.Contains(diagnostic.String(), "database-owner-unavailable") {
		t.Fatalf("code=%d output=%q diagnostic=%q", code, output.String(), diagnostic.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls=%d", calls.Load())
	}
}

func TestSetupRejectsInvalidArgumentsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, arguments := range [][]string{
		{"setup"},
		{"setup", "--mirakurun-url", server.URL, "extra"},
		{"setup", "--mirakurun-url", server.URL, "--unknown"},
	} {
		var output, diagnostic bytes.Buffer
		if code := runContext(context.Background(), arguments, &output, &diagnostic); code != 2 {
			t.Fatalf("arguments=%v code=%d output=%q diagnostic=%q", arguments, code, output.String(), diagnostic.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls=%d", calls.Load())
	}
}

func TestSetupPreservesDifferentExistingChannelMap(t *testing.T) {
	root := ownerOnlyRoot(t)
	path := filepath.Join(root, "channels.json")
	original := []byte("operator-owned\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	server := setupServer(t)
	defer server.Close()
	var output, diagnostic bytes.Buffer
	code := runContext(context.Background(), []string{"setup", "--mirakurun-url", server.URL,
		"--data-root", root}, &output, &diagnostic)
	if code != 1 || !strings.Contains(diagnostic.String(), "channel-map-exists") {
		t.Fatalf("code=%d output=%q diagnostic=%q", code, output.String(), diagnostic.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("after=%q err=%v", after, err)
	}
}

func setupServer(t *testing.T) *httptest.Server {
	t.Helper()
	startMS := time.Now().UTC().Add(time.Hour).UnixMilli()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/version":
			writeCommandJSON(writer, `{"current":"test","latest":"test"}`)
		case "/api/services":
			writeCommandJSON(writer, `[{"id":100003,"networkId":1,"serviceId":3,"name":"test","type":1}]`)
		case "/api/programs":
			writeCommandJSON(writer, fmt.Sprintf(`[{"id":10000300005,"networkId":1,"serviceId":3,"eventId":5,"startAt":%d,"duration":1800000,"isFree":true,"name":"test","description":""}]`, startMS))
		case "/api/services/100003/stream":
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(setupPAT(2, 3))
		default:
			http.NotFound(writer, request)
		}
	}))
}

func setupPAT(transportStreamID, serviceID uint16) []byte {
	section := []byte{0x00, 0xb0, 0, byte(transportStreamID >> 8), byte(transportStreamID), 0xc1, 0, 0,
		byte(serviceID >> 8), byte(serviceID), 0xe1, 0x00}
	length := len(section) + 4 - 3
	section[1] = section[1]&0xf0 | byte(length>>8)
	section[2] = byte(length)
	crc := mpegts.CRC32(section)
	section = append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	packets, err := mpegts.PacketizeSection(0, 0, section)
	if err != nil {
		panic(err)
	}
	result := bytes.Join(packets, nil)
	for len(result) < 5*mpegts.PacketBytes {
		nullPacket := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
		nullPacket[0], nullPacket[1], nullPacket[2], nullPacket[3] = 0x47, 0x1f, 0xff, 0x10
		result = append(result, nullPacket...)
	}
	return result
}
