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

// startWithTimeouts starts an httpServer built by newHTTPServer with its header
// and idle timeouts shortened, so tests can cross them without waiting out the
// real values. Everything else about the server is what bedrock ships.
func startWithTimeouts(t *testing.T, header, idle time.Duration, handler http.Handler) string {
	t.Helper()
	srv := newHTTPServer("http", "127.0.0.1:0", handler, quietLogger())
	srv.srv.ReadHeaderTimeout = header
	srv.srv.IdleTimeout = idle
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

func TestHTTPServerSetsTimeouts(t *testing.T) {
	srv := newHTTPServer("http", "127.0.0.1:0", http.NewServeMux(), quietLogger()).srv
	if srv.ReadHeaderTimeout != readHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %s, want %s", srv.ReadHeaderTimeout, readHeaderTimeout)
	}
	if srv.IdleTimeout != idleTimeout {
		t.Errorf("IdleTimeout = %s, want %s", srv.IdleTimeout, idleTimeout)
	}
}

// TestReadHeaderTimeoutClosesSlowHeaderClients is the attack the timeout
// exists for: a client that starts a request and never finishes its headers
// must lose the connection rather than hold it forever.
func TestReadHeaderTimeoutClosesSlowHeaderClients(t *testing.T) {
	addr := startWithTimeouts(t, 100*time.Millisecond, idleTimeout, http.NewServeMux())

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

// TestIdleTimeoutClosesIdleConnections: a client that makes one request and
// then leaves its keep-alive connection open must lose it.
func TestIdleTimeoutClosesIdleConnections(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {})
	addr := startWithTimeouts(t, readHeaderTimeout, 100*time.Millisecond, mux)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example\r\n\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.Close {
		t.Fatal("server did not offer keep-alive, so idling proves nothing")
	}

	// Send nothing more. The server should hang up well before this
	// client-side deadline; if it does not, the read returns a timeout error.
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("server kept an idle connection open: %v", err)
	}
}

// TestTimeoutsSpareRunningHandlers holds readHeaderTimeout and idleTimeout to
// their doc comments: neither applies once a request's headers are in. A body
// sent slowly and a handler that runs long each outlast both timeouts, and a
// keep-alive connection that idles between requests outlasts the header
// timeout; all must be unaffected — in particular the request context must
// not be cancelled, or serveRoute would log it as a client disconnect.
func TestTimeoutsSpareRunningHandlers(t *testing.T) {
	const (
		header = 100 * time.Millisecond
		idle   = 300 * time.Millisecond
		step   = 100 * time.Millisecond
	)

	cancelled := make(chan bool, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			cancelled <- true
		case <-time.After(2 * idle):
			cancelled <- false
		}
	})
	addr := startWithTimeouts(t, header, idle, mux)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)

	// roundTrip sends a POST with a 4-byte body, one byte every step, so the
	// body alone takes longer than both timeouts to arrive.
	roundTrip := func(name string) {
		t.Helper()
		if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: example\r\nContent-Length: 4\r\n\r\n"); err != nil {
			t.Fatalf("%s: write headers: %v", name, err)
		}
		for _, b := range []string{"a", "b", "c", "d"} {
			time.Sleep(step)
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
			t.Errorf("%s: request context was cancelled by a server timeout", name)
		}
	}

	roundTrip("first request")
	// Idle on the keep-alive connection past the header timeout but inside
	// the idle timeout.
	time.Sleep(2 * header)
	roundTrip("request after idle")
}
