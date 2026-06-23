package daemon

import "net"

// Transport abstracts the IPC mechanism between the vix TUI and the vixd daemon.
//
// Today the daemon listens on a Unix domain socket (AF_UNIX) and clients dial
// it, with one site type-asserting the connection to *net.UnixConn to half-close
// the write side (CloseWrite) so the daemon sees EOF. That mechanism is
// POSIX-only. This interface formalizes the seam so a later wave can slot in a
// Windows named-pipe backend without touching the 5 call sites.
//
// Wave 0 ships only the Unix (AF_UNIX) implementation plus a Windows compile
// stub; it adds no Windows backend and changes no Unix behaviour.
type Transport interface {
	// Listen binds the transport's local address (a socket path on Unix).
	Listen(addr string) (net.Listener, error)
	// Dial connects to an address previously Listen'd on.
	Dial(addr string) (net.Conn, error)
}

// transport is the Transport used by all call sites. It is bound to the
// platform-appropriate implementation by the per-OS transport_*.go file.
var transport Transport

// transportListen/transportDial are thin wrappers over the active Transport so
// the existing call sites stay one-liners.
func transportListen(addr string) (net.Listener, error) {
	return transport.Listen(addr)
}

func transportDial(addr string) (net.Conn, error) {
	return transport.Dial(addr)
}
