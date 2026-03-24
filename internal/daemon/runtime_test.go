package daemon

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestListenUnixSocket_RemovesStaleFile(t *testing.T) {
	t.Parallel()

	path := fmt.Sprintf("/tmp/herd-%d.sock", time.Now().UnixNano())
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	lis, err := ListenUnixSocket(path)
	if err != nil {
		t.Fatalf("ListenUnixSocket returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = lis.Close()
		_ = RemoveUnixSocket(path)
	})

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perms := fi.Mode().Perm(); perms != 0o600 {
		t.Fatalf("expected socket perms 0600, got %o", perms)
	}
}

func TestNewDataPlaneHandler_Healthz(t *testing.T) {
	t.Parallel()

	h := NewDataPlaneHandler(nil, "/metrics")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "ok" {
		t.Fatalf("expected body 'ok', got %q", string(body))
	}
}
