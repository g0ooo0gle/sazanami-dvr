// Package liverelayはCtrlCmdやMirakurunの形式から独立した、一時的なライブ利用を管理する。
package liverelay

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

const (
	// MaximumSessionsはKonomiTVとKomorebiが同時に利用できるライブ接続数である。
	MaximumSessions = 4
	// SelectionLifetimeはチャンネル選択後に映像受信を始められる時間である。
	SelectionLifetime = 30 * time.Second
	// RelayLifetimeは一つのライブ接続を保持できる最長時間である。
	RelayLifetime = 12 * time.Hour
)

// ServiceはCtrlCmd境界で指定された一つの放送サービスを表す。
type Service struct {
	NetworkID         uint16
	TransportStreamID uint16
	ServiceID         uint16
}

// Resolverは公開済みチャンネルからProviderの接続先を一意に解決する。
type Resolver interface {
	ResolveLiveService(context.Context, uint16, uint16, uint16) (provider.TuningTarget, error)
}

// ManagerはNetworkTV IDと内部process IDを上限付きで対応付ける。
// 状態はprocess内だけに保持し、CloseAll後に利用を残さない。
type Manager struct {
	resolver Resolver
	provider providerstream.Provider

	mu                sync.Mutex
	byNetwork         map[int32]*selection
	byProcess         map[int32]*selection
	next              int32
	selectionLifetime time.Duration
	relayLifetime     time.Duration
}

type selection struct {
	networkID int32
	processID int32
	target    provider.TuningTarget
	timer     *time.Timer
	cancel    context.CancelFunc
	streaming bool
}

// NewManagerは依存を検証し、まだProviderへ接続せずに利用管理を作る。
func NewManager(resolver Resolver, streamProvider providerstream.Provider) (*Manager, error) {
	if resolver == nil || streamProvider == nil {
		return nil, stable("live-dependency-missing")
	}
	return &Manager{
		resolver: resolver, provider: streamProvider,
		byNetwork: make(map[int32]*selection, MaximumSessions),
		byProcess: make(map[int32]*selection, MaximumSessions), next: 1,
		selectionLifetime: SelectionLifetime, relayLifetime: RelayLifetime,
	}, nil
}

// Selectは放送サービスを照合し、30秒だけ有効な正のprocess IDを割り当てる。
// 同じNetworkTV IDの古い利用は取り消して置き換える。
func (manager *Manager) Select(ctx context.Context, service Service, networkTVID int32) (int32, error) {
	if manager == nil || ctx == nil || networkTVID <= 0 || service.NetworkID == 0 ||
		service.TransportStreamID == 0 || service.ServiceID == 0 {
		return 0, stable("live-selection-invalid")
	}
	target, err := manager.resolver.ResolveLiveService(ctx, service.NetworkID, service.TransportStreamID, service.ServiceID)
	if err != nil {
		return 0, stable("live-service-unavailable")
	}

	manager.mu.Lock()
	previous := manager.byNetwork[networkTVID]
	if previous == nil && len(manager.byNetwork) >= MaximumSessions {
		manager.mu.Unlock()
		return 0, stable("live-capacity-reached")
	}
	if previous != nil {
		manager.removeLocked(previous)
	}
	processID, err := manager.nextProcessIDLocked()
	if err != nil {
		manager.mu.Unlock()
		manager.cancel(previous)
		return 0, err
	}
	selected := &selection{networkID: networkTVID, processID: processID, target: target}
	manager.byNetwork[networkTVID] = selected
	manager.byProcess[processID] = selected
	selected.timer = time.AfterFunc(manager.selectionLifetime, func() { manager.expire(selected) })
	manager.mu.Unlock()
	manager.cancel(previous)
	return processID, nil
}

func (manager *Manager) nextProcessIDLocked() (int32, error) {
	for attempts := 0; attempts <= MaximumSessions; attempts++ {
		candidate := manager.next
		manager.next++
		if manager.next <= 0 {
			manager.next = 1
		}
		if candidate > 0 && manager.byProcess[candidate] == nil {
			return candidate, nil
		}
	}
	return 0, stable("live-process-id-unavailable")
}

func (manager *Manager) expire(selected *selection) {
	manager.mu.Lock()
	if manager.byProcess[selected.processID] == selected && !selected.streaming {
		manager.removeLocked(selected)
	}
	manager.mu.Unlock()
}

// Streamは開始済みProvider leaseを一つの出力先へ転送し、明示的に閉じる境界である。
type Stream interface {
	Copy(io.Writer) error
	Close() error
}

