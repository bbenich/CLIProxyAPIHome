package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

const userManagementTOTPSecret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

func createUserManagementTOTPUser(t *testing.T, handler *Handler) *cluster.UserRecord {
	t.Helper()
	username := "alice"
	mfa := cluster.JSONB(`{"enabled":true,"totp":{"enabled":true,"secret":"` + userManagementTOTPSecret + `","issuer":"Home","account":"alice","period":30,"digits":6,"algorithm":"SHA1","bound_at":"2026-05-27T10:00:00Z","last_used_counter":59999999}}`)
	record, errCreate := handler.repo.CreateUser(context.Background(), cluster.UserUpdate{Username: &username, MFA: &mfa})
	if errCreate != nil {
		t.Fatalf("CreateUser() error = %v", errCreate)
	}
	return record
}

func assertUserManagementMFARedacted(t *testing.T, label string, body string) {
	t.Helper()
	for _, forbidden := range []string{userManagementTOTPSecret, `"secret"`, "last_used_counter", "59999999"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("%s leaked %q: %s", label, forbidden, body)
		}
	}
	for _, kept := range []string{`"issuer":"Home"`, `"bound_at":"2026-05-27T10:00:00Z"`, `"enabled":true`} {
		if !strings.Contains(body, kept) {
			t.Fatalf("%s dropped non-secret %q: %s", label, kept, body)
		}
	}
}

func storedUserManagementMFA(t *testing.T, handler *Handler, id uint) string {
	t.Helper()
	record, errRecord := handler.repo.GetUser(context.Background(), id)
	if errRecord != nil {
		t.Fatalf("GetUser() error = %v", errRecord)
	}
	return string(record.MFA)
}

func TestUserManagementResponsesRedactMFASecrets(t *testing.T) {
	handler, engine, closeRepo := newUserManagementHTTPTestServer(t)
	defer closeRepo()
	record := createUserManagementTOTPUser(t, handler)

	list := performUserManagementRequest(t, engine, http.MethodGet, "/users", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", list.Code, list.Body.String())
	}
	assertUserManagementMFARedacted(t, "GET /users", list.Body.String())

	get := performUserManagementRequest(t, engine, http.MethodGet, fmt.Sprintf("/users/%d", record.ID), "")
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", get.Code, get.Body.String())
	}
	assertUserManagementMFARedacted(t, "GET /users/:id", get.Body.String())

	legacy := redactMFA(cluster.JSONB(`{"enabled":true,"secret":"` + userManagementTOTPSecret + `","period":30}`))
	if strings.Contains(string(legacy), userManagementTOTPSecret) || !strings.Contains(string(legacy), `"enabled":true`) {
		t.Fatalf("legacy redaction = %s", string(legacy))
	}
	if raw := redactMFA(cluster.JSONB(`"` + userManagementTOTPSecret + `"`)); raw != nil {
		t.Fatalf("non-object mfa redaction = %s, want null", string(raw))
	}
}

func TestUserManagementRedactedMFARoundTripKeepsSecret(t *testing.T) {
	handler, engine, closeRepo := newUserManagementHTTPTestServer(t)
	defer closeRepo()
	record := createUserManagementTOTPUser(t, handler)
	path := fmt.Sprintf("/users/%d", record.ID)

	get := performUserManagementRequest(t, engine, http.MethodGet, path, "")
	var getBody struct {
		User userManagementRecordResponse `json:"user"`
	}
	decodeUserManagementResponse(t, get, &getBody)

	// The panel echoes the redacted MFA object back alongside another edit.
	payload, errMarshal := json.Marshal(map[string]any{"credits": 5, "mfa": getBody.User.MFA})
	if errMarshal != nil {
		t.Fatalf("marshal patch: %v", errMarshal)
	}
	patch := performUserManagementRequest(t, engine, http.MethodPatch, path, string(payload))
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", patch.Code, patch.Body.String())
	}
	assertUserManagementMFARedacted(t, "PATCH /users/:id", patch.Body.String())
	if stored := storedUserManagementMFA(t, handler, record.ID); !strings.Contains(stored, userManagementTOTPSecret) || !strings.Contains(stored, "59999999") {
		t.Fatalf("redacted round trip wiped stored mfa: %s", stored)
	}

	// An explicit disable still replaces the stored settings.
	disable := performUserManagementRequest(t, engine, http.MethodPatch, path, `{"mfa":{"enabled":false}}`)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable status = %d body=%s", disable.Code, disable.Body.String())
	}
	if stored := storedUserManagementMFA(t, handler, record.ID); strings.Contains(stored, userManagementTOTPSecret) {
		t.Fatalf("explicit disable kept secret: %s", stored)
	}

	// A write that carries a new secret is applied as-is.
	newSecret := "KRSXG5CTMVRXEZLUKRSXG5CTMVRXEZLU"
	replace := performUserManagementRequest(t, engine, http.MethodPatch, path, `{"mfa":{"enabled":true,"secret":"`+newSecret+`"}}`)
	if replace.Code != http.StatusOK {
		t.Fatalf("replace status = %d body=%s", replace.Code, replace.Body.String())
	}
	if strings.Contains(replace.Body.String(), newSecret) {
		t.Fatalf("replace response leaked secret: %s", replace.Body.String())
	}
	if stored := storedUserManagementMFA(t, handler, record.ID); !strings.Contains(stored, newSecret) {
		t.Fatalf("explicit secret write not stored: %s", stored)
	}

	// null clears MFA.
	clearResp := performUserManagementRequest(t, engine, http.MethodPatch, path, `{"mfa":null}`)
	if clearResp.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", clearResp.Code, clearResp.Body.String())
	}
	if stored := storedUserManagementMFA(t, handler, record.ID); stored != "" {
		t.Fatalf("null mfa left stored value: %s", stored)
	}
}
