package mirakurun

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

func TestObserveBootstrapServicesDecodesRequiredFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/services" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("request=%s %s accept=%q", request.Method, request.URL.Path, request.Header.Get("Accept"))
		}
		writeJSON(writer, fmt.Sprintf(`[
			{"id":%d,"networkId":1,"serviceId":2,"name":"地上波","type":1,"remoteControlKeyId":7},
			{"id":%d,"networkId":1,"serviceId":3,"name":"衛星","type":2,"remoteControlKeyId":null},
			{"id":%d,"networkId":1,"serviceId":4,"name":"欠落","type":161}
		]`, serviceProviderID(1, 2), serviceProviderID(1, 3), serviceProviderID(1, 4)))
	}))
	defer server.Close()
	services, err := mustAdapter(t, server.URL).ObserveBootstrapServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 3 {
		t.Fatalf("services=%+v", services)
	}
	if services[0].Locator != "100002" || services[0].NetworkID != 1 || services[0].ServiceID != 2 ||
		services[0].Name != "地上波" || services[0].ServiceType != 1 || services[0].RemoteControlKey != 7 {
		t.Fatalf("first=%+v", services[0])
	}
	if services[1].RemoteControlKey != 0 || services[2].RemoteControlKey != 0 {
		t.Fatalf("nullable remocon=%+v", services)
	}
}

func TestObserveBootstrapServicesRejectsInvalidAndDuplicateEntries(t *testing.T) {
	validID := serviceProviderID(1, 2)
	tests := []struct {
		name   string
		body   string
		reason provider.Reason
	}{
		{name: "zero id", body: `[{"id":0,"networkId":0,"serviceId":0,"name":"x","type":1}]`, reason: provider.ReasonMalformed},
		{name: "empty name", body: fmt.Sprintf(`[{"id":%d,"networkId":1,"serviceId":2,"name":"","type":1}]`, validID), reason: provider.ReasonMalformed},
		{name: "negative remocon", body: fmt.Sprintf(`[{"id":%d,"networkId":1,"serviceId":2,"name":"x","type":1,"remoteControlKeyId":-1}]`, validID), reason: provider.ReasonMalformed},
		{name: "remocon overflow", body: fmt.Sprintf(`[{"id":%d,"networkId":1,"serviceId":2,"name":"x","type":1,"remoteControlKeyId":256}]`, validID), reason: provider.ReasonOverLimit},
		{name: "duplicate locator", body: fmt.Sprintf(`[{"id":%d,"networkId":1,"serviceId":2,"name":"x","type":1},{"id":%d,"networkId":1,"serviceId":2,"name":"y","type":2}]`, validID, validID), reason: provider.ReasonMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, test.body) }))
			defer server.Close()
			_, err := mustAdapter(t, server.URL).ObserveBootstrapServices(context.Background())
			if !provider.IsReason(err, test.reason) {
				t.Fatalf("error=%v want=%s", err, test.reason)
			}
		})
	}
}

