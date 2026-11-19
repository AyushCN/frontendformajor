package orchestrator

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/api-sandbox-links/backend/internal/config"
)

func TestWaitHealthyRequiresSuccessfulHTTPResponse(t *testing.T) {
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probes.Add(1) <= 2 {
			http.Error(w, "not ready", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hostPort := server.Listener.Addr().(*net.TCPAddr)
	o := &Orchestrator{}
	err := o.WaitHealthy(context.Background(), "test-container", "127.0.0.1", hostPort.Port, 3*time.Second)
	if err != nil {
		t.Fatalf("WaitHealthy returned error after HTTP became ready: %v", err)
	}
	if probes.Load() < 3 {
		t.Fatalf("expected HTTP readiness polling, got %d probe(s)", probes.Load())
	}
}

func TestSandboxHostConfigIsolationDefaults(t *testing.T) {
	cfg := &config.Config{
		SandboxInit:     true,
		SandboxHardened: false,
		SandboxRuntime:  "",
	}

	hc := sandboxHostConfig(cfg)
	if hc.Init == nil || !*hc.Init {
		t.Error("expected hc.Init to be true by default")
	}
	if hc.ReadonlyRootfs {
		t.Error("expected hc.ReadonlyRootfs to be false by default")
	}
	if len(hc.Tmpfs) != 0 {
		t.Errorf("expected no tmpfs mounts by default, got %v", hc.Tmpfs)
	}
	if hc.Runtime != "" {
		t.Errorf("expected empty Runtime by default, got %q", hc.Runtime)
	}
	if len(hc.CapDrop) == 0 || hc.CapDrop[0] != "ALL" {
		t.Errorf("expected CapDrop ALL, got %v", hc.CapDrop)
	}
	if len(hc.Binds) != 0 {
		t.Errorf("expected no host binds (workspace unmounted), got %v", hc.Binds)
	}
}

// TestRuntimeWritesPreventedHostWorkspaceNotMounted verifies that the container's
// host configuration never mounts the host workspace directory, ensuring that a
// running app container cannot write to or mutate the host workspace during a fork.
func TestRuntimeWritesPreventedHostWorkspaceNotMounted(t *testing.T) {
	cfg := &config.Config{
		SandboxWorkDir: "/tmp/api-sandbox-links/work",
	}
	hc := sandboxHostConfig(cfg)
	if len(hc.Binds) != 0 {
		t.Fatalf("CRITICAL: host workspace directory is bind-mounted into container: %v", hc.Binds)
	}
}

func TestSandboxHostConfigHardened(t *testing.T) {
	cfg := &config.Config{
		SandboxInit:     true,
		SandboxHardened: true,
		SandboxRuntime:  "runsc",
	}

	hc := sandboxHostConfig(cfg)
	if hc.Init == nil || !*hc.Init {
		t.Error("expected hc.Init to be true")
	}
	if !hc.ReadonlyRootfs {
		t.Error("expected hc.ReadonlyRootfs to be true under SandboxHardened")
	}
	if hc.Tmpfs["/tmp"] == "" || hc.Tmpfs["/run"] == "" {
		t.Errorf("expected /tmp and /run tmpfs mounts, got %v", hc.Tmpfs)
	}
	if hc.Runtime != "runsc" {
		t.Errorf("expected Runtime 'runsc', got %q", hc.Runtime)
	}
}

func TestSandboxHostConfigDisabledInit(t *testing.T) {
	cfg := &config.Config{
		SandboxInit: false,
	}

	hc := sandboxHostConfig(cfg)
	if hc.Init == nil || *hc.Init {
		t.Error("expected hc.Init to be false when SandboxInit is false")
	}
}

func TestSidecarHostConfigDefaults(t *testing.T) {
	hcPostgres := sidecarHostConfig("vol_pg", "postgres")
	if hcPostgres.Init == nil || !*hcPostgres.Init {
		t.Error("expected sidecar postgres Init to be true")
	}
	if len(hcPostgres.Binds) != 1 || hcPostgres.Binds[0] != "vol_pg" {
		t.Errorf("expected bind vol_pg, got %v", hcPostgres.Binds)
	}

	hcMySQL := sidecarHostConfig("vol_my", "mysql")
	if hcMySQL.Init == nil || !*hcMySQL.Init {
		t.Error("expected sidecar mysql Init to be true")
	}
	if len(hcMySQL.Binds) != 1 || hcMySQL.Binds[0] != "vol_my:/var/lib/mysql" {
		t.Errorf("expected bind vol_my:/var/lib/mysql, got %v", hcMySQL.Binds)
	}
}
