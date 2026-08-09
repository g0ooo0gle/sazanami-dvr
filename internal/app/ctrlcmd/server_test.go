package ctrlcmd

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/status"
)

type handlerFunc func(context.Context, []byte, io.Writer) error

func (f handlerFunc) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	return f(ctx, request, destination)
}

type normalSource struct{}

func (normalSource) Current(context.Context) (status.StartupStatus, error) {
	return status.StartupNormal, nil
}

type constantClock struct{ instant time.Time }

func (c constantClock) Now() time.Time { return c.instant }

func testConfig() Config {
	config := DefaultConfig()
	config.Address = "127.0.0.1:0"
	config.HeaderTimeout = 250 * time.Millisecond
	config.ConnectionLifetime = time.Second
	return config
}

func request2200() []byte {
	request := make([]byte, 14)
	binary.LittleEndian.PutUint32(request[0:4], uint32(status.Command))
	binary.LittleEndian.PutUint32(request[4:8], status.RequestBodySize)
	binary.LittleEndian.PutUint16(request[8:10], status.Version)
	return request
}

func startServer(t *testing.T, server *Server) (net.Listener, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	return listener, cancel, done
}

func stopServer(t *testing.T, listener net.Listener, cancel context.CancelFunc, done <-chan error, server *Server) {
	t.Helper()
	cancel()
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
	server.Wait()
}

func TestConfigRejectsUnsafeOrUnboundedValues(t *testing.T) {
	cases := map[string]func(*Config){
		"wildcard v4":      func(c *Config) { c.Address = "0.0.0.0:4510" },
		"wildcard v6":      func(c *Config) { c.Address = "[::]:4510" },
		"non-loopback":     func(c *Config) { c.Address = "192.0.2.1:4510" },
		"hostname":         func(c *Config) { c.Address = "localhost:4510" },
		"missing port":     func(c *Config) { c.Address = "127.0.0.1" },
		"connections zero": func(c *Config) { c.MaxConnections = 0 },
		"connections over": func(c *Config) { c.MaxConnections = DefaultConnections + 1 },
		"handlers zero":    func(c *Config) { c.MaxHandlers = 0 },
		"handlers over":    func(c *Config) { c.MaxHandlers = DefaultHandlers + 1 },
		"body over":        func(c *Config) { c.MaxRequestBody = 1*1024*1024 + 1 },
		"header timeout":   func(c *Config) { c.HeaderTimeout = DefaultHeaderTimeout + time.Nanosecond },
		"lifetime":         func(c *Config) { c.ConnectionLifetime = MaximumLifetime + time.Nanosecond },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config := DefaultConfig()
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
	for _, address := range []string{"127.0.0.1:4510", "[::1]:4510"} {
		config := DefaultConfig()
		config.Address = address
		if err := config.Validate(); err != nil {
			t.Fatalf("valid address %s: %v", address, err)
		}
	}
}

func TestRecordingConfigUsesAcceptedConnectionDeadline(t *testing.T) {
	config := RecordingConfig()
	if config.ConnectionLifetime != 14*time.Second {
		t.Fatalf("lifetime=%s", config.ConnectionLifetime)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"10.1.1.39:4510", "192.168.1.39:4510", "[fd00::39]:4510", "0.0.0.0:4510", "[::]:4510"} {
		config.Address = address
		if err := config.Validate(); err != nil {
			t.Fatalf("private address %s: %v", address, err)
		}
	}
	for _, address := range []string{"169.254.1.39:4510", "203.0.113.39:4510", "localhost:4510"} {
		config.Address = address
		if err := config.Validate(); err == nil {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
}

func TestLoopbackEndToEndSyntheticJourney(t *testing.T) {
	handler := status.Handler{
		Source: normalSource{},
		Clock:  constantClock{instant: time.Date(2026, 7, 31, 0, 0, 0, 123_000_000, time.UTC)},
	}
	server, err := NewServer(testConfig(), handler)
	if err != nil {
		t.Fatal(err)
	}
	listener, cancel, done := startServer(t, server)
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(request2200()); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, status.ResponseFrameSize)
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(response[0:4]) != uint32(status.ResultSuccess) ||
		binary.LittleEndian.Uint32(response[4:8]) != status.ResponseBodySize ||
		binary.LittleEndian.Uint32(response[10:14]) != status.StructureExtent {
		t.Fatalf("response=%x", response)
	}
	extra := []byte{0}
	if n, readErr := connection.Read(extra); n != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatalf("one-response lifecycle n=%d err=%v", n, readErr)
	}
	_ = connection.Close()
	stopServer(t, listener, cancel, done, server)
	metrics := server.Metrics()
	if metrics.Accepted != 1 || metrics.Completed != 1 || metrics.Failed != 0 || metrics.Active != 0 {
		t.Fatalf("metrics=%+v", metrics)
	}
}

func TestMalformedLengthClosesWithoutHandler(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	handler := handlerFunc(func(context.Context, []byte, io.Writer) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	})
	server, _ := NewServer(testConfig(), handler)
	listener, cancel, done := startServer(t, server)
	for _, declared := range []uint32{^uint32(0), 1*1024*1024 + 1} {
		connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 8)
		binary.LittleEndian.PutUint32(header[4:], declared)
		_, _ = connection.Write(header)
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		if n, readErr := connection.Read(make([]byte, 1)); n != 0 || readErr == nil {
			t.Fatalf("declared=%d n=%d err=%v", declared, n, readErr)
		}
		_ = connection.Close()
	}
	stopServer(t, listener, cancel, done, server)
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 || server.Metrics().Failed != 2 {
		t.Fatalf("calls=%d metrics=%+v", calls, server.Metrics())
	}
}

