package fake

import "sync"

// ManualClockはsleepを行わない、再現可能な単調時刻sourceである。
type ManualClock struct {
	mu    sync.RWMutex
	nanos int64
}

// NewManualClockは指定nanosecond値を初期値としてManualClockを生成する。
func NewManualClock(initialNanos int64) *ManualClock {
	return &ManualClock{nanos: initialNanos}
}

// Nowは現在の単調時刻をnanosecond単位で返す。
func (c *ManualClock) Now() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nanos
}

// Advanceは時計を前方へ進める。負数とint64 overflowは拒否する。
func (c *ManualClock) Advance(deltaNanos int64) error {
	if deltaNanos < 0 {
		return providerError("negative-clock-advance")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if deltaNanos > 0 && c.nanos > (1<<63-1)-deltaNanos {
		return providerError("clock-overflow")
	}
	c.nanos += deltaNanos
	return nil
}
