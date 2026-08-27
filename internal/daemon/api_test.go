package daemon

import (
	"net/http/httptest"
	"testing"
	"context"
	"io"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHealthEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2 * time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", "/healthz", nil)
	w := httptest.NewRecorder()

	h := NewControlPlaneHandler(nil)
	h.ServeHTTP(w, req)

	res := w.Result()
	
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	assert.NoError(t, err)
	assert.Equal(t, "OK", string(data))

}