func TestObserveBootstrapServicesRejectsOneOverOperationLimit(t *testing.T) {
	var body strings.Builder
	body.WriteByte('[')
	for index := 1; index <= provider.MaxServiceOperation+1; index++ {
		if index != 1 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"id":%d,"networkId":1,"serviceId":%d,"name":"service","type":1}`, serviceProviderID(1, uint16(index)), index)
	}
	body.WriteByte(']')
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, body.String()) }))
	defer server.Close()
	_, err := mustAdapter(t, server.URL).ObserveBootstrapServices(context.Background())
	if !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("error=%v", err)
	}
}

func TestProbeTransportStreamIDRequestsRawTSAndReleasesSlot(t *testing.T) {
	pat := probeTransportStream(t, 0x1234, 2)
	requestDone := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/services/100002/stream" || request.URL.RawQuery != "decode=0" ||
			request.Header.Get("Accept") != "video/MP2T" || request.Header.Get("X-Mirakurun-Priority") != "0" {
			t.Errorf("request=%s %s?%s headers=%v", request.Method, request.URL.Path, request.URL.RawQuery, request.Header)
		}
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(pat)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(requestDone)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamID(context.Background(), "100002", 2)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("probe成功後もbodyが閉じられていません")
	}
	if calls.Load() != 1 || adapter.active != 0 {
		t.Fatalf("calls=%d active=%d", calls.Load(), adapter.active)
	}
	got, err = adapter.ProbeTransportStreamID(context.Background(), "100002", 2)
	if err != nil || got != 0x1234 || calls.Load() != 2 || adapter.active != 0 {
		t.Fatalf("second tsid=%x calls=%d active=%d err=%v", got, calls.Load(), adapter.active, err)
	}
}

func TestProbeTransportStreamIDRejectsResponseAndReleasesSlot(t *testing.T) {
	pat := probeTransportStream(t, 0x1234, 2)
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(pat)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/services/301/stream":
			http.Redirect(writer, request, target.URL, http.StatusFound)
		case "/api/services/302/stream":
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.Header().Set("Content-Encoding", "gzip")
			writer.WriteHeader(http.StatusOK)
		case "/api/services/303/stream":
			http.Error(writer, "private", http.StatusNotFound)
		case "/api/services/304/stream":
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.WriteHeader(http.StatusOK)
		case "/api/services/305/stream":
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(pat)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	for _, test := range []struct {
		id     string
		reason provider.Reason
	}{
		{id: "301", reason: provider.ReasonRejected},
		{id: "302", reason: provider.ReasonRejected},
		{id: "303", reason: provider.ReasonNotFound},
		{id: "304", reason: provider.ReasonMalformed},
	} {
		t.Run(test.id, func(t *testing.T) {
			_, probeErr := adapter.ProbeTransportStreamID(context.Background(), test.id, 2)
			if !provider.IsReason(probeErr, test.reason) || adapter.active != 0 {
				t.Fatalf("error=%v active=%d", probeErr, adapter.active)
			}
		})
	}
	if targetCalls.Load() != 0 {
		t.Fatalf("redirect target calls=%d", targetCalls.Load())
	}
	if _, err := adapter.ProbeTransportStreamID(context.Background(), "305", 2); err != nil || adapter.active != 0 {
		t.Fatalf("recovery err=%v active=%d", err, adapter.active)
	}
}

func TestProbeTransportStreamIDValidatesPATAndBoundedRead(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		reason provider.Reason
	}{
		{name: "sid mismatch", body: probeTransportStream(t, 0x1234, 3), reason: provider.ReasonMalformed},
		{name: "multiple programs", body: probeTransportStreamWithPrograms(t, 0x1234, 2, 3), reason: provider.ReasonMalformed},
		{name: "early eof", body: bytes.Repeat(probeNullPacket(), 5), reason: provider.ReasonEarlyEOF},
		{name: "one mib", body: bytes.Repeat(probeNullPacket(), (1<<20)/mpegts.PacketBytes+1)[:1<<20], reason: provider.ReasonOverLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "video/MP2T")
				_, _ = writer.Write(test.body)
			}))
			defer server.Close()
			adapter, err := NewStream(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.CloseIdleConnections()
			_, err = adapter.ProbeTransportStreamID(context.Background(), "100002", 2)
			if !provider.IsReason(err, test.reason) || adapter.active != 0 {
				t.Fatalf("error=%v want=%s active=%d", err, test.reason, adapter.active)
			}
		})
	}
}

func TestProbeTransportStreamIDHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.ProbeTransportStreamID(ctx, "100002", 2)
	if !provider.IsReason(err, provider.ReasonCancelled) || adapter.active != 0 {
		t.Fatalf("error=%v active=%d", err, adapter.active)
	}
}

func probeTransportStream(t *testing.T, transportID, serviceID uint16) []byte {
	return probeTransportStreamWithPrograms(t, transportID, serviceID)
}

func probeTransportStreamWithPrograms(t *testing.T, transportID uint16, programs ...uint16) []byte {
	t.Helper()
	section := []byte{0x00, 0xb0, 0x00, byte(transportID >> 8), byte(transportID), 0xc1, 0x00, 0x00}
	for index, serviceID := range programs {
		pid := uint16(0x100 + index)
		section = append(section, byte(serviceID>>8), byte(serviceID), 0xe0|byte(pid>>8), byte(pid))
	}
	sectionLength := len(section) + 4 - 3
	section[1] = 0xb0 | byte(sectionLength>>8)
	section[2] = byte(sectionLength)
	crc := mpegts.CRC32(section)
	section = append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	packets, err := mpegts.PacketizeSection(0, 0, section)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]byte, 0, 5*mpegts.PacketBytes)
	for _, packet := range packets {
		result = append(result, packet...)
	}
	for len(result) < 5*mpegts.PacketBytes {
		result = append(result, probeNullPacket()...)
	}
	return result
}

func probeNullPacket() []byte {
	packet := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	packet[0], packet[1], packet[2], packet[3] = 0x47, 0x1f, 0xff, 0x10
	return packet
}
