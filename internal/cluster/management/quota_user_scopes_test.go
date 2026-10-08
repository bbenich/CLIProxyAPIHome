package management

import (
	"context"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuotaUserScopesResponseDoesNotExposeAPIKeys(t *testing.T) {
	h, engine, closeRepo := newUserManagementHTTPTestServer(t)
	defer closeRepo()
	name := "fixture-user"
	user, err := h.repo.CreateUser(context.Background(), cluster.UserUpdate{Username: &name})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "fixture-do-not-expose"
	if _, err := h.repo.CreateAPIKey(context.Background(), cluster.APIKeyEntryUpdate{APIKey: secret, UserID: &user.ID, UserIDSet: true}); err != nil {
		t.Fatal(err)
	}
	engine.GET("/quota/users", h.GetQuotaUserScopes)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/quota/users", nil))
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, `"username":"fixture-user"`) || !strings.Contains(body, `"credential_ids":[]`) {
		t.Fatalf("unexpected response: %d %s", response.Code, body)
	}
	for _, forbidden := range []string{secret, "api_key", "password", "credits", "mfa"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response exposed %s", forbidden)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
}
