package managementhttp

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

const (
	// managementMaxRequestBodyBytes caps request bodies on the authenticated
	// Management API groups. It is far above the largest legitimate payloads
	// (multi-file auth uploads, config.yaml, billing imports, API calls) and
	// above every per-route limit, which keep applying inside this cap.
	managementMaxRequestBodyBytes int64 = 64 << 20
	// oauthCallbackMaxRequestBodyBytes caps the unauthenticated OAuth callback.
	// Real callbacks only carry state, code, error, provider and redirect_url.
	oauthCallbackMaxRequestBodyBytes int64 = 64 << 10
)

// requestBodyLimitMiddleware rejects request bodies larger than limit with 413.
// Bodies that declare a larger Content-Length are rejected before any read.
// Bodies of unknown length are cut off at limit; when that happens, any error
// status the handler writes is reported as 413.
func requestBodyLimitMiddleware(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody || limit <= 0 {
			if c != nil {
				c.Next()
			}
			return
		}
		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request_body_too_large"})
			return
		}
		body := &limitedRequestBody{ReadCloser: http.MaxBytesReader(c.Writer, c.Request.Body, limit)}
		c.Request.Body = body
		originalWriter := c.Writer
		c.Writer = &limitedBodyResponseWriter{ResponseWriter: originalWriter, body: body}
		c.Next()
		c.Writer = originalWriter
	}
}

// limitedRequestBody records whether the wrapped body hit its size limit.
type limitedRequestBody struct {
	io.ReadCloser
	exceeded atomic.Bool
}

// Read reads from the limited body and records a size-limit overflow.
func (b *limitedRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			b.exceeded.Store(true)
		}
	}
	return n, err
}

// limitedBodyResponseWriter reports error responses caused by an oversized body as 413.
type limitedBodyResponseWriter struct {
	gin.ResponseWriter
	body *limitedRequestBody
}

// WriteHeader rewrites error statuses to 413 after the body exceeded its limit.
func (w *limitedBodyResponseWriter) WriteHeader(code int) {
	if code >= http.StatusBadRequest && w.body != nil && w.body.exceeded.Load() {
		code = http.StatusRequestEntityTooLarge
	}
	w.ResponseWriter.WriteHeader(code)
}
