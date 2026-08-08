// Package bgrpc runs a gRPC server as a bedrock.Server, so bedrock owns its
// startup ordering and its drain the same way it owns the HTTP router's.
//
// It lives in a separate Go module. google.golang.org/grpc and protobuf are a
// large dependency tree, and a service that speaks only HTTP should not carry
// them in its module graph.
//
// The package is named bgrpc rather than grpc so that the upstream grpc package
// can be referred to unqualified inside it, and so callers importing both need
// no alias:
//
//	import (
//		bgrpc "github.com/Jack4Code/bedrock/grpc" // (alias optional)
//		"google.golang.org/grpc"
//	)
package bgrpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/Jack4Code/bedrock"
)

// Config configures the gRPC server.
type Config struct {
	// Port is the TCP port to listen on. Port 0 binds an arbitrary free port,
	// which Addr reports once Start has returned.
	Port int

	// Host restricts the listen address. Empty listens on all interfaces.
	Host string

	// Health is the tracker the grpc.health.v1 service reports. Pass the same
	// instance given to bedrock.Options.Health so the gRPC health check and the
	// HTTP /ready endpoint cannot disagree. When nil, the server reports SERVING
	// for as long as it is running.
	Health *bedrock.HealthStatus

	// Logger defaults to slog.Default().
	Logger *slog.Logger

	// ServerOptions are passed through to grpc.NewServer: interceptors, TLS
	// credentials, keepalive policy, message size limits. Bedrock has no opinion
	// on any of them.
	ServerOptions []grpc.ServerOption
}

// Option adjusts a Server at construction.
type Option func(*Server)

// WithReflection enables the gRPC server reflection service, which lets tools
// like grpcurl discover the API without a copy of the .proto files.
//
// Opt-in rather than default: reflection publishes the entire service surface,
// which is convenient in development and a disclosure in production.
func WithReflection() Option {
	return func(s *Server) { s.reflection = true }
}

// Server runs a gRPC server under bedrock's supervision. It implements
// bedrock.Server.
type Server struct {
	cfg        Config
	logger     *slog.Logger
	grpc       *grpc.Server
	health     *health.Server
	reflection bool

	stopOnce sync.Once
	stopped  chan struct{}

	mu   sync.Mutex
	addr net.Addr
}

var _ bedrock.Server = (*Server)(nil)

// New builds a gRPC server. register is called immediately with the underlying
// *grpc.Server so services are attached before anything is served; it may be
// nil for a server that exposes only health and reflection.
//
// Note the ordering, because it is the opposite of the HTTP side: bedrock reads
// App.Routes after OnStart, so route handlers can close over state OnStart
// initialised, but register runs here in main, before bedrock.Run is called at
// all. *grpc.Server has no way to add a service once it is serving, so this
// cannot be deferred.
//
// Register a pointer whose fields OnStart fills in:
//
//	svc := myservice.New(logger)      // OnStart populates svc.db
//	bgrpc.New(cfg, svc.Register)      // safe: the pointer is what's registered
//
// Do not resolve dependencies inside the callback — they do not exist yet:
//
//	bgrpc.New(cfg, func(g *grpc.Server) {
//	    pb.RegisterMyServiceServer(g, newService(app.db)) // app.db is still nil
//	})
//
// The health service is always registered — it costs nothing and an
// orchestrator configured for gRPC probes needs it to exist.
func New(cfg Config, register func(*grpc.Server), opts ...Option) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		cfg:     cfg,
		logger:  logger,
		health:  health.NewServer(),
		stopped: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}

	s.grpc = grpc.NewServer(cfg.ServerOptions...)
	healthpb.RegisterHealthServer(s.grpc, s.health)
	if register != nil {
		register(s.grpc)
	}
	// Reflection must be registered before serving begins, like any other
	// service.
	if s.reflection {
		reflection.Register(s.grpc)
	}
	return s
}

func (s *Server) Name() string { return "grpc" }

// Start binds the listener synchronously — so a port already in use is a
// startup error rather than a line in the log after the process claimed to be
// healthy — then serves in the background.
func (s *Server) Start(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	s.mu.Lock()
	s.addr = lis.Addr()
	s.mu.Unlock()

	s.startHealthTracking()

	go func() {
		if err := s.grpc.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			s.logger.Error("grpc server error", "err", err)
		}
	}()

	s.logger.Info("grpc server listening", "addr", lis.Addr().String(), "reflection", s.reflection)
	return nil
}

// Shutdown drains in-flight RPCs, then forces the issue if they will not
// finish in time.
//
// GracefulStop has no timeout of its own: it waits for every open RPC to
// return, forever if necessary. A server with long-polling or streaming
// endpoints always has open RPCs, so relying on GracefulStop alone turns a
// routine SIGTERM into a hung deploy. The context bedrock supplies carries the
// drain budget, and when it expires Stop() closes the connections outright.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopOnce.Do(func() { close(s.stopped) })

	// Tell anyone watching the health service that this instance is going away
	// before connections start closing under them.
	s.health.Shutdown()

	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.grpc.Stop()
		<-done // GracefulStop returns once Stop has torn the connections down
		s.logger.Warn("grpc server force-stopped: RPCs still in flight at the shutdown deadline")
		return ctx.Err()
	}
}

// Addr reports the address the server bound to, or nil before Start succeeds.
// Useful when Port is 0.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}
