package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// recordingMiddleware appends its name to order when the request passes through
// it, which is how these tests observe composition rather than just effect.
func recordingMiddleware(name string, order *[]string) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r *http.Request) Response {
			*order = append(*order, name)
			return next(ctx, r)
		}
	}
}

// rejectingMiddleware short-circuits every request, standing in for the edge
// token check Options.Middleware exists for: it never calls next, so a handler
// that runs at all is a failure.
func rejectingMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r *http.Request) Response {
			return JSON(http.StatusForbidden, map[string]string{"error": "rejected"})
		}
	}
}

// okHandler records that it ran and answers 200.
func okHandler(order *[]string) Handler {
	return func(ctx context.Context, r *http.Request) Response {
		*order = append(*order, "handler")
		return JSON(http.StatusOK, map[string]string{"ok": "true"})
	}
}

// getStatus drives a built handler with one GET and returns the status code.
func getStatus(t *testing.T, h http.Handler, host, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// TestGlobalMiddlewareAppliesWithoutRouteMiddleware is the base case the
// feature exists for: a route that declares no middleware of its own still gets
// the globals, so a route added later cannot silently miss the check.
func TestGlobalMiddlewareAppliesWithoutRouteMiddleware(t *testing.T) {
	var order []string
	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    okHandler(&order),
		Visibility: Public,
	}}

	h := buildRouter(routes, nil, HostConfig{}, []Middleware{recordingMiddleware("global", &order)},
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	if got := getStatus(t, h, "example.com", "/widgets"); got != http.StatusOK {
		t.Fatalf("GET /widgets: got %d, want %d", got, http.StatusOK)
	}
	if want := []string{"global", "handler"}; !slices.Equal(order, want) {
		t.Errorf("execution order = %v, want %v", order, want)
	}
}

// TestGlobalMiddlewareRunsBeforeRouteMiddleware pins the composition: globals
// are outermost, so they see a request before anything the route declares and
// can reject it before route middleware does any work.
func TestGlobalMiddlewareRunsBeforeRouteMiddleware(t *testing.T) {
	var order []string
	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    okHandler(&order),
		Middleware: []Middleware{recordingMiddleware("route", &order)},
		Visibility: Public,
	}}

	h := buildRouter(routes, nil, HostConfig{}, []Middleware{recordingMiddleware("global", &order)},
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	if got := getStatus(t, h, "example.com", "/widgets"); got != http.StatusOK {
		t.Fatalf("GET /widgets: got %d, want %d", got, http.StatusOK)
	}
	if want := []string{"global", "route", "handler"}; !slices.Equal(order, want) {
		t.Errorf("execution order = %v, want %v", order, want)
	}
}

// TestGlobalMiddlewareExecutesInOrderGiven mirrors Chain's contract: globals
// execute left-to-right, so the slice reads in execution order.
func TestGlobalMiddlewareExecutesInOrderGiven(t *testing.T) {
	var order []string
	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    okHandler(&order),
		Middleware: []Middleware{recordingMiddleware("route", &order)},
		Visibility: Public,
	}}

	globals := []Middleware{
		recordingMiddleware("first", &order),
		recordingMiddleware("second", &order),
		recordingMiddleware("third", &order),
	}
	h := buildRouter(routes, nil, HostConfig{}, globals,
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	if got := getStatus(t, h, "example.com", "/widgets"); got != http.StatusOK {
		t.Fatalf("GET /widgets: got %d, want %d", got, http.StatusOK)
	}
	if want := []string{"first", "second", "third", "route", "handler"}; !slices.Equal(order, want) {
		t.Errorf("execution order = %v, want %v", order, want)
	}
}

// TestNoGlobalMiddlewareChangesNothing checks the upgrade path: a nil or empty
// Options.Middleware leaves per-route behaviour exactly as it was.
func TestNoGlobalMiddlewareChangesNothing(t *testing.T) {
	for _, c := range []struct {
		name    string
		globals []Middleware
	}{
		{"nil", nil},
		{"empty", []Middleware{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var order []string
			routes := []Route{{
				Method:     http.MethodGet,
				Path:       "/widgets",
				Handler:    okHandler(&order),
				Middleware: []Middleware{recordingMiddleware("route", &order)},
				Visibility: Public,
			}}

			h := buildRouter(routes, nil, HostConfig{}, c.globals,
				NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

			if got := getStatus(t, h, "example.com", "/widgets"); got != http.StatusOK {
				t.Fatalf("GET /widgets: got %d, want %d", got, http.StatusOK)
			}
			if want := []string{"route", "handler"}; !slices.Equal(order, want) {
				t.Errorf("execution order = %v, want %v", order, want)
			}
		})
	}
}

// TestGlobalMiddlewareCanShortCircuit is the property the edge token check
// depends on: a global that never calls next stops the request, and the handler
// does not run.
func TestGlobalMiddlewareCanShortCircuit(t *testing.T) {
	var order []string
	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    okHandler(&order),
		Middleware: []Middleware{recordingMiddleware("route", &order)},
		Visibility: Public,
	}}

	h := buildRouter(routes, nil, HostConfig{}, []Middleware{rejectingMiddleware()},
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	if got := getStatus(t, h, "example.com", "/widgets"); got != http.StatusForbidden {
		t.Fatalf("GET /widgets: got %d, want %d", got, http.StatusForbidden)
	}
	if len(order) != 0 {
		t.Errorf("nothing past the rejecting global should have run; got %v", order)
	}
}

