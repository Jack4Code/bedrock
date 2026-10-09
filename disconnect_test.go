package bedrock

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// syncBuffer is a bytes.Buffer safe to read from the test goroutine while the
// server goroutine logs into it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// failingResponse is a Response whose Write always fails, and records that it
// was called so tests can tell "skipped" apart from "called and failed".
type failingResponse struct{ called *bool }

func (r failingResponse) Write(ctx context.Context, w http.ResponseWriter) error {
	*r.called = true
	return errors.New("write failed")
}

const disconnectMsg = "connection closed before response was written"

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// TestServeRouteLogsClientDisconnect is the end-to-end case: a real client
// gives up while the handler is still working, and the handler — which, like
// most real handlers, finishes its work regardless — returns into a dead
// connection. That must show up in the log rather than vanish.
func TestServeRouteLogsClientDisconnect(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	handlerDone := make(chan struct{})
	handler := func(ctx context.Context, r *http.Request) Response {
		defer close(handlerDone)
		<-ctx.Done()
		return JSON(http.StatusOK, map[string]string{"ok": "true"})
	}

	srv := httptest.NewServer(serveRoute(handler, logger))
	defer srv.Close()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	if resp, err := client.Get(srv.URL + "/slow"); err == nil {
		resp.Body.Close()
		t.Fatal("expected the client to time out")
	}

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("request context was never cancelled after the client went away")
	}

	// The log line is written after the handler returns, so poll briefly.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), disconnectMsg) {
		if time.Now().After(deadline) {
			t.Fatalf("disconnect was not logged; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "path=/slow") {
		t.Errorf("disconnect log is missing the path; logs:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "remote_addr=127.0.0.1:") {
		t.Errorf("disconnect log is missing the peer address; logs:\n%s", logs.String())
	}
}

// TestServeRouteDisconnectSkipsErrorFallback: when the client is gone, a
// failing Write must not be answered with a 500 — there is nobody to send it
// to — but Write itself is still called so a Response that cleans up in Write
// keeps doing so.
func TestServeRouteDisconnectSkipsErrorFallback(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	var writeCalled bool
	handler := func(ctx context.Context, r *http.Request) Response {
		return failingResponse{called: &writeCalled}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/gone", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	serveRoute(handler, logger).ServeHTTP(rec, req)

	if !writeCalled {
		t.Error("Response.Write was skipped for a disconnected client")
	}
	if rec.Code == http.StatusInternalServerError {
		t.Error("wrote a 500 to a client that had already disconnected")
	}
	if !strings.Contains(logs.String(), disconnectMsg) {
		t.Errorf("disconnect was not logged; logs:\n%s", logs.String())
	}
}

// TestServeRouteWriteErrorStill500s: a Write failure with the client still
// connected keeps the existing behaviour.
func TestServeRouteWriteErrorStill500s(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	var writeCalled bool
	handler := func(ctx context.Context, r *http.Request) Response {
		return failingResponse{called: &writeCalled}
	}

	rec := httptest.NewRecorder()
	serveRoute(handler, logger).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(logs.String(), disconnectMsg) {
		t.Errorf("logged a disconnect for a connected client; logs:\n%s", logs.String())
	}
}

// TestServeRouteNormalRequestIsQuiet: the common path logs nothing.
func TestServeRouteNormalRequestIsQuiet(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	var order []string
	rec := httptest.NewRecorder()
	serveRoute(okHandler(&order), logger).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if logs.String() != "" {
		t.Errorf("expected no logs, got:\n%s", logs.String())
	}
}
