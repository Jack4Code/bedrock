package bgrpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	"github.com/Jack4Code/bedrock"
)

// The health service doubles as the test fixture: Check is a unary RPC and
// Watch is a long-lived streaming one, which is everything these tests need to
// exercise draining. That avoids dragging a protoc toolchain into the module
// just to define a throwaway service.

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// startServer brings up a server on an ephemeral port and returns it with a
// connected client.
func startServer(t *testing.T, cfg Config, opts ...Option) (*Server, *grpc.ClientConn) {
	t.Helper()
	cfg.Port = 0
	cfg.Host = "127.0.0.1"
	if cfg.Logger == nil {
		cfg.Logger = quiet()
	}

	s := New(cfg, nil, opts...)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s, dial(t, s.Addr().String())
}

func TestHealthTracksBedrockReadiness(t *testing.T) {
	h := bedrock.NewHealthStatus()
	_, conn := startServer(t, Config{Health: h})
	client := healthpb.NewHealthClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// bedrock has not marked the app ready, so neither should gRPC.
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("status = %v before readiness, want NOT_SERVING", resp.Status)
	}

	h.SetReady(true)

	waitFor(t, "grpc health to report SERVING", func() bool {
		resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
		return err == nil && resp.Status == healthpb.HealthCheckResponse_SERVING
	})
}

func TestHealthDefaultsToServingWithoutTracker(t *testing.T) {
	_, conn := startServer(t, Config{}) // no Health configured
	client := healthpb.NewHealthClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING when no tracker is configured", resp.Status)
	}
}

// TestShutdownForceStopsAtDeadline is the regression test for a hung deploy.
//
// GracefulStop waits for every open RPC to return, with no timeout of its own.
// A server with long-polling or streaming endpoints always has open RPCs, so
// without the force-stop fallback this Shutdown would never return and SIGTERM
// would hang until the orchestrator killed the container.
func TestShutdownForceStopsAtDeadline(t *testing.T) {
	h := bedrock.NewHealthStatus()
	h.SetReady(true)

	s := New(Config{Port: 0, Host: "127.0.0.1", Health: h, Logger: quiet()}, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	conn := dial(t, s.Addr().String())
	client := healthpb.NewHealthClient(conn)

	// Open a stream and leave it open — a client that is not going to hang up.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	stream, err := client.Watch(watchCtx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first watch message: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(shutdownCtx) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown returned %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Errorf("Shutdown took %v, far past its 300ms budget", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown never returned with an RPC still open: this is the deploy hang")
	}
}

