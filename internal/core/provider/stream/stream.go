// Package streamはpull型stream lease Portを定義する。
package stream

import (
	"context"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

// UsageClassはstreamを開く業務目的を表す。
type UsageClass string

const UsageRecording UsageClass = "RECORDING"

// Requestはstreamを開くtarget、利用目的、期限、相関IDを指定する。
type Request struct {
	Target             provider.TuningTarget
	Usage              UsageClass
	PriorityPolicy     string
	RequireDescrambled bool
	CorrelationID      string
	MonotonicDeadline  provider.MonotonicDeadline
}

// TerminalReasonはstreamが継続中または終了した理由を表す。
type TerminalReason string

const (
	TerminalActive    TerminalReason = "ACTIVE"
	TerminalCleanEnd  TerminalReason = "CLEAN_END"
	TerminalEarlyEOF  TerminalReason = "EARLY_EOF"
	TerminalTimeout   TerminalReason = "TIMEOUT"
	TerminalRejected  TerminalReason = "REJECTED"
	TerminalCancelled TerminalReason = "CANCELLED"
	TerminalPeer      TerminalReason = "PEER_FAILURE"
)

// TerminalはRead後のstream終端状態を返す。
type Terminal struct {
	Done   bool
	Reason TerminalReason
}

// Leaseはbounded bufferへpullし、cancelとcloseを明示するstream resourceである。
type Lease interface {
	Read(context.Context, []byte) (int, Terminal, error)
	Cancel() error
	Close() error
}

// Providerは指定targetのstream leaseを開くPortである。
type Provider interface {
	OpenStream(context.Context, Request) (Lease, error)
}
