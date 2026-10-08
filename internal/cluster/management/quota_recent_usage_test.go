package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuotaRecentUsageResponseAndUnavailableRepository(t *testing.T) {
	h, engine, closeRepo := newUserManagementHTTPTestServer(t)
	defer closeRepo()
	engine.GET("/quota/recent-usage", h.GetQuotaRecentUsage)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/quota/recent-usage", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"accounts":[]`) || !strings.Contains(response.Body.String(), `"generated_at":`) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	h.repo = nil
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/quota/recent-usage", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", response.Code)
	}
}
