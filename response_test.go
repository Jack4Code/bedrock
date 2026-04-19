package bedrock

import (
	"context"
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
