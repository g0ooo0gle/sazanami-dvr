package mirakurun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

func TestStreamRequestAndChunkedRead(t *testing.T) {
	requestEnded := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/services/1003/stream" ||
			request.URL.RawQuery != "decode=1" || request.Header.Get("X-Mirakurun-Priority") != "0" ||
			request.Header.Get("Accept") != "video/MP2T" {
			t.Errorf("request=%s %s?%s headers=%v", request.Method, request.URL.Path, request.URL.RawQuery, request.Header)
		}
		writer.Header().Set("Content-Type", "video/MP2T; charset=binary")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(requestEnded)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := adapter.client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.ForceAttemptHTTP2 || len(transport.TLSNextProto) != 0 {
		t.Fatalf("unsafe transport=%+v", transport)
	}
	lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, provider.MaxStreamChunk)
	read, terminal, err := lease.Read(context.Background(), buffer)
	if err != nil || read != 188 || terminal.Done || !bytes.Equal(buffer[:read], bytes.Repeat([]byte{0x47}, 188)) {
		t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
	}
	if err := lease.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestEnded:
	case <-time.After(time.Second):
		t.Fatal("request bodyがcancel後も開いたままです")
	}
}

func TestProbeTransportStreamIDWithSDTFallbackUsesChannelStreamAfterPATNotFound(t *testing.T) {
	var serviceCalls, channelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/services/100002/stream":
			serviceCalls.Add(1)
			if request.URL.RawQuery != "decode=0" {
				t.Errorf("service query=%q", request.URL.RawQuery)
			}
			writer.Header().Set("Content-Type", "video/MP2T")
		case "/api/channels/GR/13/stream":
			channelCalls.Add(1)
			if request.URL.RawQuery != "decode=0" || request.Header.Get("Accept") != "video/MP2T" ||
				request.Header.Get("X-Mirakurun-Priority") != "0" {
				t.Errorf("channel request=%s?%s headers=%v", request.Method, request.URL.RawQuery, request.Header)
			}
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeSDTTransportStream(t, 0x1234, 1, 2))
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
	got, err := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), BootstrapService{
		Locator: "100002", NetworkID: 1, ServiceID: 2,
		Channel: &BootstrapChannel{Type: "GR", Channel: "13"},
	})
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
	if serviceCalls.Load() != 1 || channelCalls.Load() != 1 || adapter.active != 0 {
		t.Fatalf("serviceCalls=%d channelCalls=%d active=%d", serviceCalls.Load(), channelCalls.Load(), adapter.active)
	}
}

func TestProbeTransportStreamIDWithSDTFallbackChecksSharedChannelPerService(t *testing.T) {
	var serviceCalls, channelCalls atomic.Int32
	body := probeSDTStream(t, probeSDTSection(t, 0x42, 0x1234, 1, 2, true, 0, 0, 2, 3))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/services/100002/stream", "/api/services/100003/stream":
			serviceCalls.Add(1)
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeNullPacket())
		case "/api/channels/GR/13/stream":
			channelCalls.Add(1)
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(body)
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
	for _, service := range []BootstrapService{
		{Locator: "100002", NetworkID: 1, ServiceID: 2, Channel: &BootstrapChannel{Type: "GR", Channel: "13"}},
		{Locator: "100003", NetworkID: 1, ServiceID: 3, Channel: &BootstrapChannel{Type: "GR", Channel: "13"}},
	} {
		if got, err := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), service); err != nil || got != 0x1234 {
			t.Fatalf("service=%s tsid=%x err=%v", service.Locator, got, err)
		}
	}
	if serviceCalls.Load() != 2 || channelCalls.Load() != 2 || adapter.active != 0 {
		t.Fatalf("serviceCalls=%d channelCalls=%d active=%d", serviceCalls.Load(), channelCalls.Load(), adapter.active)
	}
}

