package jfrog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "artifactory-mcp/internal/artifactory"
	"artifactory-mcp/internal/config"

	sdk "github.com/jfrog/jfrog-client-go/artifactory"
)

// Only the socket is replaced. HTTP framing, TLS, the httptest.Server, and
// the configured JFrog SDK all run unchanged. This works without listen rights.
type memoryListener struct {
	incoming chan net.Conn
	done     chan struct{}
	once     sync.Once
	address  *net.TCPAddr
}

var memoryServers sync.Map
var nextMemoryPort atomic.Int32

func (l *memoryListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.incoming:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *memoryListener) Close() error {
	l.once.Do(func() { memoryServers.Delete(l.address.String()); close(l.done) })
	return nil
}
func (l *memoryListener) Addr() net.Addr { return l.address }
func dialMemory(ctx context.Context, network, address string) (net.Conn, error) {
	entry, ok := memoryServers.Load(address)
	if !ok {
		return nil, fmt.Errorf("no in-memory HTTP server for %s", address)
	}
	listener := entry.(*memoryListener)
	client, server, err := pipeConnections()
	if err != nil {
		return nil, err
	}
	select {
	case listener.incoming <- server:
		return client, nil
	case <-listener.done:
		client.Close()
		server.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	}
}
func testHTTPServer(handler http.Handler, tls bool) *httptest.Server {
	listener := &memoryListener{incoming: make(chan net.Conn), done: make(chan struct{}), address: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000 + int(nextMemoryPort.Add(1))}}
	memoryServers.Store(listener.address.String(), listener)
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	if tls {
		server.StartTLS()
	} else {
		server.Start()
	}
	return server
}
func memoryServer(handler http.Handler) *httptest.Server    { return testHTTPServer(handler, false) }
func memoryTLSServer(handler http.Handler) *httptest.Server { return testHTTPServer(handler, true) }
func newTestClient(t *testing.T, c config.Config) *app.Client {
	t.Helper()
	client, err := newClient(c, func(ctx context.Context, c config.Config, certificates string) (sdk.ArtifactoryServicesManager, error) {
		manager, err := newManager(ctx, c, certificates)
		if err != nil {
			return nil, err
		}
		// Retain the SDK's TLS config, context, authentication, and retry settings.
		transport := manager.Client().GetHttpClient().GetClient().Transport.(*http.Transport)
		transport.Proxy = nil
		transport.DialContext = dialMemory
		return manager, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// Kernel pipes provide TCP-like buffering without network listen permissions;
// unbuffered net.Pipe can deadlock when both TLS peers send close_notify.
type fileConn struct{ reader, writer *os.File }

func pipeConnections() (net.Conn, net.Conn, error) {
	ar, bw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	br, aw, err := os.Pipe()
	if err != nil {
		ar.Close()
		bw.Close()
		return nil, nil, err
	}
	return &fileConn{ar, aw}, &fileConn{br, bw}, nil
}
func (c *fileConn) Read(p []byte) (int, error)         { return c.reader.Read(p) }
func (c *fileConn) Write(p []byte) (int, error)        { return c.writer.Write(p) }
func (c *fileConn) Close() error                       { return errors.Join(c.reader.Close(), c.writer.Close()) }
func (c *fileConn) LocalAddr() net.Addr                { return pipeAddr{} }
func (c *fileConn) RemoteAddr() net.Addr               { return pipeAddr{} }
func (c *fileConn) SetReadDeadline(t time.Time) error  { return c.reader.SetReadDeadline(t) }
func (c *fileConn) SetWriteDeadline(t time.Time) error { return c.writer.SetWriteDeadline(t) }
func (c *fileConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }
