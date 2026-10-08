package cluster

import (
	"context"
	"errors"
	"testing"
)

// A login that read MFA before an admin reset must not restore the old secret.
func TestUpdateUserExpectedMFARejectsConcurrentChange(t *testing.T) {
	ctx := context.Background()
	repo := newCredentialFoundationTestRepository(t)
	name := "mfa-cas"
	bound := JSONB(`{"totp":{"enabled":true,"secret":"OLDSECRET"}}`)
	user, errCreate := repo.CreateUser(ctx, UserUpdate{Username: &name, MFA: &bound})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	readByLogin := user.MFA

	reset := JSONB(`null`)
	if _, errReset := repo.UpdateUser(ctx, user.ID, UserUpdate{MFA: &reset}); errReset != nil {
		t.Fatal(errReset)
	}
	stale := JSONB(`{"totp":{"enabled":true,"secret":"OLDSECRET","last_used_counter":7}}`)
	if _, errStale := repo.UpdateUser(ctx, user.ID, UserUpdate{MFA: &stale, ExpectedMFA: &readByLogin}); !errors.Is(errStale, ErrUserMFAChanged) {
		t.Fatalf("stale conditional MFA update error = %v, want ErrUserMFAChanged", errStale)
	}
	current, errGet := repo.GetUser(ctx, user.ID)
	if errGet != nil {
		t.Fatal(errGet)
	}
	if !jsonbEqual(current.MFA, reset) {
		t.Fatalf("admin reset was overwritten: mfa = %s", current.MFA)
	}
}

func TestUpdateUserExpectedMFAAppliesWhenUnchanged(t *testing.T) {
	ctx := context.Background()
	repo := newCredentialFoundationTestRepository(t)
	name := "mfa-cas-ok"
	bound := JSONB(`{"totp":{"enabled":true,"secret":"SECRET"}}`)
	user, errCreate := repo.CreateUser(ctx, UserUpdate{Username: &name, MFA: &bound})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	// Formatting differences, as produced by JSONB normalization, still match.
	expected := JSONB(`{ "totp" : { "secret" : "SECRET", "enabled" : true } }`)
	next := JSONB(`{"totp":{"enabled":true,"secret":"SECRET","last_used_counter":9}}`)
	updated, errUpdate := repo.UpdateUser(ctx, user.ID, UserUpdate{MFA: &next, ExpectedMFA: &expected})
	if errUpdate != nil {
		t.Fatalf("conditional MFA update: %v", errUpdate)
	}
	if !jsonbEqual(updated.MFA, next) {
		t.Fatalf("mfa = %s, want %s", updated.MFA, next)
	}
}
