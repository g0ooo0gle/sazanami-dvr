// Package ctrlcmdは認証なしのCtrlCmd受付と、接続ごとの固定上限を提供する。
package ctrlcmd

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
)

// FrameHandlerは検証対象frameを1件処理し、responseを出力先へ書く境界である。
type FrameHandler interface {
	Handle(context.Context, []byte, io.Writer) error
}

// LongLivedFrameHandlerは通常の一問一答より長く同じ接続を使う要求だけを識別する。
// 要求の解釈と実際の終了上限はhandler側が所有する。
type LongLivedFrameHandler interface {
	LongLived([]byte) bool
}

// Metricsはprocess内で保持するbounded serverの累積counterである。
type Metrics struct {
	Accepted  uint64
	Rejected  uint64
	Completed uint64
	Failed    uint64
	Active    int64
}

// Serverは接続数と同時handler数を別々の上限で管理する。
type Server struct {
	config      Config
	handler     FrameHandler
	connections chan struct{}
	handlers    chan struct{}
	wait        sync.WaitGroup
	accepted    atomic.Uint64
	rejected    atomic.Uint64
	completed   atomic.Uint64
	failed      atomic.Uint64
	active      atomic.Int64
}

// NewServerは設定とhandlerを検証し、socketをまだ作らずにServerを生成する。
func NewServer(config Config, handler FrameHandler) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, errors.New("ctrlcmd: nil frame handler")
	}
	return &Server{
		config:      config,
		handler:     handler,
		connections: make(chan struct{}, config.MaxConnections),
		handlers:    make(chan struct{}, config.MaxHandlers),
	}, nil
}

// Listenはsocket作成前の設定値と、作成後の実bind addressをともに検証する。
func (s *Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", s.config.Address)
	if err != nil {
		return nil, err
	}
	if err := validateBoundAddress(listener.Addr(), s.config.AllowLAN); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// Serveはcontextがcancelされるかlistenerが閉じるまで、許可済みaddressから上限付きで接続を受け付ける。
// listenerのCloseは呼び出し元の責任とする。Acceptを確実に解除するにはCloseを使用する。
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if ctx == nil || listener == nil {
		return errors.New("ctrlcmd: nil serve dependency")
	}
	if err := validateBoundAddress(listener.Addr(), s.config.AllowLAN); err != nil {
		return err
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case s.connections <- struct{}{}:
			s.accepted.Add(1)
			s.active.Add(1)
			s.wait.Add(1)
			go s.serveConnection(ctx, connection)
		default:
			s.rejected.Add(1)
			_ = connection.Close()
		}
	}
}

func (s *Server) serveConnection(parent context.Context, connection net.Conn) {
	defer func() {
		_ = connection.Close()
		<-s.connections
		s.active.Add(-1)
		s.wait.Done()
	}()
	// 1接続につき1 request・1 responseで終了する。接続全体の絶対期限を延長しない。
	deadline := time.Now().Add(s.config.ConnectionLifetime)
	if err := connection.SetDeadline(deadline); err != nil {
		s.failed.Add(1)
		return
	}
	if headerDeadline := time.Now().Add(s.config.HeaderTimeout); headerDeadline.Before(deadline) {
		if err := connection.SetReadDeadline(headerDeadline); err != nil {
			s.failed.Add(1)
			return
		}
	}
	var header [codec.HeaderSize]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		s.failed.Add(1)
		return
	}
	declared := int32(binary.LittleEndian.Uint32(header[4:8]))
	if declared < 0 || int64(declared) > int64(s.config.MaxRequestBody) {
		s.failed.Add(1)
		return
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		s.failed.Add(1)
		return
	}
	request := make([]byte, codec.HeaderSize+int(declared))
	copy(request, header[:])
	if _, err := io.ReadFull(connection, request[codec.HeaderSize:]); err != nil {
		s.failed.Add(1)
		return
	}
	select {
	case s.handlers <- struct{}{}:
		defer func() { <-s.handlers }()
	default:
		s.rejected.Add(1)
		return
	}
	requestContext := parent
	var destination io.Writer = connection
	var cancel context.CancelFunc
	longLived, ok := s.handler.(LongLivedFrameHandler)
	if ok && longLived.LongLived(request) {
		if err := connection.SetDeadline(time.Time{}); err != nil {
			s.failed.Add(1)
			return
		}
		requestContext, cancel = context.WithCancel(parent)
		destination = rollingDeadlineWriter{connection: connection, timeout: LongWriteTimeout}
	} else {
		requestContext, cancel = context.WithDeadline(parent, deadline)
	}
	defer cancel()
	if err := s.handler.Handle(requestContext, request, destination); err != nil {
		s.failed.Add(1)
		return
	}
	s.completed.Add(1)
}

type rollingDeadlineWriter struct {
	connection net.Conn
	timeout    time.Duration
}

// Writeは長時間接続の各送信へ進捗期限を設定し直し、受信停止したclientを上限内で解放する。
func (writer rollingDeadlineWriter) Write(data []byte) (int, error) {
	if err := writer.connection.SetWriteDeadline(time.Now().Add(writer.timeout)); err != nil {
		return 0, err
	}
	return writer.connection.Write(data)
}

// Waitは受付済みconnectionの処理終了を待つ。先にlistenerを閉じてServeを終了させる必要がある。
func (s *Server) Wait() { s.wait.Wait() }

// Metricsは現在のcounterをatomicにsnapshotして返す。
func (s *Server) Metrics() Metrics {
	return Metrics{
		Accepted: s.accepted.Load(), Rejected: s.rejected.Load(), Completed: s.completed.Load(),
		Failed: s.failed.Load(), Active: s.active.Load(),
	}
}
