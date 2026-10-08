package protocolmux

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

const testWait = 5 * time.Second

func startServe(t *testing.T, ctx context.Context, tlsConfig *tls.Config) (string, chan net.Conn, chan net.Conn) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })
	respConns := make(chan net.Conn, 4)
	httpConns := make(chan net.Conn, 4)
	go func() {
		_ = Serve(ctx, listener, nil, func(conn net.Conn) { respConns <- conn }, func(conn net.Conn) { httpConns <- conn }, tlsConfig)
	}()
	return listener.Addr().String(), respConns, httpConns
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, errDial := net.Dial("tcp", addr)
	if errDial != nil {
		t.Fatalf("dial: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func receive(t *testing.T, conns chan net.Conn, what string) net.Conn {
	t.Helper()
	select {
	case conn := <-conns:
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	case <-time.After(testWait):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

func readFirstByte(t *testing.T, conn net.Conn) byte {
	t.Helper()
	buf := make([]byte, 1)
	if _, errRead := io.ReadFull(conn, buf); errRead != nil {
		t.Fatalf("read routed conn: %v", errRead)
	}
	return buf[0]
}

func expectClosedByServer(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(testWait))
	_, errRead := conn.Read(make([]byte, 1))
	if errRead == nil {
		t.Fatal("expected server to close the connection")
	}
	if netErr, ok := errRead.(net.Error); ok && netErr.Timeout() {
		t.Fatal("server did not close the connection")
	}
}

func TestServeSilentClientDoesNotBlockLaterConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, respConns, _ := startServe(t, ctx, nil)

	dial(t, addr) // Connects but never sends a byte.
	active := dial(t, addr)
	if _, errWrite := active.Write([]byte("*1\r\n$4\r\nPING\r\n")); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if got := readFirstByte(t, receive(t, respConns, "RESP conn behind a silent client")); got != '*' {
		t.Fatalf("RESP conn first byte = %q, want '*'", got)
	}
}

func TestServeClosesUnclassifiedConnectionsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addr, _, _ := startServe(t, ctx, nil)
	silent := dial(t, addr)
	cancel()
	expectClosedByServer(t, silent)
}

func TestServeDropsStalledTLSHandshake(t *testing.T) {
	previous := tlsHandshakeTimeout
	tlsHandshakeTimeout = 50 * time.Millisecond
	defer func() { tlsHandshakeTimeout = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, _, _ := startServe(t, ctx, testTLSConfig(t))

	stalled := dial(t, addr)
	if _, errWrite := stalled.Write([]byte{0x16}); errWrite != nil { // Partial ClientHello.
		t.Fatalf("write: %v", errWrite)
	}
	expectClosedByServer(t, stalled)
}

func TestServeRoutesHTTPAndTLSRESP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, respConns, httpConns := startServe(t, ctx, testTLSConfig(t))

	plain := dial(t, addr)
	if _, errWrite := plain.Write([]byte("GET / HTTP/1.1\r\n\r\n")); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if got := readFirstByte(t, receive(t, httpConns, "plain HTTP conn")); got != 'G' {
		t.Fatalf("HTTP conn first byte = %q, want 'G'", got)
	}

	secure := tls.Client(dial(t, addr), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // Test-only self-signed certificate.
	if _, errWrite := secure.Write([]byte("*1\r\n$4\r\nPING\r\n")); errWrite != nil {
		t.Fatalf("tls write: %v", errWrite)
	}
	if got := readFirstByte(t, receive(t, respConns, "TLS RESP conn")); got != '*' {
		t.Fatalf("TLS RESP conn first byte = %q, want '*'", got)
	}
}

func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, errKey := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if errKey != nil {
		t.Fatalf("generate key: %v", errKey)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "protocolmux-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, errCert := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if errCert != nil {
		t.Fatalf("create certificate: %v", errCert)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}
