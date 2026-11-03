package herdr

import (
	"context"
	"net"
)

// DialFunc opens a connection to the herdr socket at path socket. Tests
// inject a net.Pipe-backed dialer (herdrtest.Server.Dialer) so they can run
// inside a testing/synctest bubble.
type DialFunc func(ctx context.Context, socket string) (net.Conn, error)
