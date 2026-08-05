package fake

import (
	"context"
	"sync"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

// StreamProviderはScenarioのstep列をpull型leaseとして返すFake Portである。
type StreamProvider struct{ runtime *runtimeState }

// OpenStreamはtargetと相関IDを検証し、同時open上限内でleaseを生成する。
func (p *StreamProvider) OpenStream(ctx context.Context, request providerstream.Request) (providerstream.Lease, error) {
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	if request.Target.Opaque == "" || request.CorrelationID == "" {
		return nil, malformed("invalid-stream-request")
	}
	for name, value := range map[string]string{
		"stream-target": request.Target.Opaque, "stream-correlation": request.CorrelationID, "priority-policy": request.PriorityPolicy,
	} {
		if err := validateText(value, provider.MaxDiagnosticBytes, name); err != nil {
			return nil, err
		}
	}
	if expected := p.runtime.scenario.expected.StreamTarget; expected != "" && expected != request.Target.Opaque {
		return nil, provider.NewFailure(provider.ReasonRejected, "stream-request-mismatch")
	}
	if failure := p.runtime.scenario.fault(FaultStreamOpen, 0); failure != nil {
		return nil, failure
	}
	p.runtime.mu.Lock()
	defer p.runtime.mu.Unlock()
	if err := p.runtime.acquireLocked("stream-open"); err != nil {
		return nil, err
	}
	p.runtime.counters.StreamOpens++
	return &streamLease{runtime: p.runtime, request: request}, nil
}

type streamLease struct {
	mu        sync.Mutex
	runtime   *runtimeState
	request   providerstream.Request
	step      int
	pending   []byte
	offset    int
	closed    bool
	cancelled bool
	settled   bool
	terminal  providerstream.Terminal
}

// Readはdestination上限内でScenarioを1 stepずつ進め、終端理由を明示する。
func (l *streamLease) Read(ctx context.Context, destination []byte) (int, providerstream.Terminal, error) {
	if len(destination) == 0 {
		return 0, providerstream.Terminal{}, malformed("empty-read-buffer")
	}
	if len(destination) > provider.MaxStreamChunk {
		return 0, providerstream.Terminal{}, overLimit("read-buffer")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled})
		return 0, l.terminal, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, l.terminal, provider.NewFailure(provider.ReasonRejected, "stream-lease-closed")
	}
	if l.settled {
		return 0, l.terminal, nil
	}
	if l.request.MonotonicDeadline.Enabled && l.runtime.clock.Now() >= l.request.MonotonicDeadline.Nanos {
		l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalTimeout})
		return 0, l.terminal, provider.NewFailure(provider.ReasonTimeout, "stream-deadline")
	}
	for {
		if l.offset < len(l.pending) {
			n := copy(destination, l.pending[l.offset:])
			if err := l.recordReadLocked(n); err != nil {
				settleErr := l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer})
				if settleErr != nil {
					return 0, l.terminal, settleErr
				}
				return 0, l.terminal, err
			}
			l.offset += n
			if l.offset == len(l.pending) {
				l.pending = nil
				l.offset = 0
			}
			return n, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
		}
		step, exists := l.runtime.scenario.streamStep(l.step)
		if !exists {
			l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalCleanEnd})
			return 0, l.terminal, nil
		}
		switch step.Kind {
		case StepChunk:
			l.pending = step.Data
			l.step++
		case StepStall:
			if l.runtime.clock.Now() < step.ReleaseAt {
				if err := l.recordReadLocked(0); err != nil {
					_ = l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer})
					return 0, l.terminal, err
				}
				return 0, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
			}
			l.step++
		case StepCleanEnd:
			l.step++
			l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalCleanEnd})
			return 0, l.terminal, nil
		case StepEarlyEOF:
			l.step++
			l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalEarlyEOF})
			return 0, l.terminal, provider.NewFailure(provider.ReasonEarlyEOF, step.Diagnostic)
		case StepFailure:
			l.step++
			reason := step.Reason
			if reason == "" {
				reason = provider.ReasonUnavailable
			}
			l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer})
			return 0, l.terminal, provider.NewFailure(reason, step.Diagnostic)
		default:
			l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer})
			return 0, l.terminal, provider.NewFailure(provider.ReasonInternal, "unknown-stream-step")
		}
	}
}

func (l *streamLease) recordReadLocked(size int) error {
	l.runtime.mu.Lock()
	defer l.runtime.mu.Unlock()
	return l.runtime.recordLocked("stream-read", size)
}

func (l *streamLease) settleLocked(terminal providerstream.Terminal) error {
	if l.settled {
		return nil
	}
	l.settled = true
	l.terminal = terminal
	l.runtime.mu.Lock()
	err := l.runtime.releaseLocked("stream-release")
	l.runtime.mu.Unlock()
	return err
}

// CancelはidempotentにleaseをCancelled終端へ遷移させる。
func (l *streamLease) Cancel() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancelled {
		return nil
	}
	l.cancelled = true
	l.runtime.mu.Lock()
	l.runtime.counters.Cancels++
	recordErr := l.runtime.recordLocked("stream-cancel", l.step)
	l.runtime.mu.Unlock()
	settleErr := l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled})
	if recordErr != nil {
		return recordErr
	}
	return settleErr
}

// Closeはidempotentにresourceを解放し、未終端ならCancelledとしてsettleする。
func (l *streamLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.runtime.mu.Lock()
	l.runtime.counters.Closes++
	recordErr := l.runtime.recordLocked("stream-close", l.step)
	l.runtime.mu.Unlock()
	var settleErr error
	if !l.settled {
		settleErr = l.settleLocked(providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled})
	}
	if recordErr != nil {
		return recordErr
	}
	return settleErr
}
