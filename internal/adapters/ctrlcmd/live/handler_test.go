package live

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/liverelay"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

type contractResolver struct{}

func (contractResolver) ResolveLiveService(_ context.Context, networkID, transportStreamID, serviceID uint16) (provider.TuningTarget, error) {
	if networkID != 1 || transportStreamID != 2 || serviceID != 3 {
		return provider.TuningTarget{}, errors.New("unknown service")
	}
	return provider.TuningTarget{Opaque: "1003"}, nil
}

type contractProvider struct {
	request providerstream.Request
	fail    bool
}

func (stream *contractProvider) OpenStream(_ context.Context, request providerstream.Request) (providerstream.Lease, error) {
	stream.request = request
	if stream.fail {
		return nil, errors.New("provider failure")
	}
	return &contractLease{data: bytes.Repeat([]byte{0x47}, 376)}, nil
}

type contractLease struct {
	data   []byte
	read   bool
	closed bool
}

func (lease *contractLease) Read(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
	if lease.read {
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCleanEnd}, nil
	}
	lease.read = true
	return copy(destination, lease.data), providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}

func (lease *contractLease) Cancel() error { return nil }
func (lease *contractLease) Close() error  { lease.closed = true; return nil }

func newContractHandler(t *testing.T, stream *contractProvider) (Handler, *liverelay.Manager) {
	t.Helper()
	manager, err := liverelay.NewManager(contractResolver{}, stream)
	if err != nil {
		t.Fatal(err)
	}
	return Handler{Operations: manager, Limits: codec.DefaultLimits()}, manager
}

func requestFrame(command int32, body []byte) []byte {
	request := make([]byte, codec.HeaderSize+len(body))
	binary.LittleEndian.PutUint32(request[:4], uint32(command))
	binary.LittleEndian.PutUint32(request[4:8], uint32(len(body)))
	copy(request[8:], body)
	return request
}

func setChannelBody(networkTVID int32) []byte {
	body := make([]byte, 26)
	binary.LittleEndian.PutUint32(body[0:4], 26)
	binary.LittleEndian.PutUint32(body[4:8], 1)
	binary.LittleEndian.PutUint16(body[8:10], 1)
	binary.LittleEndian.PutUint16(body[10:12], 2)
	binary.LittleEndian.PutUint16(body[12:14], 3)
	binary.LittleEndian.PutUint32(body[14:18], 1)
	binary.LittleEndian.PutUint32(body[18:22], uint32(networkTVID))
	binary.LittleEndian.PutUint32(body[22:26], 2)
	return body
}

func intBody(value int32) []byte {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, uint32(value))
	return body
}

func TestFixedClientSelectRelayAndCloseContracts(t *testing.T) {
	for _, client := range []struct {
		name        string
		networkTVID int32
	}{
		{name: "KonomiTV v0.14.1", networkTVID: 500},
		{name: "Komorebi 1.1.0-beta6", networkTVID: 10_000},
	} {
		t.Run(client.name, func(t *testing.T) {
			stream := &contractProvider{}
			handler, manager := newContractHandler(t, stream)
			defer manager.CloseAll()
			var selected bytes.Buffer
			if err := handler.Handle(context.Background(), requestFrame(CommandSelect, setChannelBody(client.networkTVID)), &selected); err != nil {
				t.Fatal(err)
			}
			if selected.Len() != 12 || binary.LittleEndian.Uint32(selected.Bytes()[:4]) != 1 ||
				binary.LittleEndian.Uint32(selected.Bytes()[4:8]) != 4 {
				t.Fatalf("select response=%x", selected.Bytes())
			}
			processID := int32(binary.LittleEndian.Uint32(selected.Bytes()[8:12]))
			var relayed bytes.Buffer
			if err := handler.Handle(context.Background(), requestFrame(CommandRelay, intBody(processID)), &relayed); err != nil {
				t.Fatal(err)
			}
			if relayed.Len() != 8+376 || binary.LittleEndian.Uint32(relayed.Bytes()[:4]) != 1 ||
				binary.LittleEndian.Uint32(relayed.Bytes()[4:8]) != 0 ||
				!bytes.Equal(relayed.Bytes()[8:], bytes.Repeat([]byte{0x47}, 376)) {
				t.Fatalf("relay bytes=%d header=%x", relayed.Len(), relayed.Bytes()[:8])
			}
			if stream.request.Usage != providerstream.UsageLive || stream.request.Target.Opaque != "1003" {
				t.Fatalf("provider request=%+v", stream.request)
			}
			var closed bytes.Buffer
			if err := handler.Handle(context.Background(), requestFrame(CommandClose, intBody(client.networkTVID)), &closed); err != nil ||
				!bytes.Equal(closed.Bytes(), []byte{1, 0, 0, 0, 0, 0, 0, 0}) {
				t.Fatalf("close=%x err=%v", closed.Bytes(), err)
			}
		})
	}
}

func TestCloseUnknownIsIdempotent(t *testing.T) {
	handler, manager := newContractHandler(t, &contractProvider{})
	defer manager.CloseAll()
	for range 2 {
		var response bytes.Buffer
		if err := handler.Handle(context.Background(), requestFrame(CommandClose, intBody(9999)), &response); err != nil || response.Len() != 8 {
			t.Fatalf("response=%x err=%v", response.Bytes(), err)
		}
	}
}

func TestProviderFailureDoesNotWriteSuccessHeader(t *testing.T) {
	handler, manager := newContractHandler(t, &contractProvider{fail: true})
	defer manager.CloseAll()
	var selected bytes.Buffer
	if err := handler.Handle(context.Background(), requestFrame(CommandSelect, setChannelBody(500)), &selected); err != nil {
		t.Fatal(err)
	}
	processID := int32(binary.LittleEndian.Uint32(selected.Bytes()[8:12]))
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), requestFrame(CommandRelay, intBody(processID)), &response); err == nil || response.Len() != 0 {
		t.Fatalf("response=%x err=%v", response.Bytes(), err)
	}
}

func TestMalformedLiveRequestsAreRejected(t *testing.T) {
	handler, manager := newContractHandler(t, &contractProvider{})
	defer manager.CloseAll()
	valid := setChannelBody(500)
	cases := map[string][]byte{
		"truncated": valid[:25],
		"trailing":  append(append([]byte{}, valid...), 0),
		"extent": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint32(value[:4], 25)
			return value
		}(),
		"use sid": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint32(value[4:8], 0)
			return value
		}(),
		"network": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint16(value[8:10], 0)
			return value
		}(),
		"transport": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint16(value[10:12], 0)
			return value
		}(),
		"service": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint16(value[12:14], 0)
			return value
		}(),
		"use nwtv": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint32(value[14:18], 0)
			return value
		}(),
		"nwtv": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint32(value[18:22], 0)
			return value
		}(),
		"mode": func() []byte {
			value := append([]byte{}, valid...)
			binary.LittleEndian.PutUint32(value[22:26], 1)
			return value
		}(),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := handler.Handle(context.Background(), requestFrame(CommandSelect, body), &bytes.Buffer{}); err == nil {
				t.Fatal("不正要求を受理しました")
			}
		})
	}
	for _, command := range []int32{CommandRelay, CommandClose} {
		for _, body := range [][]byte{nil, intBody(0), intBody(-1), append(intBody(1), 0)} {
			if err := handler.Handle(context.Background(), requestFrame(command, body), &bytes.Buffer{}); err == nil {
				t.Fatalf("command=%d body=%x", command, body)
			}
		}
	}
}
