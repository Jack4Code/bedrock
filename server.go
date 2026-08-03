package bedrock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
)

// Server is a long-running component bedrock supervises alongside the HTTP
// router: it is started after the app's OnStart and drained before its OnStop.
//
// This is the extension point for anything that listens on its own port or runs
// its own loop — a gRPC server, a metrics endpoint, a queue consumer. Bedrock
// owns the ordering and the shutdown deadline so each service does not have to
// rediscover them.
//
// Implementations live outside this package (see the bedrock/grpc module);
// nothing here depends on what a Server actually does.
type Server interface {
	// Name identifies the server in log lines.
	Name() string

	// Start begins serving and returns once the server is accepting work.
	//
	// It must not block for the lifetime of the server — spawn a goroutine and
	// return. Returning promptly is what lets bedrock report a bind failure as a
	// startup error instead of logging it from a goroutine after the process has
	// already announced itself healthy.
	//
	// ctx is the process lifetime context, provided for start-time work. It is
	// not a request context, and cancelling it does not stop the server; bedrock
	// always calls Shutdown for that.
	Start(ctx context.Context) error

	// Shutdown stops accepting new work and blocks until work already in flight
	// finishes or ctx expires, whichever happens first. It must return when ctx
	// expires rather than waiting indefinitely — a Shutdown that can hang turns
	// a routine deploy into a stuck one.
	Shutdown(ctx context.Context) error
}

// httpServer adapts net/http to the Server interface. It is what bedrock wraps
// its own router and health endpoints in, so they follow exactly the same
// startup and drain rules as anything a service supplies.
type httpServer struct {
	name   string
	srv    *http.Server
	logger *slog.Logger

	mu   sync.Mutex
	addr net.Addr
}

func newHTTPServer(name, addr string, handler http.Handler, logger *slog.Logger) *httpServer {
	return &httpServer{
		name:   name,
		srv:    &http.Server{Addr: addr, Handler: handler},
		logger: logger,
	}
}

func (s *httpServer) Name() string { return s.name }

// Start binds the listener synchronously so that a port already in use is
// returned as an error, then serves in the background.
func (s *httpServer) Start(_ context.Context) error {
	lis, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.srv.Addr, err)
	}

	s.mu.Lock()
	s.addr = lis.Addr()
	s.mu.Unlock()

	go func() {
		if err := s.srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("server error", "server", s.name, "err", err)
		}
	}()
	return nil
}

func (s *httpServer) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

// boundAddr reports the address the listener actually bound to, which differs
// from the configured address when port 0 was requested. Used by tests.
func (s *httpServer) boundAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// healthMux builds the handler serving the health endpoints. These are
// deliberately free of CORS and app middleware: they are infrastructure probes,
// not part of the application surface.
func healthMux(status *HealthStatus) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthCheckHandler(status))
	mux.HandleFunc("/ready", readyCheckHandler(status))
	mux.HandleFunc("/live", liveCheckHandler(status))
	return mux
}