func TestProbeTransportStreamIDFromChannelCollectsSDTGeneration(t *testing.T) {
	first := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 1, 2)
	second := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 1, 1, 9)
	for _, test := range []struct {
		name   string
		body   []byte
		want   uint16
		reason provider.Reason
	}{
		{name: "all sections", body: probeSDTStream(t, first, second), want: 0x1234},
		{name: "missing target", body: probeSDTStream(t, probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 0, 9)), reason: provider.ReasonMalformed},
		{name: "missing section", body: probeSDTStream(t, first), reason: provider.ReasonEarlyEOF},
		{name: "other table ignored", body: probeSDTStream(t, probeSDTSection(t, 0x46, 0x1234, 1, 3, true, 0, 0, 2)), reason: provider.ReasonEarlyEOF},
		{name: "next table ignored", body: probeSDTStream(t, probeSDTSection(t, 0x42, 0x1234, 1, 3, false, 0, 0, 2)), reason: provider.ReasonEarlyEOF},
		{name: "wrong network", body: probeSDTStream(t, probeSDTSection(t, 0x42, 0x1234, 9, 3, true, 0, 0, 2)), reason: provider.ReasonMalformed},
		{name: "crc", body: probeSDTStream(t, append(append([]byte(nil), first[:len(first)-1]...), first[len(first)-1]^1)), reason: provider.ReasonMalformed},
		{name: "version changed", body: probeSDTStream(t, first, probeSDTSection(t, 0x42, 0x1234, 1, 4, true, 1, 1, 9)), reason: provider.ReasonMalformed},
		{name: "transport stream changed", body: probeSDTStream(t, first, probeSDTSection(t, 0x42, 0x9999, 1, 3, true, 1, 1, 9)), reason: provider.ReasonMalformed},
		{name: "service repeated across sections", body: probeSDTStream(t, first, probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 1, 1, 2)), reason: provider.ReasonMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/channels/GR/13/stream" {
					t.Errorf("path=%q", request.URL.Path)
				}
				writer.Header().Set("Content-Type", "video/MP2T")
				_, _ = writer.Write(test.body)
			}))
			defer server.Close()
			adapter, err := NewStream(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.CloseIdleConnections()
			got, probeErr := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2)
			if test.reason != "" {
				if !provider.IsReason(probeErr, test.reason) || adapter.active != 0 {
					t.Fatalf("tsid=%x err=%v active=%d want=%s", got, probeErr, adapter.active, test.reason)
				}
				return
			}
			if probeErr != nil || got != test.want || adapter.active != 0 {
				t.Fatalf("tsid=%x err=%v active=%d want=%x", got, probeErr, adapter.active, test.want)
			}
		})
	}
}

func TestProbeTransportStreamIDFromChannelAllowsIdenticalSectionResend(t *testing.T) {
	first := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 1, 2)
	second := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 1, 1, 9)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(probeSDTStream(t, first, first, second))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
}

func TestProbeTransportStreamIDFromChannelStopsAfterCompleteGeneration(t *testing.T) {
	complete := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 0, 2)
	changed := probeSDTSection(t, 0x42, 0x1234, 1, 4, true, 0, 0, 9)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(probeSDTStream(t, complete, changed))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
}

func TestProbeTransportStreamIDFromChannelStopsBeforeTrailingInvalidPacket(t *testing.T) {
	complete := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 0, 2)
	body := append(probeSDTStream(t, complete), bytes.Repeat([]byte{0}, mpegts.PacketBytes)...)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
}

func TestProbeTransportStreamIDFromChannelStopsBeforeLaterInvalidSectionInPacket(t *testing.T) {
	complete := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 0, 2)
	invalid := append([]byte(nil), complete...)
	invalid[1], invalid[2] = 0xbf, 0xff
	body := append(probeSDTSectionPacket(t, complete, invalid), bytes.Repeat(probeNullPacket(), 4)...)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
}

func TestSDTProbeUsesDeadlineAndReadBufferCap(t *testing.T) {
	if probeDeadline != 15*time.Second || probeReadBytes != 32*1024 {
		t.Fatalf("probe limits deadline=%v read=%d", probeDeadline, probeReadBytes)
	}
	body := &recordingProbeBody{reader: bytes.NewReader(probeSDTTransportStream(t, 0x1234, 1, 2))}
	connection, peer := net.Pipe()
	defer peer.Close()
	deadlines := make(chan time.Time, 1)
	adapter, err := NewStream("http://probe.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	adapter.client.Transport = &probeRoundTripper{body: body, connection: connection, deadlines: deadlines}
	defer adapter.CloseIdleConnections()

	got, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
	deadline := <-deadlines
	if remaining := time.Until(deadline); remaining <= probeDeadline-time.Second || remaining > probeDeadline {
		t.Fatalf("deadline remaining=%v", remaining)
	}
	if body.maxRead != 32*1024 {
		t.Fatalf("read buffer=%d", body.maxRead)
	}
}

func TestProbeTransportStreamIDFromChannelStopsAt64MiB(t *testing.T) {
	body := &patternProbeBody{remaining: sdtProbeMaxBytes, pattern: probeNullPacket()}
	connection, peer := net.Pipe()
	defer peer.Close()
	adapter, err := NewStream("http://probe.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	adapter.client.Transport = &probeRoundTripper{body: body, connection: connection}
	defer adapter.CloseIdleConnections()

	if _, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2); !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("err=%v remaining=%d maxRead=%d", err, body.remaining, body.maxRead)
	}
	if body.remaining != 0 || body.maxRead != 32*1024 {
		t.Fatalf("remaining=%d maxRead=%d", body.remaining, body.maxRead)
	}
}