// TestShutdownDrainsGracefully: when the in-flight RPCs do finish, Shutdown
// returns cleanly rather than waiting out its whole budget.
func TestShutdownDrainsGracefully(t *testing.T) {
	s := New(Config{Port: 0, Host: "127.0.0.1", Logger: quiet()}, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	conn := dial(t, s.Addr().String())
	client := healthpb.NewHealthClient(conn)

	watchCtx, watchCancel := context.WithCancel(context.Background())
	stream, err := client.Watch(watchCtx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first watch message: %v", err)
	}
	watchCancel() // the client hangs up, so the RPC can finish

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	started := time.Now()
	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned %v, want nil once RPCs finished", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("graceful Shutdown took %v; it should not have waited out the budget", elapsed)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	s := New(Config{Port: 0, Host: "127.0.0.1", Logger: quiet()}, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	// bedrock only calls Shutdown once, but a second call must not panic on the
	// already-closed stop channel.
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

func TestStartReportsBindFailure(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	port := lis.Addr().(*net.TCPAddr).Port

	s := New(Config{Port: port, Host: "127.0.0.1", Logger: quiet()}, nil)
	if err := s.Start(context.Background()); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		t.Fatal("Start succeeded on a port already in use")
	}
}

func TestRegisterCallbackRuns(t *testing.T) {
	called := false
	s := New(Config{Port: 0, Host: "127.0.0.1", Logger: quiet()}, func(g *grpc.Server) {
		if g == nil {
			t.Error("register received a nil server")
		}
		called = true
	})
	if !called {
		t.Error("register was not called during New; services would be attached after Serve")
	}
	_ = s
}

func TestReflectionIsOptIn(t *testing.T) {
	listServices := func(t *testing.T, conn *grpc.ClientConn) ([]string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		client := grpc_reflection_v1.NewServerReflectionClient(conn)
		stream, err := client.ServerReflectionInfo(ctx)
		if err != nil {
			return nil, err
		}
		req := &grpc_reflection_v1.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{},
		}
		// Send reports io.EOF once the stream is done — including when the
		// server rejected it outright — and the real status only comes back
		// from Recv. Returning the Send error directly makes this test's
		// outcome depend on which side wins the race.
		if err := stream.Send(req); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		var names []string
		for _, svc := range resp.GetListServicesResponse().GetService() {
			names = append(names, svc.GetName())
		}
		return names, nil
	}

	t.Run("disabled by default", func(t *testing.T) {
		_, conn := startServer(t, Config{})
		_, err := listServices(t, conn)
		if err == nil {
			t.Fatal("reflection answered without WithReflection")
		}
		if got := status.Code(err); got != codes.Unimplemented {
			t.Errorf("code = %v, want Unimplemented", got)
		}
	})

	t.Run("enabled by option", func(t *testing.T) {
		_, conn := startServer(t, Config{}, WithReflection())
		names, err := listServices(t, conn)
		if err != nil {
			t.Fatalf("reflection: %v", err)
		}
		found := false
		for _, n := range names {
			if n == "grpc.health.v1.Health" {
				found = true
			}
		}
		if !found {
			t.Errorf("reflection listed %v, expected it to include grpc.health.v1.Health", names)
		}
	})
}

// ---------------------------------------------------------------------------
// integration with bedrock's lifecycle
// ---------------------------------------------------------------------------

type grpcOnlyApp struct {
	stopped chan struct{}
}

func (a *grpcOnlyApp) OnStart(ctx context.Context) error { return nil }
func (a *grpcOnlyApp) OnStop(ctx context.Context) error  { close(a.stopped); return nil }
func (a *grpcOnlyApp) Routes() []bedrock.Route           { return nil }

// TestServesUnderBedrockRun is the end-to-end proof that the two halves compose:
// a service with no HTTP routes at all comes up under bedrock, answers gRPC
// health checks, and shuts down cleanly on SIGTERM.
//
// It signals its own process, which bedrock's signal.NotifyContext handler
// catches. That is safe only because the handler is installed before anything
// starts listening, so the health check below guarantees it is in place.
func TestServesUnderBedrockRun(t *testing.T) {
	grpcPort := freeTCPPort(t)
	healthPort := freeTCPPort(t)
	httpPort := freeTCPPort(t)

	h := bedrock.NewHealthStatus()
	app := &grpcOnlyApp{stopped: make(chan struct{})}
	srv := New(Config{Port: grpcPort, Host: "127.0.0.1", Health: h, Logger: quiet()}, nil)

	done := make(chan error, 1)
	go func() {
		done <- bedrock.RunWithOptions(app, bedrockConfig(httpPort, healthPort), bedrock.Options{
			Logger:          quiet(),
			Health:          h,
			Servers:         []bedrock.Server{srv},
			ShutdownTimeout: 3 * time.Second,
		})
	}()

	conn := dial(t, fmt.Sprintf("127.0.0.1:%d", grpcPort))
	client := healthpb.NewHealthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	waitFor(t, "grpc health to report SERVING under bedrock", func() bool {
		resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
		return err == nil && resp.Status == healthpb.HealthCheckResponse_SERVING
	})

	signalSelf(t)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWithOptions returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("bedrock never shut down after SIGTERM")
	}

	select {
	case <-app.stopped:
	default:
		t.Error("OnStop did not run")
	}
}
