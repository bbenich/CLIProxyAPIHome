package userapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

const (
	loginTestPassword = "correct-password"
	loginTestVictimIP = "203.0.113.9"
	loginTestAttacker = "198.51.100.7"
)

// loginTestClock is a controllable handler clock.
type loginTestClock struct {
	current time.Time
}

func (c *loginTestClock) now() time.Time {
	return c.current
}

func (c *loginTestClock) advance(d time.Duration) {
	c.current = c.current.Add(d)
}

func newLoginTestClock() *loginTestClock {
	// Start a few seconds into a TOTP step so +/-1 step math is unambiguous.
	base := int64(1_800_000_000)
	base -= base % defaultTOTPPeriod
	return &loginTestClock{current: time.Unix(base+5, 0).UTC()}
}

func performUserLoginRequest(t *testing.T, router http.Handler, path string, clientIP string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatalf("marshal request body: %v", errMarshal)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = clientIP + ":40000"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertUserLoginStatus(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantError string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body=%s", response.Code, wantStatus, response.Body.String())
	}
	if wantError == "" {
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if errDecode := json.Unmarshal(response.Body.Bytes(), &body); errDecode != nil || body.Error != wantError {
		t.Fatalf("error = %q (decode %v), want %q, body=%s", body.Error, errDecode, wantError, response.Body.String())
	}
}

func createLoginTestUser(t *testing.T, handler *Handler, username string, mfa cluster.JSONB) *cluster.UserRecord {
	t.Helper()
	// A plaintext stored password keeps the tests fast; passwordMatches
	// accepts it as a legacy credential.
	password := loginTestPassword
	update := cluster.UserUpdate{Username: &username, Password: &password}
	if len(mfa) > 0 {
		update.MFA = &mfa
	}
	record, errCreate := handler.repo.CreateUser(context.Background(), update)
	if errCreate != nil {
		t.Fatalf("CreateUser(%s) error = %v", username, errCreate)
	}
	return record
}

func totpTestCode(t *testing.T, secret string, at time.Time, stepOffset int64) string {
	t.Helper()
	code, errCode := hotpCode(secret, at.Unix()/int64(defaultTOTPPeriod)+stepOffset)
	if errCode != nil {
		t.Fatalf("hotpCode() error = %v", errCode)
	}
	return code
}

func TestUserLoginFailuresAreRateLimitedWithoutLockingOutOtherAddresses(t *testing.T) {
	handler, router, _ := newUserEmailTestHandler(t, nil)
	clock := newLoginTestClock()
	handler.now = clock.now
	createLoginTestUser(t, handler, "alice", nil)

	for attempt := 0; attempt < loginIPUserLimit; attempt++ {
		response := performUserLoginRequest(t, router, "/user/login", loginTestAttacker, map[string]any{"username": "alice", "password": fmt.Sprintf("guess-%d", attempt)})
		assertUserLoginStatus(t, response, http.StatusUnauthorized, "invalid_credentials")
	}

	// Even the correct password is refused from the throttled address, so a
	// guesser cannot learn which attempt was right.
	limited := performUserLoginRequest(t, router, "/user/login", loginTestAttacker, map[string]any{"username": "alice", "password": loginTestPassword})
	assertUserLoginStatus(t, limited, http.StatusTooManyRequests, "login_rate_limited")
	if got := limited.Header().Get("Retry-After"); got != strconv.Itoa(int(loginIPUserWindow/time.Second)) {
		t.Fatalf("Retry-After = %q, want %d", got, int(loginIPUserWindow/time.Second))
	}
	totpLimited := performUserLoginRequest(t, router, "/user/login/totp", loginTestAttacker, map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": "123456"})
	assertUserLoginStatus(t, totpLimited, http.StatusTooManyRequests, "login_rate_limited")
	passkeyLimited := performUserLoginRequest(t, router, "/user/login/passkey", loginTestAttacker, map[string]any{"username": "alice", "challenge_id": "challenge", "credential": map[string]any{"id": "x"}})
	assertUserLoginStatus(t, passkeyLimited, http.StatusTooManyRequests, "login_rate_limited")

	// The victim logging in from another address is not locked out.
	victim := performUserLoginRequest(t, router, "/user/login", loginTestVictimIP, map[string]any{"username": "alice", "password": loginTestPassword})
	assertUserLoginStatus(t, victim, http.StatusOK, "")

	// Spraying many usernames from one address hits the per-IP limit.
	sprayIP := "192.0.2.50"
	for attempt := 0; attempt < loginIPLimit; attempt++ {
		response := performUserLoginRequest(t, router, "/user/login", sprayIP, map[string]any{"username": fmt.Sprintf("ghost-%d", attempt), "password": "guess"})
		assertUserLoginStatus(t, response, http.StatusUnauthorized, "invalid_credentials")
	}
	sprayLimited := performUserLoginRequest(t, router, "/user/login", sprayIP, map[string]any{"username": "alice", "password": loginTestPassword})
	assertUserLoginStatus(t, sprayLimited, http.StatusTooManyRequests, "login_rate_limited")

	// The window resets after it expires.
	clock.advance(loginIPUserWindow)
	recovered := performUserLoginRequest(t, router, "/user/login", loginTestAttacker, map[string]any{"username": "alice", "password": loginTestPassword})
	assertUserLoginStatus(t, recovered, http.StatusOK, "")
}

func TestUserLoginTOTPRejectsReplayedCode(t *testing.T) {
	handler, router, _ := newUserEmailTestHandler(t, nil)
	clock := newLoginTestClock()
	handler.now = clock.now
	secret, errSecret := generateTOTPSecret()
	if errSecret != nil {
		t.Fatalf("generateTOTPSecret() error = %v", errSecret)
	}
	mfa, errMFA := marshalTOTP("alice", secret, "", 0)
	if errMFA != nil {
		t.Fatalf("marshalTOTP() error = %v", errMFA)
	}
	record := createLoginTestUser(t, handler, "alice", mfa)

	code := totpTestCode(t, secret, clock.now(), 0)
	first := performUserLoginRequest(t, router, "/user/login/totp", "203.0.113.10", map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": code})
	assertUserLoginStatus(t, first, http.StatusOK, "")

	stored, errStored := handler.repo.GetUser(context.Background(), record.ID)
	if errStored != nil {
		t.Fatalf("GetUser() error = %v", errStored)
	}
	settings, enabled := loadTOTP(stored.MFA)
	wantCounter := clock.now().Unix() / int64(defaultTOTPPeriod)
	if !enabled || settings.Secret != normalizeTOTPSecret(secret) || settings.LastUsedCounter != wantCounter {
		t.Fatalf("stored totp = enabled:%v counter:%d (want %d) secret kept:%v", enabled, settings.LastUsedCounter, wantCounter, settings != nil && settings.Secret == normalizeTOTPSecret(secret))
	}

	replay := performUserLoginRequest(t, router, "/user/login/totp", "203.0.113.11", map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": code})
	assertUserLoginStatus(t, replay, http.StatusUnauthorized, "invalid_totp")
	older := performUserLoginRequest(t, router, "/user/login/totp", "203.0.113.12", map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": totpTestCode(t, secret, clock.now(), -1)})
	assertUserLoginStatus(t, older, http.StatusUnauthorized, "invalid_totp")

	// Simulate a concurrent request that loaded MFA before the counter was
	// persisted: the atomic counter claim must still reject the replay.
	if _, errReset := handler.repo.UpdateUser(context.Background(), record.ID, cluster.UserUpdate{MFA: &mfa}); errReset != nil {
		t.Fatalf("reset MFA error = %v", errReset)
	}
	racing := performUserLoginRequest(t, router, "/user/login/totp", "203.0.113.13", map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": code})
	assertUserLoginStatus(t, racing, http.StatusUnauthorized, "invalid_totp")

	clock.advance(time.Duration(defaultTOTPPeriod) * time.Second)
	next := performUserLoginRequest(t, router, "/user/login/totp", "203.0.113.14", map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": totpTestCode(t, secret, clock.now(), 0)})
	assertUserLoginStatus(t, next, http.StatusOK, "")
}

func TestUserTOTPBindCodeCannotBeReplayedForLogin(t *testing.T) {
	handler, router, _ := newUserEmailTestHandler(t, nil)
	clock := newLoginTestClock()
	handler.now = clock.now
	record := createLoginTestUser(t, handler, "alice", nil)
	secret, errSecret := generateTOTPSecret()
	if errSecret != nil {
		t.Fatalf("generateTOTPSecret() error = %v", errSecret)
	}
	code := totpTestCode(t, secret, clock.now(), 0)
	token := createUserTestBearerToken(t, handler, record.ID, record.SessionVersion)
	bind := performUserJSONRequest(t, router, http.MethodPost, "/user/totp/bind", map[string]any{"secret": secret, "code": code}, token)
	assertUserLoginStatus(t, bind, http.StatusOK, "")

	login := performUserLoginRequest(t, router, "/user/login/totp", "203.0.113.20", map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": code})
	assertUserLoginStatus(t, login, http.StatusUnauthorized, "invalid_totp")
}

func TestUserLoginTOTPAttemptsAreLimitedPerUser(t *testing.T) {
	handler, router, _ := newUserEmailTestHandler(t, nil)
	clock := newLoginTestClock()
	handler.now = clock.now
	secret, errSecret := generateTOTPSecret()
	if errSecret != nil {
		t.Fatalf("generateTOTPSecret() error = %v", errSecret)
	}
	mfa, errMFA := marshalTOTP("alice", secret, "", 0)
	if errMFA != nil {
		t.Fatalf("marshalTOTP() error = %v", errMFA)
	}
	createLoginTestUser(t, handler, "alice", mfa)

	valid := map[string]bool{}
	for offset := int64(-1); offset <= 1; offset++ {
		valid[totpTestCode(t, secret, clock.now(), offset)] = true
	}
	wrong := ""
	for candidate := 0; candidate < 10; candidate++ {
		value := fmt.Sprintf("%06d", candidate)
		if !valid[value] {
			wrong = value
			break
		}
	}
	// Distinct source addresses so only the per-user TOTP limit applies.
	for attempt := 0; attempt < loginTOTPUserLimit; attempt++ {
		response := performUserLoginRequest(t, router, "/user/login/totp", fmt.Sprintf("198.51.100.%d", attempt+20), map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": wrong})
		assertUserLoginStatus(t, response, http.StatusUnauthorized, "invalid_totp")
	}
	limited := performUserLoginRequest(t, router, "/user/login/totp", "198.51.100.200", map[string]any{"username": "alice", "password": loginTestPassword, "totp_code": totpTestCode(t, secret, clock.now(), 0)})
	assertUserLoginStatus(t, limited, http.StatusTooManyRequests, "login_rate_limited")
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("limited TOTP login omitted Retry-After")
	}
}

func TestWithTOTPLastUsedCounterKeepsLegacyLayoutAndUnknownFields(t *testing.T) {
	secret, errSecret := generateTOTPSecret()
	if errSecret != nil {
		t.Fatalf("generateTOTPSecret() error = %v", errSecret)
	}
	legacy := cluster.JSONB(`{"enabled":true,"secret":"` + secret + `","custom":"kept"}`)
	next, errNext := withTOTPLastUsedCounter(legacy, 42)
	if errNext != nil {
		t.Fatalf("withTOTPLastUsedCounter(legacy) error = %v", errNext)
	}
	settings, enabled := loadTOTP(next)
	if !enabled || settings.LastUsedCounter != 42 || settings.Secret != secret {
		t.Fatalf("legacy settings = %+v enabled=%v", settings, enabled)
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(next, &root); errUnmarshal != nil || root["custom"] != "kept" {
		t.Fatalf("legacy unknown field lost: %s (%v)", string(next), errUnmarshal)
	}

	nested := cluster.JSONB(`{"enabled":true,"totp":{"enabled":true,"secret":"` + secret + `","issuer":"Home","extra":1}}`)
	next, errNext = withTOTPLastUsedCounter(nested, 43)
	if errNext != nil {
		t.Fatalf("withTOTPLastUsedCounter(nested) error = %v", errNext)
	}
	settings, enabled = loadTOTP(next)
	if !enabled || settings.LastUsedCounter != 43 || settings.Issuer != "Home" {
		t.Fatalf("nested settings = %+v enabled=%v", settings, enabled)
	}
	var nestedRoot struct {
		TOTP map[string]any `json:"totp"`
	}
	if errUnmarshal := json.Unmarshal(next, &nestedRoot); errUnmarshal != nil || nestedRoot.TOTP["extra"] != float64(1) {
		t.Fatalf("nested unknown field lost: %s (%v)", string(next), errUnmarshal)
	}
}