func TestSDTGenerationResourceCeilings(t *testing.T) {
	if sdtProbeMaxBytes != 64*1024*1024 || sdtProbeSectionBytes != 256*1024 || sdtProbeManagementBytes != 64*1024 {
		t.Fatalf("probe bounds bytes=%d section=%d management=%d", sdtProbeMaxBytes, sdtProbeSectionBytes, sdtProbeManagementBytes)
	}
	section := bytes.Repeat([]byte{0x42}, mpegts.MaxSDTSectionBytes)
	generation := sdtGeneration{}
	for sectionNumber := 0; sectionNumber < sdtProbeMaxSections; sectionNumber++ {
		table := mpegts.SDT{
			TransportStreamID: 0x1234, OriginalNetworkID: 1, CurrentNext: true,
			SectionNumber: byte(sectionNumber), LastSectionNumber: sdtProbeMaxSections - 1,
		}
		if sectionNumber == 0 {
			table.ServiceIDs = []uint16{2}
		}
		transportStreamID, complete, err := generation.accept(table, section, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if sectionNumber == sdtProbeMaxSections-1 && (!complete || transportStreamID != 0x1234) {
			t.Fatalf("complete=%v tsid=%x", complete, transportStreamID)
		}
	}
	if generation.sectionBytes != sdtProbeSectionBytes {
		t.Fatalf("section bytes=%d", generation.sectionBytes)
	}
	if managementBytes := generation.managementBytes(sdtProbeMaxSections, sdtProbeMaxServices); managementBytes != 20*1024 || managementBytes > sdtProbeManagementBytes {
		t.Fatalf("management bytes=%d", managementBytes)
	}

	over := sdtGeneration{
		initialized: true, transportStreamID: 0x1234, originalNetworkID: 1,
		lastSection: 1, sectionCount: 1, sectionBytes: sdtProbeSectionBytes - len(section) + 1,
	}
	_, _, err := over.accept(mpegts.SDT{
		TransportStreamID: 0x1234, OriginalNetworkID: 1, CurrentNext: true,
		SectionNumber: 1, LastSectionNumber: 1,
	}, section, 1, 2)
	if !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("section overflow err=%v", err)
	}
}

func TestProbeTransportStreamIDFromChannelIgnoresNextBeforeCurrent(t *testing.T) {
	next := probeSDTSection(t, 0x42, 0x9999, 9, 31, false, 0, 0, 2)
	current := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(probeSDTStream(t, next, current))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
}

func TestProbeTransportStreamIDFromChannelRejectsChangedSectionResend(t *testing.T) {
	first := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 1, 2)
	changed := probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 1, 9)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(probeSDTStream(t, first, changed))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	if _, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2); !provider.IsReason(err, provider.ReasonMalformed) {
		t.Fatalf("err=%v", err)
	}
}

func TestProbeTransportStreamIDWithSDTFallbackAllowsOnlyListedPATFailures(t *testing.T) {
	largePATFailure := bytes.Repeat(probeNullPacket(), (1<<20)/mpegts.PacketBytes+1)[:1<<20]
	for _, test := range []struct {
		name  string
		setup func(http.ResponseWriter, *http.Request)
	}{
		{name: "early eof", setup: func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeNullPacket())
		}},
		{name: "byte limit", setup: func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(largePATFailure)
		}},
		{name: "read timeout", setup: func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var channelCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/services/100002/stream":
					test.setup(writer, request)
				case "/api/channels/GR/13/stream":
					channelCalls.Add(1)
					writer.Header().Set("Content-Type", "video/MP2T")
					_, _ = writer.Write(probeSDTTransportStream(t, 0x1234, 1, 2))
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			adapter, err := newStreamAdapter(server.URL, streamLimits{connectHeader: 50 * time.Millisecond, readIdle: 20 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.CloseIdleConnections()
			got, probeErr := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), BootstrapService{
				Locator: "100002", NetworkID: 1, ServiceID: 2,
				Channel: &BootstrapChannel{Type: "GR", Channel: "13"},
			})
			if probeErr != nil || got != 0x1234 || channelCalls.Load() != 1 || adapter.active != 0 {
				t.Fatalf("tsid=%x err=%v channelCalls=%d active=%d", got, probeErr, channelCalls.Load(), adapter.active)
			}
		})
	}
}

