package bedrock

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// startWithHeaderTimeout starts an httpServer built by newHTTPServer with its
// header timeout shortened to d, so tests can cross it without waiting out the
// real ten seconds. Everything else about the server is what bedrock ships.
func startWithHeaderTimeout(t *testing.T, d time.Duration, handler http.Handler) string {
	t.Helper()
	srv := newHTTPServer("http", "127.0.0.1:0", handler, quietLogger())
	srv.srv.ReadHeaderTimeout = d
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv.boundAddr().String()
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestHTTPServerSetsReadHeaderTimeout(t *testing.T) {
	srv := newHTTPServer("http", "127.0.0.1:0", http.NewServeMux(), quietLogger()).srv
	if srv.ReadHeaderTimeout != readHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %s, want %s", srv.ReadHeaderTimeout, readHeaderTimeout)
	}
}

// TestReadHeaderTimeoutClosesSlowHeaderClients is the attack the timeout
// exists for: a client that starts a request and never finishes its headers
// must lose the connection rather than hold it forever.
func TestReadHeaderTimeoutClosesSlowHeaderClients(t *testing.T) {
	addr := startWithHeaderTimeout(t, 100*time.Millisecond, http.NewServeMux())

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Half a request: no terminating blank line, so the headers never end.
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The server should hang up well before this client-side deadline. If the
	// deadline fires first, the read returns a timeout error instead of EOF.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("server kept a slow-header connection open: %v", err)
	}
}

// TestReadHeaderTimeoutSparesRunningHandlers holds readHeaderTimeout to its
// doc comment: once the headers are in, the deadline is gone. A body sent
// slowly, a handler that runs long, and a keep-alive connection that sits
// idle between requests each outlast the timeout several times over and must
// all be unaffected — in particular the request context must not be
// cancelled, or serveRoute would log it as a client disconnect.
func TestReadHeaderTimeoutSparesRunningHandlers(t *testing.T) {
	const timeout = 100 * time.Millisecond

	cancelled := make(chan bool, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			cancelled <- true
		case <-time.After(3 * timeout):
			cancelled <- false
		}
	})
	addr := startWithHeaderTimeout(t, timeout, mux)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)

	// roundTrip sends a POST with a 4-byte body, one byte every timeout, so
	// the body alone takes several header timeouts to arrive.
	roundTrip := func(name string) {
		t.Helper()
		if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: example\r\nContent-Length: 4\r\n\r\n"); err != nil {
			t.Fatalf("%s: write headers: %v", name, err)
		}
		for _, b := range []string{"a", "b", "c", "d"} {
			time.Sleep(timeout)
			if _, err := io.WriteString(conn, b); err != nil {
				t.Fatalf("%s: write body: %v", name, err)
			}
		}
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("%s: read response: %v", name, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, resp.StatusCode)
		}
		if <-cancelled {
			t.Errorf("%s: request context was cancelled by the header timeout", name)
		}
	}

	roundTrip("first request")
	// Idle on the keep-alive connection for several header timeouts.
	time.Sleep(3 * timeout)
	roundTrip("request after idle")
}
