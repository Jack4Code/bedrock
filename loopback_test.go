package bedrock

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"localhost:8080", true},
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"::1", true},
		{"[::1]:8080", true},
		{"api-dev.speechbag.com", false},
		{"api-dev.speechbag.com:8080", false},
		{"localhost.evil.com", false},
		{"127.0.0.1.evil.com", false},
		{"", false},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
		req.Host = c.host
		if got := isLoopbackHost(req, &mux.RouteMatch{}); got != c.want {
			t.Errorf("isLoopbackHost(Host=%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestLoopbackSubrouterRouting verifies the matcher composes with a host-scoped
// subrouter the same way RunWithOptions wires them: a gated route registered on
// its visibility host AND on the loopback subrouter is reachable via the gated
// host and via localhost, but not via an unrelated external host.
func TestLoopbackSubrouterRouting(t *testing.T) {
	const gatedHost = "api-dev.speechbag.com"

	router := mux.NewRouter()
	handler := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }

	loopback := router.NewRoute().MatcherFunc(isLoopbackHost).Subrouter()
	loopback.HandleFunc("/api/users/me", handler).Methods(http.MethodGet)
	router.Host(gatedHost).Subrouter().HandleFunc("/api/users/me", handler).Methods(http.MethodGet)

	cases := []struct {
		host string
		want int
	}{
		{"localhost:8080", http.StatusTeapot},                 // co-located SSR caller
		{"127.0.0.1:8080", http.StatusTeapot},                 // co-located, IP form
		{gatedHost, http.StatusTeapot},                        // external via reverse proxy
		{"api-dev.public.speechbag.com", http.StatusNotFound}, // wrong host => no match
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://"+c.host+"/api/users/me", nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("GET /api/users/me with Host=%q: got %d, want %d", c.host, rec.Code, c.want)
		}
	}
}