func TestProbeTransportStreamIDWithSDTFallbackAllowsHeaderTimeout(t *testing.T) {
	var channelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/services/100002/stream":
			<-request.Context().Done()
		case "/api/channels/GR/13/stream":
			channelCalls.Add(1)
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeSDTTransportStream(t, 0x1234, 1, 2))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	adapter, err := newStreamAdapter(server.URL, streamLimits{connectHeader: 20 * time.Millisecond, readIdle: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, probeErr := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), BootstrapService{
		Locator: "100002", NetworkID: 1, ServiceID: 2,
		Channel: &BootstrapChannel{Type: "GR", Channel: "13"},
	})
	if probeErr != nil || got != 0x1234 || channelCalls.Load() != 1 || adapter.active != 0 {
		t.Fatalf("tsid=%x err=%v channelCalls=%d active=%d", got, probeErr, channelCalls.Load(), adapter.active)
	}
}

func TestProbeTransportStreamIDWithSDTFallbackFindsSDTAfterOneMiBOfOtherPackets(t *testing.T) {
	var channelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/services/100002/stream":
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeNullPacket())
		case "/api/channels/GR/13/stream":
			channelCalls.Add(1)
			writer.Header().Set("Content-Type", "video/MP2T")
			prefix := bytes.Repeat(probeNullPacket(), (1<<20)/mpegts.PacketBytes+1)
			_, _ = writer.Write(append(prefix, probeSDTStream(t, probeSDTSection(t, 0x42, 0x1234, 1, 3, true, 0, 0, 2))...))
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
	got, probeErr := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), BootstrapService{
		Locator: "100002", NetworkID: 1, ServiceID: 2,
		Channel: &BootstrapChannel{Type: "GR", Channel: "13"},
	})
	if probeErr != nil || got != 0x1234 || channelCalls.Load() != 1 || adapter.active != 0 {
		t.Fatalf("tsid=%x err=%v channelCalls=%d active=%d", got, probeErr, channelCalls.Load(), adapter.active)
	}
}

func TestProbeTransportStreamIDWithSDTFallbackDoesNotUseChannelOnForbiddenFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		serviceBody func(http.ResponseWriter, *http.Request)
	}{
		{name: "status", serviceBody: func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "no", http.StatusServiceUnavailable)
		}},
		{name: "wrong content type", serviceBody: func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/octet-stream")
		}},
		{name: "pat mismatch", serviceBody: func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeTransportStream(t, 0x1234, 3))
		}},
		{name: "invalid packet", serviceBody: func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			packet := bytes.Repeat([]byte{0}, mpegts.PacketBytes)
			packet[0] = 0x47
			_, _ = writer.Write(bytes.Repeat(packet, 5))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var channelCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/services/100002/stream" {
					test.serviceBody(writer, request)
					return
				}
				if request.URL.Path == "/api/channels/GR/13/stream" {
					channelCalls.Add(1)
				}
				http.NotFound(writer, request)
			}))
			defer server.Close()
			adapter, err := NewStream(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.CloseIdleConnections()
			if _, probeErr := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), BootstrapService{
				Locator: "100002", NetworkID: 1, ServiceID: 2,
				Channel: &BootstrapChannel{Type: "GR", Channel: "13"},
			}); probeErr == nil || channelCalls.Load() != 0 || adapter.active != 0 {
				t.Fatalf("err=%v channelCalls=%d active=%d", probeErr, channelCalls.Load(), adapter.active)
			}
		})
	}
}

func TestProbeTransportStreamIDWithSDTFallbackNeverUsesChannelAfterParentCancellation(t *testing.T) {
	var serviceCalls, channelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/services/100002/stream" {
			serviceCalls.Add(1)
			<-request.Context().Done()
			return
		}
		if request.URL.Path == "/api/channels/GR/13/stream" {
			channelCalls.Add(1)
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, probeErr := adapter.ProbeTransportStreamIDWithSDTFallback(ctx, BootstrapService{
		Locator: "100002", NetworkID: 1, ServiceID: 2,
		Channel: &BootstrapChannel{Type: "GR", Channel: "13"},
	}); !provider.IsReason(probeErr, provider.ReasonCancelled) || serviceCalls.Load() != 0 || channelCalls.Load() != 0 {
		t.Fatalf("err=%v serviceCalls=%d channelCalls=%d", probeErr, serviceCalls.Load(), channelCalls.Load())
	}
}

