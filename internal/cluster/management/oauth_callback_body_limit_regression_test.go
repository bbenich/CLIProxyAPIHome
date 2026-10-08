package management

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

// callbackFillerBody yields a JSON prefix followed by filler and counts the
// bytes the handler consumed.
type callbackFillerBody struct {
	prefix string
	size   int64
	read   atomic.Int64
}

func (b *callbackFillerBody) Read(p []byte) (int, error) {
	offset := b.read.Load()
	if offset >= b.size {
		return 0, io.EOF
	}
	if remaining := b.size - offset; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	for i := range p {
		if position := offset + int64(i); position < int64(len(b.prefix)) {
			p[i] = b.prefix[position]
		} else {
			p[i] = 'A'
		}
	}
	b.read.Add(int64(len(p)))
	return len(p), nil
}

func (b *callbackFillerBody) Close() error { return nil }

func TestOAuthCallbackBoundsUnauthenticatedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, nil, "127.0.0.1", 0)
	engine := gin.New()
	engine.POST("/v8/management/oauth/callback", handler.PostOAuthCallback)

	body := &callbackFillerBody{prefix: `{"state":"`, size: 8 << 20}
	req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/callback", nil)
	req.Body = body
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if read := body.read.Load(); read > 2*(64<<10) {
		t.Fatalf("handler read %d bytes of an unauthenticated callback body", read)
	}
}

func TestOAuthCallbackSmallBodyStillValidated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, nil, "127.0.0.1", 0)
	engine := gin.New()
	engine.POST("/v8/management/oauth/callback", handler.PostOAuthCallback)

	req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/callback", strings.NewReader(`{"code":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "state is required") {
		t.Fatalf("status = %d body = %s, want 400 state is required", rec.Code, rec.Body.String())
	}
}
