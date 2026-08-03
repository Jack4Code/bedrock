package bgrpc

import (
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/Jack4Code/bedrock/config"
)

// waitFor polls cond until it holds, failing the test with what it was waiting
// for if it never does.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// freeTCPPort returns a port that was free a moment ago.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}

func bedrockConfig(httpPort, healthPort int) config.BaseConfig {
	return config.BaseConfig{
		HTTPPort:   httpPort,
		HealthPort: healthPort,
		LogLevel:   "error",
	}
}

// signalSelf sends SIGTERM to this process so bedrock's own signal handler
// begins shutdown. Only safe once the handler is known to be installed.
func signalSelf(t *testing.T) {
	t.Helper()
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find own process: %v", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal self: %v", err)
	}
}
