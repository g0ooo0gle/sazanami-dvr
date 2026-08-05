package webui

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

// ValidateListenAddressはnumeric loopback IPと有効なTCP portだけを受理する。
func ValidateListenAddress(address string, allowTestPortZero bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return errors.New("webui: invalid listen address")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return errors.New("webui: listen address is not loopback")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 || (number == 0 && !allowTestPortZero) {
		return errors.New("webui: invalid listen port")
	}
	return nil
}

// NewServerはhard capとtimeoutを固定したHTTP serverを返す。待受自体は開始しない。
func NewServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
}