func TestAllowsPATSDTFallbackUsesExactFailureAllowlist(t *testing.T) {
	for _, test := range []struct {
		name       string
		reason     provider.Reason
		diagnostic string
		want       bool
	}{
		{name: "header timeout", reason: provider.ReasonTimeout, diagnostic: "http-timeout", want: true},
		{name: "read timeout", reason: provider.ReasonTimeout, diagnostic: "probe-read-timeout", want: true},
		{name: "probe deadline", reason: provider.ReasonTimeout, diagnostic: "context-deadline", want: true},
		{name: "pat eof", reason: provider.ReasonEarlyEOF, diagnostic: "probe-pat-not-found", want: true},
		{name: "pat limit", reason: provider.ReasonOverLimit, diagnostic: "probe-byte-limit", want: true},
		{name: "zero progress", reason: provider.ReasonUnavailable, diagnostic: "probe-zero-progress"},
		{name: "deadline read failure", reason: provider.ReasonUnavailable, diagnostic: "probe-read-deadline-failed"},
		{name: "content length", reason: provider.ReasonOverLimit, diagnostic: "probe-content-length-over-limit"},
		{name: "http status", reason: provider.ReasonUnavailable, diagnostic: "http-server-error"},
		{name: "wrong reason", reason: provider.ReasonMalformed, diagnostic: "probe-pat-invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allowsPATSDTFallback(context.Background(), provider.NewFailure(test.reason, test.diagnostic)); got != test.want {
				t.Fatalf("allowed=%v want=%v", got, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if allowsPATSDTFallback(ctx, provider.NewFailure(provider.ReasonEarlyEOF, "probe-pat-not-found")) {
		t.Fatal("parent cancellation allowed fallback")
	}
}

func TestProbeTransportStreamIDWithSDTFallbackDoesNotOpenChannelAfterPATSuccess(t *testing.T) {
	var channelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/services/100002/stream" {
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeTransportStream(t, 0x1234, 2))
			return
		}
		if request.URL.Path == "/api/channels/GR/13/stream" {
			channelCalls.Add(1)
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), BootstrapService{
		Locator: "100002", NetworkID: 1, ServiceID: 2,
		Channel: &BootstrapChannel{Type: "GR", Channel: "13"},
	})
	if err != nil || got != 0x1234 || channelCalls.Load() != 0 || adapter.active != 0 {
		t.Fatalf("tsid=%x err=%v channelCalls=%d active=%d", got, err, channelCalls.Load(), adapter.active)
	}
}

func TestProbeTransportStreamIDWithSDTFallbackSkipsInvalidOptionalChannel(t *testing.T) {
	var channelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/services/100002/stream" {
			writer.Header().Set("Content-Type", "video/MP2T")
			_, _ = writer.Write(probeNullPacket())
			return
		}
		if strings.HasPrefix(request.URL.Path, "/api/channels/") {
			channelCalls.Add(1)
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	if _, err := adapter.ProbeTransportStreamIDWithSDTFallback(context.Background(), BootstrapService{
		Locator: "100002", NetworkID: 1, ServiceID: 2,
	}); err == nil || channelCalls.Load() != 0 || adapter.active != 0 {
		t.Fatalf("err=%v channelCalls=%d active=%d", err, channelCalls.Load(), adapter.active)
	}
}

func TestProbeTransportStreamIDFromChannelAcceptsFourThousandNinetySixServices(t *testing.T) {
	sections := make([][]byte, 0, 256)
	for sectionNumber := 0; sectionNumber < 256; sectionNumber++ {
		serviceIDs := make([]uint16, 0, 16)
		for offset := 0; offset < 16; offset++ {
			serviceIDs = append(serviceIDs, uint16(sectionNumber*16+offset+1))
		}
		sections = append(sections, probeSDTSection(t, 0x42, 0x1234, 1, 3, true, byte(sectionNumber), 255, serviceIDs...))
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(probeSDTStream(t, sections...))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	got, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 4096)
	if err != nil || got != 0x1234 {
		t.Fatalf("tsid=%x err=%v", got, err)
	}
}

