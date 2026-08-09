package recordinghttp

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

// ValidateListenAddressはnumeric loopback、private IP、全interfaceと有効なTCP portだけを受理する。
func ValidateListenAddress(address string, allowTestPortZero bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return errors.New("recordinghttp: invalid listen address")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || (!ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified()) || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return errors.New("recordinghttp: listen address is not local")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 || (number == 0 && !allowTestPortZero) {
		return errors.New("recordinghttp: invalid listen port")
	}
	return nil
}

// ValidateListenerは作成後のlistenerも許可済みaddressへbindされたか確認する。
func ValidateListener(listener net.Listener, allowTestPortZero bool) error {
	if listener == nil {
		return errors.New("recordinghttp: nil listener")
	}
	return ValidateListenAddress(listener.Addr().String(), allowTestPortZero)
}

// NewServerは録画の長時間配信を妨げず、要求headerと読取期限を固定したHTTP serverを返す。
func NewServer(address string, handler http.Handler) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
}
