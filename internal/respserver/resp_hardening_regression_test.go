package respserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"net"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	appconfig "github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/respserver/dispatch"
)

// deadlineTestConn is a scripted connection that records deadlines. A read with
// a pending read deadline and no buffered input fails immediately with a
// timeout, which models a silent peer whose deadline has elapsed without
// relying on wall-clock waits.
type deadlineTestConn struct {
	mu             sync.Mutex
	input          []byte
	readDeadline   time.Time
	writeDeadlines []time.Time
	output         bytes.Buffer
	wake           chan struct{}
	readWaiting    chan struct{}
	closed         chan struct{}
	closeOnce      sync.Once
	state          tls.ConnectionState
	remote         net.Addr
}

func newDeadlineTestConn(input string, mtls bool, remoteIP string) *deadlineTestConn {
	conn := &deadlineTestConn{
		input:       []byte(input),
		wake:        make(chan struct{}),
		readWaiting: make(chan struct{}, 1),
		closed:      make(chan struct{}),
		remote:      &net.TCPAddr{IP: net.ParseIP(remoteIP), Port: 40000},
	}
	if mtls {
		certificate := &x509.Certificate{
			Raw:     []byte("deadline-test-certificate-" + remoteIP),
			Subject: pkix.Name{CommonName: "deadline-test-node"},
		}
		conn.state = tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{certificate},
			VerifiedChains:   [][]*x509.Certificate{{certificate}},
		}
	}
	return conn
}

func (c *deadlineTestConn) Read(payload []byte) (int, error) {
	for {
		c.mu.Lock()
		select {
		case <-c.closed:
			c.mu.Unlock()
			return 0, io.EOF
		default:
		}
		if len(c.input) > 0 {
			count := copy(payload, c.input)
			c.input = c.input[count:]
			c.mu.Unlock()
			return count, nil
		}
		if !c.readDeadline.IsZero() {
			c.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		wake := c.wake
		select {
		case c.readWaiting <- struct{}{}:
		default:
		}
		c.mu.Unlock()
		select {
		case <-wake:
		case <-c.closed:
		}
	}
}

func (c *deadlineTestConn) Write(payload []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	return c.output.Write(payload)
}

func (c *deadlineTestConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *deadlineTestConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8327}
}

func (c *deadlineTestConn) RemoteAddr() net.Addr { return c.remote }

func (c *deadlineTestConn) SetDeadline(deadline time.Time) error {
	if errRead := c.SetReadDeadline(deadline); errRead != nil {
		return errRead
	}
	return c.SetWriteDeadline(deadline)
}

func (c *deadlineTestConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = deadline
	close(c.wake)
	c.wake = make(chan struct{})
	return nil
}

func (c *deadlineTestConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadlines = append(c.writeDeadlines, deadline)
	return nil
}

func (c *deadlineTestConn) ConnectionState() tls.ConnectionState { return c.state }

func (c *deadlineTestConn) currentReadDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readDeadline
}

func (c *deadlineTestConn) Output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

func (c *deadlineTestConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func runHandleConn(srv *Server, conn net.Conn) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.HandleConn(context.Background(), conn)
	}()
	return done
}