// TestGlobalMiddlewareSkipsHealthEndpoints is the regression this feature is
// most likely to cause. With health merged onto the application port, a global
// that rejects everything must not reach the probes: an orchestrator whose
// liveness checks start failing rolls or restarts the task indefinitely, so a
// deploy that adds an edge token check would take the service down rather than
// protect it.
func TestGlobalMiddlewareSkipsHealthEndpoints(t *testing.T) {
	health := NewHealthStatus()
	health.SetHealthy(true)
	health.SetReady(true)

	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    func(ctx context.Context, r *http.Request) Response { return JSON(http.StatusOK, nil) },
		Visibility: Public,
	}}

	// mergeServers = true: health endpoints share the application router.
	h := buildRouter(routes, nil, HostConfig{}, []Middleware{rejectingMiddleware()},
		health, true, DefaultCORSConfig(), quietLogger())

	for _, path := range []string{"/health", "/ready", "/live"} {
		if got := getStatus(t, h, "example.com", path); got != http.StatusOK {
			t.Errorf("GET %s: got %d, want %d — global middleware must not gate probes", path, got, http.StatusOK)
		}
	}

	if got := getStatus(t, h, "example.com", "/widgets"); got != http.StatusForbidden {
		t.Errorf("GET /widgets: got %d, want %d", got, http.StatusForbidden)
	}
}

// TestGlobalMiddlewareAppliesToLoopbackSubrouter is the other half of that
// boundary. isLoopbackHost matches on the Host header, which the client
// supplies, so a global that did not apply to the loopback subrouter could be
// stepped around by any remote caller sending "Host: localhost" — which would
// make this feature a hole rather than a control.
func TestGlobalMiddlewareAppliesToLoopbackSubrouter(t *testing.T) {
	const publicHost = "api.example.com"
	hosts := HostConfig{Public: publicHost, Private: "api.internal.example.com", TrustLoopback: true}

	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    func(ctx context.Context, r *http.Request) Response { return JSON(http.StatusOK, nil) },
		Visibility: Public,
	}}

	h := buildRouter(routes, nil, hosts, []Middleware{rejectingMiddleware()},
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	for _, host := range []string{"localhost:8080", "127.0.0.1:8080", publicHost} {
		if got := getStatus(t, h, host, "/widgets"); got != http.StatusForbidden {
			t.Errorf("GET /widgets with Host=%q: got %d, want %d — loopback must not bypass globals",
				host, got, http.StatusForbidden)
		}
	}
}

// TestGlobalMiddlewareDoesNotApplyToPreflight records the decision rather than
// merely tolerating it: OPTIONS is answered outside the chain, so a preflight
// succeeds for a registered path whatever the globals would say. That is what
// keeps CORS working for browsers, and it means OPTIONS can be used to probe
// which paths exist. If this test starts failing, that trade has changed and
// Options.Middleware needs updating with it.
func TestGlobalMiddlewareDoesNotApplyToPreflight(t *testing.T) {
	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    func(ctx context.Context, r *http.Request) Response { return JSON(http.StatusOK, nil) },
		Visibility: Public,
	}}

	h := buildRouter(routes, nil, HostConfig{}, []Middleware{rejectingMiddleware()},
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	req := httptest.NewRequest(http.MethodOptions, "http://example.com/widgets", nil)
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("OPTIONS /widgets: got %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestGlobalMiddlewareSurvivesCORS confirms CORS stays outermost: a request the
// globals reject still carries CORS headers, so a browser sees the 403 rather
// than an opaque network error it cannot report.
func TestGlobalMiddlewareSurvivesCORS(t *testing.T) {
	routes := []Route{{
		Method:     http.MethodGet,
		Path:       "/widgets",
		Handler:    func(ctx context.Context, r *http.Request) Response { return JSON(http.StatusOK, nil) },
		Visibility: Public,
	}}

	h := buildRouter(routes, nil, HostConfig{}, []Middleware{rejectingMiddleware()},
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	req := httptest.NewRequest(http.MethodGet, "http://example.com/widgets", nil)
	req.Host = "example.com"
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /widgets: got %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Error("rejected request lost its CORS headers; CORS must wrap global middleware")
	}
}

// TestGlobalMiddlewareNotAppliedToSkippedRoutes checks the serve filter still
// wins: a route this process does not serve is absent, not merely wrapped.
func TestGlobalMiddlewareNotAppliedToSkippedRoutes(t *testing.T) {
	var order []string
	routes := []Route{
		{Method: http.MethodGet, Path: "/pub", Handler: okHandler(&order), Visibility: Public},
		{Method: http.MethodGet, Path: "/priv", Handler: okHandler(&order), Visibility: Private},
	}

	h := buildRouter(routes, serveSet{Public: true}, HostConfig{}, []Middleware{recordingMiddleware("global", &order)},
		NewHealthStatus(), false, DefaultCORSConfig(), quietLogger())

	if got := getStatus(t, h, "example.com", "/priv"); got != http.StatusNotFound {
		t.Errorf("GET /priv: got %d, want %d", got, http.StatusNotFound)
	}
	if got := getStatus(t, h, "example.com", "/pub"); got != http.StatusOK {
		t.Errorf("GET /pub: got %d, want %d", got, http.StatusOK)
	}
	if want := []string{"global", "handler"}; !slices.Equal(order, want) {
		t.Errorf("execution order = %v, want %v", order, want)
	}
}