// Openは未使用のprocess IDを一度だけSTREAMINGへ進め、Provider streamを開く。
// Provider開始に失敗した利用は表から取り除き、次の利用へ枠を返す。
func (manager *Manager) Open(ctx context.Context, processID int32) (Stream, error) {
	if manager == nil || ctx == nil || processID <= 0 {
		return nil, stable("live-process-invalid")
	}
	manager.mu.Lock()
	selected := manager.byProcess[processID]
	if selected == nil || selected.streaming {
		manager.mu.Unlock()
		return nil, stable("live-process-unavailable")
	}
	selected.streaming = true
	if selected.timer != nil {
		selected.timer.Stop()
	}
	streamContext, cancel := context.WithTimeout(ctx, manager.relayLifetime)
	selected.cancel = cancel
	manager.mu.Unlock()

	lease, err := manager.provider.OpenStream(streamContext, providerstream.Request{
		Target: selected.target, Usage: providerstream.UsageLive, PriorityPolicy: "0",
		RequireDescrambled: true, CorrelationID: "live-relay",
	})
	if err != nil {
		cancel()
		manager.release(selected)
		return nil, stable("live-provider-unavailable")
	}
	manager.mu.Lock()
	active := manager.byProcess[processID] == selected && selected.streaming
	manager.mu.Unlock()
	if !active {
		_ = lease.Cancel()
		_ = lease.Close()
		cancel()
		return nil, stable("live-process-unavailable")
	}
	return &relay{manager: manager, selected: selected, lease: lease, ctx: streamContext}, nil
}

// CloseはNetworkTV IDに対応する開始待ちまたは配信中の利用を取り消す。
// 未使用または終了済みIDにも成功するため、開始前の掃除として安全に再送できる。
func (manager *Manager) Close(networkTVID int32) {
	if manager == nil || networkTVID <= 0 {
		return
	}
	manager.mu.Lock()
	selected := manager.byNetwork[networkTVID]
	if selected != nil {
		manager.removeLocked(selected)
	}
	manager.mu.Unlock()
	manager.cancel(selected)
}

// CloseAllはprocess停止時にtimerと配信contextをすべて取り消す。
func (manager *Manager) CloseAll() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	selected := make([]*selection, 0, len(manager.byNetwork))
	for _, current := range manager.byNetwork {
		selected = append(selected, current)
		manager.removeLocked(current)
	}
	manager.mu.Unlock()
	for _, current := range selected {
		manager.cancel(current)
	}
}

func (manager *Manager) removeLocked(selected *selection) {
	if selected == nil {
		return
	}
	if manager.byNetwork[selected.networkID] == selected {
		delete(manager.byNetwork, selected.networkID)
	}
	if manager.byProcess[selected.processID] == selected {
		delete(manager.byProcess, selected.processID)
	}
	if selected.timer != nil {
		selected.timer.Stop()
	}
}

func (manager *Manager) cancel(selected *selection) {
	if selected != nil && selected.cancel != nil {
		selected.cancel()
	}
}

func (manager *Manager) release(selected *selection) {
	manager.mu.Lock()
	manager.removeLocked(selected)
	manager.mu.Unlock()
}

type relay struct {
	manager  *Manager
	selected *selection
	lease    providerstream.Lease
	ctx      context.Context
	once     sync.Once
}

// Copyは固定bufferでProviderから読み、受理されたbyteだけを同じ順序で書く。
func (relay *relay) Copy(destination io.Writer) error {
	if relay == nil || relay.lease == nil || destination == nil {
		return stable("live-relay-invalid")
	}
	buffer := make([]byte, provider.MaxStreamChunk)
	for {
		read, terminal, err := relay.lease.Read(relay.ctx, buffer)
		if read < 0 || read > len(buffer) || read == 0 && err == nil && !terminal.Done {
			return stable("live-provider-read-invalid")
		}
		if read > 0 {
			if writeErr := writeAll(destination, buffer[:read]); writeErr != nil {
				return stable("live-client-write-failed")
			}
		}
		if err != nil {
			return stable("live-provider-read-failed")
		}
		if terminal.Done {
			return nil
		}
	}
}

func writeAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if written < 0 || written > len(data) {
			return errors.New("invalid write count")
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

// Closeは配信を取り消し、Provider leaseと利用枠を解放する。
func (relay *relay) Close() error {
	if relay == nil {
		return nil
	}
	var result error
	relay.once.Do(func() {
		if relay.selected.cancel != nil {
			relay.selected.cancel()
		}
		cancelErr := relay.lease.Cancel()
		closeErr := relay.lease.Close()
		relay.manager.release(relay.selected)
		result = errors.Join(cancelErr, closeErr)
	})
	return result
}

type stable string

// Errorは接続先や放送情報を含まない固定理由を返す。
func (err stable) Error() string { return string(err) }
