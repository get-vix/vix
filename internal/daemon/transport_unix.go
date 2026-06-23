//go:build !windows

package daemon

import "net"

func init() {
	transport = unixTransport{}
}

// unixTransport speaks AF_UNIX over a filesystem socket path. Dial returns a
// *net.UnixConn (as net.Conn), so callers that need the write-half close can
// still type-assert to *net.UnixConn and call CloseWrite — preserving the prior
// inline behaviour at the one-shot RPC site.
type unixTransport struct{}

func (unixTransport) Listen(addr string) (net.Listener, error) {
	return net.Listen("unix", addr)
}

func (unixTransport) Dial(addr string) (net.Conn, error) {
	return net.Dial("unix", addr)
}
