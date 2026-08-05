package webui

import (
	"net/http"
	"testing"
	"time"
)

func TestValidateListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:40772", "[::1]:40772", "127.1.2.3:1"} {
		if err := ValidateListenAddress(address, false); err != nil {
			t.Errorf("accept %q: %v", address, err)
		}
	}
	if err := ValidateListenAddress("127.0.0.1:0", true); err != nil {
		t.Fatalf("test port 0: %v", err)
	}
	for _, address := range []string{
		"127.0.0.1:0", "0.0.0.0:40772", "192.0.2.39:40772", "localhost:40772",
		"127.0.0.1", "127.0.0.1:-1", "127.0.0.1:65536", "unix:/tmp/ui.sock",
	} {
		if err := ValidateListenAddress(address, false); err == nil {
			t.Errorf("accepted unsafe address %q", address)
		}
	}
}

func TestServerHardLimits(t *testing.T) {
	server := NewServer("127.0.0.1:40772", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second ||
		server.WriteTimeout != 15*time.Second || server.IdleTimeout != 30*time.Second ||
		server.MaxHeaderBytes != 16*1024 {
		t.Fatalf("unexpected server limits: %+v", server)
	}
}
