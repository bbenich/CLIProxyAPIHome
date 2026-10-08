package home

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

type fixtureQuotas map[string]*QuotaRoutingSnapshot

func (f fixtureQuotas) GetQuotaCredential(_ context.Context, id string, _ time.Time) (*QuotaRoutingSnapshot, error) {
	return f[id], nil
}
func quotaFixture(now time.Time, used float64, reset time.Time) *QuotaRoutingSnapshot {
	five, week := int64(18000), int64(604800)
	remaining := 1 - used
	weeklyRemaining := 0.6
	return &QuotaRoutingSnapshot{ObservedAt: &now, Freshness: "fresh", QuotaStatus: "healthy", Windows: []QuotaRoutingWindow{
		{Scope: "account", WindowSeconds: &five, RemainingRatio: &remaining},
		{Scope: "account", WindowSeconds: &week, RemainingRatio: &weeklyRemaining, ResetAt: &reset},
	}}
}
func TestQuotaResetSelection(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	a := &coreauth.Auth{ID: "a", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": "999"}}
	b := &coreauth.Auth{ID: "b", Provider: "claude", Status: coreauth.StatusActive}
	c := &coreauth.Auth{ID: "c", Provider: "claude", Status: coreauth.StatusActive}
	quotas := fixtureQuotas{"a": quotaFixture(now, .2, now.Add(time.Hour)), "b": quotaFixture(now, 0, now.Add(2*time.Hour)), "c": quotaFixture(now, 0, now.Add(3*time.Hour))}
	s := &quotaResetSelector{reader: quotas.GetQuotaCredential, now: func() time.Time { return now }}
	pick := func(want string) {
		t.Helper()
		got, err := s.Pick(context.Background(), "claude", "fixture", coreauth.Options{}, []*coreauth.Auth{a, b, c})
		if err != nil || got.ID != want {
			t.Fatalf("want %s got %v error %v", want, got, err)
		}
	}
	pick("b") // Untouched 5h outranks earliest weekly and static priority.
	pick("b") // Dispatch alone must not invent a timer.
	fiveReset := now.Add(5 * time.Hour)
	quotas["b"].Windows[0].ResetAt = &fiveReset
	pick("c") // Running timer rejoins weekly order even while capacity rounds to 100%.
	quotas["c"].Windows[0].ResetAt = &fiveReset
	pick("a")
	quotas["b"] = quotaFixture(now, .01, now.Add(30*time.Minute))
	pick("b") // 99% rejoins weekly ordering even if the provider omits its 5h timer.
	if a.Attributes["priority"] != "999" {
		t.Fatal("mutated original metadata")
	}
	b.Disabled = true
	pick("a")
	b.Disabled = false
	b.ModelStates = map[string]*coreauth.ModelState{"fixture": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)}}
	pick("a") // Cooldowns remain authoritative.
}

func TestQuotaResetHealthyStaleTimers(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	auth := &coreauth.Auth{ID: "fixture", Provider: "claude", Status: coreauth.StatusActive}
	observed := now.Add(-40 * time.Minute)
	for _, status := range []string{"healthy", "low", "exhausted"} {
		q := quotaFixture(observed, .15, now.Add(time.Hour))
		q.Freshness = "stale"
		q.QuotaStatus = status
		// Aggregate exhaustion may come from a model pool; it cannot exclude the account.
		zero, week := 0.0, int64(604800)
		q.Windows = append(q.Windows, QuotaRoutingWindow{Scope: "model", WindowSeconds: &week, RemainingRatio: &zero})
		rank := rankQuota(auth, q, now)
		if rank.tier != 1 || !rank.estimated || !rank.weekly.Equal(now.Add(time.Hour)) {
			t.Fatalf("%s stale timer discarded: %+v", status, rank)
		}
		elapsed := now.Add(-time.Second)
		q.Windows[0].ResetAt = &elapsed
		q.Windows[0].RemainingRatio = &zero
		if rankQuota(auth, q, now).tier != 1 {
			t.Fatal("elapsed 5h invalidated healthy weekly estimate")
		}
		q.Windows[1].ResetAt = &elapsed
		if rankQuota(auth, q, now).tier != 0 {
			t.Fatal("expired weekly deadline reused")
		}
	}
}

func TestQuotaResetRejectsUnknownAndUnprovenCapacity(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	auth := &coreauth.Auth{ID: "fixture"}
	q := quotaFixture(now, 0, now.Add(time.Hour))
	for _, status := range []string{"unknown", "error", "unsupported", ""} {
		q.QuotaStatus = status
		if rankQuota(auth, q, now).tier != 0 {
			t.Fatalf("%s observation used", status)
		}
	}
	q.QuotaStatus = "healthy"
	future := now.Add(time.Second)
	q.ObservedAt = &future
	if rankQuota(auth, q, now).tier != 0 {
		t.Fatal("future observation used")
	}
	q.ObservedAt = &now
	q.Freshness = "stale"
	if rank := rankQuota(auth, q, now); !rank.prime || rank.tier != 2 || !rank.estimated {
		t.Fatal("healthy stale untouched report discarded despite a still-future weekly boundary")
	}
	past := now.Add(-time.Second)
	q.Windows[1].ResetAt = &past
	if rankQuota(auth, q, now).prime {
		t.Fatal("stale untouched estimate remained active beyond its weekly boundary")
	}
	q.Windows[1].ResetAt = nil
	if rankQuota(auth, q, now).prime {
		t.Fatal("stale untouched estimate used without a weekly boundary")
	}
	q.Windows[1].ResetAt = quotaFixture(now, 0, now.Add(time.Hour)).Windows[1].ResetAt
	fiveReset := now.Add(5 * time.Hour)
	q.Windows[0].ResetAt = &fiveReset
	if rank := rankQuota(auth, q, now); rank.prime || rank.tier != 1 || !rank.estimated {
		t.Fatal("stale running five-hour timer at 100% ignored weekly ordering")
	}
	q.Windows[0].ResetAt = nil
	q.Freshness = "fresh"
	zero := 0.0
	q.Windows[1].RemainingRatio = &zero
	if rankQuota(auth, q, now).tier != -1 {
		t.Fatal("exhausted account window primed")
	}
	q = quotaFixture(now, .0001, now.Add(time.Hour))
	if rankQuota(auth, q, now).prime {
		t.Fatal("rounded 100% incorrectly primed")
	}
}

func TestQuotaResetNormalizesConfiguredStrategy(t *testing.T) {
	rt := newUsageResultTestRuntime(t, &coreauth.Auth{ID: "fixture", Provider: "claude", Status: coreauth.StatusActive})
	rt.quotaSelector = &quotaResetSelector{}
	for _, value := range []string{"quota-reset", " QUOTA-RESET ", "Quota-Reset"} {
		cfg := &config.Config{}
		cfg.Routing.Strategy = value
		if rt.selectorForConfig(cfg) != rt.quotaSelector {
			t.Fatalf("%q silently used another selector", value)
		}
	}
}
