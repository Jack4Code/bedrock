package bedrock

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func TestParseVisibility(t *testing.T) {
	cases := []struct {
		in      string
		want    Visibility
		wantErr bool
	}{
		{"public", Public, false},
		{"Private", Private, false},
		{"  gated  ", Gated, false},
		{"PUBLIC", Public, false},
		{"", 0, true},
		{"internal", 0, true},
	}
	for _, c := range cases {
		got, err := ParseVisibility(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseVisibility(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("ParseVisibility(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestServeSetServes(t *testing.T) {
	// Empty set serves everything.
	var all serveSet
	for _, v := range []Visibility{Public, Private, Gated} {
		if !all.serves(v) {
			t.Errorf("empty serveSet should serve %v", v)
		}
	}

	only := serveSet{Public: true}
	if !only.serves(Public) {
		t.Error("serveSet{Public} should serve Public")
	}
	if only.serves(Private) || only.serves(Gated) {
		t.Error("serveSet{Public} should not serve Private or Gated")
	}
}

func TestResolveServe(t *testing.T) {
	t.Run("env overrides options", func(t *testing.T) {
		t.Setenv("BEDROCK_SERVE", "private")
		got, err := resolveServe([]Visibility{Public, Gated})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.serves(Private) || got.serves(Public) || got.serves(Gated) {
			t.Errorf("env should win: got %v", got)
		}
	})

	t.Run("env multi-value with spaces", func(t *testing.T) {
		t.Setenv("BEDROCK_SERVE", " public , gated ")
		got, err := resolveServe(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.serves(Public) || !got.serves(Gated) || got.serves(Private) {
			t.Errorf("got %v, want public,gated", got)
		}
	})

	t.Run("env unknown token errors", func(t *testing.T) {
		t.Setenv("BEDROCK_SERVE", "public,internal")
		if _, err := resolveServe(nil); err == nil {
			t.Error("expected error for unknown visibility token")
		}
	})

	t.Run("env set but empty errors", func(t *testing.T) {
		t.Setenv("BEDROCK_SERVE", " , ")
		if _, err := resolveServe(nil); err == nil {
			t.Error("expected error when BEDROCK_SERVE names no valid visibility")
		}
	})

	t.Run("options used when env unset", func(t *testing.T) {
		got, err := resolveServe([]Visibility{Gated})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.serves(Gated) || got.serves(Public) {
			t.Errorf("options should apply: got %v", got)
		}
	})

	t.Run("nil when nothing set means serve all", func(t *testing.T) {
		got, err := resolveServe(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty (serve all), got %v", got)
		}
		if !got.serves(Private) {
			t.Error("empty result should serve all visibilities")
		}
	})
}

func TestResolveRunJobs(t *testing.T) {
	t.Run("default is true", func(t *testing.T) {
		got, err := resolveRunJobs(nil)
		if err != nil || !got {
			t.Errorf("default should be true, got %v err %v", got, err)
		}
	})

	t.Run("options false honored", func(t *testing.T) {
		f := false
		got, err := resolveRunJobs(&f)
		if err != nil || got {
			t.Errorf("expected false, got %v err %v", got, err)
		}
	})

	t.Run("env overrides options", func(t *testing.T) {
		t.Setenv("BEDROCK_RUN_JOBS", "true")
		f := false
		got, err := resolveRunJobs(&f)
		if err != nil || !got {
			t.Errorf("env true should win over options false, got %v err %v", got, err)
		}
	})

	t.Run("env invalid errors", func(t *testing.T) {
		t.Setenv("BEDROCK_RUN_JOBS", "yesplease")
		if _, err := resolveRunJobs(nil); err == nil {
			t.Error("expected error for invalid bool")
		}
	})
}

// TestServeFilterRegistration is an integration-style check that the serve
// filter, host matching and route registration compose the way RunWithOptions
// wires them: a process serving only {Public} answers a public route on the
// public host but 404s a private route on the private host — the private route
// is absent, not merely host-gated.
func TestServeFilterRegistration(t *testing.T) {
	const (
		publicHost  = "api.example.com"
		privateHost = "api.internal.example.com"
	)
	hosts := HostConfig{Public: publicHost, Private: privateHost, Gated: publicHost}

	routes := []Route{
		{Method: http.MethodGet, Path: "/pub", Visibility: Public},
		{Method: http.MethodGet, Path: "/priv", Visibility: Private},
	}

	// Build a router applying the same serve-filter + host-subrouter logic.
	build := func(served serveSet) *mux.Router {
		router := mux.NewRouter()
		handler := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }
		for _, r := range routes {
			if !served.serves(r.Visibility) {
				continue
			}
			router.Host(hosts.hostFor(r.Visibility)).Subrouter().
				HandleFunc(r.Path, handler).Methods(r.Method)
		}
		return router
	}

	publicOnly := build(serveSet{Public: true})

	cases := []struct {
		host, path string
		want       int
	}{
		{publicHost, "/pub", http.StatusTeapot},     // served
		{privateHost, "/priv", http.StatusNotFound}, // not registered in this process
		{publicHost, "/priv", http.StatusNotFound},  // wrong host anyway
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://"+c.host+c.path, nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		publicOnly.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("public-only process GET %s Host=%q: got %d, want %d", c.path, c.host, rec.Code, c.want)
		}
	}

	// A process serving everything answers both.
	all := build(nil)
	for _, c := range []struct {
		host, path string
	}{{publicHost, "/pub"}, {privateHost, "/priv"}} {
		req := httptest.NewRequest(http.MethodGet, "http://"+c.host+c.path, nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		all.ServeHTTP(rec, req)
		if rec.Code != http.StatusTeapot {
			t.Errorf("serve-all process GET %s Host=%q: got %d, want %d", c.path, c.host, rec.Code, http.StatusTeapot)
		}
	}
}
