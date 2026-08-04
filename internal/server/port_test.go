package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenListenerUsesConfiguredPortWhenAvailable(t *testing.T) {
	t.Setenv("GHR_RUNTIME_DIR", t.TempDir())
	listener, port, cleanup, err := openListener(0)
	if err != nil {
		t.Fatalf("expected free listener, got %v", err)
	}
	defer cleanup()
	defer listener.Close()
	if port == 0 || listener.Addr().String() == "" {
		t.Fatalf("expected assigned listener port, got %d/%s", port, listener.Addr())
	}
}

func TestOpenListenerFallsBackWhenExternalProcessOwnsConfiguredPort(t *testing.T) {
	t.Setenv("GHR_RUNTIME_DIR", t.TempDir())
	external, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve external port: %v", err)
	}
	defer external.Close()
	occupied := external.Addr().(*net.TCPAddr).Port

	listener, port, cleanup, err := openListener(occupied)
	if err != nil {
		t.Fatalf("expected fallback listener, got %v", err)
	}
	defer cleanup()
	defer listener.Close()
	if port == occupied {
		t.Fatalf("expected different port from external occupant %d", occupied)
	}
}

func TestSessionPathUsesIsolatedRuntimeDirectory(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	t.Setenv("GHR_RUNTIME_DIR", runtimeDir)
	if got, want := sessionPath(), filepath.Join(runtimeDir, "session.json"); got != want {
		t.Fatalf("expected session path %q, got %q", want, got)
	}
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("expected lazy runtime directory, got %v", err)
	}
}

func TestValidateBindHostFailsClosedForRemoteWithoutACL(t *testing.T) {
	if err := validateBindHost("0.0.0.0", false); err == nil {
		t.Fatal("expected remote bind without ACL to be rejected")
	}
	if err := validateBindHost("0.0.0.0", true); err != nil {
		t.Fatalf("expected authenticated remote bind to be allowed: %v", err)
	}
	for _, host := range []string{"127.0.0.1", "::1", "localhost", ""} {
		if err := validateBindHost(host, false); err != nil {
			t.Fatalf("expected loopback host %q to be allowed: %v", host, err)
		}
	}
}
