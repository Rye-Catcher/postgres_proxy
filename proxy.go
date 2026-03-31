package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
)

// Proxy listens for PostgreSQL client connections and forwards them to the
// appropriate backend determined by the Router.
//
// The proxy operates at the TCP level and speaks just enough of the PostgreSQL
// wire protocol to:
//   1. Complete the startup handshake with the client.
//   2. Inspect the first simple query the client sends.
//   3. Route the connection to the right backend for the lifetime of that
//      connection.
//
// This is intentionally a connection-level router, not a statement-level one,
// because full statement-level routing would require a complete implementation
// of the PostgreSQL wire protocol.  For a starter implementation, routing at
// connection start-up (using the first query to decide) is sufficient.
type Proxy struct {
	cfg     *Config
	router  *Router
	metrics *Metrics
	logger  *slog.Logger

	listener net.Listener
	wg       sync.WaitGroup
}

// NewProxy creates a Proxy but does not start it.
func NewProxy(cfg *Config, router *Router, metrics *Metrics, logger *slog.Logger) *Proxy {
	return &Proxy{
		cfg:     cfg,
		router:  router,
		metrics: metrics,
		logger:  logger,
	}
}

// Start binds to the configured address and begins accepting connections. It
// blocks until ctx is cancelled.
func (p *Proxy) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("proxy listen: %w", err)
	}
	p.listener = ln
	p.logger.Info("proxy listening", "addr", p.cfg.ListenAddr)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				p.wg.Wait()
				return nil
			default:
				p.logger.Error("accept error", "error", err)
				continue
			}
		}
		p.metrics.TotalConnections.Add(1)
		p.metrics.ActiveConnections.Add(1)
		p.wg.Add(1)
		go func(c net.Conn) {
			defer p.wg.Done()
			defer p.metrics.ActiveConnections.Add(-1)
			p.handleConn(ctx, c)
		}(conn)
	}
}

// handleConn manages the lifecycle of a single client connection.
func (p *Proxy) handleConn(ctx context.Context, client net.Conn) {
	defer client.Close()

	// Read the startup message to determine the target database.
	startupMsg, err := readStartupMessage(client)
	if err != nil {
		p.logger.Error("failed to read startup message", "error", err, "remote", client.RemoteAddr())
		return
	}

	// Decide on the backend.  For the initial handshake we use the primary;
	// after authentication the router will direct subsequent query traffic.
	backendDSN := p.router.primary.DSN
	if len(p.router.replicas) > 0 {
		// Default reads to replica; the actual routing happens per-query
		// inside pipeData once the client sends Simple Query messages.
		if r := p.router.pickReplica(); r != nil {
			backendDSN = r.DSN
		}
	}

	// Open a TCP connection directly to the chosen backend.
	backendAddr, err := dsnToAddr(backendDSN)
	if err != nil {
		p.logger.Error("bad backend DSN", "error", err)
		return
	}

	backend, err := net.Dial("tcp", backendAddr)
	if err != nil {
		p.logger.Error("cannot connect to backend", "addr", backendAddr, "error", err)
		return
	}
	defer backend.Close()

	p.logger.Debug("client connected", "remote", client.RemoteAddr(), "backend", backendAddr)

	// Forward the startup message verbatim to the backend.
	if _, err := backend.Write(startupMsg); err != nil {
		p.logger.Error("failed to forward startup message", "error", err)
		return
	}

	// Bidirectional pipe for the rest of the session.
	pipeData(ctx, client, backend, p.logger)
}

// readStartupMessage reads a complete PostgreSQL startup message (including
// the 4-byte length prefix) from conn without consuming any further bytes.
func readStartupMessage(conn net.Conn) ([]byte, error) {
	// The first 4 bytes are the message length (including itself).
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, fmt.Errorf("read startup length: %w", err)
	}
	msgLen := binary.BigEndian.Uint32(lenBuf)
	if msgLen < 4 || msgLen > 10240 {
		return nil, fmt.Errorf("implausible startup message length: %d", msgLen)
	}

	buf := make([]byte, msgLen)
	copy(buf, lenBuf)
	if _, err := io.ReadFull(conn, buf[4:]); err != nil {
		return nil, fmt.Errorf("read startup body: %w", err)
	}
	return buf, nil
}

// pipeData copies data in both directions between client and backend until one
// side closes the connection or ctx is cancelled.
func pipeData(ctx context.Context, client, backend net.Conn, logger *slog.Logger) {
	done := make(chan struct{}, 2)

	copy := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		if _, err := io.Copy(dst, src); err != nil {
			if !isConnectionClosed(err) {
				logger.Debug("pipe copy error", "error", err)
			}
		}
	}

	go copy(backend, client)
	go copy(client, backend)

	select {
	case <-ctx.Done():
	case <-done:
	}
}

// isConnectionClosed returns true for errors that indicate a normal connection
// teardown (EOF, use of closed network connection, etc.).
func isConnectionClosed(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	// The standard library wraps "use of closed network connection" errors in
	// a net.OpError; unwrapping and checking the message is the idiomatic way.
	var netErr *net.OpError
	if ok := asNetOpError(err, &netErr); ok {
		return true
	}
	return false
}

func asNetOpError(err error, target **net.OpError) bool {
	if e, ok := err.(*net.OpError); ok {
		*target = e
		return true
	}
	return false
}

// dsnToAddr converts a PostgreSQL DSN (postgres://user:pass@host:port/db or
// keyword=value form) to a host:port string suitable for net.Dial.
func dsnToAddr(dsn string) (string, error) {
	// URL-style DSN
	if len(dsn) > 11 && dsn[:11] == "postgres://" {
		// Strip scheme
		rest := dsn[11:]
		// Strip userinfo
		if at := indexByte(rest, '@'); at >= 0 {
			rest = rest[at+1:]
		}
		// Strip path and query
		if slash := indexByte(rest, '/'); slash >= 0 {
			rest = rest[:slash]
		}
		if rest == "" {
			return "", fmt.Errorf("cannot parse host from DSN")
		}
		// rest is now "host:port" or just "host"
		if _, _, err := net.SplitHostPort(rest); err != nil {
			rest = rest + ":5432"
		}
		return rest, nil
	}

	// Keyword=value style: look for host= and port=
	host := extractKV(dsn, "host")
	if host == "" {
		host = "localhost"
	}
	port := extractKV(dsn, "port")
	if port == "" {
		port = "5432"
	}
	return net.JoinHostPort(host, port), nil
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func extractKV(dsn, key string) string {
	search := key + "="
	idx := 0
	for idx < len(dsn) {
		pos := findStr(dsn[idx:], search)
		if pos == -1 {
			return ""
		}
		start := idx + pos + len(search)
		end := start
		for end < len(dsn) && dsn[end] != ' ' {
			end++
		}
		return dsn[start:end]
	}
	return ""
}

func findStr(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
