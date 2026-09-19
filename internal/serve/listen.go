package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ErrNoTLS is returned when ListenAndServe is called without a TLS certificate and without InsecureHTTP.
var ErrNoTLS = errors.New("no certificate; run relay serve init or pass --insecure-http")

// ListenConfig configures the server's network listener.
type ListenConfig struct {
	Addr         string           // e.g. ":7777"
	TLS          *tls.Certificate // nil only with InsecureHTTP
	InsecureHTTP bool
}

// Addr returns the bound address once listening, or nil before listening starts.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// ListenAndServe starts the HTTP/HTTPS server and the daemon tick loop.
// It requires a TLS certificate unless InsecureHTTP is set.
// It runs until ctx is cancelled, then shuts down gracefully with a 5-second budget.
func (s *Server) ListenAndServe(ctx context.Context, lc ListenConfig) error {
	if lc.TLS == nil && !lc.InsecureHTTP {
		return ErrNoTLS
	}

	addr := lc.Addr
	if addr == "" {
		addr = ":7777"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.addr = ln.Addr()
	s.insecureHTTP = lc.InsecureHTTP
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.addr = nil
		s.mu.Unlock()
	}()

	var listener net.Listener = ln
	if lc.TLS != nil {
		tlsConfig := &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{*lc.TLS},
		}
		listener = tls.NewListener(ln, tlsConfig)
	} else if lc.InsecureHTTP {
		slog.Warn("serving plain HTTP; every client request is readable on the network")
	}

	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		_ = s.Run(ctx)
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	serveErr := srv.Serve(listener)
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}
