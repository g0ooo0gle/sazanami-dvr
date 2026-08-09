package recording

import (
	"context"
	"errors"
	"sync"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// stopRegistryは実行中録画一件の取消し関数を内部予約IDで引く、小さい通知表である。
// 停止の正本はDBであり、この表は確定済み要求の反映を早めるためだけに使う。
type stopRegistry struct {
	mu      sync.Mutex
	maximum int
	items   map[catalogmodel.ID]context.CancelFunc
}

func newStopRegistry(maximum int) *stopRegistry {
	return &stopRegistry{maximum: maximum, items: make(map[catalogmodel.ID]context.CancelFunc, maximum)}
}

func (registry *stopRegistry) register(id catalogmodel.ID, cancel context.CancelFunc) (func(), error) {
	if registry == nil || id == (catalogmodel.ID{}) || cancel == nil || registry.maximum < 1 || registry.maximum > MaximumConcurrentRecordings {
		return nil, errors.New("recording: invalid stop registration")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.items[id]; exists || len(registry.items) >= registry.maximum {
		return nil, errors.New("recording: stop registration limit")
	}
	registry.items[id] = cancel
	return func() {
		registry.mu.Lock()
		delete(registry.items, id)
		registry.mu.Unlock()
	}, nil
}

func (registry *stopRegistry) notify(id catalogmodel.ID) {
	if registry == nil || id == (catalogmodel.ID{}) {
		return
	}
	registry.mu.Lock()
	if cancel := registry.items[id]; cancel != nil {
		cancel()
	}
	registry.mu.Unlock()
}
