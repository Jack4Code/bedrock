package bedrock

import (
	"encoding/json"
	"net/http"
	"sync"
)

// HealthStatus tracks application health
type HealthStatus struct {
	mu      sync.RWMutex
	healthy bool
	ready   bool
}

// NewHealthStatus returns a health tracker that starts out neither healthy nor
// ready. Bedrock creates one per process, but a caller can supply its own via
// Options.Health when something outside the HTTP endpoints — the gRPC health
// service, for instance — needs to report the same state.
func NewHealthStatus() *HealthStatus {
	return &HealthStatus{
		healthy: false, // Not healthy until OnStart succeeds
		ready:   false, // Not ready until app says so
	}
}

func (h *HealthStatus) SetHealthy(healthy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthy = healthy
}

func (h *HealthStatus) SetReady(ready bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ready = ready
}

func (h *HealthStatus) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.healthy
}

func (h *HealthStatus) IsReady() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ready
}

// healthCheckHandler returns an http.HandlerFunc for the /health endpoint
func healthCheckHandler(status *HealthStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if status.IsHealthy() {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy"})
		}
	}
}

// readyCheckHandler returns an http.HandlerFunc for the /ready endpoint
func readyCheckHandler(status *HealthStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if status.IsReady() {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "not ready"})
		}
	}
}

// liveCheckHandler returns an http.HandlerFunc for the /live endpoint (alias for health)
func liveCheckHandler(status *HealthStatus) http.HandlerFunc {
	return healthCheckHandler(status)
}
