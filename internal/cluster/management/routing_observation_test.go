package management

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRoutingStrategyRoundTripsEverySupportedChoice(t *testing.T) {
	h, closeRepo := newUsageObservabilityTestHandler(t)
	defer closeRepo()
	if err := h.repo.ReplaceConfigSnapshot(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.PUT("/routing/strategy", h.PutRoutingStrategy)
	engine.GET("/routing/strategy", h.GetRoutingStrategy)
	for _, strategy := range []string{"round-robin", "fill-first", "weighted-round-robin", "quota-reset"} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("PUT", "/routing/strategy", strings.NewReader(`{"value":"`+strategy+`"}`)))
		if w.Code != 200 {
			t.Fatalf("save %s: %d %s", strategy, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("GET", "/routing/strategy", nil))
		var body struct {
			Strategy string `json:"strategy"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Strategy != strategy {
			t.Fatalf("saved %s read %s", strategy, body.Strategy)
		}
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("PUT", "/routing/strategy", strings.NewReader(`{"value":"invalid"}`)))
	if w.Code != 400 {
		t.Fatal("invalid strategy accepted")
	}
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/routing/strategy", nil))
	if !strings.Contains(w.Body.String(), "quota-reset") {
		t.Fatal("invalid write changed strategy")
	}
}

func TestRoutingObservationWithoutRuntimeReturnsUnavailable(t *testing.T) {
	h, closeRepo := newUsageObservabilityTestHandler(t)
	defer closeRepo()
	engine := gin.New()
	engine.GET("/quota/routing", h.GetQuotaRouting)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/quota/routing", nil))
	if w.Code != 503 {
		t.Fatalf("status %d", w.Code)
	}
}
