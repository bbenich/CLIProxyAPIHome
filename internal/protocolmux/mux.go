package protocolmux

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// IsRESPPrefix reports whether is resp prefix.
func IsRESPPrefix(prefix byte) bool {
	switch prefix {
	case '*', '$', '+', '-', ':':
		return true
	default:
		return false
	}
}

// tlsHandshakeTimeout bounds how long a client may take to finish a TLS
// handshake before its connection is dropped.
var tlsHandshakeTimeout = 10 * time.Second

// Serve accepts connections and routes each one to RESP or HTTP handling.
// Protocol detection runs per connection, off the accept loop, so a silent or
// slow client cannot block later connections. Connections still being
// classified are closed when ctx is canceled.
func Serve(ctx context.Context, listener net.Listener, httpListener *Listener, onRESPConn func(net.Conn), onHTTPConn func(net.Conn), tlsConfig *tls.Config) error {
	if listener == nil {
		return net.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return errAccept
		}
		if conn == nil {
			continue
		}
		go dispatchConn(ctx, conn, httpListener, onRESPConn, onHTTPConn, tlsConfig)
	}
}

func dispatchConn(ctx context.Context, conn net.Conn, httpListener *Listener, onRESPConn func(net.Conn), onHTTPConn func(net.Conn), tlsConfig *tls.Config) {
	stopClose := context.AfterFunc(ctx, func() { closeConn(conn, "after shutdown") })
	routed, isHTTP := classifyConn(ctx, conn, tlsConfig)
	if !stopClose() || routed == nil {
		// Shutdown already closed the connection, or classification closed it.
		return
	}
	if isHTTP {
		routeHTTPConn(routed, httpListener, onHTTPConn)
		return
	}
	if onRESPConn != nil {
		onRESPConn(routed)
		return
	}
	closeConn(routed, "without RESP handler")
}

// classifyConn completes any TLS handshake and reports whether the connection
// carries HTTP. It returns nil after closing connections that fail detection.
func classifyConn(ctx context.Context, conn net.Conn, tlsConfig *tls.Config) (net.Conn, bool) {
	// TLS listeners hand over *tls.Conn; negotiated HTTP protocols route directly.
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if !completeHandshake(ctx, tlsConn, conn) {
			return nil, false
		}
		if negotiatedHTTP(tlsConn) {
			return tlsConn, true
		}
	}

	reader := bufio.NewReader(conn)
	prefix, errPeek := reader.Peek(1)
	if errPeek != nil {
		closeConn(conn, "after peek error")
		return nil, false
	}

	if tlsConfig != nil && isTLSClientHello(prefix[0]) {
		tlsConn := tls.Server(&BufferedConn{Conn: conn, reader: reader}, tlsConfig)
		if !completeHandshake(ctx, tlsConn, conn) {
			return nil, false
		}
		if negotiatedHTTP(tlsConn) {
			return tlsConn, true
		}
		reader = bufio.NewReader(tlsConn)
		prefix, errPeek = reader.Peek(1)
		if errPeek != nil {
			closeConn(tlsConn, "after TLS peek error")
			return nil, false
		}
		conn = tlsConn
	}

	return &BufferedConn{Conn: conn, reader: reader}, !IsRESPPrefix(prefix[0])
}

func completeHandshake(ctx context.Context, tlsConn *tls.Conn, raw net.Conn) bool {
	handshakeCtx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()
	if errHandshake := tlsConn.HandshakeContext(handshakeCtx); errHandshake != nil {
		closeConn(raw, "after TLS handshake error")
		return false
	}
	return true
}

func negotiatedHTTP(tlsConn *tls.Conn) bool {
	proto := strings.TrimSpace(tlsConn.ConnectionState().NegotiatedProtocol)
	return proto == "h2" || proto == "http/1.1"
}

func routeHTTPConn(conn net.Conn, httpListener *Listener, onHTTPConn func(net.Conn)) {
	if onHTTPConn != nil {
		onHTTPConn(conn)
		return
	}
	if httpListener == nil {
		closeConn(conn, "without HTTP handler")
		return
	}
	if errPut := httpListener.Put(conn); errPut != nil {
		closeConn(conn, "after http route failure")
	}
}

func closeConn(conn net.Conn, reason string) {
	if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
		log.Errorf("protocol mux: close conn %s: %v", reason, errClose)
	}
}

func isTLSClientHello(prefix byte) bool {
	return prefix == 0x16
}

// NormalizeServeError normalizes a serve error.
func NormalizeServeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
