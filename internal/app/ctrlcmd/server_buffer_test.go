package ctrlcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// bufferConnection observes the socket boundary without timing-dependent packet counts.
type bufferConnection struct {
	input        *bytes.Reader
	output       bytes.Buffer
	writes       int
	writeFailure func([]byte) (int, error)
	closed       bool
	deadline     time.Time
}

func (connection *bufferConnection) Read(data []byte) (int, error) {
	return connection.input.Read(data)
}
func (connection *bufferConnection) Write(data []byte) (int, error) {
	connection.writes++
	if connection.writeFailure != nil {
		return connection.writeFailure(data)
	}
	return connection.output.Write(data)
}
func (connection *bufferConnection) Close() error { connection.closed = true; return nil }
func (*bufferConnection) LocalAddr() net.Addr     { return &net.TCPAddr{} }
func (*bufferConnection) RemoteAddr() net.Addr    { return &net.TCPAddr{} }
func (connection *bufferConnection) SetDeadline(deadline time.Time) error {
	connection.deadline = deadline
	return nil
}
func (*bufferConnection) SetReadDeadline(time.Time) error  { return nil }
func (*bufferConnection) SetWriteDeadline(time.Time) error { return nil }

func runBufferConnection(t *testing.T, ctx context.Context, handler FrameHandler, connection *bufferConnection) Metrics {
	t.Helper()
	server, err := NewServer(RecordingConfig(), handler)
	if err != nil {
		t.Fatal(err)
	}
	connection.input = bytes.NewReader(request2200())
	server.connections <- struct{}{}
	server.active.Add(1)
	server.wait.Add(1)
	server.serveConnection(ctx, connection)
	if !connection.closed || server.Metrics().Active != 0 || len(server.handlers) != 0 || len(server.connections) != 0 {
		t.Fatal("connection resources were not released")
	}
	return server.Metrics()
}

func TestNormalResponseCoalescesSmallWrites(t *testing.T) {
	for _, size := range []int{0, 1, 4095, 4096, 4097, 16384} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := bytes.Repeat([]byte{0x42}, size)
			connection := &bufferConnection{}
			metrics := runBufferConnection(t, context.Background(), handlerFunc(func(_ context.Context, _ []byte, output io.Writer) error {
				for offset := 0; offset < len(payload); offset += 2 {
					if _, err := output.Write(payload[offset:min(offset+2, len(payload))]); err != nil {
						return err
					}
				}
				return nil
			}), connection)
			if !bytes.Equal(connection.output.Bytes(), payload) {
				t.Fatal("response bytes changed")
			}
			if want := (size + 4095) / 4096; connection.writes != want {
				t.Fatalf("socket writes=%d want=%d for %d bytes", connection.writes, want, size)
			}
			if metrics.Completed != 1 || metrics.Failed != 0 {
				t.Fatalf("metrics=%+v", metrics)
			}
			if connection.deadline.IsZero() {
				t.Fatal("normal connection lost its deadline")
			}
		})
	}
}

func TestNormalResponseFlushFailureIsNotCompleted(t *testing.T) {
	for name, write := range map[string]func([]byte) (int, error){
		"error": func([]byte) (int, error) { return 0, io.ErrClosedPipe },
		"short": func(data []byte) (int, error) { return len(data) - 1, nil },
		"zero":  func([]byte) (int, error) { return 0, nil },
	} {
		t.Run(name, func(t *testing.T) {
			connection := &bufferConnection{writeFailure: write}
			handlerSucceeded := false
			metrics := runBufferConnection(t, context.Background(), handlerFunc(func(_ context.Context, _ []byte, output io.Writer) error {
				_, err := output.Write([]byte("reply"))
				handlerSucceeded = err == nil
				return err
			}), connection)
			if !handlerSucceeded || connection.writes != 1 || metrics.Completed != 0 || metrics.Failed != 1 {
				t.Fatalf("handler succeeded=%v writes=%d metrics=%+v", handlerSucceeded, connection.writes, metrics)
			}
		})
	}
}

func TestNormalResponseDiscardsBufferedTailAfterFailure(t *testing.T) {
	for _, mode := range []string{"handler-error", "canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
			}
			connection := &bufferConnection{}
			metrics := runBufferConnection(t, ctx, handlerFunc(func(_ context.Context, _ []byte, output io.Writer) error {
				if _, err := output.Write([]byte("incomplete")); err != nil {
					return err
				}
				if mode == "handler-error" {
					return errors.New("generation failed")
				}
				cancel()
				return nil
			}), connection)
			if connection.writes != 0 || metrics.Completed != 0 || metrics.Failed != 1 {
				t.Fatalf("writes=%d metrics=%+v", connection.writes, metrics)
			}
		})
	}
}

type immediateStreamHandler struct{ connection *bufferConnection }

func (immediateStreamHandler) LongLived([]byte) bool { return true }
func (handler immediateStreamHandler) Handle(_ context.Context, _ []byte, output io.Writer) error {
	for _, data := range [][]byte{{1, 0, 0, 0, 0, 0, 0, 0}, {0x47}} {
		before := handler.connection.output.Len()
		if _, err := output.Write(data); err != nil {
			return err
		}
		if handler.connection.output.Len() != before+len(data) {
			return errors.New("live data is waiting in a buffer")
		}
	}
	return nil
}

func TestLongLivedResponseStillWritesImmediately(t *testing.T) {
	connection := &bufferConnection{}
	metrics := runBufferConnection(t, context.Background(), immediateStreamHandler{connection}, connection)
	if connection.writes != 2 || metrics.Completed != 1 || metrics.Failed != 0 || !connection.deadline.IsZero() {
		t.Fatalf("writes=%d deadline=%v metrics=%+v", connection.writes, connection.deadline, metrics)
	}
}
