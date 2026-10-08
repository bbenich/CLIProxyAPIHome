package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuotaRoutingCollectsIdleAndHonorsBackoff(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	seedCollectorProviderAuth(t, repo, "idle-codex", "codex", map[string]any{"type": "codex", "access_token": "fixture-token"})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reset" {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	c := NewCollector(repo, Options{Owner: "fixture", CodexUsageURL: server.URL + "/usage", CodexResetCreditsURL: server.URL + "/reset", Now: func() time.Time { return now }, TrackIdleCredentials: func() bool { return true }})
	c.collect(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("idle account calls=%d want1", calls.Load())
	}
	c.collect(context.Background())
	if calls.Load() != 1 {
		t.Fatal("probe ignored provider failure backoff")
	}
	now = now.Add(2 * time.Hour)
	c.collect(context.Background())
	if calls.Load() != 2 {
		t.Fatal("probe did not resume after backoff")
	}
}

func TestQuotaRoutingScheduledCollectionRetainsOnDemandOwnership(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	seedCollectorProviderAuth(t, repo, "idle-owner", "codex", map[string]any{"type": "codex", "access_token": "fixture-token"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer server.Close()
	c := NewCollector(repo, Options{Owner: "scheduled", CodexUsageURL: server.URL, Now: func() time.Time { return now }, TrackIdleCredentials: func() bool { return true }})
	c.onDemandJobs["idle-owner"] = struct{}{}
	c.collect(context.Background())
	if _, exists := c.onDemandJobs["idle-owner"]; !exists {
		t.Fatal("scheduled collection released an on-demand job it did not own")
	}
}

func TestQuotaStaggeredRotationRespectsBackoffAndManualRefresh(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a-failing", "b-healthy", "c-healthy"} {
		seedCollectorProviderAuth(t, repo, id, "codex", map[string]any{"type": "codex", "access_token": id})
	}
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reset" {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		calls = append(calls, id)
		mu.Unlock()
		if id == "a-failing" {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_after_seconds":600}}}`))
	}))
	defer server.Close()
	c := NewCollector(repo, Options{Owner: "paced", CodexUsageURL: server.URL + "/usage", CodexResetCreditsURL: server.URL + "/reset", Now: func() time.Time { return now }, SnapshotFreshness: time.Minute, StaggerInterval: 15 * time.Second, TrackIdleCredentials: func() bool { return true }})
	assertCalls := func(want ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls=%v want%v", calls, want)
		}
	}
	c.collect(context.Background())
	assertCalls("a-failing")
	c.collect(context.Background())
	assertCalls("a-failing") // Same slot never bursts.
	now = now.Add(15 * time.Second)
	c.collect(context.Background())
	assertCalls("a-failing", "b-healthy")
	now = now.Add(15 * time.Second)
	c.collect(context.Background())
	assertCalls("a-failing", "b-healthy", "c-healthy")
	accepted, err := c.TriggerCollection(context.Background(), nil, nil)
	if err != nil || accepted != 0 {
		t.Fatalf("manual bypassed freshness/backoff: %d %v", accepted, err)
	}
	now = now.Add(time.Minute)
	c.collect(context.Background())
	assertCalls("a-failing", "b-healthy", "c-healthy", "b-healthy")
	item, err := repo.GetQuotaCredential(context.Background(), "a-failing", now)
	if err != nil || item.NextProbeAt == nil || item.NextProbeAt.Before(time.Date(2026, 10, 8, 12, 10, 0, 0, time.UTC)) {
		t.Fatalf("Retry-After not retained: %+v %v", item, err)
	}
}

func TestQuotaStaggeredManualQueueUsesSharedCadenceWithoutIdleMonitor(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"idle-a", "idle-b"} {
		seedCollectorProviderAuth(t, repo, id, "codex", map[string]any{"type": "codex", "access_token": id})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusTooManyRequests) }))
	defer server.Close()
	c := NewCollector(repo, Options{Owner: "paced", StaggerInterval: 15 * time.Second, Now: func() time.Time { return now }, CodexUsageURL: server.URL})
	accepted, err := c.TriggerCollection(context.Background(), nil, nil)
	if err != nil || accepted != 2 || calls.Load() != 0 {
		t.Fatalf("queue caused immediate probes: %d %v calls%d", accepted, err, calls.Load())
	}
	accepted, err = c.TriggerCollection(context.Background(), nil, nil)
	if err != nil || accepted != 0 {
		t.Fatalf("queue failed to deduplicate: %d %v", accepted, err)
	}
	c.collect(context.Background())
	if calls.Load() != 1 {
		t.Fatal("manual queue did not run one idle account")
	}
	c.collect(context.Background())
	if calls.Load() != 1 {
		t.Fatal("manual queue bypassed shared cadence")
	}
	now = now.Add(15 * time.Second)
	c.collect(context.Background())
	if calls.Load() != 2 {
		t.Fatal("failed first account blocked second queued account")
	}
	if len(c.onDemandJobs) != 0 {
		t.Fatal("completed jobs remained queued")
	}
}

func TestQuotaStaggeredPartialRateLimitHonorsRetryAfter(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	seedCollectorProviderAuth(t, repo, "claude-partial", "claude", map[string]any{"type": "claude", "access_token": "fixture"})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/profile" {
			w.Header().Set("Retry-After", "900")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":20,"resets_at":"2026-10-08T17:00:00Z"}}`))
	}))
	defer server.Close()
	c := NewCollector(repo, Options{Owner: "paced", StaggerInterval: 15 * time.Second, SnapshotFreshness: time.Minute, Now: func() time.Time { return now }, ClaudeUsageURL: server.URL + "/usage", ClaudeProfileURL: server.URL + "/profile", TrackIdleCredentials: func() bool { return true }})
	c.collect(context.Background())
	item, err := repo.GetQuotaCredential(context.Background(), "claude-partial", now)
	if err != nil || item.CollectionStatus != "partial" || item.NextProbeAt == nil || item.NextProbeAt.Before(now.Add(15*time.Minute)) || item.ConsecutiveFailure != 1 {
		t.Fatalf("partial429 schedule %+v %v", item, err)
	}
	now = now.Add(2 * time.Minute)
	c.collect(context.Background())
	if calls.Load() != 1 {
		t.Fatal("partial429 retried before cooldown")
	}
}
