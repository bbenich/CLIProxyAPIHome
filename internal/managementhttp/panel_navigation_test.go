package managementhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPanelNavigationServesNativeAppsWithoutOuterShell(t *testing.T) {
	original := []byte("<!doctype html><html><body>native Home root</body></html>")
	quota := []byte("<!doctype html><html><body>quota content only</body></html>")
	console := []byte("<!doctype html><html><body>native Console root</body></html>")
	js := []byte("console.log('original asset');")
	engine := newAssetTestEngine(t, map[string][]byte{"index.html": original, "management.html": original, "quota-dashboard.html": quota, "console.html": console, "assets/js/lazy.js": js})
	for path, expected := range map[string][]byte{"/": original, "/management.html": original, "/home-panel.html": original, "/home-management-panel.html": original, "/quota-panel.html": quota, "/console.html": console, "/console-panel.html": console, "/assets/js/lazy.js": js} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != 200 || response.Body.String() != string(expected) {
			t.Fatalf("app was wrapped or changed at %s: %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/quota-dashboard.html", nil))
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/management.html#/admin/quota" {
		t.Fatalf("old bookmark did not redirect into Home: %d", response.Code)
	}
}
