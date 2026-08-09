package liverelay

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

type fixedResolver struct{ fail bool }

func (resolver fixedResolver) ResolveLiveService(_ context.Context, networkID, transportStreamID, serviceID uint16) (provider.TuningTarget, error) {
	if resolver.fail || networkID != 1 || transportStreamID != 2 || serviceID != 3 {
		return provider.TuningTarget{}, errors.New("unavailable")
	}
	return provider.TuningTarget{Opaque: "1003"}, nil
}

type fakeProvider struct {
	mu       sync.Mutex
	requests []providerstream.Request
	leases   []*fakeLease
	fail     bool
	block    <-chan struct{}
}

func (stream *fakeProvider) OpenStream(ctx context.Context, request providerstream.Request) (providerstream.Lease, error) {
	stream.mu.Lock()
	stream.requests = append(stream.requests, request)
	fail, block := stream.fail, stream.block
	stream.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("provider failure")
	}
	lease := &fakeLease{chunks: [][]byte{{0x47, 0x01}, {0x47, 0x02}}}
	stream.mu.Lock()
	stream.leases = append(stream.leases, lease)
	stream.mu.Unlock()
	return lease, nil
}

type fakeLease struct {
	mu        sync.Mutex
	chunks    [][]byte
	position  int
	cancelled atomic.Bool
	closed    atomic.Bool
}

type blockingProvider struct{}

func (blockingProvider) OpenStream(context.Context, providerstream.Request) (providerstream.Lease, error) {
	return &blockingLease{}, nil
}

type blockingLease struct {
	cancelled atomic.Bool
	closed    atomic.Bool
}

func (lease *blockingLease) Read(ctx context.Context, _ []byte) (int, providerstream.Terminal, error) {
	<-ctx.Done()
	return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled}, ctx.Err()
}

func (lease *blockingLease) Cancel() error { lease.cancelled.Store(true); return nil }
func (lease *blockingLease) Close() error  { lease.closed.Store(true); return nil }

func (lease *fakeLease) Read(ctx context.Context, destination []byte) (int, providerstream.Terminal, error) {
	if ctx.Err() != nil || lease.cancelled.Load() {
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled}, ctx.Err()
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.position == len(lease.chunks) {
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCleanEnd}, nil
	}
	chunk := lease.chunks[lease.position]
	lease.position++
	return copy(destination, chunk), providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}

func (lease *fakeLease) Cancel() error { lease.cancelled.Store(true); return nil }
func (lease *fakeLease) Close() error  { lease.closed.Store(true); return nil }

func validService() Service { return Service{NetworkID: 1, TransportStreamID: 2, ServiceID: 3} }

func TestSelectOpenCopyAndCloseReleaseSlot(t *testing.T) {
	stream := &fakeProvider{}
	manager, err := NewManager(fixedResolver{}, stream)
	if err != nil {
		t.Fatal(err)
	}
	processID, err := manager.Select(context.Background(), validService(), 500)
	if err != nil || processID <= 0 {
		t.Fatalf("process=%d err=%v", processID, err)
	}
	relay, err := manager.Open(context.Background(), processID)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := relay.Copy(&output); err != nil || !bytes.Equal(output.Bytes(), []byte{0x47, 0x01, 0x47, 0x02}) {
		t.Fatalf("output=%x err=%v", output.Bytes(), err)
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
	stream.mu.Lock()
	request, lease := stream.requests[0], stream.leases[0]
	stream.mu.Unlock()
	if request.Usage != providerstream.UsageLive || request.Target.Opaque != "1003" ||
		request.PriorityPolicy != "0" || !request.RequireDescrambled || !lease.cancelled.Load() || !lease.closed.Load() {
		t.Fatalf("request=%+v cancelled=%v closed=%v", request, lease.cancelled.Load(), lease.closed.Load())
	}
	if _, err := manager.Open(context.Background(), processID); err == nil {
		t.Fatal("使用済みprocess IDを再利用しました")
	}
	if _, err := manager.Select(context.Background(), validService(), 501); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityReplacementExpirationAndCloseAll(t *testing.T) {
	manager, _ := NewManager(fixedResolver{}, &fakeProvider{})
	manager.selectionLifetime = 20 * time.Millisecond
	processes := make(map[int32]struct{})
	for id := int32(500); id < 500+MaximumSessions; id++ {
		processID, err := manager.Select(context.Background(), validService(), id)
		if err != nil {
			t.Fatal(err)
		}
		processes[processID] = struct{}{}
	}
	if len(processes) != MaximumSessions {
		t.Fatalf("processes=%v", processes)
	}
	if _, err := manager.Select(context.Background(), validService(), 900); err == nil {
		t.Fatal("5件目を受理しました")
	}
	replaced, err := manager.Select(context.Background(), validService(), 500)
	if err != nil {
		t.Fatal(err)
	}
	if _, duplicate := processes[replaced]; duplicate {
		t.Fatalf("process IDを早期再利用しました: %d", replaced)
	}
	manager.Close(9999)
	manager.Close(501)
	if _, err := manager.Select(context.Background(), validService(), 900); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	manager.mu.Lock()
	remaining := len(manager.byNetwork)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired selections=%d", remaining)
	}
	manager.CloseAll()
}

func TestSelectionAndProviderFailuresDoNotLeak(t *testing.T) {
	manager, _ := NewManager(fixedResolver{fail: true}, &fakeProvider{})
	if _, err := manager.Select(context.Background(), validService(), 500); err == nil {
		t.Fatal("未知serviceを受理しました")
	}
	stream := &fakeProvider{fail: true}
	manager, _ = NewManager(fixedResolver{}, stream)
	processID, _ := manager.Select(context.Background(), validService(), 500)
	if _, err := manager.Open(context.Background(), processID); err == nil {
		t.Fatal("provider失敗を受理しました")
	}
	manager.mu.Lock()
	remaining := len(manager.byNetwork)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining=%d", remaining)
	}
}

func TestCloseCancelsProviderOpen(t *testing.T) {
	block := make(chan struct{})
	stream := &fakeProvider{block: block}
	manager, _ := NewManager(fixedResolver{}, stream)
	processID, _ := manager.Select(context.Background(), validService(), 500)
	result := make(chan error, 1)
	go func() {
		_, err := manager.Open(context.Background(), processID)
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	manager.Close(500)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled open succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("CloseでProvider openが解除されません")
	}
}

func TestCloseCancelsStreamingRead(t *testing.T) {
	manager, _ := NewManager(fixedResolver{}, blockingProvider{})
	processID, _ := manager.Select(context.Background(), validService(), 500)
	relay, err := manager.Open(context.Background(), processID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		copyErr := relay.Copy(&bytes.Buffer{})
		_ = relay.Close()
		result <- copyErr
	}()
	manager.Close(500)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("取消した配信が成功終了しました")
		}
	case <-time.After(time.Second):
		t.Fatal("Closeでstream readが解除されません")
	}
}

func TestRelayLifetimeCancelsRead(t *testing.T) {
	stream := &fakeProvider{}
	manager, _ := NewManager(fixedResolver{}, stream)
	manager.relayLifetime = 20 * time.Millisecond
	processID, _ := manager.Select(context.Background(), validService(), 500)
	relay, err := manager.Open(context.Background(), processID)
	if err != nil {
		t.Fatal(err)
	}
	stream.mu.Lock()
	stream.leases[0].chunks = nil
	stream.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	if err := relay.Copy(&bytes.Buffer{}); err == nil {
		t.Fatal("期限到達後のreadが成功しました")
	}
	_ = relay.Close()
}