func TestProbeTransportStreamIDFromChannelRejectsMoreThanFourThousandNinetySixServices(t *testing.T) {
	sections := make([][]byte, 0, 256)
	for sectionNumber := 0; sectionNumber < 256; sectionNumber++ {
		serviceIDs := make([]uint16, 0, 17)
		for offset := 0; offset < 17; offset++ {
			serviceIDs = append(serviceIDs, uint16(sectionNumber*17+offset+1))
		}
		sections = append(sections, probeSDTSection(t, 0x42, 0x1234, 1, 3, true, byte(sectionNumber), 255, serviceIDs...))
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		_, _ = writer.Write(probeSDTStream(t, sections...))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	if _, err := adapter.ProbeTransportStreamIDFromChannel(context.Background(), BootstrapChannel{Type: "GR", Channel: "13"}, 1, 2); !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestStreamAcceptsIndependentLiveUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStreamWithLimit(server.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	request := validStreamRequest("1003")
	request.Usage = providerstream.UsageLive
	request.CorrelationID = "live-test"
	lease, err := adapter.OpenStream(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamRejectsStatusContentTypeAndRedirect(t *testing.T) {
	redirectTarget := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/services/300/stream":
			http.Redirect(writer, request, "/target", http.StatusFound)
		case "/api/services/404/stream":
			http.Error(writer, "private body", http.StatusNotFound)
		case "/api/services/500/stream":
			http.Error(writer, "private body", http.StatusInternalServerError)
		case "/api/services/200/stream":
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.WriteHeader(http.StatusOK)
		case "/target":
			redirectTarget.Add(1)
		}
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id     string
		reason provider.Reason
	}{
		{id: "300", reason: provider.ReasonRejected},
		{id: "404", reason: provider.ReasonNotFound},
		{id: "500", reason: provider.ReasonUnavailable},
		{id: "200", reason: provider.ReasonMalformed},
	} {
		if _, err := adapter.OpenStream(context.Background(), validStreamRequest(test.id)); !provider.IsReason(err, test.reason) {
			t.Fatalf("id=%s err=%v", test.id, err)
		}
	}
	if redirectTarget.Load() != 0 {
		t.Fatal("redirect先へ接続しました")
	}
}

func TestStreamReadTimeoutDisconnectAndCancel(t *testing.T) {
	t.Run("idle timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		defer server.Close()
		adapter, err := newStreamAdapter(server.URL, streamLimits{connectHeader: time.Second, readIdle: 40 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		read, terminal, err := lease.Read(context.Background(), make([]byte, 188))
		if read != 0 || terminal.Reason != providerstream.TerminalTimeout || !provider.IsReason(err, provider.ReasonTimeout) ||
			time.Since(started) < 20*time.Millisecond || time.Since(started) > time.Second {
			t.Fatalf("read=%d terminal=%+v elapsed=%v err=%v", read, terminal, time.Since(started), err)
		}
		_ = lease.Close()
	})

	t.Run("truncated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.Header().Set("Content-Length", "376")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
		}))
		defer server.Close()
		adapter, _ := NewStream(server.URL)
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatal(err)
		}
		read, terminal, err := lease.Read(context.Background(), make([]byte, 376))
		if err == nil && !terminal.Done {
			second, secondTerminal, secondErr := lease.Read(context.Background(), make([]byte, 376))
			read += second
			terminal, err = secondTerminal, secondErr
		}
		if read != 188 || terminal.Reason != providerstream.TerminalPeer || err == nil {
			t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
		}
		_ = lease.Close()
	})

	t.Run("cancel", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		defer server.Close()
		adapter, _ := NewStream(server.URL)
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			_, terminal, readErr := lease.Read(context.Background(), make([]byte, 188))
			if terminal.Reason != providerstream.TerminalCancelled {
				result <- errors.New("cancelled終端ではありません")
				return
			}
			result <- readErr
		}()
		time.Sleep(20 * time.Millisecond)
		if err := lease.Cancel(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if !provider.IsReason(err, provider.ReasonCancelled) {
				t.Fatalf("err=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelでreadが解除されません")
		}
	})
}

