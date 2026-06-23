//go:build windows

package daemon

import (
	"errors"
	"net"
)

func init() {
	transport = windowsTransport{}
}

// windowsTransport is a Wave 0 compile stub. The real Windows backend (a named
// pipe at \\.\pipe\vixd) arrives in a later wave. The daemon does not yet run
// on Windows, so returning an error keeps the package compiling without adding
// a backend or changing Unix behaviour.
type windowsTransport struct{}

var errWindowsTransport = errors.New("vix: named-pipe transport is not yet supported on Windows")

func (windowsTransport) Listen(addr string) (net.Listener, error) {
	return nil, errWindowsTransport
}

func (windowsTransport) Dial(addr string) (net.Conn, error) {
	return nil, errWindowsTransport
}
