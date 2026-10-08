package home

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

func TestRoutingObservationMatchesQuotaDispatchWithoutMutating(t *testing.T) {
	now := time.Now()
	a := &coreauth.Auth{ID: "a", Provider: "claude", Status: coreauth.StatusActive}
	b := &coreauth.Auth{ID: "b", Provider: "claude", Status: coreauth.StatusActive}
	disabled := &coreauth.Auth{ID: "disabled", Provider: "claude", Status: coreauth.StatusDisabled, Disabled: true}
	rt := newUsageResultTestRuntime(t, a)
	for _, auth := range []*coreauth.Auth{b, disabled} {
		if _, err := rt.coreManager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Routing.Strategy = "quota-reset"
	rt.cfg = cfg
	quotas := fixtureQuotas{"a": quotaFixture(now, .2, now.Add(time.Hour)), "b": quotaFixture(now, 0, now.Add(2*time.Hour))}
	rt.quotaSelector = &quotaResetSelector{reader: quotas.GetQuotaCredential, now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		preview := rt.RoutingObservation(context.Background())
		if preview.Accounts[0].CredentialID != "b" || *preview.Accounts[0].Rank != 1 || preview.Accounts[0].Reason != "untouched-five-hour" {
			t.Fatalf("unexpected preview: %+v", preview)
		}
	}

	picked, err := rt.quotaSelector.Pick(context.Background(), "claude", "fixture", coreauth.Options{}, []*coreauth.Auth{a, b, disabled})
	if err != nil || picked.ID != "b" {
		t.Fatalf("pick=%v err=%v", picked, err)
	}
	fiveReset := now.Add(5 * time.Hour)
	quotas["b"].Windows[0].ResetAt = &fiveReset
	after := rt.RoutingObservation(context.Background())
	if after.Accounts[0].CredentialID != "a" || after.Accounts[0].Reason != "weekly-reset" {
		t.Fatalf("provider timer not reflected: %+v", after)
	}
	for _, item := range after.Accounts {
		if item.CredentialID == "disabled" && item.Rank != nil {
			t.Fatal("disabled account ranked")
		}
	}
	now = now.Add(3 * time.Minute)
	quotas["a"] = quotaFixture(now, .2, now.Add(time.Hour))
	quotas["b"] = quotaFixture(now, 0, now.Add(2*time.Hour))
	if rt.RoutingObservation(context.Background()).Accounts[0].CredentialID != "b" {
		t.Fatal("new untouched observation not reflected")
	}

}

func TestRoutingObservationSelectedStrategyChangesValues(t *testing.T) {
	now := time.Now()
	a := &coreauth.Auth{ID: "a", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": "3", "weight": "0"}}
	b := &coreauth.Auth{ID: "b", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": "3", "weight": "5"}, ModelStates: map[string]*coreauth.ModelState{"model": {Unavailable: true, NextRetryAfter: now.Add(time.Hour)}}}
	rt := newUsageResultTestRuntime(t, a)
	if _, err := rt.coreManager.Register(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	rt.cfg = &config.Config{}
	for _, strategy := range []string{"round-robin", "fill-first", "weighted-round-robin"} {
		rt.cfg.Routing.Strategy = strategy
		result := rt.RoutingObservation(context.Background())
		if result.Strategy != strategy {
			t.Fatal("strategy hardcoded")
		}
		first, second := result.Accounts[0], result.Accounts[1]
		switch strategy {
		case "round-robin":
			if *first.Rank != 1 || *second.Rank != 1 || second.Reason != "rotation" {
				t.Fatalf("rotation invented order: %+v", result)
			}
		case "fill-first":
			if *first.Rank != 1 || *second.Rank != 2 {
				t.Fatalf("fill-first order: %+v", result)
			}
		case "weighted-round-robin":
			if first.Rank != nil || first.Reason != "zero-weight" || *second.Rank != 1 || second.Weight != 5 {
				t.Fatalf("weight exclusion: %+v", result)
			}
		}
		if !second.ModelDependent {
			t.Fatal("model cooldown lost")
		}
	}
}

func TestRoutingObservationHonorsEffectiveCooldownPolicy(t *testing.T) {
	now := time.Now()
	auth := &coreauth.Auth{ID: "cooldown", Provider: "claude", Status: coreauth.StatusActive, ModelStates: map[string]*coreauth.ModelState{"model": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour), Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}}}
	rt := newUsageResultTestRuntime(t, auth)
	rt.cfg = &config.Config{}
	rt.coreManager.SetConfig(rt.cfg)
	if !rt.RoutingObservation(context.Background()).Accounts[0].ModelDependent {
		t.Fatal("cooldown not reported")
	}
	rt.cfg = &config.Config{DisableCooling: true}
	rt.coreManager.SetConfig(rt.cfg)
	if rt.RoutingObservation(context.Background()).Accounts[0].ModelDependent {
		t.Fatal("ignored cooldown reported as active")
	}
}
