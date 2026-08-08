package bgrpc

import (
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// healthPollInterval is how often the gRPC health service re-reads bedrock's
// readiness.
//
// Polling rather than subscribing because bedrock.HealthStatus is a plain
// guarded bool with no change notification. Adding one for the sake of a health
// endpoint would be a lot of machinery for a signal that changes roughly twice
// in a process lifetime, and a quarter second of staleness is well inside what
// any orchestrator's probe interval can distinguish.
const healthPollInterval = 250 * time.Millisecond

// startHealthTracking makes grpc.health.v1 report the same state as bedrock's
// /ready endpoint. With no tracker configured the server simply reports SERVING
// for as long as it is running.
//
// The empty service name is the conventional overall-server check that probes
// and grpcurl use by default.
func (s *Server) startHealthTracking() {
	if s.cfg.Health == nil {
		s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		return
	}

	s.setServingFromStatus()

	go func() {
		ticker := time.NewTicker(healthPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopped:
				return
			case <-ticker.C:
				s.setServingFromStatus()
			}
		}
	}()
}

func (s *Server) setServingFromStatus() {
	status := healthpb.HealthCheckResponse_NOT_SERVING
	if s.cfg.Health.IsReady() {
		status = healthpb.HealthCheckResponse_SERVING
	}
	s.health.SetServingStatus("", status)
}
