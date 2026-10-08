package cluster

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

// Exercise the same database scope adapter, hot reload and dispatch entry point
// used by gateway requests, without contacting an upstream inference service.
func TestQuotaRoutingDispatchUsesSelectedStrategyAndDatabaseScopes(t *testing.T) {
	ctx := context.Background()
	repo := newRefreshTestRepository(t)
	const model = "quota-routing-integration-model"
	const static, weekly, untouched, outside = "quota-static", "quota-weekly", "quota-untouched", "quota-outside"
	for _, id := range []string{static, weekly, untouched, outside} {
		priority := "0"
		if id == static {
			priority = "100"
		}
		if id == outside {
			priority = "999"
		}
		auth := &coreauth.Auth{ID: id, Index: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": priority}}
		if _, err := repo.UpsertAuth(ctx, auth, "test"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	group := func(name string, ids ...string) uint {
		t.Helper()
		g, err := repo.CreateChannelGroup(ctx, name, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if _, err := repo.CreateChannelGroupDetail(ctx, g.ID, id); err != nil {
				t.Fatal(err)
			}
		}
		return g.ID
	}
	personal := group("test-personal", static, weekly, untouched)
	work := group("test-work", outside)
	empty := group("test-empty")
	weeklyOnly := group("test-weekly-only", weekly)
	models, err := repo.CreateModelGroup(ctx, "test-model-scope", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateModelGroupDetail(ctx, models.ID, model, []uint{weeklyOnly}); err != nil {
		t.Fatal(err)
	}
	key := func(value string, groups []uint, modelGroups *[]uint) {
		t.Helper()
		if _, err := repo.CreateAPIKey(ctx, APIKeyEntryUpdate{APIKey: value, Channels: &groups, ModelGroups: modelGroups}); err != nil {
			t.Fatal(err)
		}
	}
	key("personal-fixture", []uint{personal}, nil)
	key("work-fixture", []uint{work}, nil)
	key("empty-fixture", []uint{empty}, nil)
	key("model-fixture", []uint{personal}, &[]uint{models.ID})
	runtime, err := home.NewRuntime(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Stop)
	runtime.SetClusterAdapter(NewRuntimeAdapter(repo, ""))
	now := time.Now()
	five, week := int64(18000), int64(604800)
	quotas := map[string]*home.QuotaRoutingSnapshot{}
	for id, hours := range map[string]int{static: 4, weekly: 1, untouched: 3, outside: 0} {
		remaining, weeklyRemaining := .8, .7
		if id == untouched || id == outside {
			remaining = 1
		}
		reset := now.Add(time.Duration(hours)*time.Hour + 30*time.Minute)
		quotas[id] = &home.QuotaRoutingSnapshot{ObservedAt: &now, Freshness: "fresh", QuotaStatus: "healthy", Windows: []home.QuotaRoutingWindow{
			{Scope: "account", WindowSeconds: &five, RemainingRatio: &remaining},
			{Scope: "account", WindowSeconds: &week, RemainingRatio: &weeklyRemaining, ResetAt: &reset},
		}}
	}
	runtime.SetQuotaSnapshotReader(func(_ context.Context, id string, _ time.Time) (*home.QuotaRoutingSnapshot, error) {
		return quotas[id], nil
	})
	apply := func(t *testing.T, strategy string) {
		t.Helper()
		cfg := &config.Config{}
		cfg.Routing.Strategy = strategy
		if err := runtime.ApplyConfigFromCluster(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		// Supply a fixture model after auth reload registers the provider catalog.
		for id := range quotas {
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model, Object: "model", Type: "openai"}})
			runtime.CoreManager().RefreshSchedulerEntry(id)
		}
	}
	dispatch := func(t *testing.T, key, want string) {
		t.Helper()
		result, err := runtime.DispatchForAPIKey(ctx, model, nil, key)
		if want == "" {
			if err == nil || result != nil {
				t.Fatalf("%s: expected fail closed, got result=%v error=%v", key, result != nil, err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if result == nil || result.AuthID != want {
			t.Fatalf("%s: wanted %s, got %#v", key, want, result)
		}
	}
	for _, strategy := range []string{"round-robin", "weighted-round-robin", "fill-first", "quota-reset"} {
		t.Run(strategy, func(t *testing.T) {
			apply(t, strategy)
			want := static
			if strategy == "quota-reset" {
				want = untouched
			}
			dispatch(t, "personal-fixture", want)
			dispatch(t, "model-fixture", weekly)
			dispatch(t, "empty-fixture", "")
			result, err := runtime.DispatchForAPIKeyWithConcurrency(ctx, model, nil, "personal-fixture", "", nil, outside, home.DispatchConcurrencyContext{})
			if err == nil || result != nil {
				t.Fatal("out-of-scope pinned credential bypassed permissions")
			}
			if strategy == "quota-reset" {
				// Even successful cached usage cannot invent a five-hour timer.
				runtime.RecordUsagePayload(ctx, `{"auth_index":"quota-untouched","provider":"codex","model":"quota-routing-integration-model","failed":false}`)
				dispatch(t, "personal-fixture", untouched)
				fiveReset := now.Add(5 * time.Hour)
				quotas[untouched].Windows[0].ResetAt = &fiveReset
				// A reported running timer moves it to weekly ordering even at 100%.
				dispatch(t, "personal-fixture", weekly)
				// A healthy older observation is ranked by its still-future weekly reset.
				observed := now.Add(-40 * time.Minute)
				quotas[weekly].ObservedAt = &observed
				quotas[weekly].Freshness = "stale"
				dispatch(t, "personal-fixture", weekly)
				foundEstimate := false
				for _, item := range runtime.RoutingObservation(ctx).Accounts {
					if item.CredentialID == weekly {
						foundEstimate = item.Estimated && item.Reason == "weekly-reset" && item.Rank != nil
					}
				}
				if !foundEstimate {
					t.Fatal("live observation did not expose the stale timer estimate used for dispatch")
				}
				remaining := .99
				reset := now.Add(15 * time.Minute)
				quotas[untouched].Windows[0].RemainingRatio = &remaining
				quotas[untouched].Windows[1].ResetAt = &reset
				dispatch(t, "personal-fixture", untouched)
			}
			dispatch(t, "work-fixture", outside)
		})
	}
	// Switching back must replace the quota selector in actual dispatch too.
	apply(t, "fill-first")
	dispatch(t, "personal-fixture", static)
}