func TestHeaderTimeoutIsAbsoluteAndBounded(t *testing.T) {
	config := testConfig()
	config.HeaderTimeout = 40 * time.Millisecond
	config.ConnectionLifetime = 100 * time.Millisecond
	server, _ := NewServer(config, handlerFunc(func(context.Context, []byte, io.Writer) error { return nil }))
	listener, cancel, done := startServer(t, server)
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	n, readErr := connection.Read(make([]byte, 1))
	elapsed := time.Since(started)
	if n != 0 || readErr == nil || elapsed < 20*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("n=%d err=%v elapsed=%v", n, readErr, elapsed)
	}
	_ = connection.Close()
	stopServer(t, listener, cancel, done, server)
	if server.Metrics().Failed != 1 {
		t.Fatalf("metrics=%+v", server.Metrics())
	}
}

func TestHandlerSaturationFailsClosed(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := handlerFunc(func(_ context.Context, _ []byte, destination io.Writer) error {
		entered <- struct{}{}
		<-release
		_, err := destination.Write([]byte{1})
		return err
	})
	config := testConfig()
	config.MaxConnections = 2
	config.MaxHandlers = 1
	server, _ := NewServer(config, handler)
	listener, cancel, done := startServer(t, server)
	first, _ := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	_, _ = first.Write(request2200())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first handler did not start")
	}
	second, _ := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	_, _ = second.Write(request2200())
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if n, readErr := second.Read(make([]byte, 1)); n != 0 || readErr == nil {
		t.Fatalf("second n=%d err=%v", n, readErr)
	}
	close(release)
	_ = first.SetReadDeadline(time.Now().Add(time.Second))
	firstResponse := make([]byte, 1)
	if _, err := io.ReadFull(first, firstResponse); err != nil || !bytes.Equal(firstResponse, []byte{1}) {
		t.Fatalf("response=%v err=%v", firstResponse, err)
	}
	_ = first.Close()
	_ = second.Close()
	stopServer(t, listener, cancel, done, server)
	metrics := server.Metrics()
	if metrics.Accepted != 2 || metrics.Rejected != 1 || metrics.Completed != 1 || metrics.Active != 0 {
		t.Fatalf("metrics=%+v", metrics)
	}
}

type inertListener struct{ address net.Addr }

func (l inertListener) Accept() (net.Conn, error) { return nil, errors.New("must not accept") }
func (l inertListener) Close() error              { return nil }
func (l inertListener) Addr() net.Addr            { return l.address }

func TestServeRejectsNonLoopbackListenerBeforeAccept(t *testing.T) {
	server, _ := NewServer(testConfig(), handlerFunc(func(context.Context, []byte, io.Writer) error { return nil }))
	err := server.Serve(context.Background(), inertListener{address: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 4510}})
	if err == nil {
		t.Fatal("non-loopback listener accepted")
	}
}

func TestNewServerRejectsNilHandler(t *testing.T) {
	if _, err := NewServer(testConfig(), nil); err == nil {
		t.Fatal("nil handler accepted")
	}
}
