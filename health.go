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

func startHealthServer(port string, status *HealthStatus) *http.Server {
	mux := http.NewServeMux()

	// Health check - is the app alive?
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if status.IsHealthy() {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy"})
		}
	})

	// Ready check - is the app ready to serve traffic?
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if status.IsReady() {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "not ready"})
		}
	})

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
