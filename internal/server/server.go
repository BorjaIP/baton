// Package server implements the baton lock daemon: it listens on a Unix
// domain socket, guarantees single-instance operation via an exclusive
// flock, dispatches the wire-protocol verbs (internal/proto) to a shared
// internal/core/lock Table, and shuts down gracefully. Per design §4
// layering, this package is the only non-test importer of
// internal/core/lock.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
	"github.com/BorjaIP/baton/internal/proto"
	"github.com/gofrs/flock"
)

// Config configures a Server. Every duration field defaults to a sane value
// when left zero.
type Config struct {
	Paths         proto.Paths
	Logger        *slog.Logger
	Version       string
	HelloTimeout  time.Duration // default 5s: deadline for each pre-hello frame
	WriteTimeout  time.Duration // default 5s per response write
	ShutdownGrace time.Duration // default 5s before force-closing connections
	Table         *lock.Table   // nil => lock.New(); test hook
}

func (c Config) withDefaults() Config {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.HelloTimeout <= 0 {
		c.HelloTimeout = 5 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 5 * time.Second
	}
	if c.ShutdownGrace <= 0 {
		c.ShutdownGrace = 5 * time.Second
	}
	if c.Table == nil {
		c.Table = lock.New()
	}
	return c
}

// Server is one running daemon instance: a single flock-guarded Unix socket
// listener dispatching onto a single shared *lock.Table.
type Server struct {
	cfg Config
	reg *registry
	h   *handler

	fl *flock.Flock
	ln *net.UnixListener

	base   context.Context
	cancel context.CancelCauseFunc

	mu       sync.Mutex
	closed   bool
	conns    map[*conn]struct{}
	nextConn uint64

	wg   sync.WaitGroup
	once sync.Once
	done chan struct{}
}

// New constructs a Server. Listen must be called before Serve.
func New(cfg Config) *Server {
	cfg = cfg.withDefaults()
	base, cancel := context.WithCancelCause(context.Background())
	reg := &registry{m: make(map[regKey]*regEntry)}

	return &Server{
		cfg:    cfg,
		reg:    reg,
		h:      &handler{table: cfg.Table, reg: reg, pid: os.Getpid(), version: cfg.Version},
		base:   base,
		cancel: cancel,
		conns:  make(map[*conn]struct{}),
		done:   make(chan struct{}),
	}
}

// Listen resolves the single-instance guard and binds the listener (design
// ADR-9): create the socket directory, acquire the exclusive flock, clean up
// a stale socket file left by a crashed prior daemon (only the flock winner
// ever touches it), then bind and chmod the new listener.
func (s *Server) Listen() error {
	if err := os.MkdirAll(s.cfg.Paths.Dir, 0o700); err != nil {
		return fmt.Errorf("server: create socket directory %q: %w", s.cfg.Paths.Dir, err)
	}

	fl := flock.New(s.cfg.Paths.Lock)
	if err := tryLockInstance(fl); err != nil {
		return err
	}

	if err := removeStaleSocket(s.cfg.Paths.Socket); err != nil {
		fl.Unlock()
		return err
	}

	ln, err := listenUnix(s.cfg.Paths.Socket)
	if err != nil {
		fl.Unlock()
		return err
	}

	s.fl = fl
	s.ln = ln
	return nil
}

// Addr returns the bound socket's address, or "" before Listen succeeds.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Serve runs the accept loop until ctx is cancelled, at which point it
// performs a full Shutdown and returns nil once that shutdown completes.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		return errors.New("server: Listen must be called before Serve")
	}

	stop := context.AfterFunc(ctx, func() {
		_ = s.Shutdown(context.Background())
	})
	defer stop()

	for {
		nc, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				// A controlled shutdown closed the listener; wait for it to
				// finish (idempotent: sync.Once blocks concurrent callers
				// until the first Do's function returns) and report clean.
				_ = s.Shutdown(context.Background())
				return nil
			default:
				_ = s.Shutdown(context.Background())
				return fmt.Errorf("server: accept: %w", err)
			}
		}
		s.acceptConn(nc)
	}
}

// acceptConn registers nc as a new tracked connection and runs its
// connection loop in its own goroutine, tracked by s.wg so Shutdown can wait
// for it. wg.Add and the closed check share s.mu with Shutdown's own
// closed-flag flip, so every Add is guaranteed (by that mutex) to
// happen-before the Wait Shutdown starts once it sees closed==true — a
// connection accepted in the race window between Listener.Accept returning
// and Shutdown running is either fully registered before Wait starts, or
// rejected outright and never counted.
func (s *Server) acceptConn(nc net.Conn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = nc.Close()
		return
	}

	s.nextConn++
	id := s.nextConn
	ctx, cancel := context.WithCancelCause(s.base)
	c := &conn{
		id:       id,
		nc:       nc,
		ctx:      ctx,
		cancel:   cancel,
		inflight: make(chan struct{}, 1),
	}
	s.conns[c] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		s.serveConn(c)
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
}

// Shutdown performs the graceful teardown sequence (design ADR-9 / "Graceful
// Shutdown"): stop accepting, cancel every in-flight blocked Await, unblock
// idle readers, wait (bounded by ShutdownGrace) for connections to finish,
// remove the socket file, and release the flock. It is idempotent: repeated
// or concurrent calls all block until the first call's teardown completes,
// then return nil.
func (s *Server) Shutdown(context.Context) error {
	s.once.Do(func() {
		close(s.done)
		if s.ln != nil {
			s.ln.Close()
		}
		s.cancel(errShutdown)

		s.mu.Lock()
		s.closed = true
		conns := make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()

		now := time.Now()
		for _, c := range conns {
			_ = c.nc.SetReadDeadline(now)
		}

		waitCh := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(waitCh)
		}()

		grace := time.NewTimer(s.cfg.ShutdownGrace)
		defer grace.Stop()
		select {
		case <-waitCh:
		case <-grace.C:
			s.mu.Lock()
			for c := range s.conns {
				_ = c.nc.Close()
			}
			s.mu.Unlock()
			<-waitCh
		}

		if s.cfg.Paths.Socket != "" {
			_ = os.Remove(s.cfg.Paths.Socket)
		}
		if s.fl != nil {
			_ = s.fl.Unlock()
		}
	})
	return nil
}
