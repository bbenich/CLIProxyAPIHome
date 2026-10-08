package managementhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	cpaconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func newBodyLimitTestEngine(limit int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(requestBodyLimitMiddleware(limit))
	engine.POST("/echo", func(c *gin.Context) {
		data, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			// Mimic handlers that report read failures as a bad request.
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		c.String(http.StatusOK, "%d", len(data))
	})
	return engine
}

func TestRequestBodyLimitMiddleware(t *testing.T) {
	const limit = 1024
	engine := newBodyLimitTestEngine(limit)
	cases := []struct {
		name          string
		size          int64
		declareLength bool
		wantStatus    int
		maxRead       int64
	}{
		{name: "declared at limit", size: limit, declareLength: true, wantStatus: http.StatusOK, maxRead: limit},
		{name: "chunked at limit", size: limit, declareLength: false, wantStatus: http.StatusOK, maxRead: limit},
		{name: "declared over limit", size: limit + 1, declareLength: true, wantStatus: http.StatusRequestEntityTooLarge, maxRead: 0},
		{name: "chunked over limit", size: 64 * limit, declareLength: false, wantStatus: http.StatusRequestEntityTooLarge, maxRead: limit + 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, body := newOversizedRequest(http.MethodPost, "/echo", "application/octet-stream", "", tc.size, tc.declareLength)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if read := body.read.Load(); read > tc.maxRead {
				t.Fatalf("read %d bytes, want at most %d", read, tc.maxRead)
			}
		})
	}
}

func TestRequestBodyLimitMiddlewareKeepsSmallerInnerLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(requestBodyLimitMiddleware(1 << 20))
	engine.POST("/inner", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16)
		if _, errRead := io.ReadAll(c.Request.Body); errRead != nil {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "inner limit"})
			return
		}
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/inner", strings.NewReader(strings.Repeat("x", 17)))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "inner limit") {
		t.Fatalf("status = %d body = %s, want inner 413", rec.Code, rec.Body.String())
	}
}

func TestHandlerConfigSyncAppliesOnlyChangedSources(t *testing.T) {
	configSync := newHandlerConfigSync(nil)
	builds := 0
	build := func() *cpaconfig.Config {
		builds++
		return nil
	}
	first := configSync.apply("a", build)
	second := configSync.apply("a", build)
	if builds != 1 || first != second {
		t.Fatalf("builds = %d, same config = %v; want one build for an unchanged source", builds, first == second)
	}
	if third := configSync.apply("b", build); builds != 2 || third == first {
		t.Fatalf("builds = %d; want a rebuild for a changed source", builds)
	}
}