func TestStreamCanOpenAgainAfterTemporaryFailures(t *testing.T) {
	requests := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) {
		case 1:
			http.Error(writer, "private body", http.StatusServiceUnavailable)
		case 2:
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.Header().Set("Content-Length", "376")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
		default:
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.OpenStream(context.Background(), validStreamRequest("1003")); !provider.IsReason(err, provider.ReasonUnavailable) {
		t.Fatalf("5xx err=%v", err)
	}
	lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
	if err != nil {
		t.Fatal(err)
	}
	read, terminal, err := streamTestReadTerminal(lease)
	if read != 188 || !terminal.Done || err == nil {
		t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	lease, err = adapter.OpenStream(context.Background(), validStreamRequest("1003"))
	if err != nil {
		t.Fatal(err)
	}
	read, terminal, err = lease.Read(context.Background(), make([]byte, 188))
	if err != nil || read != 188 || terminal.Done {
		t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
	}
	_ = lease.Cancel()
	_ = lease.Close()
	if requests.Load() != 3 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestStreamUsesExplicitConcurrentLimitAndReusesSlot(t *testing.T) {
	var requests atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStreamWithLimit(server.URL, 2)
	if err != nil {
		t.Fatal(err)
	}
	transport := adapter.client.Transport.(*http.Transport)
	if transport.MaxConnsPerHost != 2 || transport.MaxIdleConnsPerHost != 2 || transport.MaxIdleConns != 2 {
		t.Fatalf("transport limits=%d/%d/%d", transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost, transport.MaxIdleConns)
	}
	firstRequest := validStreamRequest("1003")
	firstRequest.CorrelationID = "recording-first"
	first, err := adapter.OpenStream(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := validStreamRequest("1004")
	secondRequest.CorrelationID = "recording-second"
	second, err := adapter.OpenStream(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	thirdRequest := validStreamRequest("1005")
	thirdRequest.CorrelationID = "recording-third"
	if _, err := adapter.OpenStream(context.Background(), thirdRequest); !provider.IsReason(err, provider.ReasonRejected) {
		t.Fatalf("上限超過err=%v", err)
	}
	if requests.Load() != 2 || maximum.Load() != 2 {
		t.Fatalf("requests=%d maximum=%d", requests.Load(), maximum.Load())
	}
	_ = first.Cancel()
	_ = first.Close()
	third, err := adapter.OpenStream(context.Background(), thirdRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Cancel()
	_ = second.Close()
	_ = third.Cancel()
	_ = third.Close()
	adapter.mu.Lock()
	remaining := adapter.active
	adapter.mu.Unlock()
	if requests.Load() != 3 || remaining != 0 {
		t.Fatalf("requests=%d remaining=%d", requests.Load(), remaining)
	}
}

func TestStreamOpensNineIndependentRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStreamWithLimit(server.URL, 9)
	if err != nil {
		t.Fatal(err)
	}
	leases := make([]providerstream.Lease, 0, 9)
	for index := range 9 {
		request := validStreamRequest(fmt.Sprint(1000 + index))
		request.CorrelationID = fmt.Sprintf("recording-%d", index)
		lease, err := adapter.OpenStream(context.Background(), request)
		if err != nil {
			t.Fatalf("index=%d error=%v", index, err)
		}
		leases = append(leases, lease)
	}
	over := validStreamRequest("2000")
	over.CorrelationID = "recording-over"
	if _, err := adapter.OpenStream(context.Background(), over); !provider.IsReason(err, provider.ReasonRejected) {
		t.Fatalf("上限超過error=%v", err)
	}
	if requests.Load() != 9 || adapter.active != 9 {
		t.Fatalf("requests=%d active=%d", requests.Load(), adapter.active)
	}
	for _, lease := range leases {
		_ = lease.Cancel()
		_ = lease.Close()
	}
	if adapter.active != 0 {
		t.Fatalf("active=%d", adapter.active)
	}
}

func TestStreamAcceptsAnyPositiveConcurrentLimit(t *testing.T) {
	for _, maximum := range []int{-1, 0} {
		if _, err := NewStreamWithLimit("http://127.0.0.1:9", maximum); !provider.IsReason(err, provider.ReasonInternal) {
			t.Fatalf("maximum=%d err=%v", maximum, err)
		}
	}
	for _, maximum := range []int{9, 20, 1_000_000_000} {
		adapter, err := NewStreamWithLimit("http://127.0.0.1:9", maximum)
		if err != nil {
			t.Fatalf("maximum=%d err=%v", maximum, err)
		}
		transport := adapter.client.Transport.(*http.Transport)
		if adapter.active != 0 || transport.MaxConnsPerHost != maximum || transport.MaxIdleConnsPerHost != maximum {
			t.Fatalf("maximum=%d active=%d transport=%d/%d", maximum, adapter.active,
				transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
		}
	}
}

func TestStreamRepeatedDisconnectDoesNotLeakResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.Header().Set("Content-Length", "376")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	beforeGoroutines := runtime.NumGoroutine()
	beforeFDs, measureFDs := streamTestFDCount()
	for cycle := 0; cycle < 100; cycle++ {
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatalf("cycle=%d open=%v", cycle, err)
		}
		read, terminal, err := streamTestReadTerminal(lease)
		if read != 188 || !terminal.Done || err == nil {
			t.Fatalf("cycle=%d read=%d terminal=%+v err=%v", cycle, read, terminal, err)
		}
		if err := lease.Close(); err != nil {
			t.Fatalf("cycle=%d close=%v", cycle, err)
		}
	}
	adapter.CloseIdleConnections()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > beforeGoroutines+8 {
		t.Fatalf("goroutines before=%d after=%d", beforeGoroutines, after)
	}
	if after, supported := streamTestFDCount(); measureFDs && supported && after > beforeFDs+4 {
		t.Fatalf("file descriptors before=%d after=%d", beforeFDs, after)
	}
}

func TestStreamRequestValidationAndBufferCap(t *testing.T) {
	adapter, err := NewStream("http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "0", "01", "-1", "abc"} {
		if _, err := adapter.OpenStream(context.Background(), validStreamRequest(id)); !provider.IsReason(err, provider.ReasonRejected) {
			t.Fatalf("id=%q err=%v", id, err)
		}
	}
	lease := &streamLease{}
	if _, _, err := lease.Read(context.Background(), make([]byte, provider.MaxStreamChunk+1)); !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("err=%v", err)
	}
}

func validStreamRequest(id string) providerstream.Request {
	return providerstream.Request{
		Target: provider.TuningTarget{Opaque: id}, Usage: providerstream.UsageRecording,
		PriorityPolicy: "0", RequireDescrambled: true, CorrelationID: "recording-test",
	}
}

func streamTestFDCount() (int, bool) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

func streamTestReadTerminal(lease providerstream.Lease) (int, providerstream.Terminal, error) {
	total := 0
	for range 2 {
		read, terminal, err := lease.Read(context.Background(), make([]byte, 376))
		total += read
		if terminal.Done || err != nil {
			return total, terminal, err
		}
	}
	return total, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}

func probeSDTTransportStream(t *testing.T, transportID, networkID, serviceID uint16) []byte {
	t.Helper()
	return probeSDTStream(t, probeSDTSection(t, 0x42, transportID, networkID, 0, true, 0, 0, serviceID))
}

func probeSDTSection(t *testing.T, tableID byte, transportID, networkID uint16, version byte,
	currentNext bool, sectionNumber, lastSectionNumber byte, serviceIDs ...uint16,
) []byte {
	t.Helper()
	current := byte(0)
	if currentNext {
		current = 1
	}
	section := []byte{tableID, 0xb0, 0, byte(transportID >> 8), byte(transportID), 0xc0 | version<<1 | current,
		sectionNumber, lastSectionNumber, byte(networkID >> 8), byte(networkID), 0xff}
	for _, serviceID := range serviceIDs {
		section = append(section, byte(serviceID>>8), byte(serviceID), 0, 0xf0, 0)
	}
	sectionLength := len(section) + 4 - 3
	section[1] = 0xb0 | byte(sectionLength>>8)
	section[2] = byte(sectionLength)
	crc := mpegts.CRC32(section)
	return append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func probeSDTStream(t *testing.T, sections ...[]byte) []byte {
	t.Helper()
	var result []byte
	continuity := byte(0)
	for _, section := range sections {
		packets, err := mpegts.PacketizeSection(0x11, continuity, section)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, bytes.Join(packets, nil)...)
		continuity = (continuity + byte(len(packets))) & 0x0f
	}
	for len(result) < 5*mpegts.PacketBytes {
		result = append(result, probeNullPacket()...)
	}
	return result
}

func probeSDTSectionPacket(t *testing.T, sections ...[]byte) []byte {
	t.Helper()
	packet := probeNullPacket()
	packet[1], packet[2], packet[3] = 0x40, 0x11, 0x10
	payload := []byte{0}
	for _, section := range sections {
		payload = append(payload, section...)
	}
	if len(payload) > mpegts.PacketBytes-4 {
		t.Fatalf("section payload=%d", len(payload))
	}
	copy(packet[4:], payload)
	return packet
}

type recordingProbeBody struct {
	reader  io.Reader
	maxRead int
}

func (body *recordingProbeBody) Read(data []byte) (int, error) {
	if len(data) > body.maxRead {
		body.maxRead = len(data)
	}
	return body.reader.Read(data)
}

func (body *recordingProbeBody) Close() error { return nil }

type patternProbeBody struct {
	remaining int
	pattern   []byte
	patternAt int
	maxRead   int
}

func (body *patternProbeBody) Read(data []byte) (int, error) {
	if len(data) > body.maxRead {
		body.maxRead = len(data)
	}
	if body.remaining == 0 {
		return 0, io.EOF
	}
	count := min(len(data), body.remaining)
	for offset := 0; offset < count; {
		patternAt := body.patternAt % len(body.pattern)
		n := copy(data[offset:count], body.pattern[patternAt:])
		offset += n
		body.patternAt += n
	}
	body.remaining -= count
	return count, nil
}

func (body *patternProbeBody) Close() error { return nil }

type probeRoundTripper struct {
	body       io.ReadCloser
	connection net.Conn
	deadlines  chan time.Time
}

func (transport *probeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{Conn: transport.connection})
	}
	if transport.deadlines != nil {
		if deadline, ok := request.Context().Deadline(); ok {
			transport.deadlines <- deadline
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"video/MP2T"}},
		Body: transport.body, ContentLength: -1, Request: request,
	}, nil
}
