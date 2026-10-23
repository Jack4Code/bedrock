package bedrock

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONResponseHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	resp := JSONResponse{
		StatusCode: 200,
		Data:       map[string]string{"ok": "true"},
		Headers: http.Header{
			"X-Request-ID": {"abc123"},
			"X-Custom":     {"foo"},
		},
	}

	if err := resp.Write(context.Background(), w); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if got := w.Header().Get("X-Request-ID"); got != "abc123" {
		t.Errorf("X-Request-ID: got %q, want %q", got, "abc123")
	}
	if got := w.Header().Get("X-Custom"); got != "foo" {
		t.Errorf("X-Custom: got %q, want %q", got, "foo")
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", got, "application/json")
	}
	if w.Code != 200 {
		t.Errorf("StatusCode: got %d, want 200", w.Code)
	}
}

func TestJSONWithHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	resp := JSONWithHeaders(201, map[string]string{"id": "1"}, http.Header{
		"Location": {"/items/1"},
	})

	if err := resp.Write(context.Background(), w); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if got := w.Header().Get("Location"); got != "/items/1" {
		t.Errorf("Location: got %q, want %q", got, "/items/1")
	}
	if w.Code != 201 {
		t.Errorf("StatusCode: got %d, want 201", w.Code)
	}
}

func TestJSONNoHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	resp := JSON(200, map[string]string{"hello": "world"})

	if err := resp.Write(context.Background(), w); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if w.Code != 200 {
		t.Errorf("StatusCode: got %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", got, "application/json")
	}
}

func TestJSONResponseHeadersDoNotOverrideContentType(t *testing.T) {
	w := httptest.NewRecorder()
	// Caller should not be able to override Content-Type via Headers
	// since Write sets it after applying custom headers
	resp := JSONResponse{
		StatusCode: 200,
		Data:       "test",
		Headers: http.Header{
			"X-Trace": {"trace-id-999"},
		},
	}

	if err := resp.Write(context.Background(), w); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", got, "application/json")
	}
	if got := w.Header().Get("X-Trace"); got != "trace-id-999" {
		t.Errorf("X-Trace: got %q, want %q", got, "trace-id-999")
	}
}

// A status that cannot carry a body gets none, whatever Data holds:
// JSON(204, nil) would otherwise encode a `null` that net/http refuses.
func TestJSONResponseBodylessStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		data   any
	}{
		{http.StatusNoContent, nil},
		{http.StatusNoContent, map[string]string{"ignored": "true"}},
		{http.StatusNotModified, nil},
		{http.StatusContinue, nil},
	} {
		w := httptest.NewRecorder()
		resp := JSONWithHeaders(tc.status, tc.data, http.Header{"X-Trace": {"t1"}})

		if err := resp.Write(context.Background(), w); err != nil {
			t.Fatalf("%d: Write failed: %v", tc.status, err)
		}
		if w.Code != tc.status {
			t.Errorf("%d: StatusCode: got %d", tc.status, w.Code)
		}
		if w.Body.Len() != 0 {
			t.Errorf("%d: body: got %q, want none", tc.status, w.Body.String())
		}
		if got := w.Header().Get("Content-Type"); got != "" {
			t.Errorf("%d: Content-Type: got %q, want none", tc.status, got)
		}
		if got := w.Header().Get("X-Trace"); got != "t1" {
			t.Errorf("%d: X-Trace: got %q, want %q", tc.status, got, "t1")
		}
	}
}

// TestServeRouteNoContentIsClean is the end-to-end case. The client saw a 204
// even before the fix, because the header was already out; what went wrong was
// on the server, where Write's error sent serveRoute into http.Error and
// net/http logged a superfluous WriteHeader on every successful delete.
func TestServeRouteNoContentIsClean(t *testing.T) {
	handler := func(ctx context.Context, r *http.Request) Response {
		return JSON(http.StatusNoContent, nil)
	}

	var serverLog bytes.Buffer
	srv := httptest.NewUnstartedServer(serveRoute(handler, slog.New(slog.DiscardHandler), nil))
	srv.Config.ErrorLog = log.New(&serverLog, "", 0)
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/thing")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("body: got %q, want none", body)
	}
	srv.Close()
	if serverLog.Len() != 0 {
		t.Fatalf("server log: want nothing, got %q", serverLog.String())
	}
}
