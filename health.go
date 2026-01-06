package bedrock

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
)

// HealthStatus tracks application health
type HealthStatus struct {
	mu      sync.RWMutex
	healthy bool
	ready   bool
}

func newHealthStatus() *HealthStatus {
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

func startHealthServer(port string, status *HealthStatus) *http.Server {
	mux := http.NewServeMux()

	// Register health endpoints
	mux.HandleFunc("/health", healthCheckHandler(status))
	mux.HandleFunc("/ready", readyCheckHandler(status))
	mux.HandleFunc("/live", liveCheckHandler(status))

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go func() {
		log.Printf("Starting health server on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Health server error: %v", err)
		}
	}()

	return server
}