func waitHandleConnDone(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(respPipeDeadline):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func readPipeReply(t *testing.T, conn net.Conn) string {
	t.Helper()
	if errDeadline := conn.SetReadDeadline(time.Now().Add(respPipeDeadline)); errDeadline != nil {
		t.Fatalf("set read deadline: %v", errDeadline)
	}
	line, errRead := bufio.NewReader(conn).ReadString('\n')
	if errRead != nil {
		t.Fatalf("read reply line: %v", errRead)
	}
	return strings.TrimSuffix(line, "\r\n")
}

func TestRESPRejectsHugeArrayCountWithoutPanic(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	done := runHandleConn(New("", nil), server)

	go func() { _, _ = client.Write([]byte("*9223372036854775807\r\n")) }()
	if reply := readPipeReply(t, client); !strings.HasPrefix(reply, "-ERR protocol error") {
		t.Fatalf("reply = %q, want protocol error", reply)
	}
	waitHandleConnDone(t, done, "connection close after invalid array count")
}

func TestRESPRejectsOversizedUnauthenticatedBulk(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	done := runHandleConn(New("", nil), server)

	go func() { _, _ = client.Write([]byte("*1\r\n$1000000\r\n")) }()
	if reply := readPipeReply(t, client); !strings.HasPrefix(reply, "-ERR protocol error") {
		t.Fatalf("reply = %q, want protocol error", reply)
	}
	waitHandleConnDone(t, done, "connection close after oversized pre-auth bulk")
}

func TestRESPRejectsOverflowingBulkLength(t *testing.T) {
	conn := newDeadlineTestConn("*1\r\n$9223372036854775807\r\n", true, "127.0.0.1")
	done := runHandleConn(New("", nil), conn)
	waitHandleConnDone(t, done, "connection close after overflowing bulk length")
	if output := conn.Output(); !strings.HasPrefix(output, "-ERR protocol error") {
		t.Fatalf("output = %q, want protocol error", output)
	}
}

func TestRESPRejectsOverlongLine(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	done := runHandleConn(New("", nil), server)

	go func() { _, _ = client.Write([]byte("*" + strings.Repeat("1", 200*1024))) }()
	if reply := readPipeReply(t, client); !strings.HasPrefix(reply, "-ERR protocol error") {
		t.Fatalf("reply = %q, want protocol error", reply)
	}
	waitHandleConnDone(t, done, "connection close after overlong line")
}

func TestRESPBulkReadDoesNotPreallocateDeclaredLength(t *testing.T) {
	const declared = 400 * 1024 * 1024
	conn := newDeadlineTestConn("*1\r\n$419430400\r\nabc", true, "127.0.0.1")
	srv := New("", nil)

	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	done := runHandleConn(srv, conn)
	select {
	case <-conn.readWaiting:
		if errClose := conn.Close(); errClose != nil {
			t.Fatal(errClose)
		}
	case <-done:
	case <-time.After(respPipeDeadline):
		t.Fatal("timed out waiting for truncated bulk read")
	}
	waitHandleConnDone(t, done, "connection close after truncated bulk")
	goruntime.ReadMemStats(&after)

	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > declared/4 {
		t.Fatalf("allocated %d bytes for a %d-byte declared bulk carrying 3 bytes", allocated, declared)
	}
}

func TestRESPHandlerPanicClosesOnlyConnection(t *testing.T) {
	srv := New("", nil)
	registry := dispatch.NewRegistry()
	if errRegister := registry.SetDirectDefault("BOOM", func(context.Context, dispatch.Env, []string) dispatch.Reply {
		panic("handler exploded")
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	srv.registry = registry

	panicking := newSubscriptionTestConn("BOOM")
	done := runHandleConn(srv, panicking)
	waitHandleConnDone(t, done, "panicking connection close")
	select {
	case <-panicking.closed:
	default:
		t.Fatal("panicking connection was not closed")
	}

	healthy := newSubscriptionTestConn("PING")
	healthyDone := runHandleConn(srv, healthy)
	deadline := time.Now().Add(respPipeDeadline)
	for !strings.Contains(healthy.Output(), "+PONG") {
		if time.Now().After(deadline) {
			t.Fatalf("healthy output = %q, want PONG", healthy.Output())
		}
		goruntime.Gosched()
	}
	if errClose := healthy.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	waitHandleConnDone(t, healthyDone, "healthy connection close")
}

func TestRESPUnauthenticatedIdleConnectionTimesOut(t *testing.T) {
	conn := newDeadlineTestConn("", false, "127.0.0.1")
	done := runHandleConn(New("", nil), conn)
	waitHandleConnDone(t, done, "idle unauthenticated connection close")
	if !conn.isClosed() {
		t.Fatal("idle unauthenticated connection was not closed")
	}
}

func TestRESPAuthenticatedIdleConnectionHasNoReadDeadline(t *testing.T) {
	conn := newDeadlineTestConn("*1\r\n$4\r\nPING\r\n", true, "127.0.0.1")
	done := runHandleConn(New("", nil), conn)
	select {
	case <-conn.readWaiting:
	case <-done:
		t.Fatal("authenticated idle connection closed")
	case <-time.After(respPipeDeadline):
		t.Fatal("timed out waiting for idle read")
	}
	if deadline := conn.currentReadDeadline(); !deadline.IsZero() {
		t.Fatalf("idle authenticated read deadline = %v, want none", deadline)
	}
	if output := conn.Output(); output != "+PONG\r\n" {
		t.Fatalf("output = %q, want PONG", output)
	}
	if errClose := conn.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	waitHandleConnDone(t, done, "authenticated connection close")
}

func TestRESPAuthenticatedPartialFrameTimesOut(t *testing.T) {
	conn := newDeadlineTestConn("*2\r\n$4\r\nPI", true, "127.0.0.1")
	done := runHandleConn(New("", nil), conn)
	waitHandleConnDone(t, done, "partial frame connection close")
}

func TestSafeWriterSetsWriteDeadline(t *testing.T) {
	conn := newDeadlineTestConn("", false, "127.0.0.1")
	writer := newSafeWriter(conn)
	before := time.Now()
	if errWrite := writer.WriteRedisSimpleString("OK"); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errWrite := writer.WriteDispatchReply(dispatch.SensitiveBulkString([]byte("secret"))); errWrite != nil {
		t.Fatal(errWrite)
	}
	conn.mu.Lock()
	deadlines := append([]time.Time(nil), conn.writeDeadlines...)
	conn.mu.Unlock()
	if len(deadlines) < 2 {
		t.Fatalf("write deadlines = %v, want one per write", deadlines)
	}
	for _, deadline := range deadlines {
		if !deadline.After(before) {
			t.Fatalf("write deadline = %v, want a future deadline", deadline)
		}
	}
}

func TestFingerprintRegistryPrunesReleasedLifetimes(t *testing.T) {
	registry := NewFingerprintRegistry()
	bootstrap := cluster.ConnectionLifetime{Fingerprint: "fp-prune", ConnectedAt: time.Unix(10, 0), Controlled: true}
	for index := 0; index < 3; index++ {
		conn := newDeadlineTestConn("", true, "127.0.0.1")
		tracked, errAccept := registry.Accept(context.Background(), conn, bootstrap)
		if errAccept != nil {
			t.Fatal(errAccept)
		}
		finish, errBegin := tracked.BeginHandler()
		if errBegin != nil {
			t.Fatal(errBegin)
		}
		finish()
		subscription := cluster.ConnectionLifetime{Fingerprint: "fp-prune", ConnectedAt: time.Unix(int64(100+index), 0), Subscription: true}
		if errAttach := tracked.AttachSubscriptionLifetime(subscription); errAttach != nil {
			t.Fatal(errAttach)
		}
		if errClose := tracked.Close(); errClose != nil {
			t.Fatal(errClose)
		}
	}
	registry.mu.Lock()
	remaining := len(registry.entries)
	registry.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("registry retained %d released lifetime entries", remaining)
	}
}

func TestFingerprintRegistryKeepsFencedLifetime(t *testing.T) {
	registry := NewFingerprintRegistry()
	lifetime := cluster.ConnectionLifetime{Fingerprint: "fp-fenced", ConnectedAt: time.Unix(20, 0), Controlled: true}
	tracked, errAccept := registry.Accept(context.Background(), newDeadlineTestConn("", true, "127.0.0.1"), lifetime)
	if errAccept != nil {
		t.Fatal(errAccept)
	}
	if errFence := registry.Fence(context.Background(), lifetime, 3); errFence != nil {
		t.Fatal(errFence)
	}
	if errClose := tracked.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if revision := registry.LatestFenceRevision(lifetime); revision != 3 {
		t.Fatalf("fence revision = %d, want 3", revision)
	}
	if _, errAccept = registry.Accept(context.Background(), newDeadlineTestConn("", true, "127.0.0.1"), lifetime); errAccept != ErrFingerprintFenced {
		t.Fatalf("Accept() after fence error = %v, want ErrFingerprintFenced", errAccept)
	}
}

func TestRESPAllowHostChangeClosesExistingConnections(t *testing.T) {
	runtimeHome, errRuntime := home.NewRuntime(&appconfig.Config{})
	if errRuntime != nil {
		t.Fatalf("NewRuntime() error = %v", errRuntime)
	}
	srv := New("", runtimeHome)

	removed := newDeadlineTestConn("", true, "10.1.2.3")
	removedDone := runHandleConn(srv, removed)
	kept := newDeadlineTestConn("", true, "10.9.9.9")
	keptDone := runHandleConn(srv, kept)
	for _, conn := range []*deadlineTestConn{removed, kept} {
		select {
		case <-conn.readWaiting:
		case <-time.After(respPipeDeadline):
			t.Fatal("timed out waiting for connection to become idle")
		}
	}

	next := &appconfig.Config{AllowHost: []string{"10.9.9.9"}}
	if errApply := runtimeHome.ApplyConfigFromCluster(context.Background(), next); errApply != nil {
		t.Fatalf("ApplyConfigFromCluster() error = %v", errApply)
	}
	runtimeHome.PublishConfigYAML([]byte("allow-host:\n  - 10.9.9.9\n"))

	waitHandleConnDone(t, removedDone, "disallowed connection close")
	select {
	case <-keptDone:
		t.Fatal("allowed connection was closed")
	default:
	}
	if errClose := kept.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	waitHandleConnDone(t, keptDone, "allowed connection close")
}
